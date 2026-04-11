// internal/domain/intent.go
package domain

import "time"

// ActionIntent replaces the lower-level Action struct for the Voyager loop.
// It enforces strict expectations so the Critic knows exactly what to measure.
type ActionIntent struct {
	ID             string         `json:"id"`
	Action         string         `json:"action"` // "mine", "craft", "explore", "hunt", "use_skill"
	Target         string         `json:"target"` // e.g., "iron_ore", "oak_planks", or skill name
	SkillSteps     []ActionIntent `json:"skill_steps,omitempty"`
	Count          int            `json:"count"`                     // Expected quantity change
	Rationale      string         `json:"rationale"`                 // LLM's reasoning for this intent
	TargetLocation *Location      `json:"target_location,omitempty"` // Pointer so it can be nil for non-spatial tasks like crafting

	// Phase 8: Composable Skills
	SkillName string `json:"skill_name,omitempty"`
}

// StoredSkill represents a named multi-step program for the LLM.
type StoredSkill struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Steps        []ActionIntent `json:"steps"`
	SuccessCount int            `json:"success_count"`
}

// ProgressionMilestone represents a tech-tree achievement or unlocked capability.
type ProgressionMilestone struct {
	Name       string    `json:"name"`
	UnlockedAt time.Time `json:"unlocked_at"`
}

// TaskHistory stores the full lifecycle of a task for Phase 4 Vector Memory.
type TaskHistory struct {
	Intent   ActionIntent
	Success  bool
	Critique string
}
