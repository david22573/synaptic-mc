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

func (s *PolicyStage) Name() string {
	return "Policy"
}

func (s *PolicyStage) Process(ctx context.Context, input PipelineState) (PipelineState, error) {
	output := input

	if input.Normalized == nil {
		return output, fmt.Errorf("cannot run policy stage: normalized plan is nil")
	}

	if input.Simulation == nil {
		return output, fmt.Errorf("cannot run policy stage without simulation artifact")
	}

	candidatePlan := &domain.Plan{
		Objective: input.Normalized.Objective,
		Tasks:     input.Simulation.OptimizedTasks,
	}

	decisionInput := policy.DecisionInput{
		Plan:  candidatePlan,
		State: input.GameState,
	}

	select {
	case <-ctx.Done():
		return output, ctx.Err()
	default:
	}

	decision := s.engine.Decide(ctx, decisionInput)

	output.Policy = &PolicyDecision{
		IsApproved: decision.IsApproved,
		Reason:     decision.Reason,
	}

	if !decision.IsApproved {
		if decision.OverridePlan != nil {
			output.FinalPlan = decision.OverridePlan
			return output, nil
		}
		return output, fmt.Errorf("policy rejection: %s", decision.Reason)
	}

	output.FinalPlan = candidatePlan
	return output, nil
}
