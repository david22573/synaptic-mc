package execution

import (
	"container/heap"
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
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

type TaskManager struct {
	engine      *TaskExecutionEngine
	ctrlManager *ControllerManager
	logger      *slog.Logger
	timeouts    map[string]time.Duration
	OnDrain     func()

	queue       ActionPriorityQueue
	mu          sync.Mutex
	activeTask  *domain.Action
	activeCtx   context.Context // Stored derived context
	cancelTask  context.CancelFunc
	watchdog    chan struct{}
	signalCh    chan struct{}
	idleSince   time.Time
	lastFailure time.Time // Added backoff tracking

	scheduleMu  sync.Mutex
	scheduled   []scheduledItem
	scheduleSig chan struct{}

	isSystemLocked func() bool
	isBotReady     func() bool
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
		engine:      engine,
		ctrlManager: ctrlManager,
		logger:      logger.With(slog.String("component", "task_manager")),
		timeouts:    timeouts,
		queue:       make(ActionPriorityQueue, 0, 10),
		signalCh:    make(chan struct{}, 1),
		scheduleSig: make(chan struct{}, 1),
	}

	heap.Init(&tm.queue)
	return tm
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
	idleCheck := time.NewTicker(1 * time.Second)
	defer idleCheck.Stop()

	scheduleTicker := time.NewTicker(100 * time.Millisecond)
	defer scheduleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-idleCheck.C:
			tm.mu.Lock()
			// Backoff check: Don't run curiosity if we just failed a task (wait 5s for reflex)
			if time.Since(tm.lastFailure) < 5*time.Second {
				tm.mu.Unlock()
				continue
			}

			systemLocked := tm.isSystemLocked != nil && tm.isSystemLocked()
			hasController := tm.engine.HasController()
			botReady := tm.isBotReady == nil || tm.isBotReady()

			shouldRunCuriosity := tm.activeTask == nil && tm.queue.Len() == 0 && !systemLocked && hasController && botReady

			if shouldRunCuriosity {
				if tm.idleSince.IsZero() {
					tm.idleSince = time.Now()
				} else if time.Since(tm.idleSince) > 5*time.Second {
					tm.logger.Info("Curiosity loop triggered: autonomous exploration")
					exploreTask := domain.Action{
						ID:        "explore-curiosity",
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
			tm.processScheduled()

		case <-tm.scheduleSig:
			tm.processScheduled()

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
	tm.logger.Debug("Warm starting next task in background", slog.String("action", nextTask.Action))
}

func (tm *TaskManager) IsIdle() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.activeTask == nil && tm.queue.Len() == 0
}

func (tm *TaskManager) Enqueue(ctx context.Context, tasks ...domain.Action) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for _, task := range tasks {
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

func (tm *TaskManager) processScheduled() {
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
			t := action // FIX: copy value to avoid loop variable pointer capture
			heap.Push(&tm.queue, &t)
		}
		tm.mu.Unlock()

		select {
		case tm.signalCh <- struct{}{}:
		default:
		}
	}
}

func (tm *TaskManager) Complete(ctx context.Context, taskID string, success bool) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.activeTask == nil || tm.activeTask.ID != taskID {
		return nil
	}

	if tm.watchdog != nil {
		close(tm.watchdog)
		tm.watchdog = nil
	}

	if tm.cancelTask != nil {
		tm.cancelTask()
		tm.cancelTask = nil
		tm.activeCtx = nil
	}
	tm.activeTask = nil

	if !success {
		tm.lastFailure = time.Now() // Trigger Curiosity backoff
		if tm.ctrlManager != nil {
			if idm := tm.ctrlManager.GetIdempotent(); idm != nil {
				idm.Clear(taskID)
			}
		}
		tm.queue = make(ActionPriorityQueue, 0, 10)
		heap.Init(&tm.queue)
		if tm.OnDrain != nil {
			go tm.OnDrain()
		}
		return nil
	}

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
	if tm.watchdog != nil {
		close(tm.watchdog)
		tm.watchdog = nil
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
		tm.logger.Warn("Controller missing, preserving active task")
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

	// Fix: store and use the derived context so abort signals actually propagate to the engine
	derivedCtx, cancel := context.WithCancel(ctx)
	tm.activeTask = &action
	tm.activeCtx = derivedCtx
	tm.cancelTask = cancel
	tm.watchdog = make(chan struct{})
	tm.mu.Unlock()

	tm.engine.Enqueue(derivedCtx, action)

	timeout, exists := tm.timeouts[action.Action]
	if !exists {
		timeout = 45 * time.Second
	}

	go func(done chan struct{}, waitTime time.Duration, id string) {
		timer := time.NewTimer(waitTime + 10*time.Second)
		defer timer.Stop()

		select {
		case <-timer.C:
			_ = tm.Complete(context.Background(), id, false)
		case <-done:
			return
		}
	}(tm.watchdog, timeout, action.ID)
}
