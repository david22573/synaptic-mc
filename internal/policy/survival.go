// internal/policy/survival.go
package policy

import (
	"context"
)

// SurvivalPolicy replaces the old HardGuardrails. It strictly enforces
// biological and physical bot safety invariants.
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
		isRetreatingOrEating := false
		for _, t := range input.Plan.Tasks {
			if t.Action == "retreat" || t.Action == "eat" {
				isRetreatingOrEating = true
				break
			}
		}
		if !isRetreatingOrEating {
			return Decision{
				IsApproved: false,
				Reason:     "POLICY VIOLATION: Health is critical. Plan must include 'retreat' or 'eat'",
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
