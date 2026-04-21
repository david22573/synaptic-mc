package execution

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anthdm/hollywood/actor"
	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/domain"
)

type TaskExecutionEngine struct {
	controller Controller
	logger     *slog.Logger
	mu         sync.RWMutex
	inFlight   *ExecutionTask
	cfg        config.ExecutionConfig
	
	// Managed cancellation for engine-wide background tasks
	cancel context.CancelFunc
}

func NewTaskExecutionEngine(ctrl Controller, logger *slog.Logger, cfg config.ExecutionConfig) *TaskExecutionEngine {
	return &TaskExecutionEngine{
		controller: ctrl,
		logger:     logger.With(slog.String("component", "task_execution_engine")),
		cfg:        cfg,
	}
}

func (e *TaskExecutionEngine) UpdateConfig(cfg config.ExecutionConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
}

func (e *TaskExecutionEngine) PID() *actor.PID {
	return nil // Mocking for now since e.pid doesn't exist yet
}
// Start initiates the background maintenance loops.
func (e *TaskExecutionEngine) Start(ctx context.Context) {
	innerCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel

	e.mu.RLock()
	cleanupInterval := time.Duration(e.cfg.CleanupTickerMs) * time.Millisecond
	e.mu.RUnlock()

	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-innerCtx.Done():
			e.logger.Info("Task engine shutting down")
			return
		case <-cleanupTicker.C:
			e.checkStuckTasks()
		}
	}
}

// checkStuckTasks performs periodic audits of dispatched but unacknowledged tasks.
func (e *TaskExecutionEngine) checkStuckTasks() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.inFlight != nil && e.inFlight.Status == StatusDispatched {
		// threshold for the Node.js bot to acknowledge TASK_START
		ackThreshold := time.Duration(e.cfg.DispatchTimeoutMs) * time.Millisecond
		if time.Since(e.inFlight.EnqueueTime) > ackThreshold && e.inFlight.StartTime == nil {
			e.logger.Warn("Task stuck in dispatched state (no TASK_START received). Force clearing.", 
				slog.String("task_id", e.inFlight.Action.ID))
			
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
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.inFlight == nil
}

func (e *TaskExecutionEngine) GetInFlight() *domain.Action {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.inFlight != nil {
		copyAction := e.inFlight.Action
		return &copyAction
	}
	return nil
}

// ExecuteAsync dispatches a task to the bot and monitors its lifecycle.
func (e *TaskExecutionEngine) ExecuteAsync(ctx context.Context, action domain.Action) {
	e.mu.Lock()
	e.inFlight = &ExecutionTask{
		Action:      action,
		Status:      StatusDispatched,
		EnqueueTime: time.Now(),
	}
	maintenanceInterval := time.Duration(e.cfg.MaintenanceTickerMs) * time.Millisecond
	e.mu.Unlock()

	go func() {
		// Propagate context to the controller dispatch
		err := e.controller.Dispatch(ctx, action)
		if err != nil {
			e.handleFailure(ctx, action, err)
			return
		}

		// Maintenance loop for active tasks
		ticker := time.NewTicker(maintenanceInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				e.logger.Debug("Task context cancelled mid-execution", 
					slog.String("action", action.Action),
					slog.String("task_id", action.ID))
				// Use original context for abort if possible, otherwise Background
				_ = e.AbortCurrent(context.Background(), "context_cancelled")
				return
			case <-ticker.C:
				e.mu.RLock()
				inFlight := e.inFlight
				e.mu.RUnlock()

				// Exit loop if task resolved via events or replaced by new dispatch
				if inFlight == nil || inFlight.Action.ID != action.ID {
					return
				}
			}
		}
	}()
}

func (e *TaskExecutionEngine) Preload(ctx context.Context, action domain.Action) {
	e.mu.RLock()
	inFlight := e.inFlight
	e.mu.RUnlock()

	// Only preload if we're already running something else
	if inFlight == nil || inFlight.Action.ID == action.ID {
		return
	}

	go func() {
		_ = e.controller.Preload(ctx, action)
	}()
}

func (e *TaskExecutionEngine) handleFailure(ctx context.Context, action domain.Action, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Handle clean cancellations
	if err == context.Canceled || err == context.DeadlineExceeded {
		if e.inFlight != nil && e.inFlight.Action.ID == action.ID {
			e.inFlight.Status = StatusAborted
			now := time.Now()
			e.inFlight.EndTime = &now
			e.inFlight = nil
		}
		return
	}

	// Update state for hard failures
	if e.inFlight != nil && e.inFlight.Action.ID == action.ID {
		e.inFlight.Status = StatusFailed
		e.inFlight.Error = err.Error()
		e.inFlight = nil
	}

	if err.Error() != "no active controller" {
		e.logger.Error("Task execution failed", 
			slog.Any("error", err), 
			slog.String("action", action.Action),
			slog.String("task_id", action.ID))
	}
}

func (e *TaskExecutionEngine) RunEmergencyPolicy(ctx context.Context, action domain.Action) {
	e.logger.Warn("EMERGENCY: Running immediate survival policy", slog.String("action", action.Action))
	
	// 1. Interrupt Current
	_ = e.AbortCurrent(ctx, "emergency_interrupt")

	// 2. Immediate Dispatch (bypass normal queue/locks if necessary, but here we just call controller)
	go func() {
		// Create a separate background context for the emergency action to ensure it completes
		emergencyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		
		err := e.controller.Dispatch(emergencyCtx, action)
		if err != nil {
			e.logger.Error("Emergency policy dispatch failed", slog.Any("error", err))
		}
	}()
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

	if err := e.controller.AbortCurrent(ctx, reason); err != nil {
		return fmt.Errorf("failed to abort current task: %w", err)
	}
	return nil
}
