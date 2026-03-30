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

type TaskManager struct {
	controller execution.Controller
	logger     *slog.Logger
	timeouts   map[string]time.Duration
	OnDrain    func()

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

func (m *TaskManager) IsIdle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTask == nil && len(m.queue) == 0
}

func (m *TaskManager) Enqueue(ctx context.Context, tasks ...domain.Action) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(tasks) == 0 {
		return nil
	}

	if m.activeTask != nil && tasks[0].Priority < m.activeTask.Priority {
		_ = m.abortActiveLocked(ctx, "preempted_by_priority")
		m.queue = tasks
		return m.dispatchNextLocked(ctx)
	}

	m.queue = append(m.queue, tasks...)
	if m.activeTask == nil {
		return m.dispatchNextLocked(ctx)
	}

	return nil
}

func (m *TaskManager) Complete(ctx context.Context, taskID string, success bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeTask == nil || m.activeTask.ID != taskID {
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
	m.activeTask = nil

	if !success {
		// Flush queue and trigger replanning on failure
		m.queue = make([]domain.Action, 0)
		if m.OnDrain != nil {
			go m.OnDrain()
		}
		return nil
	}

	err := m.dispatchNextLocked(ctx)
	if m.activeTask == nil && len(m.queue) == 0 && m.OnDrain != nil {
		go m.OnDrain()
	}

	return err
}

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

	taskCtx, cancel := context.WithCancel(context.Background())
	m.activeTask = &next
	m.activeCancel = cancel
	m.watchdogDone = make(chan struct{})

	if err := m.controller.Dispatch(taskCtx, next); err != nil {
		m.activeTask = nil
		m.activeCancel = nil
		if m.watchdogDone != nil {
			close(m.watchdogDone)
			m.watchdogDone = nil
		}
		return fmt.Errorf("dispatch failed: %w", err)
	}

	timeout, exists := m.timeouts[next.Action]
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
	}(m.watchdogDone, timeout, next.ID)

	return nil
}
