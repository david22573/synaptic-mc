// internal/domain/skills.go
package domain

import "context"

// ExecutableSkill replaces the old StoredSkill intent-sequence concept.
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

type SkillRecord struct {
	Name        string
	Description string
}

type SkillRetriever interface {
	RetrieveSkills(ctx context.Context, query string, limit int) ([]SkillRecord, error)
}
