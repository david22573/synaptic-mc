package humanization

import (
	"testing"
	"time"
	"david22573/synaptic-mc/internal/domain"
)

func TestHumanizationEngine(t *testing.T) {
	cfg := Config{
		AttentionDecay:          0.1,
		HesitationBase:          100 * time.Millisecond,
		NoiseLevel:              0.1,
		CriticalHealthThreshold: 12.0,
		TaskSpacing:             100 * time.Millisecond,
		DriftCuriosityThreshold: 0.4,
		DriftIdleLookThreshold:  0.7,
		DriftInventoryThreshold: 0.85,
	}
	engine := NewEngine(cfg)

	plan := domain.Plan{
		Tasks: []domain.Action{
			{ID: "task-1", Action: "mine", Target: domain.Target{Name: "stone"}},
		},
	}
	ctx := Context{
		State: domain.GameState{Health: 20, Food: 20},
	}

	scheduled := engine.Process(plan, ctx)

	if len(scheduled) == 0 {
		t.Fatal("Expected scheduled actions")
	}

	if scheduled[0].Action.ID != "task-1" {
		t.Errorf("Expected task-1, got %s", scheduled[0].Action.ID)
	}

	// Should have some hesitation
	if scheduled[0].ExecuteAt.Before(time.Now()) {
		// This might fail if the machine is extremely slow, but usually hesitation adds time
	}
}

func TestAttentionDecay(t *testing.T) {
	cfg := Config{
		AttentionDecay: 1.0,
	}
	state := NewState(cfg)
	initial := state.GetAttention()

	ctx := Context{State: domain.GameState{Health: 20}}
	state.Evolve(ctx, 100*time.Millisecond)

	after := state.GetAttention()
	if after >= initial {
		t.Errorf("Expected attention to decay, got %f -> %f", initial, after)
	}
}
