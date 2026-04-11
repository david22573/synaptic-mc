// internal/voyager/vector.go
package voyager

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"david22573/synaptic-mc/internal/domain"

	_ "modernc.org/sqlite"
)

type VectorStore interface {
	SaveSkill(ctx context.Context, description string, code string, embedding []float32) error
	RetrieveSkills(ctx context.Context, queryEmbedding []float32, limit int) ([]SkillRecord, error)
	RetrieveNamedSkill(ctx context.Context, name string) (*domain.ExecutableSkill, error)
	Close() error
}

type SkillRecord struct {
	Description string
	Code        string
	Similarity  float32
}

type SQLiteVectorStore struct {
	db *sql.DB
}

func NewSQLiteVectorStore(dbPath string) (*SQLiteVectorStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open vector store sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)

	schema := `
	CREATE TABLE IF NOT EXISTS skills (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE,
		description TEXT NOT NULL UNIQUE,
		code TEXT NOT NULL,
		embedding_json TEXT NOT NULL,
		success_count INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply vector store schema: %w", err)
	}

	return &SQLiteVectorStore{db: db}, nil
}

func (s *SQLiteVectorStore) SaveSkill(ctx context.Context, description string, code string, embedding []float32) error {
	embeddingBytes, _ := json.Marshal(embedding)

	var name sql.NullString
	var skill domain.ExecutableSkill
	if err := json.Unmarshal([]byte(code), &skill); err == nil && skill.Name != "" {
		name.String = skill.Name
		name.Valid = true
	}

	query := `INSERT INTO skills (name, description, code, embedding_json, success_count) VALUES (?, ?, ?, ?, 1) 
	          ON CONFLICT(description) DO UPDATE SET 
	          code=excluded.code, 
	          embedding_json=excluded.embedding_json,
	          success_count=success_count+1`
	_, err := s.db.ExecContext(ctx, query, name, description, code, string(embeddingBytes))

	return err
}

func (s *SQLiteVectorStore) RetrieveNamedSkill(ctx context.Context, name string) (*domain.ExecutableSkill, error) {
	query := `SELECT code FROM skills WHERE name = ?`
	var code string
	err := s.db.QueryRowContext(ctx, query, name).Scan(&code)
	if err != nil {
		return nil, err
	}

	var skill domain.ExecutableSkill
	if err := json.Unmarshal([]byte(code), &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

func (s *SQLiteVectorStore) RetrieveSkills(ctx context.Context, queryEmbedding []float32, limit int) ([]SkillRecord, error) {
	query := `SELECT description, code, embedding_json FROM skills`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []SkillRecord

	for rows.Next() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		var desc, code, embJSON string
		if err := rows.Scan(&desc, &code, &embJSON); err != nil {
			continue
		}

		var embedding []float32
		if err := json.Unmarshal([]byte(embJSON), &embedding); err != nil {
			continue
		}

		sim := cosineSimilarity(queryEmbedding, embedding)
		records = append(records, SkillRecord{
			Description: desc,
			Code:        code,
			Similarity:  sim,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].Similarity > records[j].Similarity
	})

	if len(records) > limit {
		records = records[:limit]
	}

	return records, nil
}

func (s *SQLiteVectorStore) Close() error {
	return s.db.Close()
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0.0
	}
	var dotProduct, normA, normB float32
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0.0
	}
	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}
