package decision

import (
	"testing"
	"david22573/synaptic-mc/internal/domain"
)

func TestPlanManager(t *testing.T) {
	pm := NewPlanManager()

	if pm.HasActivePlan() {
		t.Error("Expected no active plan initially")
	}

	plan := &domain.Plan{
		Objective: "Test Objective",
		Tasks: []domain.Action{
			{ID: "task-1", Action: "mine", Target: domain.Target{Name: "stone"}},
			{ID: "task-2", Action: "mine", Target: domain.Target{Name: "coal"}},
		},
		Fallbacks: [][]domain.Action{
			{
				{ID: "fallback-1", Action: "explore", Target: domain.Target{Name: "surface"}},
			},
		},
	}

	pm.SetPlan(plan)

	if !pm.HasActivePlan() {
		t.Error("Expected active plan after SetPlan")
	}

	if pm.GetCurrent().Objective != "Test Objective" {
		t.Errorf("Expected objective 'Test Objective', got %s", pm.GetCurrent().Objective)
	}

	// Test PopTask
	hasMore, matched := pm.PopTask("task-1")
	if !matched {
		t.Error("Expected task-1 to match")
	}
	if !hasMore {
		t.Error("Expected more tasks after popping first task")
	}

	if len(pm.GetCurrent().Tasks) != 1 || pm.GetCurrent().Tasks[0].ID != "task-2" {
		t.Error("PopTask failed to correctly update task list")
	}

	// Test NextFallback
	hasFallback := pm.NextFallback()
	if !hasFallback {
		t.Fatal("Expected fallback to be available")
	}

	if pm.GetCurrent().Tasks[0].ID != "fallback-1" {
		t.Errorf("Expected fallback task-1, got %s", pm.GetCurrent().Tasks[0].ID)
	}

	// Popping the last task
	hasMore, matched = pm.PopTask("fallback-1")
	if !matched {
		t.Error("Expected fallback-1 to match")
	}
	if hasMore {
		t.Error("Expected no more tasks after popping last task")
	}
}

func TestPlanTransitions(t *testing.T) {
	pm := NewPlanManager()
	plan := &domain.Plan{Objective: "Transitions"}
	pm.SetPlan(plan)

	err := pm.Transition(domain.PlanStatusActive)
	if err != nil {
		t.Errorf("Transition to ACTIVE failed: %v", err)
	}

	if pm.GetCurrent().Status != domain.PlanStatusActive {
		t.Errorf("Expected status ACTIVE, got %s", pm.GetCurrent().Status)
	}

	err = pm.Transition(domain.PlanStatusCompleted)
	if err != nil {
		t.Errorf("Transition to COMPLETED failed: %v", err)
	}

	if pm.HasActivePlan() {
		t.Error("Expected no active plan after transition to COMPLETED")
	}
}

func TestPlanManagerDeepCopy(t *testing.T) {
	pm := NewPlanManager()

	originalTasks := []domain.Action{
		{ID: "task-1", Action: "mine", Target: domain.Target{Name: "stone"}},
	}
	plan := &domain.Plan{
		Objective: "Deep Copy Test",
		Tasks:     originalTasks,
	}

	pm.SetPlan(plan)

	// Mutate the original plan's tasks
	plan.Tasks[0].Action = "mutated"
	plan.Objective = "mutated objective"

	current := pm.GetCurrent()
	if current.Objective == "mutated objective" {
		t.Error("PlanManager.SetPlan did not perform a deep copy of the objective")
	}
	if current.Tasks[0].Action == "mutated" {
		t.Error("PlanManager.SetPlan did not perform a deep copy of the tasks")
	}

	// Mutate the result from GetCurrent
	current.Tasks[0].Action = "mutated from get"
	
	current2 := pm.GetCurrent()
	if current2.Tasks[0].Action == "mutated from get" {
		t.Error("PlanManager.GetCurrent did not return a deep copy")
	}
}

func TestPlanManagerNilSafety(t *testing.T) {
	pm := NewPlanManager()

	// Should not panic
	pm.SetPlan(nil)

	if pm.GetCurrent() != nil {
		t.Error("Expected GetCurrent to be nil after SetPlan(nil)")
	}

	if pm.HasActivePlan() {
		t.Error("Expected HasActivePlan to be false after SetPlan(nil)")
	}
}
