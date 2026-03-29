package pipeline

import (
	"context"

	"david22573/synaptic-mc/internal/domain"
)

// ValidationResult captures the outcome of the validation stage.
type ValidationResult struct {
	IsValid bool
	Errors  []error
}

// SimulationResult captures the optimized tasks and risk metrics.
type SimulationResult struct {
	OptimizedTasks []domain.Action
	RiskScore      float64
}

// PolicyDecision captures hard constraint checks.
type PolicyDecision struct {
	IsApproved bool
	Reason     string
}

// StageSnapshot captures the exact state before and after a stage for deterministic debugging.
type StageSnapshot struct {
	StageName string
	Input     PipelineState
	Output    PipelineState
}

// Stage represents a single, side-effect-free step in the decision pipeline.
// Implementations must take an input state and return a completely new, mutated copy.
type Stage interface {
	Name() string
	Process(ctx context.Context, input PipelineState) (PipelineState, error)
}

// PipelineState holds all decision artifacts.
// CRITICAL: Passed by value through the pipeline to guarantee immutability.
type PipelineState struct {
	// Context
	SessionID string
	GameState domain.GameState
	Trace     domain.TraceContext

	// Artifacts
	Plan       *domain.Plan
	Normalized *domain.Plan
	Validation *ValidationResult
	Simulation *SimulationResult
	Policy     *PolicyDecision
	FinalPlan  *domain.Plan
}
