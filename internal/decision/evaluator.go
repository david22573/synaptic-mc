package decision

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
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
	state := s.stateProvider.GetCurrentState().State
	if state.Health <= 0 {
		return
	}

	// Near-Cheating: Predictive Nightfall Prep
	nightfall := s.predictor.PredictNightfall(state)
	if nightfall {
		s.logger.Warn("Near-Cheating: Predicting nightfall, prioritizing shelter")
	}

	survivalOverride := hasImmediateThreat(state) ||
		hasImmediateHazard(state) ||
		(state.Health < domain.SurvivalCriticalHealth) ||
		(state.Health < domain.DecisionHealthSafe && state.Food < domain.SurvivalMinFoodForHunt) ||
		nightfall

	if !survivalOverride {
		if s.activeIntent.Load() != nil || s.planManager.HasActivePlan() || !s.execStatus.IsIdle() {
			return
		}
	}

	if currCommitment := s.commitment.Load(); currCommitment != nil {
		if time.Since(currCommitment.StartTime) < currCommitment.MinDuration {
			if state.Health >= domain.DecisionHealthSafe && len(state.Threats) == 0 && !nightfall {
				return
			}
			s.logger.Info("Breaking commitment lock for critical survival event")
		}
	}

	var plan domain.Plan

	if survivalOverride {
		s.logger.Warn("Survival priority override active")
		plan = s.planner.reactivePlan(ctx, state)
		if nightfall {
			plan.Objective = "Seek shelter for nightfall"
			plan.Tasks = append([]domain.Action{{
				ID:        fmt.Sprintf("night-prep-%d", time.Now().UnixNano()),
				Action:    "retreat",
				Target:    domain.Target{Name: "safe_base"},
				Priority:  90,
				Rationale: "Near-Cheating: Psychic nightfall prediction",
			}}, plan.Tasks...)
		}
	} else if isProgressionMode(state) && s.curriculum != nil {
		s.logger.Info("Stable state detected, curriculum driving progression")

		intent, err := s.curriculum.ProposeTask(ctx, state, s.getTaskHistory(), s.getMilestoneContext(), s.sessionID, s.flags.CurriculumHorizon)
		if err == nil && intent != nil && isValidCurriculumIntent(state, intent) {
			if intent.Action == "use_skill" && len(intent.SkillSteps) > 0 {
				s.logger.Info("Expanding composable skill", slog.String("skill", intent.Target), slog.Int("steps", len(intent.SkillSteps)))
				plan = domain.Plan{
					Objective:  intent.Rationale,
					IsFallback: false,
					Tasks:      make([]domain.Action, len(intent.SkillSteps)),
				}
				for i, step := range intent.SkillSteps {
					plan.Tasks[i] = domain.Action{
						ID:        fmt.Sprintf("%s-step-%d", intent.ID, i),
						Action:    step.Action,
						Target:    domain.Target{Name: step.Target, Type: "skill_step"},
						Count:     step.Count,
						Rationale: step.Rationale,
						Priority:  75 - i,
					}
				}
			} else {
				plan = domain.Plan{
					Objective:  intent.Rationale,
					IsFallback: false,
					Tasks: []domain.Action{
						{
							ID:        fmt.Sprintf("curr-%d", time.Now().UnixNano()),
							Action:    intent.Action,
							Target:    domain.Target{Name: intent.Target, Type: "curriculum"},
							Count:     intent.Count,
							Rationale: intent.Rationale,
							Priority:  70,
						},
					},
				}
			}
		} else {
			plan = s.planner.FastPlan(ctx, state)
		}
	} else {
		plan = s.planner.FastPlan(ctx, state)
	}

	if !Validate(&plan, state) {
		plan.IsFallback = true
		plan.Tasks = nil
	}

	if (plan.IsFallback || len(plan.Tasks) == 0) && s.curriculum != nil {
		if plan.Objective != "Curriculum Fallback" && plan.Objective != "Curriculum" {
			intent, err := s.curriculum.ProposeTask(ctx, state, s.getTaskHistory(), s.getMilestoneContext(), s.sessionID, s.flags.CurriculumHorizon)
			if err == nil && intent != nil && isValidCurriculumIntent(state, intent) {
				plan = domain.Plan{
					Objective:  "Curriculum Fallback",
					IsFallback: true,
					Tasks: []domain.Action{
						{
							ID:        fmt.Sprintf("curr-fb-%d", time.Now().UnixNano()),
							Action:    intent.Action,
							Target:    domain.Target{Name: intent.Target, Type: "inferred"},
							Count:     intent.Count,
							Rationale: intent.Rationale,
							Priority:  50,
						},
					},
				}
			}
		}
	}

	if len(plan.Tasks) == 0 {
		return
	}

	s.planManager.SetPlan(&plan)
	_ = s.planManager.Transition(domain.PlanStatusActive)
	s.dispatchActivePlan(ctx)
}

func Validate(plan *domain.Plan, state domain.GameState) bool {
	if plan == nil || len(plan.Tasks) == 0 {
		return false
	}

	task := plan.Tasks[0]
	hasPickaxe := false
	hasCraftingTable := false

	for _, item := range state.Inventory {
		if strings.Contains(item.Name, "pickaxe") {
			hasPickaxe = true
		}
		if item.Name == "crafting_table" {
			hasCraftingTable = true
		}
	}

	for _, poi := range state.POIs {
		if poi.Name == "crafting_table" {
			hasCraftingTable = true
		}
	}

	switch task.Action {
	case "eat":
		if state.Food >= domain.DecisionFoodMax {
			return false
		}
		hasFood := false
		for _, item := range state.Inventory {
			if domain.IsFood(item.Name) {
				hasFood = true
				break
			}
		}
		return hasFood
	case "craft":
		if len(state.Inventory) == 0 {
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
		if state.Health < domain.DecisionHealthHunt {
			return false
		}
	}

	return true
}

func isValidCurriculumIntent(state domain.GameState, intent *domain.ActionIntent) bool {
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
		for _, item := range state.Inventory {
			if strings.EqualFold(item.Name, target) && item.Count > 0 && domain.IsFood(item.Name) {
				return true
			}
		}
		return false
	}

	return true
}

func isProgressionMode(state domain.GameState) bool {
	return state.Health > 14 &&
		state.Food > 10 &&
		len(state.Threats) == 0 &&
		!hasImmediateHazard(state)
}

func hasImmediateThreat(state domain.GameState) bool {
	for _, threat := range state.Threats {
		if threat.Distance <= domain.SurvivalMaxThreatDist {
			return true
		}
	}
	return false
}

func hasImmediateHazard(state domain.GameState) bool {
	for _, feedback := range state.Feedback {
		switch strings.ToLower(strings.TrimSpace(feedback.Cause)) {
		case "lava_contact", "low_air", "burning", "recent_heavy_damage":
			return true
		}
		if strings.EqualFold(feedback.Type, "hazard") {
			return true
		}
	}

	for _, zone := range state.DangerZones {
		if zone.Risk < 0.85 {
			continue
		}
		if state.Position.DistanceTo(zone.Center) <= 6.0 {
			return true
		}
	}

	return false
}
