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
	retryCount    int
}

func NewTaskExecutionEngine(ctrl Controller, logger *slog.Logger) *TaskExecutionEngine {
	e := &TaskExecutionEngine{
		controller:    ctrl,
		logger:        logger.With(slog.String("component", "task_execution_engine")),
		queue:         make(actionHeap, 0),
		recentActions: make(map[string]time.Time),
		maxInFlight:   1,
		retryCount:    0,
	}
	heap.Init(&e.queue)
	return e
}

func (e *TaskExecutionEngine) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.cleanupRecentActions()
		default:
			e.pump()
			time.Sleep(10 * time.Millisecond)
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

// FIX: Return a value copy to prevent data races after the lock is released
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

	// Curiosity tasks should never be deduplicated, they are a fallback for idleness
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
				e.retryCount = 0
				e.mu.Unlock()
				return
			}

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
				qa.enqueuedAt = time.Now()
				heap.Push(&e.queue, qa)
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
