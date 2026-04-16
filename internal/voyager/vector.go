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
	RecordSkillResult(ctx context.Context, name string, success bool, durationMs int64, cause string) error
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
		bucket_id INTEGER DEFAULT 0,
		success_count INTEGER DEFAULT 0,
		total_runs INTEGER DEFAULT 0,
		total_successes INTEGER DEFAULT 0,
		avg_time_ms REAL DEFAULT 0,
		failure_causes TEXT DEFAULT '[]',
		required_items TEXT DEFAULT '[]',
		context_tags TEXT DEFAULT '[]',
		version INTEGER DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_skills_bucket ON skills(bucket_id);`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply vector store schema: %w", err)
	}

	// Migrations: ensure columns exist (ignore errors if they already exist)
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN total_runs INTEGER DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN total_successes INTEGER DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN bucket_id INTEGER DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN avg_time_ms REAL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN failure_causes TEXT DEFAULT '[]'")
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN required_items TEXT DEFAULT '[]'")
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN context_tags TEXT DEFAULT '[]'")
	_, _ = db.Exec("ALTER TABLE skills ADD COLUMN version INTEGER DEFAULT 1")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_skills_bucket ON skills(bucket_id)")

	return &SQLiteVectorStore{db: db}, nil
}

func getBucketID(emb []float32) int {
	if len(emb) < 4 {
		return 0
	}
	bucket := 0
	for i := 0; i < 4; i++ {
		// Quantize into 4 levels: < -0.5, -0.5 to 0, 0 to 0.5, > 0.5
		v := emb[i]
		q := 0
		if v > 0.5 {
			q = 3
		} else if v > 0 {
			q = 2
		} else if v > -0.5 {
			q = 1
		}
		bucket = (bucket << 2) | q
	}
	return bucket
}

func (s *SQLiteVectorStore) SaveSkill(ctx context.Context, description string, code string, embedding []float32) error {
	embeddingBytes, _ := json.Marshal(embedding)
	bucketID := getBucketID(embedding)

	var name sql.NullString
	var skill domain.ExecutableSkill
	if err := json.Unmarshal([]byte(code), &skill); err == nil && skill.Name != "" {
		name.String = skill.Name
		name.Valid = true
	}

	tagsJSON, _ := json.Marshal(skill.ContextTags)

	// When saving a newly synthesized skill, we start with 1 run/success to give it initial confidence
	query := `INSERT INTO skills (name, description, code, bucket_id, embedding_json, success_count, total_runs, total_successes, version, context_tags) 
	          VALUES (?, ?, ?, ?, ?, 1, 1, 1, ?, ?) 
	          ON CONFLICT(description) DO UPDATE SET 
	          code=excluded.code, 
	          embedding_json=excluded.embedding_json,
	          bucket_id=excluded.bucket_id,
	          version=skills.version + 1,
	          context_tags=excluded.context_tags`
	_, err := s.db.ExecContext(ctx, query, name, description, code, bucketID, string(embeddingBytes), skill.Version, string(tagsJSON))

	return err
}

func (s *SQLiteVectorStore) RecordSkillResult(ctx context.Context, name string, success bool, durationMs int64, cause string) error {
	successInc := 0
	if success {
		successInc = 1
	}

	// 1. Get current metadata
	query := `SELECT total_runs, avg_time_ms, failure_causes FROM skills WHERE name = ?`
	var runs int
	var avgTime float64
	var causesJSON string
	err := s.db.QueryRowContext(ctx, query, name).Scan(&runs, &avgTime, &causesJSON)
	if err != nil {
		return err
	}

	// 2. Update stats
	newAvgTime := (avgTime*float64(runs) + float64(durationMs)) / float64(runs+1)
	
	var causes []string
	_ = json.Unmarshal([]byte(causesJSON), &causes)
	if !success && cause != "" {
		found := false
		for _, c := range causes {
			if c == cause {
				found = true
				break
			}
		}
		if !found {
			causes = append(causes, cause)
			if len(causes) > 5 {
				causes = causes[1:]
			}
		}
	}
	newCausesJSON, _ := json.Marshal(causes)

	// 3. Persist
	updateQuery := `UPDATE skills SET total_runs = total_runs + 1, total_successes = total_successes + ?, 
	                avg_time_ms = ?, failure_causes = ? WHERE name = ?`
	_, err = s.db.ExecContext(ctx, updateQuery, successInc, newAvgTime, string(newCausesJSON), name)
	return err
}

func (s *SQLiteVectorStore) RetrieveNamedSkill(ctx context.Context, name string) (*domain.ExecutableSkill, error) {
	query := `SELECT code, total_runs, total_successes, avg_time_ms, failure_causes, required_items, context_tags, version FROM skills WHERE name = ?`
	var code string
	var runs, successes, version int
	var avgTime float64
	var causesJSON, itemsJSON, tagsJSON string
	err := s.db.QueryRowContext(ctx, query, name).Scan(&code, &runs, &successes, &avgTime, &causesJSON, &itemsJSON, &tagsJSON, &version)
	if err != nil {
		return nil, err
	}

	var skill domain.ExecutableSkill
	if err := json.Unmarshal([]byte(code), &skill); err != nil {
		return nil, err
	}

	// Calculate confidence score (0.0 to 1.0)
	if runs > 0 {
		skill.Confidence = float64(successes) / float64(runs)
	} else {
		skill.Confidence = 0.0
	}
	skill.TotalRuns = runs
	skill.TotalSuccesses = successes
	skill.AvgTime = avgTime
	skill.Version = version
	_ = json.Unmarshal([]byte(causesJSON), &skill.FailureCauses)
	_ = json.Unmarshal([]byte(itemsJSON), &skill.RequiredItems)
	_ = json.Unmarshal([]byte(tagsJSON), &skill.ContextTags)

	return &skill, nil
}

func (s *SQLiteVectorStore) RetrieveSkills(ctx context.Context, queryEmbedding []float32, limit int) ([]SkillRecord, error) {
	bucketID := getBucketID(queryEmbedding)

	// Candidate selection: filter by bucket_id and prioritize high success
	// Statistical Ranking: Weight similarity by success rate
	query := `SELECT description, code, embedding_json, total_runs, total_successes, avg_time_ms FROM skills 
	          WHERE bucket_id = ?
	          ORDER BY (CAST(total_successes AS FLOAT) / MAX(total_runs, 1)) DESC, total_successes DESC LIMIT 500`

	rows, err := s.db.QueryContext(ctx, query, bucketID)
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
		var runs, successes int
		var avgTime float64
		if err := rows.Scan(&desc, &code, &embJSON, &runs, &successes, &avgTime); err != nil {
			continue
		}

		var embedding []float32
		if err := json.Unmarshal([]byte(embJSON), &embedding); err != nil {
			continue
		}

		sim := cosineSimilarity(queryEmbedding, embedding)
		
		// Evidence-based weighting: boost similarity for reliable skills
		successRate := float32(successes) / float32(math.Max(float64(runs), 1))
		weightedSim := sim * (0.5 + (0.5 * successRate))

		records = append(records, SkillRecord{
			Description: desc,
			Code:        code,
			Similarity:  weightedSim,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fallback: if bucket is empty or too small, search all as a safety net (limited)
	if len(records) < limit {
		extraQuery := `SELECT description, code, embedding_json, total_runs, total_successes, avg_time_ms FROM skills 
		               WHERE bucket_id != ?
		               ORDER BY (CAST(total_successes AS FLOAT) / MAX(total_runs, 1)) DESC LIMIT ?`
		extraRows, err := s.db.QueryContext(ctx, extraQuery, bucketID, limit*2)
		if err == nil {
			defer extraRows.Close()
			for extraRows.Next() {
				var desc, code, embJSON string
				var runs, successes int
				var avgTime float64
				if err := extraRows.Scan(&desc, &code, &embJSON, &runs, &successes, &avgTime); err == nil {
					var embedding []float32
					if err := json.Unmarshal([]byte(embJSON), &embedding); err == nil {
						sim := cosineSimilarity(queryEmbedding, embedding)
						successRate := float32(successes) / float32(math.Max(float64(runs), 1))
						weightedSim := sim * (0.5 + (0.5 * successRate))
						
						records = append(records, SkillRecord{
							Description: desc,
							Code:        code,
							Similarity:  weightedSim,
						})
					}
				}
			}
		}
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
