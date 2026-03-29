// internal/decision/engine.go
package decision

import (
	"context"

	"david22573/synaptic-mc/internal/domain"
)

// Engine is a pure pipeline wrapper.
// It takes the current state and returns a validated, simulated, and policy-checked plan.
// It holds NO state itself.
type Engine interface {
	Evaluate(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) (*domain.Plan, error)
}

type Planner interface {
	Generate(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error)
}

type PolicyEnforcer interface {
	Enforce(plan *domain.Plan, state domain.GameState) error
}
