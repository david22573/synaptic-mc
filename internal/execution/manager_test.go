package execution

import (
	"context"
	"sync"
	"testing"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

func TestControllerManager_EvaluateFailure(t *testing.T) {
	m := NewControllerManager()

	t.Run("Retries on first failure", func(t *testing.T) {
		res := domain.ExecutionResult{Progress: 0.0}
		directive := m.EvaluateFailure(res, 1)
		if directive.Strategy != StrategyRetrySame {
			t.Errorf("Expected StrategyRetrySame on first failure, got %s", directive.Strategy)
		}
	})

	t.Run("Retries fast on progress", func(t *testing.T) {
		res := domain.ExecutionResult{Progress: 0.5}
		directive := m.EvaluateFailure(res, 5)
		if directive.Strategy != StrategyRetrySame {
			t.Errorf("Expected StrategyRetrySame on progress, got %s", directive.Strategy)
		}
		if directive.Delay != 100*time.Millisecond {
			t.Errorf("Expected 100ms delay on progress, got %v", directive.Delay)
		}
	})

	t.Run("Degrades on second failure without progress", func(t *testing.T) {
		res := domain.ExecutionResult{Action: domain.Action{Action: "mine"}, Progress: 0.0}
		directive := m.EvaluateFailure(res, 2)
		if directive.Strategy != StrategyDegrade {
			t.Errorf("Expected StrategyDegrade on second failure, got %s", directive.Strategy)
		}
		if directive.Fallback != "reposition" {
			t.Errorf("Expected 'reposition' fallback for 'mine', got %s", directive.Fallback)
		}
	})

	t.Run("Retries different on block", func(t *testing.T) {
		res := domain.ExecutionResult{Progress: 0.0, Cause: domain.CauseBlocked}
		directive := m.EvaluateFailure(res, 1)
		if directive.Strategy != StrategyRetryDifferent {
			t.Errorf("Expected StrategyRetryDifferent on block, got %s", directive.Strategy)
		}
	})

	t.Run("Aborts after too many failures", func(t *testing.T) {
		res := domain.ExecutionResult{Progress: 0.0}
		directive := m.EvaluateFailure(res, 4)
		if directive.Strategy != StrategyAbort {
			t.Errorf("Expected StrategyAbort after 4 failures, got %s", directive.Strategy)
		}
	})
}

type MockDrainingController struct {
	isReady bool
	closed  bool
	mu      sync.Mutex
}

func (m *MockDrainingController) Dispatch(ctx context.Context, action domain.Action) error { return nil }
func (m *MockDrainingController) AbortCurrent(ctx context.Context, reason string) error   { return nil }
func (m *MockDrainingController) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}
func (m *MockDrainingController) IsReady() bool { return m.isReady }

func TestControllerManager_SetController(t *testing.T) {
	m := NewControllerManager()
	c1 := &MockDrainingController{}
	c2 := &MockDrainingController{}

	m.SetController("c1", c1)
	if m.current.Load().ID != "c1" {
		t.Errorf("Expected current controller ID to be c1, got %s", m.current.Load().ID)
	}

	m.SetController("c2", c2)
	if m.current.Load().ID != "c2" {
		t.Errorf("Expected current controller ID to be c2, got %s", m.current.Load().ID)
	}

	// Wait a bit for the async close to finish
	time.Sleep(10 * time.Millisecond)

	c1.mu.Lock()
	if !c1.closed {
		t.Error("Expected c1 to be closed after being replaced")
	}
	c1.mu.Unlock()
}

func TestControllerManager_Close(t *testing.T) {
	m := NewControllerManager()
	c1 := &MockDrainingController{}

	m.SetController("c1", c1)
	err := m.Close()
	if err != nil {
		t.Errorf("Expected no error on Close, got %v", err)
	}

	if m.current.Load() != nil {
		t.Error("Expected current controller to be nil after Close")
	}

	// Wait a bit for the async close to finish
	time.Sleep(10 * time.Millisecond)

	c1.mu.Lock()
	if !c1.closed {
		t.Error("Expected c1 to be closed after manager Close")
	}
	c1.mu.Unlock()
}
