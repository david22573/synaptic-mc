package pipeline

import (
	"context"
	"sort"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type ScoredPOI struct {
	domain.POI
	Score float64
}

type PerceptionResult struct {
	RankedPOIs []ScoredPOI
	TopTarget  *ScoredPOI
}

type PerceptionStage struct{}

func NewPerceptionStage() *PerceptionStage {
	return &PerceptionStage{}
}

func (s *PerceptionStage) Name() string {
	return "Perception_Scoring"
}

func (s *PerceptionStage) Process(ctx context.Context, input PipelineState) (PipelineState, error) {
	output := input
	state := input.GameState

	var scored []ScoredPOI

	// Check biological imperatives
	isStarving := state.Food < 8
	isDying := state.Health < 10
	isNight := state.TimeOfDay > 13000 && state.TimeOfDay < 23000

	// Check inventory for basic tools
	hasWeapon := false
	for _, item := range state.Inventory {
		if strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") {
			hasWeapon = true
			break
		}
	}

	for _, poi := range state.POIs {
		// Base score heavily prefers closer objects
		score := 100.0 / (poi.Distance + 1.0)

		name := strings.ToLower(poi.Name)

		// 1. Survival & Threat Assessment
		isHostile := strings.Contains(name, "zombie") || strings.Contains(name, "skeleton") || strings.Contains(name, "creeper") || strings.Contains(name, "spider")
		if isHostile {
			if isDying || !hasWeapon {
				score -= 1000.0 // Avoid at all costs
			} else {
				score += 50.0 // Potential target if healthy and armed
			}
		}

		// 2. Hunger Drive
		isFoodSource := strings.Contains(name, "pig") || strings.Contains(name, "cow") || strings.Contains(name, "sheep") || strings.Contains(name, "chicken") || strings.Contains(name, "wheat") || strings.Contains(name, "carrot")
		if isFoodSource {
			if isStarving {
				score += 800.0 // Hyper-fixate on food
			} else {
				score += 20.0
			}
		}

		// 3. Shelter Drive
		if isNight && (strings.Contains(name, "bed") || strings.Contains(name, "door")) {
			score += 500.0
		}

		scored = append(scored, ScoredPOI{
			POI:   poi,
			Score: score,
		})
	}

	// Sort by highest score first
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	result := PerceptionResult{
		RankedPOIs: scored,
	}

	if len(scored) > 0 && scored[0].Score > 0 {
		result.TopTarget = &scored[0]
	}

	output.Perception = &result
	return output, nil
}
