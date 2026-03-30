package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"david22573/synaptic-mc/internal/domain"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteEventStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open event store sqlite: %w", err)
	}

	// Enforce single writer to prevent SQLITE_BUSY contention
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	schema := `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;

CREATE TABLE IF NOT EXISTS domain_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	action_id TEXT NOT NULL,
	milestone_id TEXT NOT NULL,
	event_type TEXT NOT NULL,
	payload JSON NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_events_session ON domain_events(session_id, id ASC);
CREATE INDEX IF NOT EXISTS idx_events_trace ON domain_events(trace_id);

CREATE TABLE IF NOT EXISTS session_snapshots (
	session_id TEXT PRIMARY KEY,
	last_event_id INTEGER NOT NULL,
	data JSON NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply event store schema: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Append(ctx context.Context, sessionID string, trace domain.TraceContext, eventType domain.EventType, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	query := `
	INSERT INTO domain_events (session_id, trace_id, action_id, milestone_id, event_type, payload) 
	VALUES (?, ?, ?, ?, ?, ?)
`

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		_, err = s.db.ExecContext(ctx, query, sessionID, trace.TraceID, trace.ActionID, trace.MilestoneID, string(eventType), data)
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("failed to append event after retries: %w", lastErr)
}

func (s *SQLiteStore) GetStream(ctx context.Context, sessionID string) ([]domain.DomainEvent, error) {
	query := `SELECT id, session_id, trace_id, action_id, milestone_id, event_type, payload, created_at  FROM domain_events  WHERE session_id = ?  ORDER BY id ASC`
	return s.queryEvents(ctx, query, sessionID)
}

func (s *SQLiteStore) GetRecentStream(ctx context.Context, sessionID string, limit int) ([]domain.DomainEvent, error) {
	query := `SELECT id, session_id, trace_id, action_id, milestone_id, event_type, payload, created_at  FROM domain_events  WHERE session_id = ? ORDER BY id DESC LIMIT ?`

	events, err := s.queryEvents(ctx, query, sessionID, limit)
	if err != nil {
		return nil, err
	}

	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nil
}

func (s *SQLiteStore) GetStreamSince(ctx context.Context, sessionID string, sinceID int64) ([]domain.DomainEvent, error) {
	query := `SELECT id, session_id, trace_id, action_id, milestone_id, event_type, payload, created_at  FROM domain_events  WHERE session_id = ? AND id > ? ORDER BY id ASC`
	return s.queryEvents(ctx, query, sessionID, sinceID)
}

func (s *SQLiteStore) SaveSnapshot(ctx context.Context, snap domain.SessionSnapshot) error {
	query := `INSERT INTO session_snapshots (session_id, last_event_id, data, created_at)  VALUES (?, ?, ?, CURRENT_TIMESTAMP) ON CONFLICT(session_id) DO UPDATE SET  last_event_id = excluded.last_event_id, data = excluded.data, created_at = CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, snap.SessionID, snap.LastEventID, string(snap.Data))
	return err
}

func (s *SQLiteStore) GetLatestSnapshot(ctx context.Context, sessionID string) (*domain.SessionSnapshot, error) {
	query := `SELECT session_id, last_event_id, data, created_at  FROM session_snapshots  WHERE session_id = ?`
	row := s.db.QueryRowContext(ctx, query, sessionID)

	var snap domain.SessionSnapshot
	var dataStr string

	err := row.Scan(&snap.SessionID, &snap.LastEventID, &dataStr, &snap.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	snap.Data = json.RawMessage(dataStr)
	return &snap, nil
}

func (s *SQLiteStore) queryEvents(ctx context.Context, query string, args ...any) ([]domain.DomainEvent, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.DomainEvent
	for rows.Next() {
		var e domain.DomainEvent
		var eventTypeStr string

		if err := rows.Scan(
			&e.ID,
			&e.SessionID,
			&e.Trace.TraceID,
			&e.Trace.ActionID,
			&e.Trace.MilestoneID,
			&eventTypeStr,
			&e.Payload,
			&e.CreatedAt,
		); err != nil {
			return nil, err
		}

		e.Type = domain.EventType(eventTypeStr)
		events = append(events, e)
	}
	return events, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
