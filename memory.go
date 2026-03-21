package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type EventMeta struct {
	SessionID string
	TraceID   string
	Status    string
	X, Y, Z   float64
}

type MemoryBank interface {
	LogEvent(action, details string, meta EventMeta)
	GetRecentContext(ctx context.Context, sessionID string, limit int) (string, error)
	SetSummary(ctx context.Context, sessionID, key, value string) error
	GetSummary(ctx context.Context, sessionID string) (string, error)
	MarkLocation(ctx context.Context, name string, x, y, z float64) error
	GetLocation(ctx context.Context, name string) (*Vec3, error)
	Close() error
}

type pendingEvent struct {
	action  string
	details string
	meta    EventMeta
}

type SQLiteMemory struct {
	db        *sql.DB
	eventChan chan pendingEvent
	wg        sync.WaitGroup
	cancel    context.CancelFunc
}

func NewSQLiteMemory(dbPath string) (*SQLiteMemory, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	schema := `
	PRAGMA journal_mode=WAL;
	PRAGMA synchronous=NORMAL;
	PRAGMA busy_timeout=5000;

	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		action TEXT,
		details TEXT,
		status TEXT,
		x REAL,
		y REAL,
		z REAL
	);
	CREATE INDEX IF NOT EXISTS idx_events_session_id ON events(session_id, id DESC);

	CREATE TABLE IF NOT EXISTS session_summary (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, key)
	);

	CREATE TABLE IF NOT EXISTS spatial_memory (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE,
		x REAL,
		y REAL,
		z REAL
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	// Safe migration for Phase 3 to add trace_id to existing DBs
	_, _ = db.Exec(`ALTER TABLE events ADD COLUMN trace_id TEXT DEFAULT ''`)

	ctx, cancel := context.WithCancel(context.Background())
	mem := &SQLiteMemory{
		db:        db,
		eventChan: make(chan pendingEvent, 1000),
		cancel:    cancel,
	}

	mem.wg.Add(1)
	go mem.processBatches(ctx)

	return mem, nil
}

func (s *SQLiteMemory) processBatches(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var batch []pendingEvent

	flush := func() {
		if len(batch) == 0 {
			return
		}
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			log.Printf("[-] Failed to begin tx for batch: %v", err)
			return
		}

		stmt, err := tx.Prepare(`INSERT INTO events (session_id, trace_id, action, details, status, x, y, z) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			tx.Rollback()
			return
		}
		defer stmt.Close()

		for _, e := range batch {
			_, err := stmt.Exec(e.meta.SessionID, e.meta.TraceID, e.action, e.details, e.meta.Status, e.meta.X, e.meta.Y, e.meta.Z)
			if err != nil {
				log.Printf("[-] Failed to insert event: %v", err)
			}
		}

		if err := tx.Commit(); err != nil {
			log.Printf("[-] Failed to commit event batch: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e := <-s.eventChan:
			batch = append(batch, e)
			if len(batch) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *SQLiteMemory) LogEvent(action, details string, meta EventMeta) {
	select {
	case s.eventChan <- pendingEvent{action: action, details: details, meta: meta}:
	default:
		log.Println("[-] Event buffer full, dropping event")
	}
}

func (s *SQLiteMemory) GetRecentContext(ctx context.Context, sessionID string, limit int) (string, error) {
	query := `SELECT timestamp, action, details, status, COALESCE(trace_id, '') FROM events WHERE session_id = ? ORDER BY id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var events []string
	for rows.Next() {
		var timestamp time.Time
		var action, details, status, traceID string
		if err := rows.Scan(&timestamp, &action, &details, &status, &traceID); err != nil {
			return "", err
		}

		traceStr := ""
		if traceID != "" {
			traceStr = fmt.Sprintf(" [Trace: %s]", traceID)
		}
		events = append(events, fmt.Sprintf("[%s] %s (%s)%s: %s", timestamp.Format("15:04:05"), action, status, traceStr, details))
	}

	var contextStr strings.Builder
	for i := len(events) - 1; i >= 0; i-- {
		contextStr.WriteString(events[i])
		contextStr.WriteString("\n")
	}

	return contextStr.String(), nil
}

func (s *SQLiteMemory) SetSummary(ctx context.Context, sessionID, key, value string) error {
	query := `
	INSERT INTO session_summary (session_id, key, value, updated_at) 
	VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(session_id, key) DO UPDATE SET 
		value = excluded.value, 
		updated_at = CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, sessionID, key, value)
	return err
}

func (s *SQLiteMemory) GetSummary(ctx context.Context, sessionID string) (string, error) {
	query := `SELECT key, value FROM session_summary WHERE session_id = ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var summary strings.Builder
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return "", err
		}
		summary.WriteString(fmt.Sprintf("- %s: %s\n", key, value))
	}

	if summary.Len() == 0 {
		return "No active summary.", nil
	}
	return summary.String(), nil
}

func (s *SQLiteMemory) MarkLocation(ctx context.Context, name string, x, y, z float64) error {
	query := `
	INSERT INTO spatial_memory (name, x, y, z) 
	VALUES (?, ?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET x=excluded.x, y=excluded.y, z=excluded.z;`
	_, err := s.db.ExecContext(ctx, query, name, x, y, z)
	return err
}

func (s *SQLiteMemory) GetLocation(ctx context.Context, name string) (*Vec3, error) {
	query := `SELECT x, y, z FROM spatial_memory WHERE name = ?`
	var vec Vec3
	err := s.db.QueryRowContext(ctx, query, name).Scan(&vec.X, &vec.Y, &vec.Z)
	if err != nil {
		return nil, err
	}
	return &vec, nil
}

func (s *SQLiteMemory) Close() error {
	s.cancel()
	s.wg.Wait()
	close(s.eventChan)
	return s.db.Close()
}
