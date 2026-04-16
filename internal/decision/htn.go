package decision

import (
	"context"
	"fmt"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/state"
)

type HTNPlanner struct {
	worldModel *domain.WorldModel
}

func NewHTNPlanner(wm *domain.WorldModel) *HTNPlanner {
	return &HTNPlanner{worldModel: wm}
}

func (h *HTNPlanner) Decompose(ctx context.Context, goal string, state domain.GameState, danger state.DangerState) (*domain.Plan, error) {
	switch goal {
	case "survive":
		return h.planSurvive(ctx, state, danger)
	case "progression":
		return h.planProgression(ctx, state)
	default:
		return nil, fmt.Errorf("unknown high-level goal: %s", goal)
	}
}

func (h *HTNPlanner) planSurvive(ctx context.Context, s domain.GameState, danger state.DangerState) (*domain.Plan, error) {
	plan := &domain.Plan{
		StrategicGoal: "Survival",
		Objective:     "Decomposing Survival Goal",
	}

	if danger == state.DangerEscape || danger == state.DangerAlert {
		plan.Tasks = []domain.Action{{
			ID:        fmt.Sprintf("htn-escape-%d", time.Now().UnixNano()),
			Action:    "retreat",
			Target:    domain.Target{Name: "safe_zone"},
			Priority:  100,
			Rationale: "HTN: Immediate danger detected, initiating escape",
		}}
	} else if s.Health < 10 {
		plan.Tasks = []domain.Action{{
			ID:        fmt.Sprintf("htn-heal-%d", time.Now().UnixNano()),
			Action:    "eat",
			Target:    domain.Target{Name: "food"},
			Priority:  90,
			Rationale: "HTN: Health low, prioritizing recovery",
		}}
	}

	return plan, nil
}

func (h *HTNPlanner) planProgression(ctx context.Context, s domain.GameState) (*domain.Plan, error) {
	// Simple HTN for early game
	hasWood := false
	for _, item := range s.Inventory {
		if item.Name == "oak_log" || item.Name == "oak_planks" {
			hasWood = true
			break
		}
	}

	if !hasWood {
		return &domain.Plan{
			StrategicGoal: "Progression",
			Objective:     "Gather Wood",
			Tasks: []domain.Action{{
				ID:        fmt.Sprintf("htn-wood-%d", time.Now().UnixNano()),
				Action:    "gather",
				Target:    domain.Target{Name: "log", Type: "category"},
				Priority:  50,
				Rationale: "HTN: Base resource required for progression",
			}},
		}, nil
	}

	return nil, nil // Fallback to LLM
}
