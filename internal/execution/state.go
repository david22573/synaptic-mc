package execution

import (
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type ExecutionState struct {
	mu sync.RWMutex

	activeTask    *domain.Action
	startTime     time.Time
	leaseTimeout  time.Duration
	retryCount    map[string]int
	lastFailTime  map[string]time.Time
}

func NewExecutionState() *ExecutionState {
	return &ExecutionState{
		retryCount:   make(map[string]int),
		lastFailTime: make(map[string]time.Time),
	}
}

func (s *ExecutionState) AcquireLease(task domain.Action, timeout time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.activeTask != nil {
		// Only allow preempting with higher priority
		if GetPriority(task.Action) <= GetPriority(s.activeTask.Action) {
			return false
		}
	}

	s.activeTask = &task
	s.startTime = time.Now()
	s.leaseTimeout = timeout
	return true
}

func (s *ExecutionState) ReleaseLease(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.activeTask != nil && s.activeTask.ID == taskID {
		s.activeTask = nil
	}
}

func (s *ExecutionState) GetActiveTask() *domain.Action {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeTask
}

func (s *ExecutionState) RecordFailure(action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryCount[action]++
	s.lastFailTime[action] = time.Now()
}

func (s *ExecutionState) ResetRetries(action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.retryCount, action)
}

func (s *ExecutionState) GetRetryStats(action string) (int, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retryCount[action], s.lastFailTime[action]
}
