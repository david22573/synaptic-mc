// internal/policy/survival_test.go
package policy

import (
	"context"
	"testing"

	"david22573/synaptic-mc/internal/domain"
)

func TestSurvivalPolicy_Decide(t *testing.T) {
	p := NewSurvivalPolicy()
	ctx := context.Background()

	t.Run("Rejects low health without retreat", func(t *testing.T) {
		input := DecisionInput{
			State: domain.GameState{Health: 3.0},
			Plan: &domain.Plan{
				Tasks: []domain.Action{{Action: "mine"}},
			},
		}
		decision := p.Decide(ctx, input)
		if decision.IsApproved {
			t.Errorf("Expected rejection due to critical health")
		}
	})

	t.Run("Approves low health with eat", func(t *testing.T) {
		input := DecisionInput{
			State: domain.GameState{Health: 3.0},
			Plan: &domain.Plan{
				Tasks: []domain.Action{{Action: "eat"}},
			},
		}
		decision := p.Decide(ctx, input)
		if !decision.IsApproved {
			t.Errorf("Expected approval for life-saving action")
		}
	})

	t.Run("Rejects hunting dangerous targets", func(t *testing.T) {
		input := DecisionInput{
			State: domain.GameState{Health: 20.0},
			Plan: &domain.Plan{
				Tasks: []domain.Action{{Action: "hunt", Target: domain.Target{Name: "warden"}}},
			},
		}
		decision := p.Decide(ctx, input)
		if decision.IsApproved {
			t.Errorf("Expected rejection for hunting a warden")
		}
	})
}
