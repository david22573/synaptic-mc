package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

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

	// Optimize SQLite for high-throughput append-only workloads
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
	_, err = s.db.ExecContext(ctx, query, sessionID, trace.TraceID, trace.ActionID, trace.MilestoneID, string(eventType), data)
	return err
}

func (s *SQLiteStore) GetStream(ctx context.Context, sessionID string) ([]domain.DomainEvent, error) {
	query := `
		SELECT id, session_id, trace_id, action_id, milestone_id, event_type, payload, created_at 
		FROM domain_events 
		WHERE session_id = ? 
		ORDER BY id ASC
	`

	rows, err := s.db.QueryContext(ctx, query, sessionID)
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

// 3.2 FIX: Added GetRecentStream to limit the history window
func (s *SQLiteStore) GetRecentStream(ctx context.Context, sessionID string, limit int) ([]domain.DomainEvent, error) {
	query := `
		SELECT id, session_id, trace_id, action_id, milestone_id, event_type, payload, created_at 
		FROM domain_events 
		WHERE session_id = ? 
		ORDER BY id DESC LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
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

	// Reverse the slice to restore chronological order (since we queried DESC)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return events, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
