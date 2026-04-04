package pipeline_test

import (
	"context"
	"testing"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/pipeline"
	"david22573/synaptic-mc/internal/policy"
)

// 4.4 FIX: Acceptance test covering the complex policy override flow
func TestPipelineWithSurvivalOverride(t *testing.T) {
	ctx := context.Background()

	// 1. Create the initial bad plan that attempts to mine while the bot is starving
	fixedPlan := &domain.Plan{
		Objective: "Mine cobblestone",
		Tasks: []domain.Action{
			{Action: "mine", Target: domain.Target{Name: "cobblestone"}},
		},
	}

	survPolicy := policy.NewSurvivalPolicy()
	compositePolicy := policy.NewCompositePolicy(survPolicy)

	// Test the stage logic directly to bypass the asynchronous executor pipeline
	policyStage := pipeline.NewPolicyStage(compositePolicy)

	// 2. Evaluate with critical health but some food in inventory
	evalState := domain.GameState{
		Health: 3.0,
		Food:   2.0,
		Inventory: []domain.Item{
			{Name: "bread", Count: 2},
			{Name: "wooden_pickaxe", Count: 1}, // Satisfies item requirements
		},
	}
	trace := domain.TraceContext{TraceID: "test-override"}

	// FIX: PolicyStage now strictly requires Normalized and Simulation artifacts
	// from the previous stages. We mock them here for the isolated stage test.
	pipeState := pipeline.PipelineState{
		GameState:  evalState,
		Trace:      trace,
		Plan:       fixedPlan,
		Normalized: fixedPlan,
		Simulation: &pipeline.SimulationResult{
			OptimizedTasks: fixedPlan.Tasks,
			RiskScore:      0.0,
		},
	}

	// 3. Process the state
	nextState, err := policyStage.Process(ctx, pipeState)

	// 4. Asserts err == nil
	if err != nil {
		t.Fatalf("Expected nil error (override should suppress rejection), got: %v", err)
	}

	finalPlan := nextState.FinalPlan

	// 5. Asserts plan != nil and len >= 1
	if finalPlan == nil || len(finalPlan.Tasks) == 0 {
		t.Fatalf("Expected non-empty override plan, got nil or empty")
	}

	// 6. Asserts the stage forcefully substituted the task to prioritize survival
	firstAction := finalPlan.Tasks[0].Action
	if firstAction != "eat" && firstAction != "retreat" {
		t.Errorf("Expected first action to be 'eat' or 'retreat', got '%s'", firstAction)
	}

	if firstAction == "eat" && finalPlan.Tasks[0].Target.Name != "bread" {
		t.Errorf("Expected to eat 'bread', got target '%s'", finalPlan.Tasks[0].Target.Name)
	}
}
