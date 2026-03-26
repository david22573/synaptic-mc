package main

import (
	"encoding/json"
	"log/slog"
	"math"
)

type InternalSimulator struct {
	validator *PlanValidator
	logger    *slog.Logger
}

func NewInternalSimulator(logger *slog.Logger) *InternalSimulator {
	return &InternalSimulator{
		validator: NewPlanValidator(),
		logger:    logger.With(slog.String("component", "InternalSimulator")),
	}
}

// RankCandidates simulates the candidate plans, scores them based on cost/risk, and returns the optimal path.
func (s *InternalSimulator) RankCandidates(candidates [][]Action, rawState json.RawMessage) []Action {
	var state GameState
	if err := json.Unmarshal(rawState, &state); err != nil {
		s.logger.Error("Failed to unmarshal raw state in RankCandidates", slog.Any("error", err))
	}

	poiMap := make(map[string]POI)
	for _, p := range state.POIs {
		poiMap[p.Name] = p
	}

	var bestPlan []Action
	bestScore := math.Inf(-1)

	for _, plan := range candidates {
		// 1. Hard Feasibility Check
		mockPlan := &LLMPlan{Tasks: plan}
		if err := s.validator.ValidatePlan(mockPlan, rawState); err != nil {
			continue // Plan is impossible based on game rules, discard immediately
		}

		// 2. Cost / Benefit Heuristic
		score := 100.0 // Base valid score

		for _, task := range plan {
			// Penalize physical distance (prefer targets that are closer)
			if p, exists := poiMap[task.Target.Name]; exists {
				score -= p.Distance * 0.5
			} else if task.Action == "gather" || task.Action == "hunt" || task.Action == "mine" {
				// Penalize interacting with things we can't currently see in the POI list
				score -= 20.0
			}

			// Penalize complexity (longer sequences have a higher risk of failing mid-execution)
			score -= 5.0

			// Reward life-saving priorities
			if state.Health < 10 {
				if task.Action == "eat" {
					score += 50.0
				}
				if task.Action == "retreat" {
					score += 40.0
				}
			}
		}

		if score > bestScore {
			bestScore = score
			bestPlan = plan
		}
	}

	// Fallback: If all plans failed validation, return the first one anyway
	// so the validator can throw the proper localized error up the retry chain.
	if bestPlan == nil && len(candidates) > 0 {
		return candidates[0]
	}

	return bestPlan
}
