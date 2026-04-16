// internal/voyager/skills.go
package voyager

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

// ExecutableSkill represents a JS function stored in the vector database.
type ExecutableSkill struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	JSCode        string   `json:"js_code"`
	Confidence    float64  `json:"confidence"`
	Version       int      `json:"version"`
	AvgTime       float64  `json:"avg_time_ms"`
	RequiredItems []string `json:"required_items"`
	FailureCauses []string `json:"failure_causes"`
}

// SkillManager bridges the vector store and the decision planner.
type SkillManager struct {
	store  VectorStore
	client LLMClient
}

func NewSkillManager(store VectorStore, client LLMClient) *SkillManager {
	return &SkillManager{
		store:  store,
		client: client,
	}
}

// RetrieveSkills implements the domain.SkillRetriever interface.
func (sm *SkillManager) RetrieveSkills(ctx context.Context, query string, limit int) ([]domain.SkillRecord, error) {
	emb, err := sm.client.CreateEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed skill query: %w", err)
	}

	records, err := sm.store.RetrieveSkills(ctx, emb, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve skills from vector db: %w", err)
	}

	var results []domain.SkillRecord
	for _, r := range records {
		var skill ExecutableSkill
		// Expecting the Code field to store the JSON representation of ExecutableSkill
		if err := json.Unmarshal([]byte(r.Code), &skill); err == nil && skill.Name != "" {
			results = append(results, domain.SkillRecord{
				Name:        skill.Name,
				Description: skill.Description,
			})
		}
	}

	return results, nil
}

// ValidateSkill performs static analysis and dry-runs to ensure safety.
func (sm *SkillManager) ValidateSkill(ctx context.Context, jsCode string) error {
	// A-1: Static Lint: check for banned patterns
	bannedPatterns := []string{
		"bot.quit",
		"bot.drop",
		"eval(",
		"require(",
		"process.exit",
		"process.kill",
	}
	for _, pattern := range bannedPatterns {
		if strings.Contains(jsCode, pattern) {
			return fmt.Errorf("skill contains banned pattern: %s", pattern)
		}
	}

	// Basic check: must look like an async function expression
	trimmed := strings.TrimSpace(jsCode)
	if !strings.HasPrefix(trimmed, "async") {
		return fmt.Errorf("skill must be an async function (starts with 'async')")
	}

	// A-1: Dry-run in headless mock environment
	// This ensures the code is syntactically valid and compiles.
	cmd := exec.CommandContext(ctx, "npx", "tsx", "js/scripts/validate_skill.ts", jsCode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dry-run validation failed: %s", string(output))
	}

	return nil
}

// SaveSkill stores an executable JS behavior back into the vector database.
func (sm *SkillManager) SaveSkill(ctx context.Context, skill ExecutableSkill) error {
	// A-1: Validate before saving
	if err := sm.ValidateSkill(ctx, skill.JSCode); err != nil {
		return fmt.Errorf("skill validation failed: %w", err)
	}

	emb, err := sm.client.CreateEmbedding(ctx, skill.Description)
	if err != nil {
		return fmt.Errorf("failed to embed skill description: %w", err)
	}

	codeBytes, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("failed to serialize skill: %w", err)
	}

	return sm.store.SaveSkill(ctx, skill.Description, string(codeBytes), emb)
}

func (sm *SkillManager) RecordSkillResult(ctx context.Context, name string, success bool, durationMs int64, cause string) error {
	return sm.store.RecordSkillResult(ctx, name, success, durationMs, cause)
}
