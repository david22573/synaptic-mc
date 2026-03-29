package pipeline

import (
	"context"
	"fmt"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/policy"
)

type PolicyStage struct {
	engine policy.Engine
}

func NewPolicyStage(engine policy.Engine) *PolicyStage {
	return &PolicyStage{engine: engine}
}

func (s *PolicyStage) Process(ctx context.Context, state *PipelineState) error {
	// FIX: Check for nil Normalized plan
	if state.Normalized == nil {
		return fmt.Errorf("cannot run policy stage: normalized plan is nil")
	}

	if state.Simulation == nil {
		return fmt.Errorf("cannot run policy stage without simulation artifact")
	}

	candidatePlan := &domain.Plan{
		Objective: state.Normalized.Objective,
		Tasks:     state.Simulation.OptimizedTasks,
	}

	input := policy.DecisionInput{
		Plan:  candidatePlan,
		State: state.GameState,
	}

	// FIX: Respect context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	decision := s.engine.Decide(ctx, input)

	state.Policy = &PolicyDecision{
		IsApproved: decision.IsApproved,
		Reason:     decision.Reason,
	}

	if !decision.IsApproved {
		return fmt.Errorf("policy rejection: %s", decision.Reason)
	}

	state.FinalPlan = candidatePlan
	return nil
}
