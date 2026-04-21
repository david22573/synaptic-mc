package execution

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/state"
)

type ActionPriorityQueue []*domain.Action

func (pq ActionPriorityQueue) Len() int { return len(pq) }

func (pq ActionPriorityQueue) Less(i, j int) bool {
	return pq[i].Priority > pq[j].Priority
}

func (pq ActionPriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *ActionPriorityQueue) Push(x interface{}) {
	item := x.(*domain.Action)
	*pq = append(*pq, item)
}

func (pq *ActionPriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[0 : n-1]
	return item
}

type scheduledItem struct {
	action    domain.Action
	executeAt time.Time
}

type DangerStateProvider interface {
	GetDangerState() state.DangerState
}

type TaskManager struct {
	engine      *TaskExecutionEngine
	ctrlManager *ControllerManager
	logger      *slog.Logger
	timeouts    map[string]time.Duration
	OnDrain     func()

	queue         ActionPriorityQueue
	recentActions map[string]time.Time
	mu            sync.Mutex
	activeTask    *domain.Action
	activeCtx     context.Context
	cancelTask    context.CancelFunc
	signalCh      chan struct{}
	idleSince     time.Time
	lastFailure   time.Time
	failureCount  int

	scheduleMu  sync.Mutex
	scheduled   []scheduledItem
	scheduleSig chan struct{}

	isSystemLocked func() bool
	isBotReady     func() bool
	dangerProvider DangerStateProvider
}

func NewTaskManager(
	engine *TaskExecutionEngine,
	ctrlManager *ControllerManager,
	timeouts map[string]time.Duration,
	logger *slog.Logger,
) *TaskManager {
	if timeouts == nil {
		timeouts = make(map[string]time.Duration)
	}
	tm := &TaskManager{
		engine:        engine,
		ctrlManager:   ctrlManager,
		logger:        logger.With(slog.String("component", "task_manager")),
		timeouts:      timeouts,
		queue:         make(ActionPriorityQueue, 0, 10),
		recentActions: make(map[string]time.Time),
		signalCh:      make(chan struct{}, 1),
		scheduleSig:   make(chan struct{}, 1),
	}

	heap.Init(&tm.queue)
	return tm
}

func (tm *TaskManager) SetDangerProvider(provider DangerStateProvider) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.dangerProvider = provider
}

func (tm *TaskManager) SetLockChecker(checker func() bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.isSystemLocked = checker
}

func (tm *TaskManager) SetReadyChecker(checker func() bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.isBotReady = checker
}

func (tm *TaskManager) Run(ctx context.Context) {
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tm.Halt(shutdownCtx, "task manager shutdown")
	}()

	idleCheck := time.NewTicker(1 * time.Second)
	defer idleCheck.Stop()

	scheduleTicker := time.NewTicker(100 * time.Millisecond)
	defer scheduleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			tm.logger.InfoContext(ctx, "Task manager stopping due to context cancellation")
			return
		case <-idleCheck.C:
			tm.mu.Lock()

			// Clean up recent actions dedup mapping
			now := time.Now()
			for k, t := range tm.recentActions {
				if now.Sub(t) > 5*time.Second {
					delete(tm.recentActions, k)
				}
			}

			backoffSec := math.Min(300, 5*math.Pow(2, float64(tm.failureCount)))
			if time.Since(tm.lastFailure) < time.Duration(backoffSec)*time.Second {
				tm.mu.Unlock()
				continue
			}

			systemLocked := tm.isSystemLocked != nil && tm.isSystemLocked()
			hasController := tm.engine.HasController()
			botReady := tm.isBotReady == nil || tm.isBotReady()
			
			dangerSafe := true
			if tm.dangerProvider != nil {
				dangerSafe = tm.dangerProvider.GetDangerState() == state.DangerSafe
			}

			shouldRunCuriosity := tm.activeTask == nil && tm.queue.Len() == 0 && !systemLocked && hasController && botReady && dangerSafe

			if shouldRunCuriosity {
				if tm.idleSince.IsZero() {
					tm.idleSince = time.Now()
				} else if time.Since(tm.idleSince) > 6*time.Second {
					tm.logger.InfoContext(ctx, "Curiosity loop triggered: autonomous exploration", slog.Int("failure_count", tm.failureCount))

					exploreTask := domain.Action{
						ID:        fmt.Sprintf("explore-curiosity-%d", time.Now().UnixNano()),
						Action:    "explore",
						Target:    domain.Target{Type: "category", Name: "surroundings"},
						Priority:  -1,
						Rationale: "Curiosity loop: self-starting exploration",
					}
					t := exploreTask
					heap.Push(&tm.queue, &t)
					tm.idleSince = time.Time{}
					select {
					case tm.signalCh <- struct{}{}:
					default:
					}
				}
			} else {
				tm.idleSince = time.Time{}
			}
			tm.mu.Unlock()

		case <-scheduleTicker.C:
			tm.processScheduled(ctx)

		case <-tm.scheduleSig:
			tm.processScheduled(ctx)

		case <-tm.signalCh:
			tm.mu.Lock()
			if tm.queue.Len() > 0 {
				peek := tm.queue[0]
				if tm.activeTask == nil || peek.Priority > tm.activeTask.Priority {
					next := heap.Pop(&tm.queue).(*domain.Action)

					if tm.queue.Len() > 0 {
						peekNext := tm.queue[0]
						tm.warmStartNext(ctx, peekNext)
					}

					tm.idleSince = time.Time{}
					tm.mu.Unlock()
					tm.dispatchCurrent(ctx, *next)
					continue
				}
			}
			tm.mu.Unlock()
		}
	}
}

func (tm *TaskManager) warmStartNext(ctx context.Context, nextTask *domain.Action) {
	tm.logger.DebugContext(ctx, "Warm starting next task in background", slog.String("action", nextTask.Action))
	tm.engine.Preload(ctx, *nextTask)
}

func (tm *TaskManager) IsIdle() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.activeTask == nil && tm.queue.Len() == 0 && tm.engine.IsIdle()
}

func (tm *TaskManager) Enqueue(ctx context.Context, tasks ...domain.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	now := time.Now()
	for _, task := range tasks {
		isCuriosity := strings.HasPrefix(task.ID, "explore-curiosity-")
		dedupKey := task.Action + ":" + task.Target.Name

		if !isCuriosity {
			if lastSeen, exists := tm.recentActions[dedupKey]; exists {
				if now.Sub(lastSeen) < 2*time.Second {
					tm.logger.Debug("Dropped duplicate action within dedup window", slog.String("action", task.Action))
					continue
				}
			}
		}
		tm.recentActions[dedupKey] = now

		t := task
		heap.Push(&tm.queue, &t)
	}
	tm.idleSince = time.Time{}

	select {
	case tm.signalCh <- struct{}{}:
	default:
	}
	return nil
}

func (tm *TaskManager) EnqueueScheduled(ctx context.Context, action domain.Action, executeAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	tm.scheduleMu.Lock()
	tm.scheduled = append(tm.scheduled, scheduledItem{action, executeAt})
	sort.Slice(tm.scheduled, func(i, j int) bool {
		return tm.scheduled[i].executeAt.Before(tm.scheduled[j].executeAt)
	})
	tm.scheduleMu.Unlock()

	select {
	case tm.scheduleSig <- struct{}{}:
	default:
	}
	return nil
}

func (tm *TaskManager) processScheduled(_ context.Context) {
	tm.scheduleMu.Lock()
	defer tm.scheduleMu.Unlock()

	if len(tm.scheduled) == 0 {
		return
	}

	now := time.Now()
	ready := make([]domain.Action, 0)

	for len(tm.scheduled) > 0 && tm.scheduled[0].executeAt.Before(now) {
		ready = append(ready, tm.scheduled[0].action)
		tm.scheduled = tm.scheduled[1:]
	}

	if len(ready) > 0 {
		tm.mu.Lock()
		for _, action := range ready {
			t := action
			heap.Push(&tm.queue, &t)
		}
		tm.mu.Unlock()

		select {
		case tm.signalCh <- struct{}{}:
		default:
		}
	}
}

func (tm *TaskManager) Complete(ctx context.Context, taskID string, success bool, cause string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.activeTask == nil || tm.activeTask.ID != taskID {
		return nil
	}

	if tm.cancelTask != nil {
		tm.cancelTask()
		tm.cancelTask = nil
		tm.activeCtx = nil
	}
	tm.activeTask = nil

	if !success {
		// Controlled stops like panic or preemption shouldn't trigger idleness backoff
		if !domain.IsControlledStop(cause) {
			tm.lastFailure = time.Now()
			tm.failureCount++
		}

		if tm.ctrlManager != nil {
			// Idempotency is now managed internally by the wsActor
		}
		tm.queue = make(ActionPriorityQueue, 0, 10)
		heap.Init(&tm.queue)

		if tm.OnDrain != nil {
			go tm.OnDrain()
		}
		return nil
	}

	tm.failureCount = 0

	if tm.queue.Len() == 0 {
		if tm.OnDrain != nil {
			go tm.OnDrain()
		}
	} else {
		select {
		case tm.signalCh <- struct{}{}:
		default:
		}
	}

	return nil
}

func (tm *TaskManager) Halt(ctx context.Context, reason string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.queue = make(ActionPriorityQueue, 0, 10)
	heap.Init(&tm.queue)
	return tm.abortActiveLocked(ctx, reason)
}

func (tm *TaskManager) abortActiveLocked(ctx context.Context, reason string) error {
	if tm.activeTask == nil {
		return nil
	}
	if tm.cancelTask != nil {
		tm.cancelTask()
		tm.cancelTask = nil
		tm.activeCtx = nil
	}

	err := tm.engine.AbortCurrent(ctx, reason)
	tm.activeTask = nil
	return err
}

func (tm *TaskManager) dispatchCurrent(ctx context.Context, action domain.Action) {
	tm.mu.Lock()

	if tm.activeTask != nil && !tm.engine.HasController() {
		tm.logger.WarnContext(ctx, "Controller missing, preserving active task")
		tm.mu.Unlock()
		return
	}

	if tm.activeTask != nil && action.Priority > tm.activeTask.Priority {
		if action.Priority-tm.activeTask.Priority > 5 {
			_ = tm.abortActiveLocked(ctx, "preempted_by_priority")
		} else {
			t := action
			heap.Push(&tm.queue, &t)
			tm.mu.Unlock()
			return
		}
	} else if tm.activeTask != nil {
		tm.mu.Unlock()
		return
	}

	if err := ctx.Err(); err != nil {
		tm.mu.Unlock()
		return
	}

	derivedCtx, cancel := context.WithCancel(ctx)
	tm.activeTask = &action
	tm.activeCtx = derivedCtx
	tm.cancelTask = cancel
	tm.mu.Unlock()

	tm.engine.ExecuteAsync(derivedCtx, action)
}
