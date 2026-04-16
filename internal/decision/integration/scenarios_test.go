package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/eventstore"
	"david22573/synaptic-mc/internal/planner"
	"david22573/synaptic-mc/internal/strategy"
	"david22573/synaptic-mc/internal/voyager"
)

func setupScenarioHarness(t *testing.T, initialState domain.GameState, llm *MockLLM) (*decision.Service, *domain.LocalEventBus, context.CancelFunc, *MockStateProvider, *domain.WorldModel) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	bus := domain.NewEventBus()

	evaluator := strategy.NewEvaluatorWithLLM(llm)
	critic := voyager.NewStateCritic()
	worldModel := domain.NewWorldModel()

	vStore, _ := voyager.NewSQLiteVectorStore(":memory:")
	eStore, _ := eventstore.NewSQLiteStore(":memory:", logger)
	curriculum := voyager.NewAutonomousCurriculum(llm, vStore, nil, worldModel)
	skillManager := voyager.NewSkillManager(vStore, llm)

	advPlanner := decision.NewAdvancedPlanner(llm, evaluator, critic, nil, eStore, worldModel, nil, logger, config.FeatureFlags{}, skillManager)
	pm := decision.NewPlanManager()

	stateProv := &MockStateProvider{state: initialState}
	execStatus := &MockExecutionStatus{idle: true}
	feedback := planner.NewFeedbackAnalyzer(worldModel, logger)

	svc := decision.NewService(
		ctx, "test-session", bus, advPlanner, pm, curriculum, critic,
		stateProv, execStatus, worldModel, nil, feedback, skillManager, logger, config.FeatureFlags{},
	)

	return svc, bus, cancel, stateProv, worldModel
}

func TestScenario_Starvation(t *testing.T) {
	llm := NewMockLLM()
	initialState := domain.GameState{
		Health:      20,
		Food:        5, // Starvation level
		Position:    domain.Vec3{X: 0, Y: 64, Z: 0},
		Inventory:   []domain.Item{{Name: "apple", Count: 5}},
		Initialized: true,
	}

	_, bus, cancel, _, _ := setupScenarioHarness(t, initialState, llm)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var capturedPlanEvent domain.DomainEvent

	bus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		capturedPlanEvent = ev
		wg.Done()
	}))

	bus.Publish(context.Background(), domain.DomainEvent{
		Type:      domain.EventTypeStateUpdated,
		Payload:   []byte(`{}`),
		CreatedAt: time.Now(),
	})
	wg.Wait()

	var plan domain.Plan
	json.Unmarshal(capturedPlanEvent.Payload, &plan)

	if plan.Tasks[0].Action != "eat" {
		t.Errorf("Expected starvation to trigger 'eat' action, got: %s", plan.Tasks[0].Action)
	}
}

func TestScenario_NightfallLowHealth(t *testing.T) {
	llm := NewMockLLM()
	initialState := domain.GameState{
		Health:      8,     // Low health
		Food:        20,
		TimeOfDay:   12500, // Near-nightfall
		Position:    domain.Vec3{X: 0, Y: 64, Z: 0},
		Initialized: true,
	}

	_, bus, cancel, _, _ := setupScenarioHarness(t, initialState, llm)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var capturedPlanEvent domain.DomainEvent

	bus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		capturedPlanEvent = ev
		wg.Done()
	}))

	bus.Publish(context.Background(), domain.DomainEvent{
		Type:      domain.EventTypeStateUpdated,
		Payload:   []byte(`{}`),
		CreatedAt: time.Now(),
	})
	wg.Wait()

	var plan domain.Plan
	json.Unmarshal(capturedPlanEvent.Payload, &plan)

	if plan.Objective != "Seek shelter for nightfall" {
		t.Errorf("Expected psychic nightfall prep, got objective: %s", plan.Objective)
	}
	if plan.Tasks[0].Action != "retreat" {
		t.Errorf("Expected retreat action for nightfall prep, got: %s", plan.Tasks[0].Action)
	}
}

func TestScenario_RepeatedStuckLoop(t *testing.T) {
	llm := NewMockLLM()
	initialState := domain.GameState{
		Health:      13, // Stable but non-progression to force FastPlan usage
		Food:        20,
		Position:    domain.Vec3{X: 0, Y: 64, Z: 0},
		Initialized: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	bus := domain.NewEventBus()
	evaluator := strategy.NewEvaluatorWithLLM(llm)
	critic := voyager.NewStateCritic()
	worldModel := domain.NewWorldModel()
	vStore, _ := voyager.NewSQLiteVectorStore(":memory:")
	eStore, _ := eventstore.NewSQLiteStore(":memory:", logger)
	curriculum := voyager.NewAutonomousCurriculum(llm, vStore, nil, worldModel)
	skillManager := voyager.NewSkillManager(vStore, llm)
	advPlanner := decision.NewAdvancedPlanner(llm, evaluator, critic, nil, eStore, worldModel, nil, logger, config.FeatureFlags{}, skillManager)
	pm := decision.NewPlanManager()
	stateProv := &MockStateProvider{state: initialState}
	execStatus := &MockExecutionStatus{idle: true}
	
	// NIL feedback analyzer to ensure failure count accumulates on SAME objective
	svc := decision.NewService(
		ctx, "test-session", bus, advPlanner, pm, curriculum, critic,
		stateProv, execStatus, worldModel, nil, nil, skillManager, logger, config.FeatureFlags{},
	)

	var wg sync.WaitGroup
	wg.Add(1)

	var lastPlan domain.Plan
	bus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		_ = json.Unmarshal(ev.Payload, &lastPlan)
		wg.Done()
	}))

	// 1. Initial Plan (Planner generates "mock")
	bus.Publish(context.Background(), domain.DomainEvent{
		Type:      domain.EventTypeStateUpdated,
		Payload:   []byte(`{}`),
		CreatedAt: time.Now(),
	})
	wg.Wait()

	// 2. Fail the plan 5 times. 
	for i := 0; i < 5; i++ {
		wg.Add(1)
		taskEndPayload, _ := json.Marshal(domain.TaskEndPayload{
			CommandID: lastPlan.Tasks[0].ID,
			Status:    "FAILED",
			Success:   false,
			Cause:     domain.CauseStuckTerrain,
			Action:    lastPlan.Tasks[0].Action,
			Target:    lastPlan.Tasks[0].Target.Name,
		})

		bus.Publish(context.Background(), domain.DomainEvent{
			Type:      domain.EventTypeTaskEnd,
			Payload:   taskEndPayload,
			CreatedAt: time.Now(),
		})
		wg.Wait()
	}

	if lastPlan.Objective != "Degraded Recovery state" {
		t.Errorf("Expected plan objective to be degraded recovery, got: %s", lastPlan.Objective)
	}
	if lastPlan.Tasks[0].Action != "random_walk" {
		t.Errorf("Expected action to be random_walk, got: %s", lastPlan.Tasks[0].Action)
	}
	
	_ = svc
}

func TestScenario_PlannerTimeoutFallback(t *testing.T) {
	llm := &MockLLM{responses: make(map[string]string)}
	initialState := domain.GameState{
		Health:      20,
		Food:        20,
		Position:    domain.Vec3{X: 0, Y: 64, Z: 0},
		Initialized: true,
	}

	_, bus, cancel, _, _ := setupScenarioHarness(t, initialState, llm)
	defer cancel()

	llm.responses[""] = "ERROR: Timeout" 

	var wg sync.WaitGroup
	wg.Add(1)
	var capturedPlanEvent domain.DomainEvent

	bus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		capturedPlanEvent = ev
		wg.Done()
	}))

	bus.Publish(context.Background(), domain.DomainEvent{
		Type:      domain.EventTypeStateUpdated,
		Payload:   []byte(`{}`),
		CreatedAt: time.Now(),
	})
	wg.Wait()

	var plan domain.Plan
	json.Unmarshal(capturedPlanEvent.Payload, &plan)

	if plan.Objective == "" {
		t.Errorf("Expected fallback plan objective, got empty")
	}
}
