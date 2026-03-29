package decision

import "david22573/synaptic-mc/internal/domain"

// EvaluationFrame is the immutable snapshot the entire pipeline works against.
type EvaluationFrame struct {
	SessionID string
	State     domain.GameState
	Trace     domain.TraceContext
	History   []domain.DomainEvent // For context-aware decisions
}

// Outcome is the final, authoritative result of the pipeline.
type Outcome struct {
	Plan     *domain.Plan
	Strategy string
	Priority int
	Metadata map[string]any
}
