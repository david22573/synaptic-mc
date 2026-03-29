// internal/pipeline/stage_test.go
package pipeline

import (
	"context"
	"testing"

	"david22573/synaptic-mc/internal/domain"
)

type mockStage struct {
	processFunc func(ctx context.Context, state *PipelineState) error
}

func (m *mockStage) Process(ctx context.Context, state *PipelineState) error {
	return m.processFunc(ctx, state)
}

func TestPipelineStage_ImmutabilityContract(t *testing.T) {
	initialPlan := &domain.Plan{
		Objective: "Gather Wood",
		Tasks: []domain.Action{
			{Action: "gather", Target: domain.Target{Name: "oak_log"}},
		},
	}

	state := &PipelineState{
		Plan: initialPlan,
	}

	stage := &mockStage{
		processFunc: func(ctx context.Context, s *PipelineState) error {
			// A compliant stage creates a new artifact rather than mutating the previous one
			s.Validation = &ValidationResult{IsValid: true}
			return nil
		},
	}

	err := stage.Process(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the original plan pointer and its contents remain untouched
	if state.Plan.Objective != "Gather Wood" {
		t.Errorf("expected original plan to remain immutable")
	}

	if state.Validation == nil || !state.Validation.IsValid {
		t.Errorf("expected validation artifact to be populated correctly")
	}
}
