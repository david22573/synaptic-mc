package execution

import (
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
