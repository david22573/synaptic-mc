package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
)

// TaskManager ensures strict concurrency over the bot's physical actions.
// It guarantees only one task is in-flight and handles clean preemption.
type TaskManager struct {
	controller execution.Controller
	logger     *slog.Logger
	timeouts   map[string]time.Duration
	OnDrain    func() // 4.1 FIX: Callback triggered when the queue empties

	mu           sync.Mutex
	activeTask   *domain.Action
	activeCancel context.CancelFunc
	watchdogDone chan struct{}
	queue        []domain.Action
}

func NewTaskManager(ctrl execution.Controller, timeouts map[string]time.Duration, logger *slog.Logger) *TaskManager {
	if timeouts == nil {
		timeouts = make(map[string]time.Duration)
	}
	return &TaskManager{
		controller: ctrl,
		logger:     logger.With(slog.String("component", "task_manager")),
		timeouts:   timeouts,
		queue:      make([]domain.Action, 0),
	}
}

// IsIdle returns true if there are no active or queued tasks
func (m *TaskManager) IsIdle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTask == nil && len(m.queue) == 0
}

// Enqueue pushes a new plan to the queue.
// If it's a high-priority interrupt it aborts the current task.
func (m *TaskManager) Enqueue(ctx context.Context, tasks ...domain.Action) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(tasks) == 0 {
		return nil
	}

	// Lower number = higher priority.
	// If the new task outranks the active one, kill the active one.
	if m.activeTask != nil && tasks[0].Priority < m.activeTask.Priority {
		m.logger.Warn("Preempting active task for higher priority action",
			slog.String("active", m.activeTask.Action),
			slog.String("new", tasks[0].Action),
		)
		_ = m.abortActiveLocked(ctx, "preempted_by_priority")

		// Overwrite the queue entirely with the new plan
		m.queue = tasks
		return m.dispatchNextLocked(ctx)
	}

	m.queue = append(m.queue, tasks...)

	// If the bot is idle, start executing immediately
	if m.activeTask == nil {
		return m.dispatchNextLocked(ctx)
	}

	return nil
}

// Complete handles the lifecycle wrap-up when the TS client reports a task success or failure.
func (m *TaskManager) Complete(ctx context.Context, taskID string, success bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Drop ghost events from previously cancelled tasks
	if m.activeTask == nil || m.activeTask.ID != taskID {
		m.logger.Debug("Ignoring completion for inactive or stale task", slog.String("task_id", taskID))
		return nil
	}

	m.logger.Info("Task concluded", slog.String("action", m.activeTask.Action), slog.Bool("success", success))

	// Release resources for the active task and cancel its watchdog
	if m.watchdogDone != nil {
		close(m.watchdogDone)
		m.watchdogDone = nil
	}

	if m.activeCancel != nil {
		m.activeCancel()
		m.activeCancel = nil
	}
	m.activeTask = nil

	// If the task failed, flush the remaining queue.
	if !success {
		m.logger.Warn("Task failed, flushing remaining plan queue")
		m.queue = make([]domain.Action, 0)

		if m.OnDrain != nil {
			go m.OnDrain()
		}
		return nil
	}

	err := m.dispatchNextLocked(ctx)

	// 4.1 FIX: Trigger replanning immediately if the queue just ran dry
	if m.activeTask == nil && len(m.queue) == 0 && m.OnDrain != nil {
		go m.OnDrain()
	}

	return err
}

// Halt is the emergency brake for Bot Death or Flee events.
func (m *TaskManager) Halt(ctx context.Context, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.queue = make([]domain.Action, 0)
	return m.abortActiveLocked(ctx, reason)
}

func (m *TaskManager) abortActiveLocked(ctx context.Context, reason string) error {
	if m.activeTask == nil {
		return nil
	}

	if m.watchdogDone != nil {
		close(m.watchdogDone)
		m.watchdogDone = nil
	}

	if m.activeCancel != nil {
		m.activeCancel()
		m.activeCancel = nil
	}

	err := m.controller.AbortCurrent(ctx, reason)
	m.activeTask = nil
	return err
}

func (m *TaskManager) dispatchNextLocked(ctx context.Context) error {
	if len(m.queue) == 0 {
		return nil
	}

	next := m.queue[0]
	m.queue = m.queue[1:]

	// Create a dedicated sub-context specifically for this task's lifespan
	taskCtx, cancel := context.WithCancel(ctx)

	m.activeTask = &next
	m.activeCancel = cancel
	m.watchdogDone = make(chan struct{})

	m.logger.Info("Dispatching task", slog.String("action", next.Action), slog.String("target", next.Target.Name))

	if err := m.controller.Dispatch(taskCtx, next); err != nil {
		m.activeTask = nil
		m.activeCancel = nil
		if m.watchdogDone != nil {
			close(m.watchdogDone)
			m.watchdogDone = nil
		}
		return fmt.Errorf("dispatch failed: %w", err)
	}

	// Watchdog Setup
	timeout, exists := m.timeouts[next.Action]
	if !exists {
		timeout = 45 * time.Second // Safe fallback if the action isn't mapped
	}
	gracePeriod := timeout + 10*time.Second
	taskID := next.ID

	go func(done chan struct{}, waitTime time.Duration, id string) {
		select {
		case <-time.After(waitTime):
			m.logger.Warn("Task watchdog triggered; task timed out natively", slog.String("task_id", id))
			// Force completion with failure
			_ = m.Complete(context.Background(), id, false)
		case <-done:
			// Task completed or aborted normally before the timeout
			return
		}
	}(m.watchdogDone, gracePeriod, taskID)

	return nil
}
