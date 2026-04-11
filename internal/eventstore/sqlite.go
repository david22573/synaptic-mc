// internal/eventstore/sqlite.go
package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"david22573/synaptic-mc/internal/domain"
)

const (
	batchSize     = 50
	flushInterval = 100 * time.Millisecond
)

type pendingEvent struct {
	SessionID string
	Trace     domain.TraceContext
	EventType domain.EventType
	Payload   []byte
	Timestamp time.Time
}

type SQLiteStore struct {
	db     *sql.DB
	logger *slog.Logger
	buffer chan pendingEvent
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
}

func NewSQLiteStore(dbPath string, logger *slog.Logger) (*SQLiteStore, error) {
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	if err := runMigrations(db); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	store := &SQLiteStore{
		db:     db,
		logger: logger.With(slog.String("component", "eventstore")),
		buffer: make(chan pendingEvent, 1000),
		ctx:    ctx,
		cancel: cancel,
	}

	store.wg.Add(1)
	go store.batchWorker()

	return store, nil
}

func runMigrations(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			action_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			payload JSON,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id, id);`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			session_id TEXT PRIMARY KEY,
			last_event_id INTEGER NOT NULL,
			data JSON NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Append(ctx context.Context, sessionID string, trace domain.TraceContext, eventType domain.EventType, payload any) error {
	var payloadBytes []byte
	switch v := payload.(type) {
	case []byte:
		payloadBytes = v
	case json.RawMessage:
		payloadBytes = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("failed to marshal event payload: %w", err)
		}
		payloadBytes = b
	}

	ev := pendingEvent{
		SessionID: sessionID,
		Trace:     trace,
		EventType: eventType,
		Payload:   payloadBytes,
		Timestamp: time.Now().UTC(),
	}

	select {
	case s.buffer <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		s.logger.Warn("Event buffer full, performing synchronous insert")
		return s.insertBatch(ctx, []pendingEvent{ev})
	}
}

func (s *SQLiteStore) GetStream(ctx context.Context, sessionID string) ([]domain.DomainEvent, error) {
	query := `SELECT id, session_id, trace_id, action_id, event_type, payload, created_at 
	          FROM events WHERE session_id = ?
	          ORDER BY id ASC`
	return s.queryEvents(ctx, query, sessionID)
}

func (s *SQLiteStore) GetRecentStream(ctx context.Context, sessionID string, limit int) ([]domain.DomainEvent, error) {
	query := `
		SELECT * FROM (
			SELECT id, session_id, trace_id, action_id, event_type, payload, created_at 
			FROM events 
			WHERE session_id = ? 
			ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC
	`
	return s.queryEvents(ctx, query, sessionID, limit)
}

func (s *SQLiteStore) GetStreamSince(ctx context.Context, sessionID string, sinceID int64) ([]domain.DomainEvent, error) {
	query := `SELECT id, session_id, trace_id, action_id, event_type, payload, created_at 
	          FROM events WHERE session_id = ?
	          AND id > ? ORDER BY id ASC`
	return s.queryEvents(ctx, query, sessionID, sinceID)
}

func (s *SQLiteStore) GetLastEventID(ctx context.Context, sessionID string) (int64, error) {
	query := `SELECT COALESCE(MAX(id), 0) FROM events WHERE session_id = ?`
	var id int64
	err := s.db.QueryRowContext(ctx, query, sessionID).Scan(&id)
	return id, err
}

func (s *SQLiteStore) queryEvents(ctx context.Context, query string, args ...any) ([]domain.DomainEvent, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.DomainEvent
	for rows.Next() {
		var ev domain.DomainEvent
		var payloadStr string
		if err := rows.Scan(&ev.ID, &ev.SessionID, &ev.Trace.TraceID, &ev.Trace.ActionID, &ev.Type, &payloadStr, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.Payload = json.RawMessage(payloadStr)
		events = append(events, ev)
	}
	return events, rows.Err()
}

func (s *SQLiteStore) SaveSnapshot(ctx context.Context, snap domain.SessionSnapshot) error {
	query := `
		INSERT INTO snapshots (session_id, last_event_id, data, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(session_id) DO UPDATE SET
			last_event_id = excluded.last_event_id,
			data = excluded.data,
			updated_at = CURRENT_TIMESTAMP;
	`
	_, err := s.db.ExecContext(ctx, query, snap.SessionID, snap.LastEventID, snap.Data)
	return err
}

func (s *SQLiteStore) GetLatestSnapshot(ctx context.Context, sessionID string) (*domain.SessionSnapshot, error) {
	query := `SELECT session_id, last_event_id, data, updated_at FROM snapshots WHERE session_id = ?`
	row := s.db.QueryRowContext(ctx, query, sessionID)

	var snap domain.SessionSnapshot
	var dataStr string
	if err := row.Scan(&snap.SessionID, &snap.LastEventID, &dataStr, &snap.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	snap.Data = json.RawMessage(dataStr)
	return &snap, nil
}

func (s *SQLiteStore) Close() error {
	s.cancel()
	s.wg.Wait()
	return s.db.Close()
}

func (s *SQLiteStore) batchWorker() {
	defer s.wg.Done()

	timer := time.NewTimer(flushInterval)
	defer timer.Stop()

	batch := make([]pendingEvent, 0, batchSize)

	flush := func(drainCtx context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := s.insertBatch(drainCtx, batch); err != nil {
			s.logger.Error("Failed to flush event batch", slog.Any("error", err), slog.Int("count", len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-s.ctx.Done():
			// Safely drain remaining without closing the channel to prevent writer panics
			drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			for {
				select {
				case ev := <-s.buffer:
					batch = append(batch, ev)
					if len(batch) >= batchSize {
						flush(drainCtx)
					}
				default:
					flush(drainCtx)
					return
				}
			}

		case ev := <-s.buffer:
			batch = append(batch, ev)
			if len(batch) >= batchSize || len(s.buffer) > cap(s.buffer)/2 {
				flush(s.ctx)
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(flushInterval)
			}

		case <-timer.C:
			flush(s.ctx)
			timer.Reset(flushInterval)
		}
	}
}

func (s *SQLiteStore) insertBatch(ctx context.Context, events []pendingEvent) error {
	if len(events) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	valueStrings := make([]string, 0, len(events))
	valueArgs := make([]interface{}, 0, len(events)*6)

	for _, ev := range events {
		valueStrings = append(valueStrings, "(?, ?, ?, ?, ?, ?)")
		valueArgs = append(valueArgs, ev.SessionID, ev.Trace.TraceID, ev.Trace.ActionID, ev.EventType, string(ev.Payload), ev.Timestamp)
	}

	stmt := fmt.Sprintf("INSERT INTO events (session_id, trace_id, action_id, event_type, payload, created_at) VALUES %s",
		strings.Join(valueStrings, ","))

	if _, err := tx.Exec(stmt, valueArgs...); err != nil {
		return err
	}

	return tx.Commit()
}
