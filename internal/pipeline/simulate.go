package pipeline

import (
	"context"

	"david22573/synaptic-mc/internal/domain"
)

type SimulateStage struct{}

func NewSimulateStage() *SimulateStage {
	return &SimulateStage{}
}

func (s *SimulateStage) Name() string {
	return "Simulate"
}

func (s *SimulateStage) Process(ctx context.Context, input PipelineState) (PipelineState, error) {
	output := input

	if input.Validation == nil || !input.Validation.IsValid {
		output.Simulation = &SimulationResult{
			OptimizedTasks: []domain.Action{},
			RiskScore:      0.0,
		}
		return output, nil
	}

	poiMap := make(map[string]domain.POI)
	for _, p := range input.GameState.POIs {
		poiMap[p.Name] = p
	}

	optimizedTasks := make([]domain.Action, 0, len(input.Normalized.Tasks))
	accumulatedRisk := 0.0

	for _, task := range input.Normalized.Tasks {
		stepRisk := 1.0

		if p, exists := poiMap[task.Target.Name]; exists {
			stepRisk += p.Distance * 0.1
		}

		if task.Action == "hunt" {
			stepRisk += 5.0
		}

		accumulatedRisk += stepRisk

		if accumulatedRisk > 15.0 && len(optimizedTasks) > 0 {
			break
		}

		optimizedTasks = append(optimizedTasks, task)
	}

	output.Simulation = &SimulationResult{
		OptimizedTasks: optimizedTasks,
		RiskScore:      accumulatedRisk,
	}

	return output, nil
}
