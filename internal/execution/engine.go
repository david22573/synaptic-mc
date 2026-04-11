package execution

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type TaskExecutionEngine struct {
	controller Controller
	logger     *slog.Logger
	mu         sync.Mutex
	inFlight   *ExecutionTask
}

func NewTaskExecutionEngine(ctrl Controller, logger *slog.Logger) *TaskExecutionEngine {
	return &TaskExecutionEngine{
		controller: ctrl,
		logger:     logger.With(slog.String("component", "task_execution_engine")),
	}
}

func (e *TaskExecutionEngine) Start(ctx context.Context) {
	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanupTicker.C:
			e.checkStuckTasks()
		}
	}
}

func (e *TaskExecutionEngine) checkStuckTasks() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.inFlight != nil && e.inFlight.Status == StatusDispatched {
		if time.Since(e.inFlight.EnqueueTime) > 15*time.Second && e.inFlight.StartTime == nil {
			e.logger.Warn("Task stuck in dispatched state (no TASK_START received). Aborting to clear state.", slog.String("action", e.inFlight.Action.ID))
			e.inFlight.Status = StatusFailed
			e.inFlight.Error = "DISPATCH_TIMEOUT_NO_ACK"
			e.inFlight = nil
		}
	}
}

func (e *TaskExecutionEngine) HasController() bool {
	return e.controller.IsReady()
}

func (e *TaskExecutionEngine) IsIdle() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inFlight == nil
}

func (e *TaskExecutionEngine) GetInFlight() *domain.Action {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inFlight != nil {
		copyAction := e.inFlight.Action
		return &copyAction
	}
	return nil
}

func (e *TaskExecutionEngine) ExecuteAsync(ctx context.Context, action domain.Action) {
	e.mu.Lock()
	e.inFlight = &ExecutionTask{
		Action:      action,
		Status:      StatusDispatched,
		EnqueueTime: time.Now(),
	}
	e.mu.Unlock()

	go func() {
		err := e.controller.Dispatch(ctx, action)
		if err != nil {
			e.handleFailure(action, err)
			return
		}

		// Mid-task active loop
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				e.logger.Debug("Task context cancelled mid-execution", slog.String("action", action.Action))
				e.AbortCurrent(context.Background(), "context_cancelled")
				return
			case <-ticker.C:
				e.mu.Lock()
				inFlight := e.inFlight
				e.mu.Unlock()

				// Break loop if task resolved or replaced
				if inFlight == nil || inFlight.Action.ID != action.ID {
					return
				}

				// Here you can poll e.controller.ReadState(ctx) to send mid-task
				// corrections or updates if the Go planner needs to intervene.
			}
		}
	}()
}

func (e *TaskExecutionEngine) handleFailure(action domain.Action, err error) {
	if err == context.Canceled || err == context.DeadlineExceeded {
		e.logger.Debug("Task aborted cleanly", slog.String("action", action.Action), slog.Any("reason", err))
		e.mu.Lock()
		if e.inFlight != nil && e.inFlight.Action.ID == action.ID {
			e.inFlight.Status = StatusAborted
			now := time.Now()
			e.inFlight.EndTime = &now
			e.inFlight = nil
		}
		e.mu.Unlock()
		return
	}

	e.mu.Lock()
	if e.inFlight != nil && e.inFlight.Action.ID == action.ID {
		e.inFlight.Status = StatusFailed
		e.inFlight.Error = err.Error()
		e.inFlight = nil
	}
	e.mu.Unlock()

	if err.Error() != "no active controller" {
		e.logger.Error("Task execution failed, notifying planner", slog.Any("error", err), slog.String("action", action.Action))
	}
}

func (e *TaskExecutionEngine) ExecuteDirect(ctx context.Context, action domain.Action) error {
	return e.controller.Dispatch(ctx, action)
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
	e.mu.Unlock()

	return e.controller.AbortCurrent(ctx, reason)
}
