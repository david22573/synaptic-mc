package policy

import (
	"context"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

// SurvivalPolicy replaces the old HardGuardrails.
// It strictly enforces biological and physical bot safety invariants.
type SurvivalPolicy struct{}

func NewSurvivalPolicy() *SurvivalPolicy {
	return &SurvivalPolicy{}
}

func (p *SurvivalPolicy) Decide(ctx context.Context, input DecisionInput) Decision {
	if input.Plan == nil {
		return Decision{IsApproved: true}
	}

	// 1. Critical Health Override (Week 3: Survival Priority Override)
	if input.State.Health < 6.0 || len(input.State.Threats) > 0 {
		isValidEscape := false
		for _, t := range input.Plan.Tasks {
			if t.Action == "retreat" || t.Action == "eat" || t.Action == "gather" || t.Action == "explore" {
				isValidEscape = true
				break
			}
		}

		if !isValidEscape || len(input.State.Threats) > 0 {
			var overridePlan *domain.Plan
			var foodItemName string

			foodItems := []string{
				"beef", "porkchop", "mutton", "chicken", "rabbit",
				"cooked_beef", "cooked_porkchop", "cooked_mutton", "cooked_chicken", "cooked_rabbit",
				"apple", "sweet_berries", "bread", "carrot", "potato", "baked_potato", "kelp", "dried_kelp",
			}

			for _, invItem := range input.State.Inventory {
				if invItem.Count > 0 {
					for _, f := range foodItems {
						if strings.Contains(invItem.Name, f) {
							foodItemName = invItem.Name
							break
						}
					}
				}
				if foodItemName != "" {
					break
				}
			}

			if foodItemName != "" && len(input.State.Threats) == 0 {
				overridePlan = &domain.Plan{
					Objective: "EMERGENCY OVERRIDE: Eat food to survive.",
					Tasks: []domain.Action{
						{
							Action:   "eat",
							Target:   domain.Target{Type: "item", Name: foodItemName},
							Priority: 100, // Hard interrupt priority
						},
					},
				}
			} else {
				overridePlan = &domain.Plan{
					Objective: "EMERGENCY OVERRIDE: Retreat to safety.",
					Tasks: []domain.Action{
						{
							Action:   "retreat",
							Target:   domain.Target{Type: "none", Name: "none"},
							Priority: 100, // Hard interrupt priority
						},
					},
				}
			}

			return Decision{
				IsApproved:      false,
				Reason:          "POLICY VIOLATION: Health is critical or threats are present. Plan must prioritize survival.",
				OverridePlan:    overridePlan,
				ReflexTriggered: true, // Week 1: Reflex Lock to block planner
			}
		}
	}

	// 2. Combat Avoidance
	for _, t := range input.Plan.Tasks {
		if t.Action == "hunt" {
			if input.State.Health < 12.0 {
				return Decision{
					IsApproved: false,
					Reason:     "POLICY VIOLATION: Cannot initiate hunt with health under 12",
				}
			}
			if t.Target.Name == "creeper" || t.Target.Name == "warden" || t.Target.Name == "iron_golem" {
				return Decision{
					IsApproved: false,
					Reason:     "POLICY VIOLATION: Target is on the absolute do-not-engage list",
				}
			}
		}
	}

	return Decision{IsApproved: true}
}
