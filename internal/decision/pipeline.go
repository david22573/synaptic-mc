// internal/decision/pipeline.go
package decision

import (
	"context"
	"fmt"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/pipeline"
	"david22573/synaptic-mc/internal/policy"
)

type Stage interface {
	Process(ctx context.Context, frame EvaluationFrame, plan *domain.Plan) (*domain.Plan, error)
}

// Pipeline implements the DecisionEngine with a strict multi-step process.
type Pipeline struct {
	planner Planner
	stages  []pipeline.Stage
}

func NewPipeline(p Planner, policyEngine policy.Engine) *Pipeline {
	return &Pipeline{
		planner: p,
		stages: []pipeline.Stage{
			pipeline.NewNormalizeStage(),
			pipeline.NewValidateStage(),
			pipeline.NewSimulateStage(),
			pipeline.NewPolicyStage(policyEngine), // Policy is now officially in the pipeline
		},
	}
}

func (p *Pipeline) Evaluate(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) (*domain.Plan, error) {
	rawPlan, err := p.planner.Generate(ctx, sessionID, state)
	if err != nil {
		return nil, fmt.Errorf("planning phase failed: %w", err)
	}

	pipeState := &pipeline.PipelineState{
		SessionID: sessionID,
		GameState: state,
		Trace:     trace,
		Plan:      rawPlan,
	}

	// Execute Pure Stages: Normalize -> Validate -> Simulate -> Policy
	for _, stage := range p.stages {
		if err := stage.Process(ctx, pipeState); err != nil {
			return nil, fmt.Errorf("pipeline stage failed: %w", err)
		}
	}

	if pipeState.Validation != nil && !pipeState.Validation.IsValid {
		return nil, fmt.Errorf("validation phase failed: %v", pipeState.Validation.Errors)
	}

	return pipeState.FinalPlan, nil
}
