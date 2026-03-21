package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DomainEvent represents an immutable fact about the system at a point in time.
type DomainEvent struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	TraceID   string          `json:"trace_id"`
	Type      string          `json:"type"` // e.g., "MilestoneGenerated", "TaskDispatched", "StateTick"
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type EventStore interface {
	Append(ctx context.Context, sessionID, traceID, eventType string, payload interface{}) error
	GetStream(ctx context.Context, sessionID string) ([]DomainEvent, error)
	Close() error
}

type SQLiteEventStore struct {
	db *sql.DB
}

func NewSQLiteEventStore(dbPath string) (*SQLiteEventStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open event store sqlite: %w", err)
	}

	schema := `
	PRAGMA journal_mode=WAL;
	PRAGMA synchronous=NORMAL;
	PRAGMA busy_timeout=5000;

	CREATE TABLE IF NOT EXISTS domain_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		trace_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		payload JSON NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_events_session ON domain_events(session_id, id ASC);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply event store schema: %w", err)
	}

	return &SQLiteEventStore{db: db}, nil
}

func (s *SQLiteEventStore) Append(ctx context.Context, sessionID, traceID, eventType string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	query := `INSERT INTO domain_events (session_id, trace_id, event_type, payload) VALUES (?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, query, sessionID, traceID, eventType, data)
	return err
}

func (s *SQLiteEventStore) GetStream(ctx context.Context, sessionID string) ([]DomainEvent, error) {
	query := `SELECT id, session_id, trace_id, event_type, payload, created_at FROM domain_events WHERE session_id = ? ORDER BY id ASC`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []DomainEvent
	for rows.Next() {
		var e DomainEvent
		if err := rows.Scan(&e.ID, &e.SessionID, &e.TraceID, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

func (s *SQLiteEventStore) Close() error {
	return s.db.Close()
}
