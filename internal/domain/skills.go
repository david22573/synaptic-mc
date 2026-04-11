// internal/domain/skills.go
package domain

import "context"

// ExecutableSkill replaces the old StoredSkill intent-sequence concept.
type ExecutableSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	JSCode      string `json:"js_code"`
}

type SkillRecord struct {
	Name        string
	Description string
}

type SkillRetriever interface {
	RetrieveSkills(ctx context.Context, query string, limit int) ([]SkillRecord, error)
}
