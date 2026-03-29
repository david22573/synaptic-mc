package decision

import (
	"david22573/synaptic-mc/internal/domain"
)

// CostSimulator evaluates the validated plan and optimizes it (e.g., dropping overly risky tail-end tasks).
type CostSimulator struct{}

func NewCostSimulator() *CostSimulator {
	return &CostSimulator{}
}

func (s *CostSimulator) RankAndSelect(plan *domain.Plan, state domain.GameState) *domain.Plan {
	if plan == nil || len(plan.Tasks) == 0 {
		return plan
	}

	poiMap := make(map[string]domain.POI)
	for _, p := range state.POIs {
		poiMap[p.Name] = p
	}

	optimizedTasks := make([]domain.Action, 0, len(plan.Tasks))
	accumulatedRisk := 0.0

	// We iterate through the chain. If a step pushes the cumulative risk too high,
	// we truncate the plan early to prevent mid-execution failures.
	for _, task := range plan.Tasks {
		stepRisk := 1.0

		// Penalize physical distance [cite: 161]
		if p, exists := poiMap[task.Target.Name]; exists {
			stepRisk += p.Distance * 0.1
		}

		if task.Action == "hunt" {
			stepRisk += 5.0
		}

		accumulatedRisk += stepRisk

		// If the chain gets too complex or dangerous, stop adding tasks.
		// The orchestrator will replan naturally after these complete.
		if accumulatedRisk > 15.0 && len(optimizedTasks) > 0 {
			break
		}

		optimizedTasks = append(optimizedTasks, task)
	}

	plan.Tasks = optimizedTasks
	return plan
}
