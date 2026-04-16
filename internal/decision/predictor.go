package decision

import (
	"context"
	"strings"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
)

// StrategyPredictor handles future-state forecasting for decision making.
type StrategyPredictor struct {
	memStore memory.Store
}

func NewStrategyPredictor(m memory.Store) *StrategyPredictor {
	return &StrategyPredictor{memStore: m}
}

// ForecastInventory predicts if the bot will run out of resources during a plan.
func (p *StrategyPredictor) ForecastInventory(plan domain.Plan, state domain.GameState) string {
	// Simple forecasting logic
	if strings.Contains(strings.ToLower(plan.Objective), "mine") {
		hasPickaxe := false
		for _, item := range state.Inventory {
			if strings.Contains(item.Name, "pickaxe") {
				hasPickaxe = true
				break
			}
		}
		if !hasPickaxe {
			return "INVENTORY_CRITICAL: Mining planned but no pickaxe in inventory"
		}
	}
	return ""
}

// PredictNightfall checks if the bot needs to seek shelter.
func (p *StrategyPredictor) PredictNightfall(state domain.GameState) bool {
	// Minecraft night starts around 13000
	return state.TimeOfDay > 11000 && state.TimeOfDay < 13000
}

// RouteScorer evaluates travel paths based on known world nodes and safety.
type RouteScorer struct{}

func (s *RouteScorer) ScoreRoute(ctx context.Context, from, to domain.Vec3, world *domain.WorldModel) float64 {
	dist := from.DistanceTo(to)
	
	// Higher zone cost = lower score
	cost := world.GetZoneCost(domain.Location{X: to.X, Y: to.Y, Z: to.Z})
	
	// Simple heuristic: distance + danger penalty
	return 100.0 - (dist * 0.1) - (cost * 20.0)
}
