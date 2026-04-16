package execution

import (
	"context"
	"log/slog"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/observability"
)

type Supervisor struct {
	state  *ExecutionState
	policy RetryPolicy
	logger *slog.Logger
	bus    domain.EventBus

	// Panic Circuit Breaker
	lastPanicTime time.Time
	panicCooldown time.Duration
}

func NewSupervisor(bus domain.EventBus, logger *slog.Logger) *Supervisor {
	return &Supervisor{
		state:         NewExecutionState(),
		policy:        DefaultRetryPolicy,
		logger:        logger.With(slog.String("component", "execution_supervisor")),
		bus:           bus,
		panicCooldown: 10 * time.Second,
	}
}

func (s *Supervisor) Dispatch(ctx context.Context, task domain.Action) bool {
	// 1. Panic Circuit Breaker check
	if time.Since(s.lastPanicTime) < s.panicCooldown {
		s.logger.Warn("Dispatch denied: Panic circuit breaker open", 
			slog.Duration("remaining", s.panicCooldown - time.Since(s.lastPanicTime)))
		return false
	}

	// 2. Dedupe: Don't dispatch if already active
	active := s.state.GetActiveTask()
	if active != nil && active.ID == task.ID {
		return true
	}

	// Retry Budget & Backoff
	retries, lastFail := s.state.GetRetryStats(task.Action)
	if retries > 0 {
		backoff := s.calculateBackoff(retries)
		if time.Since(lastFail) < backoff {
			s.logger.Debug("Action in backoff cooldown", 
				slog.String("action", task.Action), 
				slog.Int("retries", retries),
				slog.Duration("remaining", backoff - time.Since(lastFail)))
			return false
		}
	}

	if retries >= s.policy.MaxRetries {
		s.logger.Warn("Action retry budget exhausted", slog.String("action", task.Action))
		return false
	}

	// Acquire Lease
	timeout := 30 * time.Second
	minHold := 0 * time.Second
	canPreempt := true

	priority := GetPriority(task.Action)
	if priority >= 100 {
		timeout = 5 * time.Second    // Survival tasks must be snappy
		minHold = 2 * time.Second    // But they must be allowed to stabilize
		canPreempt = false           // DO NOT INTERRUPT ESCAPE
	}

	if s.state.AcquireLease(task, timeout, minHold, canPreempt) {
		s.logger.Info("Task lease acquired", 
			slog.String("task_id", task.ID), 
			slog.String("action", task.Action),
			slog.Int("priority", priority))
		observability.Metrics.IncDispatch()
		return true
	}

	activeTask := s.state.GetActiveTask()
	if activeTask != nil {
		s.logger.Debug("Task lease denied (locked by higher priority)", 
			slog.String("requested", task.Action),
			slog.String("active", activeTask.Action))
		observability.Metrics.IncPreemption()
	} else {
		s.logger.Debug("Task lease denied (unknown lock)", slog.String("action", task.Action))
	}
	return false
}

func (s *Supervisor) HandleTaskEnd(payload domain.TaskEndPayload) {
	s.state.ReleaseLease(payload.CommandID)

	if payload.Status == "COMPLETED" {
		s.state.ResetRetries(payload.Action)
		return
	}

	class := ClassifyFailure(payload.Cause)
	s.logger.Warn("Task failed", 
		slog.String("action", payload.Action), 
		slog.String("cause", payload.Cause),
		slog.String("class", string(class)))

	switch class {
	case FailureFatal:
		// Don't retry fatals, and trip the circuit breaker
		s.state.RecordFailure(payload.Action)
		s.lastPanicTime = time.Now()
		s.logger.Error("Fatal panic detected: Tripping circuit breaker", slog.Duration("cooldown", s.panicCooldown))
	case FailureEnvironmental, FailureRecoverable:
		s.state.RecordFailure(payload.Action)
	case FailurePreempted:
		// Preempted doesn't count against budget
	}
}

func (s *Supervisor) calculateBackoff(retries int) time.Duration {
	backoff := s.policy.InitialBackoff
	for i := 0; i < retries && i < 10; i++ {
		backoff = time.Duration(float64(backoff) * s.policy.Multiplier)
	}
	if backoff > s.policy.MaxBackoff {
		backoff = s.policy.MaxBackoff
	}
	return backoff
}
