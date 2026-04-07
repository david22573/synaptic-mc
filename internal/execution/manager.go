package execution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

var ErrDraining = errors.New("controller is draining")

type RetryStrategy string

const (
	StrategyRetrySame      RetryStrategy = "RETRY_SAME"
	StrategyRetryDifferent RetryStrategy = "RETRY_DIFFERENT"
	StrategyDegrade        RetryStrategy = "DEGRADE"
	StrategyAbort          RetryStrategy = "ABORT"
)

type RetryDirective struct {
	Strategy RetryStrategy
	Delay    time.Duration
	Fallback string
}

var DegradationMap = map[string]string{
	"explore": "random_walk",
	"mine":    "reposition",
	"build":   "gather",
	"gather":  "explore",
}

type VersionedController struct {
	ID       string
	Ctrl     Controller
	draining atomic.Bool
	active   sync.WaitGroup
}

type ControllerManager struct {
	current atomic.Pointer[VersionedController]
	mu      sync.Mutex

	// Phase 1 & 3: Track execution feedback loop
	lastResults []domain.ExecutionResult
	historyMu   sync.RWMutex
}

func NewControllerManager() *ControllerManager {
	return &ControllerManager{
		lastResults: make([]domain.ExecutionResult, 0, 20),
	}
}

func (m *ControllerManager) HasActiveController() bool {
	return m.current.Load() != nil
}

func (m *ControllerManager) IsReady() bool {
	vc := m.current.Load()
	if vc == nil {
		return false
	}
	return vc.Ctrl.IsReady()
}

// GetIdempotent returns the IdempotentController if it is the currently active controller.
func (m *ControllerManager) GetIdempotent() *IdempotentController {
	vc := m.current.Load()
	if vc == nil {
		return nil
	}
	if idm, ok := vc.Ctrl.(*IdempotentController); ok {
		return idm
	}
	return nil
}

func (m *ControllerManager) RemoveController(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.current.Load()
	if current != nil && current.ID == id {
		current.draining.Store(true)
		m.current.Store(nil)

		// Close in background to avoid blocking
		go func(vc *VersionedController) {
			vc.active.Wait()
			_ = vc.Ctrl.Close()
		}(current)
	}
}

func (m *ControllerManager) SetController(id string, ctrl Controller) {
	m.mu.Lock()
	old := m.current.Load()
	if old != nil {
		old.draining.Store(true)
	}

	vc := &VersionedController{
		ID:   id,
		Ctrl: ctrl,
	}
	m.current.Store(vc)
	m.mu.Unlock()

	// Drain and close old controller outside the lock to avoid stalling the dispatch queue
	if old != nil {
		old.active.Wait()
		_ = old.Ctrl.Close()
	}
}

func (m *ControllerManager) Dispatch(ctx context.Context, action domain.Action) error {
	vc := m.current.Load()
	if vc == nil {
		return errors.New("no active controller")
	}
	if vc.draining.Load() {
		return ErrDraining
	}

	vc.active.Add(1)
	defer vc.active.Done()

	action.ControllerID = vc.ID
	return vc.Ctrl.Dispatch(ctx, action)
}

func (m *ControllerManager) AbortCurrent(ctx context.Context, reason string) error {
	vc := m.current.Load()
	if vc == nil {
		return nil
	}

	vc.active.Add(1)
	defer vc.active.Done()

	return vc.Ctrl.AbortCurrent(ctx, reason)
}

// EvaluateFailure calculates the intelligent backoff and degradation strategy.
func (m *ControllerManager) EvaluateFailure(
	res domain.ExecutionResult,
	attempts int,
) RetryDirective {
	baseDelay := 500 * time.Millisecond
	delay := baseDelay * time.Duration(attempts)

	// Cap backoff at 5 seconds to prevent stalling
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}

	directive := RetryDirective{
		Strategy: StrategyRetrySame,
		Delay:    delay,
	}

	// 1. If we are making clear progress, KEEP RETRYING even if attempts are high
	if res.Progress > 0.3 || res.Cause == domain.CausePartial {
		directive.Strategy = StrategyRetrySame
		directive.Delay = 100 * time.Millisecond
		return directive
	}

	// 2. Critical failures or too many attempts -> Degrade or Abort
	if attempts > 3 {
		directive.Strategy = StrategyAbort
		return directive
	}

	if attempts > 1 {
		directive.Strategy = StrategyDegrade
		directive.Fallback = m.getDegradedAction(res.Action)
		return directive
	}

	// 3. Environment/Internal blocks -> Try different target
	if res.Cause == domain.CauseTimeout || res.Cause == domain.CauseBlocked ||
		res.Cause == domain.CauseStuck {
		directive.Strategy = StrategyRetryDifferent
		return directive
	}

	// Default to StrategyRetrySame for the first failure
	return directive
}

func (m *ControllerManager) getDegradedAction(action domain.Action) string {
	if fallback, exists := DegradationMap[action.Action]; exists {
		return fallback
	}
	return "idle" // Ultimate safe fallback
}

// RecordResult tracks execution trends for the planner and humanizer
func (m *ControllerManager) RecordResult(res domain.ExecutionResult) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	m.lastResults = append(m.lastResults, res)
	if len(m.lastResults) > 20 { // Keep last 20
		m.lastResults = m.lastResults[1:]
	}
}

// GetRecentFailures allows the planner to see the execution feedback loop
func (m *ControllerManager) GetRecentFailures() []domain.ExecutionResult {
	m.historyMu.RLock()
	defer m.historyMu.RUnlock()
	var failures []domain.ExecutionResult
	for _, res := range m.lastResults {
		if !res.Success || res.Progress < 1.0 {
			failures = append(failures, res)
		}
	}
	return failures
}

func (m *ControllerManager) GetSuccessRate() float64 {
	m.historyMu.RLock()
	defer m.historyMu.RUnlock()
	if len(m.lastResults) == 0 {
		return 1.0 // Default to perfect if no history
	}
	successes := 0
	for _, res := range m.lastResults {
		if res.Success && res.Progress >= 1.0 {
			successes++
		}
	}
	return float64(successes) / float64(len(m.lastResults))
}

func (m *ControllerManager) Close() error {
	m.mu.Lock()
	vc := m.current.Load()
	if vc != nil {
		vc.draining.Store(true)
	}
	m.current.Store(nil)
	m.mu.Unlock()

	// Drain and close outside the lock
	if vc != nil {
		vc.active.Wait()
		return vc.Ctrl.Close()
	}
	return nil
}
