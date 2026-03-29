// internal/policy/engine.go
package policy

import (
	"context"

	"david22573/synaptic-mc/internal/domain"
)

// DecisionInput encapsulates everything the policy engine needs to make a ruling.
type DecisionInput struct {
	Plan  *domain.Plan
	State domain.GameState
}

// Decision represents the authoritative ruling from the policy engine.
type Decision struct {
	IsApproved   bool
	Reason       string
	OverridePlan *domain.Plan // Optional: A fallback plan if the original is rejected
}

// Engine defines the contract for all behavioral guardrails.
type Engine interface {
	Decide(ctx context.Context, input DecisionInput) Decision
}
