// internal/pipeline/policy.go
package pipeline

import (
	"context"
	"fmt"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/policy"
)

// PolicyStage applies the authoritative policy engine against the optimized plan.
type PolicyStage struct {
	engine policy.Engine
}

func NewPolicyStage(engine policy.Engine) *PolicyStage {
	return &PolicyStage{engine: engine}
}

func (s *PolicyStage) Process(ctx context.Context, state *PipelineState) error {
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

	decision := s.engine.Decide(ctx, input)

	state.Policy = &PolicyDecision{
		IsApproved: decision.IsApproved,
		Reason:     decision.Reason,
	}

	if !decision.IsApproved {
		return fmt.Errorf("policy rejection: %s", decision.Reason)
	}

	// The plan survives the entire pipeline. Lock it in.
	state.FinalPlan = candidatePlan
	return nil
}
