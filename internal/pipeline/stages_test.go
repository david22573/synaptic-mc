package pipeline

import (
	"context"
	"testing"

	"david22573/synaptic-mc/internal/domain"
)

func TestPipelineStages_Integration(t *testing.T) {
	ctx := context.Background()

	rawPlan := &domain.Plan{
		Objective: "  Get Stone  ",
		Tasks: []domain.Action{
			{Action: " MINE ", Target: domain.Target{Name: " stone "}},
			{Action: "explore", Target: domain.Target{Name: "none"}},
		},
	}

	state := &PipelineState{
		GameState: domain.GameState{
			Inventory: []domain.Item{{Name: "wooden_pickaxe", Count: 1}},
			POIs:      []domain.POI{{Name: "stone", Distance: 5.0}},
		},
		Trace: domain.TraceContext{TraceID: "tr-123"},
		Plan:  rawPlan,
	}

	// 1. Normalize
	normStage := NewNormalizeStage()
	if err := normStage.Process(ctx, state); err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}

	if state.Normalized.Tasks[0].Action != "mine" {
		t.Errorf("Expected 'mine', got '%s'", state.Normalized.Tasks[0].Action)
	}
	if state.Normalized.Tasks[0].Trace.TraceID != "tr-123" {
		t.Errorf("Expected trace context to be applied during normalization")
	}

	// 2. Validate
	valStage := NewValidateStage()
	if err := valStage.Process(ctx, state); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	if !state.Validation.IsValid {
		t.Errorf("Expected valid plan, got errors: %v", state.Validation.Errors)
	}

	// 3. Simulate
	simStage := NewSimulateStage()
	if err := simStage.Process(ctx, state); err != nil {
		t.Fatalf("Simulate failed: %v", err)
	}

	if len(state.Simulation.OptimizedTasks) != 2 {
		t.Errorf("Expected 2 optimized tasks, got %d", len(state.Simulation.OptimizedTasks))
	}
}
