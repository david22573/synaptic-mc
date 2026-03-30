package pipeline

import (
	"context"

	"david22573/synaptic-mc/internal/domain"
)

type ValidationResult struct {
	IsValid bool
	Errors  []error
}

type SimulationResult struct {
	OptimizedTasks []domain.Action
	RiskScore      float64
}

type PolicyDecision struct {
	IsApproved bool
	Reason     string
}

type PipelineState struct {
	Trace      domain.TraceContext
	Plan       *domain.Plan
	GameState  domain.GameState
	Normalized *domain.Plan
	Perception *PerceptionResult
	Validation *ValidationResult
	Simulation *SimulationResult // NEW
	Policy     *PolicyDecision   // NEW
	FinalPlan  *domain.Plan      // NEW
}

type Stage interface {
	Name() string
	Process(ctx context.Context, input PipelineState) (PipelineState, error)
}
