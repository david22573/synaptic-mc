package decision

import (
	"context"
	"log/slog"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/pipeline"
	"david22573/synaptic-mc/internal/policy"
)

// Engine manages the sequential execution of decision stages.
type Engine struct {
	stages []pipeline.Stage
}

// NewEngine constructs the complete decision pipeline.
func NewEngine(store domain.EventStore, sessionID string, logger *slog.Logger) *Engine {
	return &Engine{
		stages: []pipeline.Stage{
			pipeline.NewNormalizeStage(),
			pipeline.NewPerceptionStage(),
			pipeline.NewValidateStage(),
			pipeline.NewSimulateStage(), // NEW: Triggers the risk simulation
			pipeline.NewPolicyStage(
				policy.NewCompositePolicy(
					policy.NewIntelligencePolicy(store, sessionID, logger),
				),
			),
		},
	}
}

// Execute runs the input state through all configured stages.
func (e *Engine) Execute(ctx context.Context, input pipeline.PipelineState) (pipeline.PipelineState, error) {
	var err error
	current := input

	for _, stage := range e.stages {
		current, err = stage.Process(ctx, current)
		if err != nil {
			return current, err
		}

		if current.Validation != nil && !current.Validation.IsValid {
			break
		}
	}

	return current, nil
}
