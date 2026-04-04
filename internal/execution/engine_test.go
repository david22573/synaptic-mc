package execution

import (
	"context"
	"testing"
	"log/slog"
	"os"
	"david22573/synaptic-mc/internal/domain"
)

type MockController struct {
	dispatched []domain.Action
	ready      bool
}

func (m *MockController) Dispatch(ctx context.Context, action domain.Action) error {
	m.dispatched = append(m.dispatched, action)
	return nil
}

func (m *MockController) AbortCurrent(ctx context.Context, reason string) error {
	return nil
}

func (m *MockController) Close() error {
	return nil
}

func (m *MockController) IsReady() bool {
	return m.ready
}

func TestTaskExecutionEngine_Priority(t *testing.T) {
	mockCtrl := &MockController{ready: true}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	engine := NewTaskExecutionEngine(mockCtrl, logger)

	ctx := context.Background()
	
	// Enqueue low priority task
	engine.Enqueue(ctx, domain.Action{ID: "low", Priority: 1, Action: "mine"})
	// Enqueue high priority task
	engine.Enqueue(ctx, domain.Action{ID: "high", Priority: 10, Action: "eat"})

	// Pump once
	engine.pump()
	
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.inFlight == nil || engine.inFlight.Action.ID != "high" {
		t.Errorf("Expected 'high' task to be in-flight, got %v", engine.inFlight)
	}
}

func TestTaskExecutionEngine_Deduplication(t *testing.T) {
	mockCtrl := &MockController{ready: true}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	engine := NewTaskExecutionEngine(mockCtrl, logger)

	ctx := context.Background()
	action := domain.Action{ID: "task-1", Action: "mine", Target: domain.Target{Name: "stone"}}

	engine.Enqueue(ctx, action)
	engine.Enqueue(ctx, action) // Should be dropped

	engine.mu.Lock()
	qLen := len(engine.queue)
	engine.mu.Unlock()

	if qLen != 1 {
		t.Errorf("Expected queue length 1 after deduplication, got %d", qLen)
	}
}

func TestTaskExecutionEngine_CuriosityBypass(t *testing.T) {
	mockCtrl := &MockController{ready: true}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	engine := NewTaskExecutionEngine(mockCtrl, logger)

	ctx := context.Background()
	curiosity := domain.Action{ID: "explore-curiosity-stable", Action: "explore"}

	engine.Enqueue(ctx, curiosity)
	engine.Enqueue(ctx, curiosity) // Curiosity should NOT be dropped

	engine.mu.Lock()
	qLen := len(engine.queue)
	engine.mu.Unlock()

	if qLen != 2 {
		t.Errorf("Expected queue length 2 for curiosity (no dedup), got %d", qLen)
	}
}
