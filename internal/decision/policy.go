package decision

import (
	"david22573/synaptic-mc/internal/domain"
	"errors"
)

// HardGuardrails enforces absolute survival rules that override any LLM hallucination.
type HardGuardrails struct{}

func NewHardGuardrails() *HardGuardrails {
	return &HardGuardrails{}
}

func (p *HardGuardrails) Enforce(plan *domain.Plan, state domain.GameState) error {
	// 1. Critical Health Override [cite: 165]
	if state.Health < 6.0 {
		isRetreatingOrEating := false
		for _, t := range plan.Tasks {
			if t.Action == "retreat" || t.Action == "eat" {
				isRetreatingOrEating = true
				break
			}
		}
		if !isRetreatingOrEating {
			return errors.New("POLICY VIOLATION: Health is critical. Plan must include 'retreat' or 'eat'")
		}
	}

	// 2. Combat Avoidance
	for _, t := range plan.Tasks {
		if t.Action == "hunt" {
			if state.Health < 12.0 {
				return errors.New("POLICY VIOLATION: Cannot initiate hunt with health under 12")
			}
			if t.Target.Name == "creeper" || t.Target.Name == "warden" || t.Target.Name == "iron_golem" {
				return errors.New("POLICY VIOLATION: Target is on the absolute do-not-engage list")
			}
		}
	}

	return nil
}
