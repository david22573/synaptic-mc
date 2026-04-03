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

func (e *TaskExecutionEngine) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			e.pump()
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func (e *TaskExecutionEngine) HasController() bool {
	return e.controller.IsReady()
}

func (e *TaskExecutionEngine) Enqueue(ctx context.Context, action domain.Action) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if action.Priority < 0 && (e.inFlight != nil || len(e.queue) > 0) {
		return
	}

	e.queue = append(e.queue, queuedAction{ctx: ctx, action: action})
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
	e.mu.Unlock()

	err := e.controller.Dispatch(qa.ctx, qa.action)
	if err != nil {
		e.mu.Lock()
		if e.inFlight != nil && e.inFlight.Action.ID == qa.action.ID {
			e.inFlight.Status = StatusFailed
			e.inFlight.Error = err.Error()
			e.inFlight = nil
		}

		e.retryCount++
		shouldRetry := e.retryCount < 5
		e.mu.Unlock()

		if err.Error() != "no active controller" {
			e.logger.Error("Failed to dispatch task", slog.Any("error", err), slog.String("action", qa.action.Action), slog.Int("retry", e.retryCount))
		}

		if shouldRetry {
			backoff := time.Duration(e.retryCount*100) * time.Millisecond
			time.Sleep(backoff)

			e.mu.Lock()
			e.queue = append([]queuedAction{qa}, e.queue...)
			e.mu.Unlock()
		} else {
			e.logger.Error("Max dispatch retries exceeded, dropping task", slog.String("action", qa.action.Action))
			e.mu.Lock()
			e.retryCount = 0
			e.mu.Unlock()
		}
		return
	}

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
	defer e.mu.Unlock()

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
