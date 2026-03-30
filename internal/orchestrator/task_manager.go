package orchestrator

import (
	"container/heap"
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
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
	engine   *execution.TaskExecutionEngine // REPLACED CONTROLLER WITH ENGINE
	logger   *slog.Logger
	timeouts map[string]time.Duration
	OnDrain  func()

	queue      ActionPriorityQueue
	mu         sync.Mutex
	activeTask *domain.Action
	cancelTask context.CancelFunc
	watchdog   chan struct{}
	signalCh   chan struct{}
	idleSince  time.Time

	scheduleMu  sync.Mutex
	scheduled   []scheduledItem
	scheduleSig chan struct{}
}

func NewTaskManager(engine *execution.TaskExecutionEngine, timeouts map[string]time.Duration, logger *slog.Logger) *TaskManager {
	if timeouts == nil {
		timeouts = make(map[string]time.Duration)
	}
	tm := &TaskManager{
		engine:      engine, // INJECT ENGINE HERE
		logger:      logger.With(slog.String("component", "task_manager")),
		timeouts:    timeouts,
		queue:       make(ActionPriorityQueue, 0, 10),
		signalCh:    make(chan struct{}, 1),
		scheduleSig: make(chan struct{}, 1),
	}

	heap.Init(&tm.queue)
	return tm
}

func (m *TaskManager) Run(ctx context.Context) {
	idleCheck := time.NewTicker(1 * time.Second)
	defer idleCheck.Stop()

	scheduleTicker := time.NewTicker(100 * time.Millisecond)
	defer scheduleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-idleCheck.C:
			m.mu.Lock()
			if m.activeTask == nil && m.queue.Len() == 0 {
				if m.idleSince.IsZero() {
					m.idleSince = time.Now()
				} else if time.Since(m.idleSince) > 5*time.Second {
					m.logger.Info("Curiosity loop triggered: autonomous exploration")
					exploreTask := domain.Action{
						ID:        "explore-curiosity",
						Action:    "explore",
						Target:    domain.Target{Type: "category", Name: "surroundings"},
						Priority:  -1,
						Rationale: "Curiosity loop: self-starting exploration",
					}
					t := exploreTask
					heap.Push(&m.queue, &t)
					m.idleSince = time.Time{}
					select {
					case m.signalCh <- struct{}{}:
					default:
					}
				}
			} else {
				m.idleSince = time.Time{}
			}
			m.mu.Unlock()

		case <-scheduleTicker.C:
			m.processScheduled()

		case <-m.scheduleSig:
			m.processScheduled()

		case <-m.signalCh:
			m.mu.Lock()
			if m.activeTask == nil && m.queue.Len() > 0 {
				next := heap.Pop(&m.queue).(*domain.Action)

				if m.queue.Len() > 0 {
					peekNext := m.queue[0]
					m.warmStartNext(ctx, peekNext)
				}

				m.idleSince = time.Time{}
				m.mu.Unlock()
				m.dispatchCurrent(ctx, *next)
			} else {
				m.mu.Unlock()
			}
		}
	}
}

func (m *TaskManager) warmStartNext(ctx context.Context, nextTask *domain.Action) {
	m.logger.Debug("Warm starting next task in background", slog.String("action", nextTask.Action))
}

func (m *TaskManager) IsIdle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTask == nil && m.queue.Len() == 0
}

func (m *TaskManager) Enqueue(ctx context.Context, tasks ...domain.Action) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, task := range tasks {
		t := task
		heap.Push(&m.queue, &t)
	}
	m.idleSince = time.Time{}

	select {
	case m.signalCh <- struct{}{}:
	default:
	}
	return nil
}

func (m *TaskManager) EnqueueScheduled(ctx context.Context, action domain.Action, executeAt time.Time) error {
	m.scheduleMu.Lock()
	m.scheduled = append(m.scheduled, scheduledItem{action, executeAt})
	sort.Slice(m.scheduled, func(i, j int) bool {
		return m.scheduled[i].executeAt.Before(m.scheduled[j].executeAt)
	})
	m.scheduleMu.Unlock()

	select {
	case m.scheduleSig <- struct{}{}:
	default:
	}
	return nil
}

func (m *TaskManager) processScheduled() {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()

	if len(m.scheduled) == 0 {
		return
	}

	now := time.Now()
	ready := make([]domain.Action, 0)

	for len(m.scheduled) > 0 && m.scheduled[0].executeAt.Before(now) {
		ready = append(ready, m.scheduled[0].action)
		m.scheduled = m.scheduled[1:]
	}

	if len(ready) > 0 {
		m.mu.Lock()
		for _, action := range ready {
			heap.Push(&m.queue, &action)
		}
		m.mu.Unlock()

		select {
		case m.signalCh <- struct{}{}:
		default:
		}
	}
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
		m.queue = make(ActionPriorityQueue, 0, 10)
		heap.Init(&m.queue)
		if m.OnDrain != nil {
			go m.OnDrain()
		}
		return nil
	}

	if m.queue.Len() == 0 {
		if m.OnDrain != nil {
			go m.OnDrain()
		}
	} else {
		select {
		case m.signalCh <- struct{}{}:
		default:
		}
	}

	return nil
}

func (m *TaskManager) Halt(ctx context.Context, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.queue = make(ActionPriorityQueue, 0, 10)
	heap.Init(&m.queue)
	return m.abortActiveLocked(ctx, reason)
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

	// Delegate abort to the engine
	err := m.engine.AbortCurrent(ctx, reason)

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

	_, cancel := context.WithCancel(ctx)
	m.activeTask = &action
	m.cancelTask = cancel
	m.watchdog = make(chan struct{})
	m.mu.Unlock()

	// THE FIX: Delegate to Engine instead of Controller
	m.engine.Enqueue(action)

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
