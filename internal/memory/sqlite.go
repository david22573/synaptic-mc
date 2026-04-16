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
	MarkWorldNode(ctx context.Context, node domain.WorldNode) error
	GetKnownWorld(ctx context.Context, pos domain.Vec3) (string, error)
	GetNearbyNodes(ctx context.Context, pos domain.Vec3, limit int) ([]domain.WorldNode, error)
	AddEdge(ctx context.Context, fromID, toID string, cost, risk float64) error
	AddRegion(ctx context.Context, name string, nodeIDs []string) error
	GetRegions(ctx context.Context) ([]domain.Region, error)
	SetSummary(ctx context.Context, sessionID, key, value string) error
	GetSummary(ctx context.Context, sessionID string) (string, error)
	SaveTaskHistory(ctx context.Context, sessionID string, history []domain.TaskHistory) error
	GetTaskHistory(ctx context.Context, sessionID string, limit int) ([]domain.TaskHistory, error)
	SaveMilestone(ctx context.Context, sessionID string, name string) error
	GetMilestones(ctx context.Context, sessionID string) ([]domain.ProgressionMilestone, error)
	SaveFailureCount(ctx context.Context, sessionID, objective string, count int) error
	GetFailureCounts(ctx context.Context, sessionID string) (map[string]int, error)
	MarkChunkVisited(ctx context.Context, sessionID string, x, z int, rich, dangerous bool) error
	GetExplorationBias(ctx context.Context, sessionID string, x, z int) (float64, error)
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
		kind TEXT NOT NULL,
		x REAL,
		y REAL,
		z REAL,
		score REAL DEFAULT 0.0,
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS world_edges (
		from_id TEXT NOT NULL,
		to_id TEXT NOT NULL,
		cost REAL DEFAULT 1.0,
		risk REAL DEFAULT 0.0,
		PRIMARY KEY (from_id, to_id),
		FOREIGN KEY (from_id) REFERENCES world_nodes(id),
		FOREIGN KEY (to_id) REFERENCES world_nodes(id)
	);

	CREATE TABLE IF NOT EXISTS world_regions (
		name TEXT PRIMARY KEY,
		node_ids TEXT NOT NULL -- stored as JSON array
	);

	CREATE TABLE IF NOT EXISTS task_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		intent_json TEXT NOT NULL,
		success BOOLEAN NOT NULL,
		critique TEXT NOT NULL,
		reflection_json TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	
	CREATE TABLE IF NOT EXISTS milestones (
		session_id TEXT NOT NULL,
		name TEXT NOT NULL,
		unlocked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, name)
	);

	CREATE TABLE IF NOT EXISTS plan_failures (
		session_id TEXT NOT NULL,
		objective TEXT NOT NULL,
		count INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, objective)
	);
	
	CREATE TABLE IF NOT EXISTS visited_chunks (
		session_id TEXT NOT NULL,
		x INTEGER NOT NULL,
		z INTEGER NOT NULL,
		visit_count INTEGER DEFAULT 1,
		is_resource_rich BOOLEAN DEFAULT 0,
		is_dangerous BOOLEAN DEFAULT 0,
		last_visited DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, x, z)
	);`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply memory schema: %w", err)
	}

	// Migrations for Graph Memory
	_, _ = db.Exec("ALTER TABLE world_nodes ADD COLUMN kind TEXT NOT NULL DEFAULT 'unknown'")
	_, _ = db.Exec("ALTER TABLE world_nodes ADD COLUMN score REAL DEFAULT 0.0")
	_, _ = db.Exec("ALTER TABLE world_edges ADD COLUMN risk REAL DEFAULT 0.0")

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

	query := `INSERT INTO task_history (session_id, intent_json, success, critique, reflection_json) VALUES (?, ?, ?, ?, ?)`
	for _, h := range history {
		intentJSON, err := json.Marshal(h.Intent)
		if err != nil {
			return err
		}
		var reflJSON sql.NullString
		if h.Reflection != nil {
			b, _ := json.Marshal(h.Reflection)
			reflJSON.String = string(b)
			reflJSON.Valid = true
		}
		if _, err := tx.ExecContext(ctx, query, sessionID, string(intentJSON), h.Success, h.Critique, reflJSON); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetTaskHistory(ctx context.Context, sessionID string, limit int) ([]domain.TaskHistory, error) {
	query := `SELECT intent_json, success, critique, reflection_json FROM task_history WHERE session_id = ? ORDER BY created_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []domain.TaskHistory
	for rows.Next() {
		var h domain.TaskHistory
		var intentJSON string
		var reflJSON sql.NullString
		if err := rows.Scan(&intentJSON, &h.Success, &h.Critique, &reflJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(intentJSON), &h.Intent); err != nil {
			return nil, err
		}
		if reflJSON.Valid {
			_ = json.Unmarshal([]byte(reflJSON.String), &h.Reflection)
		}
		history = append(history, h)
	}

	// Reverse to get chronological order (they were DESC for LIMIT)
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}

	return history, nil
}

func (s *SQLiteStore) MarkWorldNode(ctx context.Context, node domain.WorldNode) error {
	if node.ID == "" {
		node.ID = fmt.Sprintf("%s_%d_%d_%d", node.Name, int(node.Pos.X), int(node.Pos.Y), int(node.Pos.Z))
	}

	query := `
	INSERT INTO world_nodes (id, name, kind, x, y, z, score, last_seen) 
	VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(id) DO UPDATE SET 
		score = excluded.score,
		last_seen=CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, node.ID, node.Name, node.Kind, node.Pos.X, node.Pos.Y, node.Pos.Z, node.Score)
	return err
}

func (s *SQLiteStore) GetKnownWorld(ctx context.Context, botPos domain.Vec3) (string, error) {
	query := `SELECT name, kind, x, y, z, score, last_seen FROM world_nodes`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return "KNOWN WORLD: empty", nil
	}
	defer rows.Close()

	type nodeDist struct {
		Name  string
		Kind  string
		Dist  float64
		Pos   domain.Vec3
		Score float64
	}
	var nodes []nodeDist

	for rows.Next() {
		var n nodeDist
		var lastSeen time.Time
		if err := rows.Scan(&n.Name, &n.Kind, &n.Pos.X, &n.Pos.Y, &n.Pos.Z, &n.Score, &lastSeen); err == nil {
			if n.Kind == "hazard" && time.Since(lastSeen) > 30*time.Minute {
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
		out.WriteString(fmt.Sprintf("- [%s] %s (%.0fm away at %.0f, %.0f, %.0f) | Score: %.1f\n", n.Kind, n.Name, n.Dist, n.Pos.X, n.Pos.Y, n.Pos.Z, n.Score))
	}

	return strings.TrimSpace(out.String()), nil
}

func (s *SQLiteStore) GetNearbyNodes(ctx context.Context, botPos domain.Vec3, limit int) ([]domain.WorldNode, error) {
	query := `SELECT id, name, kind, x, y, z, score FROM world_nodes`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type nodeDist struct {
		node domain.WorldNode
		dist float64
	}
	var nodes []nodeDist

	for rows.Next() {
		var n domain.WorldNode
		if err := rows.Scan(&n.ID, &n.Name, &n.Kind, &n.Pos.X, &n.Pos.Y, &n.Pos.Z, &n.Score); err == nil {
			dx, dy, dz := n.Pos.X-botPos.X, n.Pos.Y-botPos.Y, n.Pos.Z-botPos.Z
			dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
			nodes = append(nodes, nodeDist{node: n, dist: dist})
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].dist < nodes[j].dist })

	if len(nodes) > limit {
		nodes = nodes[:limit]
	}

	result := make([]domain.WorldNode, len(nodes))
	for i, nd := range nodes {
		result[i] = nd.node
	}
	return result, nil
}

func (s *SQLiteStore) AddEdge(ctx context.Context, fromID, toID string, cost, risk float64) error {
	query := `INSERT INTO world_edges (from_id, to_id, cost, risk) VALUES (?, ?, ?, ?) ON CONFLICT(from_id, to_id) DO UPDATE SET cost = excluded.cost, risk = excluded.risk`
	_, err := s.db.ExecContext(ctx, query, fromID, toID, cost, risk)
	return err
}

func (s *SQLiteStore) AddRegion(ctx context.Context, name string, nodeIDs []string) error {
	idsJSON, _ := json.Marshal(nodeIDs)
	query := `INSERT INTO world_regions (name, node_ids) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET node_ids = excluded.node_ids`
	_, err := s.db.ExecContext(ctx, query, name, string(idsJSON))
	return err
}

func (s *SQLiteStore) GetRegions(ctx context.Context) ([]domain.Region, error) {
	query := `SELECT name, node_ids FROM world_regions`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var regions []domain.Region
	for rows.Next() {
		var r domain.Region
		var idsJSON string
		if err := rows.Scan(&r.Name, &idsJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(idsJSON), &r.Nodes)
		regions = append(regions, r)
	}
	return regions, nil
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

func (s *SQLiteStore) SaveFailureCount(ctx context.Context, sessionID, objective string, count int) error {
	query := `
	INSERT INTO plan_failures (session_id, objective, count, updated_at) 
	VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(session_id, objective) DO UPDATE SET 
		count = excluded.count, 
		updated_at = CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, sessionID, objective, count)
	return err
}

func (s *SQLiteStore) GetFailureCounts(ctx context.Context, sessionID string) (map[string]int, error) {
	query := `SELECT objective, count FROM plan_failures WHERE session_id = ?`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	failures := make(map[string]int)
	for rows.Next() {
		var objective string
		var count int
		if err := rows.Scan(&objective, &count); err != nil {
			return nil, err
		}
		failures[objective] = count
	}
	return failures, nil
}

func (s *SQLiteStore) MarkChunkVisited(ctx context.Context, sessionID string, x, z int, rich, dangerous bool) error {
	query := `
	INSERT INTO visited_chunks (session_id, x, z, visit_count, is_resource_rich, is_dangerous, last_visited) 
	VALUES (?, ?, ?, 1, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(session_id, x, z) DO UPDATE SET 
		visit_count = visit_count + 1,
		is_resource_rich = excluded.is_resource_rich OR is_resource_rich,
		is_dangerous = excluded.is_dangerous OR is_dangerous,
		last_visited = CURRENT_TIMESTAMP;`
	_, err := s.db.ExecContext(ctx, query, sessionID, x, z, rich, dangerous)
	return err
}

func (s *SQLiteStore) GetExplorationBias(ctx context.Context, sessionID string, x, z int) (float64, error) {
	query := `SELECT visit_count, is_resource_rich, is_dangerous FROM visited_chunks WHERE session_id = ? AND x = ? AND z = ?`
	var visits int
	var rich, dangerous bool
	err := s.db.QueryRowContext(ctx, query, sessionID, x, z).Scan(&visits, &rich, &dangerous)
	if err == sql.ErrNoRows {
		return 1.0, nil // High bias for new areas
	}
	if err != nil {
		return 0, err
	}

	bias := 1.0 / float64(visits)
	if rich {
		bias *= 2.0
	}
	if dangerous {
		bias *= 0.1
	}
	return bias, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
