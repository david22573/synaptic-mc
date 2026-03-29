package pipeline_test

import (
	"context"
	"testing"

	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/policy"
)

type stubPlanner struct {
	plan *domain.Plan
}

func (s *stubPlanner) Generate(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error) {
	return s.plan, nil
}

// 4.4 FIX: Acceptance test covering the complex policy override flow
func TestPipelineWithSurvivalOverride(t *testing.T) {
	ctx := context.Background()

	// 1. Setup a stub planner that attempts to mine while the bot is starving
	fixedPlan := &domain.Plan{
		Objective: "Mine cobblestone",
		Tasks: []domain.Action{
			{Action: "mine", Target: domain.Target{Name: "cobblestone"}},
		},
	}
	plannerStub := &stubPlanner{plan: fixedPlan}

	survPolicy := policy.NewSurvivalPolicy()
	compositePolicy := policy.NewCompositePolicy(survPolicy)
	pipelineEngine := decision.NewPipeline(plannerStub, compositePolicy)

	// 2. Evaluate with critical health but some food in inventory
	evalState := domain.GameState{
		Health: 4.0,
		Food:   2.0,
		Inventory: []domain.Item{
			{Name: "bread", Count: 2},
			{Name: "wooden_pickaxe", Count: 1}, // Added to satisfy validate stage
		},
	}
	trace := domain.TraceContext{TraceID: "test-override"}

	plan, err := pipelineEngine.Evaluate(ctx, "test-session", evalState, trace)

	// 3. Asserts err == nil
	if err != nil {
		t.Fatalf("Expected nil error (override should suppress rejection), got: %v", err)
	}

	// 4. Asserts plan != nil and len >= 1
	if plan == nil || len(plan.Tasks) == 0 {
		t.Fatalf("Expected non-empty override plan, got nil or empty")
	}

	// 5. Asserts the pipeline forcefully substituted the task to prioritize survival
	firstAction := plan.Tasks[0].Action
	if firstAction != "eat" && firstAction != "retreat" {
		t.Errorf("Expected first action to be 'eat' or 'retreat', got '%s'", firstAction)
	}

	if firstAction == "eat" && plan.Tasks[0].Target.Name != "bread" {
		t.Errorf("Expected to eat 'bread', got target '%s'", plan.Tasks[0].Target.Name)
	}
}
