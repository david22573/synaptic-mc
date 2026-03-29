package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
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

type WorldNode struct {
	Name       string
	Type       string
	X, Y, Z    float64
	LastSeen   time.Time
	Confidence float64
}

type MemoryBank interface {
	LogEvent(action, details string, meta EventMeta)
	GetRecentContext(ctx context.Context, sessionID string, limit int) (string, error)
	SetSummary(ctx context.Context, sessionID, key, value string) error
	GetSummary(ctx context.Context, sessionID string) (string, error)
	GetSummaryValue(ctx context.Context, sessionID, key string) (string, error)

	SaveMilestone(ctx context.Context, sessionID string, ms *MilestonePlan) error
	LoadMilestone(ctx context.Context, sessionID string) (*MilestonePlan, error)
	ConsolidateSession(ctx context.Context, sessionID string) error

	MarkWorldNode(ctx context.Context, name, nodeType string, x, y, z float64) error
	GetNode(ctx context.Context, name string) (*WorldNode, error)
	GetKnownWorld(ctx context.Context, botX, botY, botZ float64) (string, error)

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
	logger    *slog.Logger
}

func NewSQLiteMemory(dbPath string, logger *slog.Logger) (*SQLiteMemory, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
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
		z REAL,
		trace_id TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_events_session_id ON events(session_id, id DESC);
	
	CREATE TABLE IF NOT EXISTS session_summary (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, key)
	);
	
	CREATE TABLE IF NOT EXISTS world_nodes (
		name TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		x REAL,
		y REAL,
		z REAL,
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
		confidence REAL DEFAULT 1.0
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	mem := &SQLiteMemory{
		db:        db,
		eventChan: make(chan pendingEvent, 1000),
		cancel:    cancel,
		logger:    logger.With(slog.String("component", "SQLiteMemory")),
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
			s.logger.Error("Failed to begin tx for batch", slog.Any("error", err))
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
				s.logger.Error("Failed to insert event", slog.Any("error", err))
			}
		}

		if err := tx.Commit(); err != nil {
			s.logger.Error("Failed to commit event batch", slog.Any("error", err))
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
	if action == "wander" || action == "idle" || action == "explore" {
		return
	}

	select {
	case s.eventChan <- pendingEvent{action: action, details: details, meta: meta}:
	default:
		s.logger.Warn("Event buffer full, dropping event")
	}
}

func (s *SQLiteMemory) GetRecentContext(ctx context.Context, sessionID string, limit int) (string, error) {
	query := `SELECT timestamp, action, details, status FROM events WHERE session_id = ? ORDER BY id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID, limit*2)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var events []string
	var lastAction string

	for rows.Next() {
		var timestamp time.Time
		var action, details, status string
		if err := rows.Scan(&timestamp, &action, &details, &status); err != nil {
			return "", err
		}

		if action == lastAction {
			continue
		}
		lastAction = action

		events = append(events, fmt.Sprintf("[%s] %s (%s): %s", timestamp.Format("15:04:05"), action, status, details))

		if len(events) >= limit {
			break
		}
	}

	var contextStr strings.Builder
	for i := len(events) - 1; i >= 0; i-- {
		contextStr.WriteString(events[i])
		contextStr.WriteString("\n")
	}

	// Append consolidated past if available
	if past, err := s.GetSummaryValue(ctx, sessionID, "past_summary"); err == nil && past != "" {
		return "PAST CONSOLIDATED:\n" + past + "\nRECENT:\n" + contextStr.String(), nil
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
	query := `SELECT key, value FROM session_summary WHERE session_id = ? AND key != 'active_milestone' AND key != 'past_summary'`
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

func (s *SQLiteMemory) GetSummaryValue(ctx context.Context, sessionID, key string) (string, error) {
	query := `SELECT value FROM session_summary WHERE session_id = ? AND key = ?`
	var value string
	err := s.db.QueryRowContext(ctx, query, sessionID, key).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

func (s *SQLiteMemory) SaveMilestone(ctx context.Context, sessionID string, ms *MilestonePlan) error {
	if ms == nil {
		return s.SetSummary(ctx, sessionID, "active_milestone", "")
	}
	data, err := json.Marshal(ms)
	if err != nil {
		return err
	}
	return s.SetSummary(ctx, sessionID, "active_milestone", string(data))
}

func (s *SQLiteMemory) LoadMilestone(ctx context.Context, sessionID string) (*MilestonePlan, error) {
	val, err := s.GetSummaryValue(ctx, sessionID, "active_milestone")
	if err != nil || val == "" {
		return nil, err
	}
	var ms MilestonePlan
	if err := json.Unmarshal([]byte(val), &ms); err != nil {
		return nil, err
	}
	return &ms, nil
}

// ConsolidateSession rolls up repetitive past events into a tighter summary to save LLM context
func (s *SQLiteMemory) ConsolidateSession(ctx context.Context, sessionID string) error {
	query := `SELECT action, status, COUNT(*) as cnt FROM events WHERE session_id = ? AND status = 'COMPLETED' GROUP BY action, status ORDER BY cnt DESC LIMIT 10`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var action, status string
		var cnt int
		if err := rows.Scan(&action, &status, &cnt); err == nil && cnt > 2 {
			sb.WriteString(fmt.Sprintf("- Successfully completed '%s' %d times\n", action, cnt))
		}
	}

	if sb.Len() > 0 {
		s.logger.Debug("Session consolidated", slog.String("session", sessionID))
		return s.SetSummary(ctx, sessionID, "past_summary", sb.String())
	}
	return nil
}

func (s *SQLiteMemory) MarkWorldNode(ctx context.Context, name, nodeType string, x, y, z float64) error {
	query := `
	INSERT INTO world_nodes (name, type, x, y, z, last_seen, confidence) 
	VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, 1.0)
	ON CONFLICT(name) DO UPDATE SET 
		x=excluded.x, y=excluded.y, z=excluded.z, 
		last_seen=CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, name, nodeType, x, y, z)
	return err
}

func (s *SQLiteMemory) GetNode(ctx context.Context, name string) (*WorldNode, error) {
	query := `SELECT name, type, x, y, z, last_seen, confidence FROM world_nodes WHERE name = ?`
	var node WorldNode
	err := s.db.QueryRowContext(ctx, query, name).Scan(&node.Name, &node.Type, &node.X, &node.Y, &node.Z, &node.LastSeen, &node.Confidence)
	if err != nil {
		return nil, err
	}
	return &node, nil
}

func (s *SQLiteMemory) GetKnownWorld(ctx context.Context, botX, botY, botZ float64) (string, error) {
	query := `SELECT name, type, x, y, z, last_seen FROM world_nodes`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return "KNOWN WORLD: empty", nil
	}
	defer rows.Close()

	type nodeDist struct {
		Name    string
		Type    string
		X, Y, Z float64
		Dist    float64
		Age     time.Duration
	}
	var nodes []nodeDist

	for rows.Next() {
		var n nodeDist
		var lastSeen time.Time
		if err := rows.Scan(&n.Name, &n.Type, &n.X, &n.Y, &n.Z, &lastSeen); err == nil {
			n.Age = time.Since(lastSeen)

			// Phase 3: Death-Zone Temporal Decay - Drop old hazards
			if n.Type == "hazard" && n.Age > 30*time.Minute {
				continue
			}

			dx, dy, dz := n.X-botX, n.Y-botY, n.Z-botZ
			n.Dist = math.Sqrt(dx*dx + dy*dy + dz*dz)
			nodes = append(nodes, n)
		}
	}

	if len(nodes) == 0 {
		return "KNOWN WORLD: empty", nil
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Dist < nodes[j].Dist
	})

	var out strings.Builder
	out.WriteString("KNOWN WORLD:\n")

	limit := 10
	if len(nodes) < limit {
		limit = len(nodes)
	}

	for i := 0; i < limit; i++ {
		n := nodes[i]
		out.WriteString(fmt.Sprintf("- [%s] %s (%.0fm away at %.0f, %.0f, %.0f)\n", n.Type, n.Name, n.Dist, n.X, n.Y, n.Z))
	}

	return strings.TrimSpace(out.String()), nil
}

func (s *SQLiteMemory) Close() error {
	s.cancel()
	s.wg.Wait()
	close(s.eventChan)
	return s.db.Close()
}
