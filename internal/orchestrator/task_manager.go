package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
)

type TaskManager struct {
	controller execution.Controller
	logger     *slog.Logger
	timeouts   map[string]time.Duration
	OnDrain    func()

	actionQueue chan domain.Action
	mu          sync.Mutex
	activeTask  *domain.Action
	cancelTask  context.CancelFunc
	watchdog    chan struct{}
}

func NewTaskManager(ctrl execution.Controller, timeouts map[string]time.Duration, logger *slog.Logger) *TaskManager {
	if timeouts == nil {
		timeouts = make(map[string]time.Duration)
	}
	tm := &TaskManager{
		controller:  ctrl,
		logger:      logger.With(slog.String("component", "task_manager")),
		timeouts:    timeouts,
		actionQueue: make(chan domain.Action, 100),
	}

	go tm.Run(context.Background())
	return tm
}

func (m *TaskManager) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case action := <-m.actionQueue:
			m.dispatchCurrent(ctx, action)
		}
	}
}

func (m *TaskManager) IsIdle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTask == nil && len(m.actionQueue) == 0
}

func (m *TaskManager) Enqueue(ctx context.Context, tasks ...domain.Action) error {
	for _, task := range tasks {
		select {
		case m.actionQueue <- task:
		default:
			m.logger.Warn("Task queue full, dropping action", slog.String("action_id", task.ID))
		}
	}
	return nil
}

func (m *TaskManager) Complete(ctx context.Context, taskID string, success bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeTask == nil || m.activeTask.ID != taskID {
		return nil
	}

	if m.watchdog != nil {
		close(m.watchdog)
		m.watchdog = nil
	}

	if m.cancelTask != nil {
		m.cancelTask()
		m.cancelTask = nil
	}
	m.activeTask = nil

	if !success {
		m.flushQueue()
		if m.OnDrain != nil {
			go m.OnDrain()
		}
		return nil
	}

	if len(m.actionQueue) == 0 && m.OnDrain != nil {
		go m.OnDrain()
	}

	return nil
}

func (m *TaskManager) Halt(ctx context.Context, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushQueue()
	return m.abortActiveLocked(ctx, reason)
}

func (m *TaskManager) flushQueue() {
	for {
		select {
		case <-m.actionQueue:
		default:
			return
		}
	}
}

func (m *TaskManager) abortActiveLocked(ctx context.Context, reason string) error {
	if m.activeTask == nil {
		return nil
	}
	if m.watchdog != nil {
		close(m.watchdog)
		m.watchdog = nil
	}
	if m.cancelTask != nil {
		m.cancelTask()
		m.cancelTask = nil
	}
	err := m.controller.AbortCurrent(ctx, reason)
	m.activeTask = nil
	return err
}

func (m *TaskManager) dispatchCurrent(ctx context.Context, action domain.Action) {
	m.mu.Lock()
	if m.activeTask != nil && action.Priority > m.activeTask.Priority {
		_ = m.abortActiveLocked(ctx, "preempted_by_priority")
	} else if m.activeTask != nil {
		m.mu.Unlock()
		return
	}

	taskCtx, cancel := context.WithCancel(context.Background())
	m.activeTask = &action
	m.cancelTask = cancel
	m.watchdog = make(chan struct{})
	m.mu.Unlock()

	if err := m.controller.Dispatch(taskCtx, action); err != nil {
		m.mu.Lock()
		m.activeTask = nil
		m.cancelTask = nil
		if m.watchdog != nil {
			close(m.watchdog)
			m.watchdog = nil
		}
		m.mu.Unlock()
		m.logger.Error("Dispatch failed", slog.Any("error", err))
		return
	}

	timeout, exists := m.timeouts[action.Action]
	if !exists {
		timeout = 45 * time.Second
	}

	go func(done chan struct{}, waitTime time.Duration, id string) {
		select {
		case <-time.After(waitTime + 10*time.Second):
			_ = m.Complete(context.Background(), id, false)
		case <-done:
			return
		}
	}(m.watchdog, timeout, action.ID)
}
