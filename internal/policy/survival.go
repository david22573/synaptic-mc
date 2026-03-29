package policy

import (
	"context"
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

	// 1. Critical Health Override
	if input.State.Health < 6.0 {
		isValidEscape := false
		for _, t := range input.Plan.Tasks {
			// Loosened to allow gathering passive food or exploring when starving
			if t.Action == "retreat" || t.Action == "eat" || t.Action == "gather" || t.Action == "explore" {
				isValidEscape = true
				break
			}
		}
		if !isValidEscape {
			return Decision{
				IsApproved: false,
				Reason:     "POLICY VIOLATION: Health is critical. Plan must prioritize survival (retreat, eat, gather, explore).",
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
