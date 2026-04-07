// internal/policy/survival.go
package policy

import (
	"context"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type SurvivalPolicy struct{}

func NewSurvivalPolicy() *SurvivalPolicy { return &SurvivalPolicy{} }

func (p *SurvivalPolicy) Decide(ctx context.Context, input DecisionInput) Decision {
	var tasks []domain.Action
	if input.Plan != nil {
		tasks = input.Plan.Tasks
	}

	// 1. Critical-health override
	if input.State.Health < domain.SurvivalCriticalHealth {
		isValidEscape := false
		for _, t := range tasks {
			if t.Action == "retreat" || t.Action == "eat" || t.Action == "gather" || t.Action == "explore" {
				isValidEscape = true
				break
			}
		}
		if !isValidEscape {
			override := p.buildEmergencyPlan(input.State)
			return Decision{
				IsApproved:      false,
				Reason:          "POLICY: health critical; forcing eat/retreat",
				OverridePlan:    override,
				ReflexTriggered: true,
			}
		}
	}

	// 2. Threat proximity override
	if len(input.State.Threats) > 0 {
		for _, t := range input.State.Threats {
			if t.Distance <= domain.SurvivalMaxThreatDist {
				isValidEscape := false
				for _, task := range tasks {
					if task.Action == "retreat" || task.Action == "eat" {
						isValidEscape = true
						break
					}
				}
				if !isValidEscape {
					override := p.buildEmergencyPlan(input.State)
					return Decision{
						IsApproved:      false,
						Reason:          "POLICY: hostile within danger zone; forcing eat/retreat",
						OverridePlan:    override,
						ReflexTriggered: true,
					}
				}
				break
			}
		}
	}

	// 3. Hunt safety check
	for _, t := range tasks {
		if t.Action == "hunt" {
			if input.State.Health < domain.SurvivalMinFoodForHunt {
				return Decision{
					IsApproved: false,
					Reason:     "POLICY: health too low; too weak to hunt",
				}
			}
			if strings.Contains(t.Target.Name, "creeper") ||
				strings.Contains(t.Target.Name, "warden") {
				return Decision{
					IsApproved: false,
					Reason:     "POLICY: target on do-not-engage list",
				}
			}
		}
	}
	return Decision{IsApproved: true}
}

func (p *SurvivalPolicy) buildEmergencyPlan(state domain.GameState) *domain.Plan {
	// prefer eat if food exists, else retreat
	foodName := ""
	for _, item := range state.Inventory {
		if isFood(item.Name) && item.Count > 0 {
			foodName = item.Name
			break
		}
	}
	action := "retreat"
	target := domain.Target{Type: "none", Name: "none"}
	if foodName != "" {
		action = "eat"
		target = domain.Target{Type: "item", Name: foodName}
	}
	return &domain.Plan{
		Objective: "EMERGENCY SURVIVAL",
		Tasks: []domain.Action{{
			Action:    action,
			Target:    target,
			Priority:  100,
			Rationale: "SurvivalPolicy emergency override",
		}},
	}
}

func isFood(name string) bool {
	foods := []string{"beef", "porkchop", "chicken", "mutton", "rabbit",
		"cooked_beef", "cooked_porkchop", "cooked_chicken", "cooked_mutton", "cooked_rabbit",
		"apple", "bread", "carrot", "potato", "baked_potato", "sweet_berries"}
	for _, f := range foods {
		if strings.Contains(name, f) {
			return true
		}
	}
	return false
}
