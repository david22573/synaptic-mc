// internal/voyager/skills.go
package voyager

import (
	"context"
	"encoding/json"
	"fmt"

	"david22573/synaptic-mc/internal/domain"
)

// ExecutableSkill represents a JS function stored in the vector database.
type ExecutableSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	JSCode      string `json:"js_code"`
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

// SaveSkill stores an executable JS behavior back into the vector database.
func (sm *SkillManager) SaveSkill(ctx context.Context, skill ExecutableSkill) error {
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
