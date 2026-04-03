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
	SaveSkill(ctx context.Context, description string, intent domain.ActionIntent, embedding []float32) error
	RetrieveSkills(ctx context.Context, queryEmbedding []float32, limit int) ([]SkillRecord, error)
	Close() error
}

type SkillRecord struct {
	Description string
	Intent      domain.ActionIntent
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
		description TEXT NOT NULL UNIQUE,
		intent_json TEXT NOT NULL,
		embedding_json TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply vector store schema: %w", err)
	}

	return &SQLiteVectorStore{db: db}, nil
}

func (s *SQLiteVectorStore) SaveSkill(ctx context.Context, description string, intent domain.ActionIntent, embedding []float32) error {
	intentBytes, _ := json.Marshal(intent)
	embeddingBytes, _ := json.Marshal(embedding)

	query := `INSERT INTO skills (description, intent_json, embedding_json) VALUES (?, ?, ?) 
	          ON CONFLICT(description) DO UPDATE SET intent_json=excluded.intent_json, embedding_json=excluded.embedding_json`
	_, err := s.db.ExecContext(ctx, query, description, string(intentBytes), string(embeddingBytes))

	return err
}

func (s *SQLiteVectorStore) RetrieveSkills(ctx context.Context, queryEmbedding []float32, limit int) ([]SkillRecord, error) {
	// Full table scan for accurate cosine similarity
	query := `SELECT description, intent_json, embedding_json FROM skills`

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

		var desc, intentJSON, embJSON string
		if err := rows.Scan(&desc, &intentJSON, &embJSON); err != nil {
			continue
		}

		var intent domain.ActionIntent
		if err := json.Unmarshal([]byte(intentJSON), &intent); err != nil {
			continue
		}

		var embedding []float32
		if err := json.Unmarshal([]byte(embJSON), &embedding); err != nil {
			continue
		}

		sim := cosineSimilarity(queryEmbedding, embedding)
		records = append(records, SkillRecord{
			Description: desc,
			Intent:      intent,
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
