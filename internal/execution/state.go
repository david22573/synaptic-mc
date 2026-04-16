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
	minHold       time.Duration // Minimum duration to hold the lease regardless of priority
	canPreempt    bool          // Whether this task can be preempted by higher priority tasks
	retryCount    map[string]int
	lastFailTime  map[string]time.Time
}

func NewExecutionState() *ExecutionState {
	return &ExecutionState{
		retryCount:   make(map[string]int),
		lastFailTime: make(map[string]time.Time),
	}
}

func (s *ExecutionState) AcquireLease(task domain.Action, timeout time.Duration, minHold time.Duration, canPreempt bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.activeTask != nil {
		// 1. Check MinHold
		if time.Since(s.startTime) < s.minHold {
			return false
		}

		// 2. Check Preemptability
		if !s.canPreempt {
			// Even if higher priority, if non-preemptable we must wait for timeout or completion
			if time.Since(s.startTime) < s.leaseTimeout {
				return false
			}
		}

		// 3. Check Priority
		if GetPriority(task.Action) <= GetPriority(s.activeTask.Action) {
			return false
		}
	}

	s.activeTask = &task
	s.startTime = time.Now()
	s.leaseTimeout = timeout
	s.minHold = minHold
	s.canPreempt = canPreempt
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
