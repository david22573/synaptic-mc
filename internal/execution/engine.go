package execution

import (
	"container/heap"
	"context"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type queuedAction struct {
	ctx        context.Context
	action     domain.Action
	enqueuedAt time.Time
}

type actionHeap []queuedAction

func (h actionHeap) Len() int { return len(h) }
func (h actionHeap) Less(i, j int) bool {
	if h[i].action.Priority == h[j].action.Priority {
		return h[i].enqueuedAt.Before(h[j].enqueuedAt)
	}
	return h[i].action.Priority > h[j].action.Priority
}
func (h actionHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *actionHeap) Push(x interface{}) { *h = append(*h, x.(queuedAction)) }
func (h *actionHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = queuedAction{} // FIX: Prevent context memory leak on pop
	*h = old[0 : n-1]
	return item
}

type TaskExecutionEngine struct {
	controller    Controller
	logger        *slog.Logger
	mu            sync.Mutex
	queue         actionHeap
	recentActions map[string]time.Time
	inFlight      *ExecutionTask
	maxInFlight   int
}

func NewTaskExecutionEngine(ctrl Controller, logger *slog.Logger) *TaskExecutionEngine {
	e := &TaskExecutionEngine{
		controller:    ctrl,
		logger:        logger.With(slog.String("component", "task_execution_engine")),
		queue:         make(actionHeap, 0),
		recentActions: make(map[string]time.Time),
		maxInFlight:   1,
	}
	heap.Init(&e.queue)
	return e
}

func (e *TaskExecutionEngine) Start(ctx context.Context) {
	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()

	pumpTicker := time.NewTicker(10 * time.Millisecond)
	defer pumpTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanupTicker.C:
			e.cleanupRecentActions()
			e.checkStuckTasks()
		case <-pumpTicker.C:
			e.pump()
		}
	}
}

func (e *TaskExecutionEngine) checkStuckTasks() {
	e.mu.Lock()
	defer e.mu.Unlock()

	// If a task is dispatched but we never receive a TASK_START ack from the TS client,
	// the ControlService watchdog won't fire. Abort to prevent permanent queue deadlock.
	if e.inFlight != nil && e.inFlight.Status == StatusDispatched {
		if time.Since(e.inFlight.EnqueueTime) > 15*time.Second && e.inFlight.StartTime == nil {
			e.logger.Warn("Task stuck in dispatched state (no TASK_START received). Aborting to clear queue deadlock.", slog.String("action", e.inFlight.Action.ID))
			e.inFlight.Status = StatusFailed
			e.inFlight.Error = "DISPATCH_TIMEOUT_NO_ACK"
			e.inFlight = nil
		}
	}
}

func (e *TaskExecutionEngine) cleanupRecentActions() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	for k, t := range e.recentActions {
		if now.Sub(t) > 5*time.Second {
			delete(e.recentActions, k)
		}
	}
}

func (e *TaskExecutionEngine) HasController() bool {
	return e.controller.IsReady()
}

func (e *TaskExecutionEngine) IsIdle() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inFlight == nil && len(e.queue) == 0
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

func (e *TaskExecutionEngine) Enqueue(ctx context.Context, action domain.Action) {
	e.mu.Lock()
	defer e.mu.Unlock()

	isCuriosity := action.ID == "explore-curiosity-stable"

	dedupKey := action.Action + ":" + action.Target.Name
	if !isCuriosity {
		if lastSeen, exists := e.recentActions[dedupKey]; exists {
			if time.Since(lastSeen) < 2*time.Second {
				e.logger.Debug("Dropped duplicate action within dedup window", slog.String("action", action.Action))
				return
			}
		}
	}
	e.recentActions[dedupKey] = time.Now()

	heap.Push(&e.queue, queuedAction{
		ctx:        ctx,
		action:     action,
		enqueuedAt: time.Now(),
	})
}

func (e *TaskExecutionEngine) pump() {
	e.mu.Lock()

	if e.inFlight != nil || len(e.queue) == 0 {
		e.mu.Unlock()
		return
	}

	qa := heap.Pop(&e.queue).(queuedAction)

	e.inFlight = &ExecutionTask{
		Action:      qa.action,
		Status:      StatusDispatched,
		EnqueueTime: time.Now(),
	}
	e.mu.Unlock()

	go func(qa queuedAction) {
		err := e.controller.Dispatch(qa.ctx, qa.action)
		if err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				e.logger.Debug("Task aborted cleanly", slog.String("action", qa.action.Action), slog.Any("reason", err))
				e.mu.Lock()
				if e.inFlight != nil && e.inFlight.Action.ID == qa.action.ID {
					e.inFlight = nil
				}
				e.mu.Unlock()
				return
			}

			e.mu.Lock()
			if e.inFlight != nil && e.inFlight.Action.ID == qa.action.ID {
				e.inFlight.Status = StatusFailed
				e.inFlight.Error = err.Error()
				e.inFlight = nil
			}
			e.mu.Unlock()

			if err.Error() != "no active controller" {
				e.logger.Error("Task execution failed, notifying planner", slog.Any("error", err), slog.String("action", qa.action.Action))
			}
			return
		}
	}(qa)
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

	e.queue = make(actionHeap, 0)
	heap.Init(&e.queue)
	e.mu.Unlock()

	return e.controller.AbortCurrent(ctx, reason)
}
