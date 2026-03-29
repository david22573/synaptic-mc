package domain

// ActionIntent replaces the lower-level Action struct for the Voyager loop.
// It enforces strict expectations so the Critic knows exactly what to measure.
type ActionIntent struct {
	ID        string `json:"id"`
	Action    string `json:"action"`    // "mine", "craft", "explore", "hunt"
	Target    string `json:"target"`    // e.g., "iron_ore", "oak_planks"
	Count     int    `json:"count"`     // Expected quantity change
	Rationale string `json:"rationale"` // LLM's reasoning for this intent
}

// TaskHistory stores the full lifecycle of a task for Phase 4 Vector Memory.
type TaskHistory struct {
	Intent   ActionIntent
	Success  bool
	Critique string
}
