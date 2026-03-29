// internal/pipeline/simulate.go
package pipeline

import (
	"context"

	"david22573/synaptic-mc/internal/domain"
)

// SimulateStage evaluates risk and trims overly dangerous or complex tails from the plan.
type SimulateStage struct{}

func NewSimulateStage() *SimulateStage {
	return &SimulateStage{}
}

func (s *SimulateStage) Process(ctx context.Context, state *PipelineState) error {
	if state.Validation == nil || !state.Validation.IsValid {
		state.Simulation = &SimulationResult{
			OptimizedTasks: []domain.Action{},
			RiskScore:      0.0,
		}
		return nil
	}

	poiMap := make(map[string]domain.POI)
	for _, p := range state.GameState.POIs {
		poiMap[p.Name] = p
	}

	optimizedTasks := make([]domain.Action, 0, len(state.Normalized.Tasks))
	accumulatedRisk := 0.0

	for _, task := range state.Normalized.Tasks {
		stepRisk := 1.0

		// Penalize physical distance
		if p, exists := poiMap[task.Target.Name]; exists {
			stepRisk += p.Distance * 0.1
		}

		if task.Action == "hunt" {
			stepRisk += 5.0
		}

		accumulatedRisk += stepRisk

		// Threshold truncation
		if accumulatedRisk > 15.0 && len(optimizedTasks) > 0 {
			break
		}

		optimizedTasks = append(optimizedTasks, task)
	}

	state.Simulation = &SimulationResult{
		OptimizedTasks: optimizedTasks,
		RiskScore:      accumulatedRisk,
	}

	return nil
}
