// internal/decision/evaluator.go
package decision

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/state"
)

func (s *Service) runEvaluationLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.evalTrigger:
			s.doEvaluateNextPlan(ctx)
		}
	}
}

func (s *Service) evaluateNextPlan() {
	select {
	case s.evalTrigger <- struct{}{}:
	default:
	}
}

func (s *Service) doEvaluateNextPlan(ctx context.Context) {
	gs := s.stateProvider.GetCurrentState().State
	if gs.Health <= 0 {
		return
	}

	// Phase 3: Danger State Machine
	dangerState := s.arbiter.UpdateDanger(gs)

	// Near-Cheating: Predictive Nightfall Prep
	nightfall := s.predictor.PredictNightfall(gs)

	// TIER 1: Instant Reaction (Hardcoded Heuristics - No LLM)
	survivalOverride := dangerState == state.DangerEscape ||
		dangerState == state.DangerAlert ||
		(gs.Health < domain.DecisionHealthSafe && gs.Food < domain.SurvivalMinFoodForHunt) ||
		nightfall

	if survivalOverride {
		s.handleTier1Reaction(ctx, gs, nightfall)
		return
	}

	// Not a survival situation, check if we should even evaluate
	if s.activeIntent.Load() != nil || s.planManager.HasActivePlan() || !s.execStatus.IsIdle() {
		return
	}

	// TIER 2: Fast Heuristic (Check if simple goal suffices before calling LLM)
	if plan, ok := s.tryFastHeuristic(ctx, gs); ok {
		s.dispatchPlan(ctx, plan)
		return
	}

	// TIER 3: Strategic Planning (Full LLM Reasoning)
	if s.modeManager.GetMode() == ModeCrisis {
		s.logger.Warn("System in CRISIS mode, skipping Tier 3 strategic reasoning")
		return
	}
	s.handleTier3Strategic(ctx, gs)
}

func (s *Service) handleTier1Reaction(ctx context.Context, gs domain.GameState, nightfall bool) {
	s.logger.Warn("Survival priority override active (Tier 1)")
	
	// Prevent lock breaking if we're already executing a survival task
	if currCommitment := s.commitment.Load(); currCommitment != nil {
		if time.Since(currCommitment.StartTime) < currCommitment.MinDuration {
			activeIntent := s.activeIntent.Load()
			if activeIntent != nil && (activeIntent.Action == "retreat" || activeIntent.Action == "emergency_reflex" || activeIntent.Action == "random_walk") {
				return
			}
			currentPlan := s.planManager.GetCurrent()
			if currentPlan != nil && (currentPlan.Objective == "Reactive Fallback Plan" || currentPlan.Objective == "Degraded Recovery state") {
				return
			}
		}
	}

	var plan domain.Plan
	fails := s.planner.GetFailureCount("Reactive Fallback Plan")
	
	if fails > 3 {
		s.logger.Warn("Survival override stuck in failure loop, degrading (Tier 1)")
		plan = domain.Plan{
			Objective:  "Degraded Recovery state",
			IsFallback: true,
			Tasks: []domain.Action{
				{
					ID:        fmt.Sprintf("panic-walk-%d", time.Now().UnixNano()),
					Action:    "random_walk",
					Target:    domain.Target{Name: "none", Type: "none"},
					Priority:  100,
					Rationale: "A* pathfinding stuck; forcing blind motor movement.",
				},
			},
		}
	} else {
		plan = s.planner.reactivePlan(ctx, gs)
	}

	if nightfall && plan.Objective != "Degraded Recovery state" {
		plan.Objective = "Seek shelter for nightfall"
		plan.Tasks = append([]domain.Action{{
			ID:        fmt.Sprintf("night-prep-%d", time.Now().UnixNano()),
			Action:    "retreat",
			Target:    domain.Target{Name: "safe_base"},
			Priority:  90,
			Rationale: "Near-Cheating: Psychic nightfall prediction",
		}}, plan.Tasks...)
	}

	s.dispatchPlan(ctx, plan)
}

func (s *Service) tryFastHeuristic(ctx context.Context, gs domain.GameState) (domain.Plan, bool) {
	// Simple wood/food gathering if we have none and are in a safe spot
	inventory := inventoryMap(gs)
	
	if inventory["oak_log"] == 0 && inventory["birch_log"] == 0 && inventory["spruce_log"] == 0 {
		return domain.Plan{
			Objective: "Acquire basic resources (Fast Tier)",
			Tasks: []domain.Action{{
				ID:        fmt.Sprintf("fast-gather-%d", time.Now().UnixNano()),
				Action:    "gather",
				Target:    domain.Target{Name: "log", Type: "category"},
				Priority:  60,
				Rationale: "Initial resource acquisition via fast heuristic",
			}},
		}, true
	}

	return domain.Plan{}, false
}

func (s *Service) handleTier3Strategic(ctx context.Context, gs domain.GameState) {
	// Phase 7: Hierarchical Task Network (HTN)
	// Try deterministic HTN decomposition for common high-level goals first
	danger := s.arbiter.GetDangerState()
	
	if danger != state.DangerSafe {
		if htnPlan, err := s.htn.Decompose(ctx, "survive", gs, danger); err == nil && htnPlan != nil && len(htnPlan.Tasks) > 0 {
			s.logger.Info("HTN: Decomposed survival goal", slog.String("objective", htnPlan.Objective))
			s.dispatchPlan(ctx, *htnPlan)
			return
		}
	}

	if isProgressionMode(danger, gs) {
		if htnPlan, err := s.htn.Decompose(ctx, "progression", gs, danger); err == nil && htnPlan != nil && len(htnPlan.Tasks) > 0 {
			s.logger.Info("HTN: Decomposed progression goal", slog.String("objective", htnPlan.Objective))
			s.dispatchPlan(ctx, *htnPlan)
			return
		}
	}

	var plan domain.Plan
	var proposedIntent *domain.ActionIntent
	var proposeErr error

	if isProgressionMode(danger, gs) && s.curriculum != nil {
		s.logger.Info("Stable state detected, curriculum driving progression (Tier 3)")

		proposedIntent, proposeErr = s.curriculum.ProposeTask(ctx, gs, s.getTaskHistory(), s.getMilestoneContext(), s.sessionID, s.flags.CurriculumHorizon)
		if proposeErr == nil && proposedIntent != nil && isValidCurriculumIntent(gs, proposedIntent) {
			if proposedIntent.Action == "use_skill" && len(proposedIntent.SkillSteps) > 0 {
				plan = domain.Plan{
					Objective:  proposedIntent.Rationale,
					IsFallback: false,
					Tasks:      make([]domain.Action, len(proposedIntent.SkillSteps)),
				}
				for i, step := range proposedIntent.SkillSteps {
					plan.Tasks[i] = domain.Action{
						ID:        fmt.Sprintf("%s-step-%d", proposedIntent.ID, i),
						Action:    step.Action,
						Target:    domain.Target{Name: step.Target, Type: "skill_step"},
						Count:     step.Count,
						Rationale: step.Rationale,
						Priority:  75 - i,
					}
				}
			} else {
				plan = domain.Plan{
					Objective:  proposedIntent.Rationale,
					IsFallback: false,
					Tasks: []domain.Action{{
						ID:        fmt.Sprintf("curr-%d", time.Now().UnixNano()),
						Action:    proposedIntent.Action,
						Target:    domain.Target{Name: proposedIntent.Target, Type: "curriculum"},
						Count:     proposedIntent.Count,
						Rationale: proposedIntent.Rationale,
						Priority:  70,
					}},
				}
			}
		} else {
			plan = s.planner.FastPlan(ctx, gs)
		}
	} else {
		plan = s.planner.FastPlan(ctx, gs)
	}

	if !Validate(&plan, gs) {
		plan.IsFallback = true
		plan.Tasks = nil
	}

	// Final curriculum fallback if everything else failed
	if (plan.IsFallback || len(plan.Tasks) == 0) && s.curriculum != nil {
		if plan.Objective != "Curriculum Fallback" && plan.Objective != "Curriculum" {
			intent := proposedIntent
			err := proposeErr
			if intent == nil && err == nil {
				intent, err = s.curriculum.ProposeTask(ctx, gs, s.getTaskHistory(), s.getMilestoneContext(), s.sessionID, s.flags.CurriculumHorizon)
			}

			if err == nil && intent != nil && isValidCurriculumIntent(gs, intent) {
				plan = domain.Plan{
					Objective:  "Curriculum Fallback",
					IsFallback: true,
					Tasks: []domain.Action{{
						ID:        fmt.Sprintf("curr-fb-%d", time.Now().UnixNano()),
						Action:    intent.Action,
						Target:    domain.Target{Name: intent.Target, Type: "inferred"},
						Count:     intent.Count,
						Rationale: intent.Rationale,
						Priority:  50,
					}},
				}
			}
		}
	}

	if len(plan.Tasks) > 0 {
		s.dispatchPlan(ctx, plan)
	}
}

func (s *Service) dispatchPlan(ctx context.Context, plan domain.Plan) {
	s.planManager.SetPlan(&plan)
	_ = s.planManager.Transition(domain.PlanStatusActive)
	s.dispatchActivePlan(ctx)
}

func Validate(plan *domain.Plan, gs domain.GameState) bool {
	if plan == nil || len(plan.Tasks) == 0 {
		return false
	}

	task := plan.Tasks[0]
	hasPickaxe := false
	hasCraftingTable := false

	for _, item := range gs.Inventory {
		if strings.Contains(item.Name, "pickaxe") {
			hasPickaxe = true
		}
		if item.Name == "crafting_table" {
			hasCraftingTable = true
		}
	}

	for _, poi := range gs.POIs {
		if poi.Name == "crafting_table" {
			hasCraftingTable = true
		}
	}

	switch task.Action {
	case "eat":
		if gs.Food >= domain.DecisionFoodMax {
			return false
		}
		hasFood := false
		for _, item := range gs.Inventory {
			if domain.IsFood(item.Name) {
				hasFood = true
				break
			}
		}
		return hasFood
	case "craft":
		if len(gs.Inventory) == 0 {
			return false
		}
		if strings.Contains(task.Target.Name, "pickaxe") && !hasCraftingTable {
			return false
		}
	case "mine":
		if !hasPickaxe {
			return false
		}
	case "hunt":
		if gs.Health < domain.DecisionHealthHunt {
			return false
		}
	}

	return true
}

func isValidCurriculumIntent(gs domain.GameState, intent *domain.ActionIntent) bool {
	if intent == nil || intent.Action == "" {
		return false
	}

	target := strings.ToLower(strings.TrimSpace(intent.Target))
	switch intent.Action {
	case "use_skill":
		return target != ""
	case "gather":
		switch target {
		case "", "none", "air", "water", "lava":
			return false
		}
	case "eat":
		for _, item := range gs.Inventory {
			if strings.EqualFold(item.Name, target) && item.Count > 0 && domain.IsFood(item.Name) {
				return true
			}
		}
		return false
	}

	return true
}

func isProgressionMode(danger state.DangerState, gs domain.GameState) bool {
	return danger == state.DangerSafe &&
		gs.Health > 14 &&
		gs.Food > 10
}
