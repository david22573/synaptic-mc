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
}

func NewSupervisor(bus domain.EventBus, logger *slog.Logger) *Supervisor {
	return &Supervisor{
		state:  NewExecutionState(),
		policy: DefaultRetryPolicy,
		logger: logger.With(slog.String("component", "execution_supervisor")),
		bus:    bus,
	}
}

func (s *Supervisor) Dispatch(ctx context.Context, task domain.Action) bool {
	// Dedupe: Don't dispatch if already active
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
	if GetPriority(task.Action) >= 100 {
		timeout = 5 * time.Second // Survival tasks must be snappy
	}

	if s.state.AcquireLease(task, timeout) {
		s.logger.Info("Task lease acquired", 
			slog.String("task_id", task.ID), 
			slog.String("action", task.Action),
			slog.Int("priority", GetPriority(task.Action)))
		observability.Metrics.IncDispatch()
		return true
	}

	s.logger.Debug("Task lease denied (priority too low or locked)", slog.String("action", task.Action))
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
		// Don't retry fatals, but record it
		s.state.RecordFailure(payload.Action)
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
