package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
	"strings"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/planner"
	"david22573/synaptic-mc/internal/voyager"
	"david22573/synaptic-mc/internal/strategy"
	"david22573/synaptic-mc/internal/eventstore"
)

// MockLLM implements the decision.LLMClient interface for deterministic testing.
type MockLLM struct {
	responses map[string]string
	mu        sync.Mutex
}

func NewMockLLM() *MockLLM {
	return &MockLLM{responses: make(map[string]string)}
}

func (m *MockLLM) Generate(ctx context.Context, systemPrompt, userContent string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if resp, ok := m.responses[systemPrompt+userContent]; ok {
		return resp, nil
	}
	
	if strings.Contains(systemPrompt, "Curriculum") || strings.Contains(systemPrompt, "autonomous Minecraft bot") {
		return `{"action": "explore", "target": "surface", "count": 1, "rationale": "curriculum mock"}`, nil
	}
	
	return `{"strategic_goal": "mock mission", "subgoals": ["mock step"], "objective": "mock", "candidates": [[{"action": "explore", "target": {"name": "surface", "type": "category"}, "count": 1, "rationale": "mock"}]]}`, nil
}

func (m *MockLLM) GenerateText(ctx context.Context, systemPrompt, userContent string) (string, error) {
	if strings.Contains(systemPrompt, "Critic") {
		return `{"failure": "mock failure", "cause": "mock cause", "fix": "mock fix", "score": 0.5}`, nil
	}
	return "mock text response", nil
}

func (m *MockLLM) CreateEmbedding(ctx context.Context, input string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3, 0.4}, nil
}

func (m *MockLLM) CompressState(state domain.GameState, events []domain.DomainEvent) string {
	return "mock compressed state"
}

// MockExecutionStatus implements decision.ExecutionStatus.
type MockExecutionStatus struct {
	idle bool
}

func (m *MockExecutionStatus) IsIdle() bool { return m.idle }

// MockStateProvider implements decision.StateProvider.
type MockStateProvider struct {
	state domain.GameState
}

func (m *MockStateProvider) GetCurrentState() domain.VersionedState {
	return domain.VersionedState{State: m.state, Version: 1}
}

func TestEndToEndPlanning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	bus := domain.NewEventBus()
	
	llm := NewMockLLM()
	evaluator := strategy.NewEvaluatorWithLLM(llm)
	critic := voyager.NewStateCritic()
	worldModel := domain.NewWorldModel()
	
	vStore, err := voyager.NewSQLiteVectorStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create vector store: %v", err)
	}
	defer vStore.Close()

	eStore, err := eventstore.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("failed to create event store: %v", err)
	}
	defer eStore.Close()

	curriculum := voyager.NewAutonomousCurriculum(llm, vStore, nil, worldModel)
	skillManager := voyager.NewSkillManager(vStore, llm)

	advPlanner := decision.NewAdvancedPlanner(llm, evaluator, critic, nil, eStore, worldModel, nil, logger, config.FeatureFlags{}, skillManager)
	pm := decision.NewPlanManager()
	
	stateProv := &MockStateProvider{
		state: domain.GameState{
			Health: 20,
			Food: 20,
			Position: domain.Vec3{X: 0, Y: 64, Z: 0},
			Inventory: []domain.Item{{Name: "wooden_pickaxe", Count: 1}},
			Initialized: true,
		},
	}
	
	execStatus := &MockExecutionStatus{idle: true}
	feedback := planner.NewFeedbackAnalyzer(worldModel, logger)

	svc := decision.NewService(
		ctx, "test-session", bus, advPlanner, pm, curriculum, critic,
		stateProv, execStatus, worldModel, nil, feedback, skillManager, logger, config.FeatureFlags{},
	)

	// Capture events using a channel to avoid race conditions
	planEvents := make(chan domain.DomainEvent, 10)
	bus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		planEvents <- ev
	}))

	// 1. Trigger State Update
	bus.Publish(ctx, domain.DomainEvent{
		Type: domain.EventTypeStateUpdated,
		Payload: []byte(`{}`),
		CreatedAt: time.Now(),
	})

	// Wait for first plan
	var firstPlan domain.Plan
	select {
	case ev := <-planEvents:
		if err := json.Unmarshal(ev.Payload, &firstPlan); err != nil {
			t.Fatalf("Failed to unmarshal plan: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for first plan")
	}

	if len(firstPlan.Tasks) == 0 {
		t.Fatal("Plan should have at least one task")
	}
	t.Logf("First Plan Created: %s", firstPlan.Objective)

	// 2. Simulate Task Completion
	taskID := firstPlan.Tasks[0].ID
	taskEndPayload, _ := json.Marshal(domain.TaskEndPayload{
		CommandID: taskID,
		Status:    "COMPLETED",
		Success:   true,
		Action:    firstPlan.Tasks[0].Action,
		Target:    firstPlan.Tasks[0].Target.Name,
	})

	bus.Publish(ctx, domain.DomainEvent{
		Type: domain.EventTypeTaskEnd,
		Payload: taskEndPayload,
		CreatedAt: time.Now(),
	})

	// Wait for second plan
	select {
	case ev := <-planEvents:
		var secondPlan domain.Plan
		if err := json.Unmarshal(ev.Payload, &secondPlan); err != nil {
			t.Fatalf("Failed to unmarshal next plan: %v", err)
		}
		t.Logf("Next Plan Created: %s", secondPlan.Objective)
	case <-ctx.Done():
		t.Fatal("Timeout waiting for next plan after task completion")
	}
	
	_ = svc
}
