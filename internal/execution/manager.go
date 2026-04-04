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
	lastFailures []domain.ExecutionResult
	historyMu    sync.RWMutex
}

func NewControllerManager() *ControllerManager {
	return &ControllerManager{
		lastFailures: make([]domain.ExecutionResult, 0, 10),
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

func (m *ControllerManager) SetController(id string, ctrl Controller) {
	m.mu.Lock()
	defer m.mu.Unlock()

	old := m.current.Load()
	if old != nil {
		old.draining.Store(true)
		old.active.Wait()
		_ = old.Ctrl.Close()
	}

	vc := &VersionedController{
		ID:   id,
		Ctrl: ctrl,
	}
	m.current.Store(vc)
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
		Strategy: StrategyAbort,
		Delay:    delay,
	}

	if attempts > 3 {
		directive.Strategy = StrategyDegrade
		directive.Fallback = m.getDegradedAction(res.Action)
		return directive
	}

	// Phase 1 + 3: If we are making progress or had partial success, retry same target fast
	if res.Progress > 0.3 || res.Cause == domain.CausePartial {
		directive.Strategy = StrategyRetrySame
		directive.Delay = 100 * time.Millisecond
		return directive
	}

	// Phase 3: Hard block / Timeout means try a different target entirely
	if res.Cause == domain.CauseTimeout || res.Cause == domain.CauseBlocked ||
		res.Cause == domain.CauseStuck {
		directive.Strategy = StrategyRetryDifferent
		return directive
	}

	directive.Strategy = StrategyDegrade
	directive.Fallback = m.getDegradedAction(res.Action)
	return directive
}

func (m *ControllerManager) getDegradedAction(action domain.Action) string {
	if fallback, exists := DegradationMap[action.Type]; exists {
		return fallback
	}
	return "idle" // Ultimate safe fallback
}

// RecordResult tracks failure trends for the planner
func (m *ControllerManager) RecordResult(res domain.ExecutionResult) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	if !res.Success || res.Progress < 1.0 {
		m.lastFailures = append(m.lastFailures, res)
		if len(m.lastFailures) > 10 { // Keep last 10
			m.lastFailures = m.lastFailures[1:]
		}
	}
}

// GetRecentFailures allows the planner to see the execution feedback loop
func (m *ControllerManager) GetRecentFailures() []domain.ExecutionResult {
	m.historyMu.RLock()
	defer m.historyMu.RUnlock()
	res := make([]domain.ExecutionResult, len(m.lastFailures))
	copy(res, m.lastFailures)
	return res
}

func (m *ControllerManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vc := m.current.Load()
	if vc != nil {
		vc.draining.Store(true)
		vc.active.Wait()
		return vc.Ctrl.Close()
	}
	return nil
}
