package execution

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type TaskExecutionEngine struct {
	controller  Controller
	logger      *slog.Logger
	mu          sync.Mutex
	queue       []domain.Action
	inFlight    *ExecutionTask
	maxInFlight int
}

func NewTaskExecutionEngine(ctrl Controller, logger *slog.Logger) *TaskExecutionEngine {
	return &TaskExecutionEngine{
		controller:  ctrl,
		logger:      logger.With(slog.String("component", "task_execution_engine")),
		queue:       make([]domain.Action, 0),
		maxInFlight: 1,
	}
}

// Enqueue adds an action to the execution queue, applying backpressure if needed.
func (e *TaskExecutionEngine) Enqueue(action domain.Action) {
	e.mu.Lock()

	// Backpressure: Drop low-priority drift/background actions if we are busy
	if action.Priority < 0 && (e.inFlight != nil || len(e.queue) > 0) {
		e.mu.Unlock()
		return
	}

	e.queue = append(e.queue, action)
	e.mu.Unlock()

	e.pump()
}

// pump drains the queue based on priority and max in-flight limits.
func (e *TaskExecutionEngine) pump() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.inFlight != nil {
		return // Enforce maxInFlight = 1
	}

	if len(e.queue) == 0 {
		return
	}

	// Find the highest priority action in the queue
	bestIdx := 0
	for i := 1; i < len(e.queue); i++ {
		if e.queue[i].Priority > e.queue[bestIdx].Priority {
			bestIdx = i
		}
	}

	action := e.queue[bestIdx]

	// Remove the selected action from the queue
	e.queue = append(e.queue[:bestIdx], e.queue[bestIdx+1:]...)

	e.inFlight = &ExecutionTask{
		Action:      action,
		Status:      StatusDispatched,
		EnqueueTime: time.Now(),
	}

	err := e.controller.Dispatch(context.Background(), action)
	if err != nil {
		e.inFlight.Status = StatusFailed
		e.inFlight.Error = err.Error()
		e.inFlight = nil
		e.logger.Error("Failed to dispatch task", slog.Any("error", err), slog.String("action", action.Action))
		go e.pump() // Try next
		return
	}
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

// AbortCurrent forces the engine to drop its active task, clears the queue, and signals the TS controller.
func (e *TaskExecutionEngine) AbortCurrent(ctx context.Context, reason string) error {
	e.mu.Lock()
	if e.inFlight != nil {
		e.inFlight.Status = StatusAborted
		now := time.Now()
		e.inFlight.EndTime = &now
		e.inFlight = nil
	}

	// Clear the engine queue to prevent backlogged tasks from instantly executing after abort
	e.queue = make([]domain.Action, 0)
	e.mu.Unlock()

	return e.controller.AbortCurrent(ctx, reason)
}
