package execution

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type queuedAction struct {
	ctx    context.Context
	action domain.Action
}

type TaskExecutionEngine struct {
	controller  Controller
	logger      *slog.Logger
	mu          sync.Mutex
	queue       []queuedAction
	inFlight    *ExecutionTask
	maxInFlight int
	retryCount  int
}

func NewTaskExecutionEngine(ctrl Controller, logger *slog.Logger) *TaskExecutionEngine {
	return &TaskExecutionEngine{
		controller:  ctrl,
		logger:      logger.With(slog.String("component", "task_execution_engine")),
		queue:       make([]queuedAction, 0),
		maxInFlight: 1,
		retryCount:  0,
	}
}

// HasController checks if a controller is actually registered.
func (e *TaskExecutionEngine) HasController() bool {
	if manager, ok := e.controller.(*ControllerManager); ok {
		return manager.current.Load() != nil
	}
	return true
}

func (e *TaskExecutionEngine) Enqueue(ctx context.Context, action domain.Action) {
	e.mu.Lock()

	if action.Priority < 0 && (e.inFlight != nil || len(e.queue) > 0) {
		e.mu.Unlock()
		return
	}

	e.queue = append(e.queue, queuedAction{ctx: ctx, action: action})
	e.mu.Unlock()

	e.pump()
}

func (e *TaskExecutionEngine) pump() {
	e.mu.Lock()

	if e.inFlight != nil || len(e.queue) == 0 {
		e.mu.Unlock()
		return
	}

	bestIdx := 0
	for i := 1; i < len(e.queue); i++ {
		if e.queue[i].action.Priority > e.queue[bestIdx].action.Priority {
			bestIdx = i
		}
	}

	qa := e.queue[bestIdx]
	e.queue = append(e.queue[:bestIdx], e.queue[bestIdx+1:]...)

	e.inFlight = &ExecutionTask{
		Action:      qa.action,
		Status:      StatusDispatched,
		EnqueueTime: time.Now(),
	}
	e.mu.Unlock() // Unlock before reaching out to the controller

	err := e.controller.Dispatch(qa.ctx, qa.action)
	if err != nil {
		e.mu.Lock()
		// Only nil it out if it hasn't already been aborted/replaced by something else
		if e.inFlight != nil && e.inFlight.Action.ID == qa.action.ID {
			e.inFlight.Status = StatusFailed
			e.inFlight.Error = err.Error()
			e.inFlight = nil
		}

		// FIX: Cap synchronous dispatch retries to prevent the goroutine hydra
		e.retryCount++
		shouldRetry := e.retryCount < 5
		e.mu.Unlock()

		if err.Error() != "no active controller" {
			e.logger.Error("Failed to dispatch task", slog.Any("error", err), slog.String("action", qa.action.Action), slog.Int("retry", e.retryCount))
		}

		if shouldRetry {
			// Throttle loop on immediate synchronous failure with backoff
			backoff := time.Duration(e.retryCount*100) * time.Millisecond
			time.Sleep(backoff)
			go e.pump()
		} else {
			e.logger.Error("Max dispatch retries exceeded, dropping task", slog.String("action", qa.action.Action))
			e.mu.Lock()
			e.retryCount = 0
			e.mu.Unlock()
		}
		return
	}

	// Reset retry counter on successful dispatch
	e.mu.Lock()
	e.retryCount = 0
	e.mu.Unlock()
}

func (e *TaskExecutionEngine) OnTaskStart(actionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.inFlight != nil && e.inFlight.Action.ID == actionID {
		e.inFlight.Status = StatusRunning
		now := time.Now()
		e.inFlight.StartTime = &now
	}
}

func (e *TaskExecutionEngine) OnTaskEnd(actionID string, success bool) {
	e.mu.Lock()
	if e.inFlight != nil && e.inFlight.Action.ID == actionID {
		if success {
			e.inFlight.Status = StatusCompleted
		} else {
			e.inFlight.Status = StatusFailed
		}
		now := time.Now()
		e.inFlight.EndTime = &now
		e.inFlight = nil
	}
	e.mu.Unlock()

	e.pump()
}

func (e *TaskExecutionEngine) AbortCurrent(ctx context.Context, reason string) error {
	e.mu.Lock()
	if e.inFlight != nil {
		e.inFlight.Status = StatusAborted
		now := time.Now()
		e.inFlight.EndTime = &now
		e.inFlight = nil
	}

	e.queue = make([]queuedAction, 0)
	e.mu.Unlock()

	return e.controller.AbortCurrent(ctx, reason)
}
