package decision

import (
	"context"
	"david22573/synaptic-mc/internal/domain"
	"fmt"
)

// Engine is a pure pipeline. It takes the current state and returns a validated, simulated, and policy-checked plan.
// It holds NO state itself. It reads from Memory/Context and returns a decision.
type Engine interface {
	Evaluate(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) (*domain.Plan, error)
}

// Pipeline implements the DecisionEngine with a strict multi-step process.
type Pipeline struct {
	planner   Planner
	validator Validator
	simulator Simulator
	policy    PolicyEnforcer
}

func NewPipeline(p Planner, v Validator, s Simulator, pe PolicyEnforcer) *Pipeline {
	return &Pipeline{
		planner:   p,
		validator: v,
		simulator: s,
		policy:    pe,
	}
}

func (p *Pipeline) Evaluate(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) (*domain.Plan, error) {
	// 1. Plan: Generate raw candidate plans (LLM or Fallback)
	rawPlan, err := p.planner.Generate(ctx, sessionID, state)
	if err != nil {
		return nil, fmt.Errorf("planning phase failed: %w", err)
	}

	// 2. Validate: Enforce strict game mechanics (e.g., Do I have the required tools?)
	if err := p.validator.Validate(rawPlan, state); err != nil {
		return nil, fmt.Errorf("validation phase failed: %w", err)
	}

	// 3. Simulate: Rank the candidates and collapse them into the optimal path
	optimalPlan := p.simulator.RankAndSelect(rawPlan, state)

	// 4. Policy: Enforce hard guardrails (e.g., Never dig straight down, Never attack Iron Golems)
	if err := p.policy.Enforce(optimalPlan, state); err != nil {
		return nil, fmt.Errorf("policy rejection: %w", err)
	}

	// Tag the final plan with the execution trace
	for i := range optimalPlan.Tasks {
		optimalPlan.Tasks[i].Trace = trace
	}

	return optimalPlan, nil
}

// Interfaces for the internal steps to keep the pipeline decoupled and testable
type Planner interface {
	Generate(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error)
}

type Validator interface {
	Validate(plan *domain.Plan, state domain.GameState) error
}

type Simulator interface {
	RankAndSelect(plan *domain.Plan, state domain.GameState) *domain.Plan
}

type PolicyEnforcer interface {
	Enforce(plan *domain.Plan, state domain.GameState) error
}
