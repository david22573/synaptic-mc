package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"

	_ "modernc.org/sqlite"
)

type Store interface {
	MarkWorldNode(ctx context.Context, name, nodeType string, pos domain.Vec3) error
	GetKnownWorld(ctx context.Context, pos domain.Vec3) (string, error)
	SetSummary(ctx context.Context, sessionID, key, value string) error
	GetSummary(ctx context.Context, sessionID string) (string, error)
	SaveTaskHistory(ctx context.Context, sessionID string, history []domain.TaskHistory) error
	GetTaskHistory(ctx context.Context, sessionID string, limit int) ([]domain.TaskHistory, error)
	SaveMilestone(ctx context.Context, sessionID string, name string) error
	GetMilestones(ctx context.Context, sessionID string) ([]domain.ProgressionMilestone, error)
	Close() error
}

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite memory: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	schema := `
	PRAGMA journal_mode=WAL;
	PRAGMA synchronous=NORMAL;

	CREATE TABLE IF NOT EXISTS session_summary (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, key)
	);

	CREATE TABLE IF NOT EXISTS world_nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		x REAL,
		y REAL,
		z REAL,
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS task_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		intent_json TEXT NOT NULL,
		success BOOLEAN NOT NULL,
		critique TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	
	CREATE TABLE IF NOT EXISTS milestones (
		session_id TEXT NOT NULL,
		name TEXT NOT NULL,
		unlocked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, name)
	);`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply memory schema: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) SaveMilestone(ctx context.Context, sessionID string, name string) error {
	query := `INSERT INTO milestones (session_id, name) VALUES (?, ?) ON CONFLICT(session_id, name) DO NOTHING`
	_, err := s.db.ExecContext(ctx, query, sessionID, name)
	return err
}

func (s *SQLiteStore) GetMilestones(ctx context.Context, sessionID string) ([]domain.ProgressionMilestone, error) {
	query := `SELECT name, unlocked_at FROM milestones WHERE session_id = ? ORDER BY unlocked_at ASC`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var milestones []domain.ProgressionMilestone
	for rows.Next() {
		var m domain.ProgressionMilestone
		if err := rows.Scan(&m.Name, &m.UnlockedAt); err != nil {
			return nil, err
		}
		milestones = append(milestones, m)
	}
	return milestones, nil
}

func (s *SQLiteStore) SaveTaskHistory(ctx context.Context, sessionID string, history []domain.TaskHistory) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `INSERT INTO task_history (session_id, intent_json, success, critique) VALUES (?, ?, ?, ?)`
	for _, h := range history {
		intentJSON, err := json.Marshal(h.Intent)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, query, sessionID, string(intentJSON), h.Success, h.Critique); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetTaskHistory(ctx context.Context, sessionID string, limit int) ([]domain.TaskHistory, error) {
	query := `SELECT intent_json, success, critique FROM task_history WHERE session_id = ? ORDER BY created_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []domain.TaskHistory
	for rows.Next() {
		var h domain.TaskHistory
		var intentJSON string
		if err := rows.Scan(&intentJSON, &h.Success, &h.Critique); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(intentJSON), &h.Intent); err != nil {
			return nil, err
		}
		history = append(history, h)
	}

	// Reverse to get chronological order (they were DESC for LIMIT)
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}

	return history, nil
}

func (s *SQLiteStore) MarkWorldNode(ctx context.Context, name, nodeType string, pos domain.Vec3) error {
	// Create a positional composite key so the bot can remember thousands of unique blocks
	id := fmt.Sprintf("%s_%d_%d_%d", name, int(pos.X), int(pos.Y), int(pos.Z))

	query := `
	INSERT INTO world_nodes (id, name, type, x, y, z, last_seen) 
	VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(id) DO UPDATE SET 
		last_seen=CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, id, name, nodeType, pos.X, pos.Y, pos.Z)
	return err
}

func (s *SQLiteStore) GetKnownWorld(ctx context.Context, botPos domain.Vec3) (string, error) {
	query := `SELECT name, type, x, y, z, last_seen FROM world_nodes`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return "KNOWN WORLD: empty", nil
	}
	defer rows.Close()

	type nodeDist struct {
		Name string
		Type string
		Dist float64
		Pos  domain.Vec3
	}
	var nodes []nodeDist

	for rows.Next() {
		var n nodeDist
		var lastSeen time.Time
		if err := rows.Scan(&n.Name, &n.Type, &n.Pos.X, &n.Pos.Y, &n.Pos.Z, &lastSeen); err == nil {
			if n.Type == "hazard" && time.Since(lastSeen) > 30*time.Minute {
				continue // Decay old death zones
			}
			dx, dy, dz := n.Pos.X-botPos.X, n.Pos.Y-botPos.Y, n.Pos.Z-botPos.Z
			n.Dist = math.Sqrt(dx*dx + dy*dy + dz*dz)
			nodes = append(nodes, n)
		}
	}

	if err := rows.Err(); err != nil {
		return "KNOWN WORLD: empty", err
	}

	if len(nodes) == 0 {
		return "KNOWN WORLD: empty", nil
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Dist < nodes[j].Dist })

	var out strings.Builder
	out.WriteString("KNOWN WORLD:\n")
	limit := 10
	if len(nodes) < limit {
		limit = len(nodes)
	}

	for i := 0; i < limit; i++ {
		n := nodes[i]
		out.WriteString(fmt.Sprintf("- [%s] %s (%.0fm away at %.0f, %.0f, %.0f)\n", n.Type, n.Name, n.Dist, n.Pos.X, n.Pos.Y, n.Pos.Z))
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *SQLiteStore) SetSummary(ctx context.Context, sessionID, key, value string) error {
	query := `
	INSERT INTO session_summary (session_id, key, value, updated_at) 
	VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(session_id, key) DO UPDATE SET 
		value = excluded.value, 
		updated_at = CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, sessionID, key, value)
	return err
}

func (s *SQLiteStore) GetSummary(ctx context.Context, sessionID string) (string, error) {
	query := `SELECT key, value FROM session_summary WHERE session_id = ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var summary strings.Builder
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err == nil {
			summary.WriteString(fmt.Sprintf("- %s: %s\n", key, value))
		}
	}

	if err := rows.Err(); err != nil {
		return "", err
	}

	if summary.Len() == 0 {
		return "No active summary.", nil
	}
	return summary.String(), nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
