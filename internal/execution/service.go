// internal/execution/service.go
package execution

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type ControlService struct {
	bus         domain.EventBus
	engine      *TaskExecutionEngine
	taskManager *TaskManager
	ctrlManager *ControllerManager
	logger      *slog.Logger

	// Control Layer State
	failures    map[string]*FailureRecord
	failuresMu  sync.RWMutex
	stability   StabilityState
	stabilityMu sync.RWMutex

	activeTimers map[string]*time.Timer
	timersMu     sync.Mutex
}

func NewControlService(bus domain.EventBus, engine *TaskExecutionEngine, tm *TaskManager, cm *ControllerManager, logger *slog.Logger) *ControlService {
	s := &ControlService{
		bus:          bus,
		engine:       engine,
		taskManager:  tm,
		ctrlManager:  cm,
		logger:       logger.With(slog.String("component", "control_service")),
		failures:     make(map[string]*FailureRecord),
		activeTimers: make(map[string]*time.Timer),
	}

	bus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(s.handlePlanCreated))
	bus.Subscribe(domain.EventTypePlanInvalidated, domain.FuncHandler(s.handlePlanInvalidated))
	bus.Subscribe(domain.EventTypeTaskStart, domain.FuncHandler(s.handleTaskStart))
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))
	bus.Subscribe(domain.EventTypePanic, domain.FuncHandler(s.handlePanic))
	bus.Subscribe(domain.EventBotDeath, domain.FuncHandler(s.handleBotDeath))

	return s
}

func (s *ControlService) SetReflexActive(active bool) {
	s.stabilityMu.Lock()
	defer s.stabilityMu.Unlock()
	s.stability.ReflexActive = active
}

func (s *ControlService) handlePlanCreated(ctx context.Context, event domain.DomainEvent) {
	s.stabilityMu.RLock()
	reflexActive := s.stability.ReflexActive
	s.stabilityMu.RUnlock()

	if reflexActive {
		s.logger.Warn("Reflex active: dropping new plan from decision layer")
		return
	}

	var plan domain.Plan
	if err := json.Unmarshal(event.Payload, &plan); err != nil {
		s.logger.Error("Failed to unmarshal plan", slog.Any("error", err))
		return
	}

	if len(plan.Tasks) > 0 {
		task := plan.Tasks[0]

		s.failuresMu.RLock()
		if record, exists := s.failures[task.ID]; exists {
			backoff := time.Duration(record.Count) * time.Second
			if time.Since(record.LastFailure) < backoff {
				s.failuresMu.RUnlock()
				s.logger.Warn("Plan backoff active: skipping task", slog.String("task_id", task.ID))
				return
			}
		}
		s.failuresMu.RUnlock()

		_ = s.taskManager.Enqueue(ctx, task)
	}
}

func (s *ControlService) handlePlanInvalidated(ctx context.Context, event domain.DomainEvent) {
	_ = s.taskManager.Halt(ctx, "plan_invalidated")
	s.clearAllWatchdogs()
}

func (s *ControlService) handlePanic(ctx context.Context, event domain.DomainEvent) {
	s.SetReflexActive(true)
	_ = s.taskManager.Halt(ctx, "panic_triggered")
	s.clearAllWatchdogs()
}

func (s *ControlService) handleBotDeath(ctx context.Context, event domain.DomainEvent) {
	s.SetReflexActive(false)
	_ = s.taskManager.Halt(ctx, "bot_died")
	s.clearAllWatchdogs()
}

func (s *ControlService) handleTaskStart(ctx context.Context, event domain.DomainEvent) {
	var payload struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err == nil {
		s.engine.OnTaskStart(payload.CommandID)

		inFlight := s.engine.GetInFlight()
		if inFlight != nil && inFlight.ID == payload.CommandID {
			s.failuresMu.Lock()
			if _, exists := s.failures[payload.CommandID]; !exists {
				s.failures[payload.CommandID] = &FailureRecord{
					IntentID: payload.CommandID,
					Action:   *inFlight,
				}
			}
			s.failuresMu.Unlock()

			// Safely access Timeout if it exists on Action, otherwise default
			timeout := 45 * time.Second

			// Padded by 15s to allow organic JS timeout to hit the bus first
			timeout += 15 * time.Second
			sessionID := event.SessionID

			timer := time.AfterFunc(timeout, func() {
				s.logger.Warn("Execution deadline exceeded, triggering recovery", slog.String("task_id", payload.CommandID))
				s.triggerRecovery(sessionID, payload.CommandID, "DEADLINE_EXCEEDED")
			})

			s.timersMu.Lock()
			s.activeTimers[payload.CommandID] = timer
			s.timersMu.Unlock()
		}
	}
}

func (s *ControlService) triggerRecovery(sessionID string, taskID string, cause string) {
	_ = s.ctrlManager.AbortCurrent(context.Background(), cause)

	failedPayload, _ := json.Marshal(map[string]interface{}{
		"status":     "FAILED",
		"command_id": taskID,
		"cause":      cause,
		"progress":   0.0,
	})

	s.bus.Publish(context.Background(), domain.DomainEvent{
		SessionID: sessionID,
		Type:      domain.EventTypeTaskEnd,
		Payload:   failedPayload,
		CreatedAt: time.Now(),
	})
}

func (s *ControlService) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	var payload struct {
		Status    string  `json:"status"`
		CommandID string  `json:"command_id"`
		Cause     string  `json:"cause"`
		Progress  float64 `json:"progress"` // Phase 1 & 3: Read execution progress
	}

	if err := json.Unmarshal(event.Payload, &payload); err == nil {
		success := payload.Status == "COMPLETED"

		s.timersMu.Lock()
		if timer, ok := s.activeTimers[payload.CommandID]; ok {
			timer.Stop()
			delete(s.activeTimers, payload.CommandID)
		}
		s.timersMu.Unlock()

		if !success && payload.Cause != "preempted_by_priority" && payload.Cause != "plan_invalidated" {
			s.failuresMu.Lock()
			record, exists := s.failures[payload.CommandID]
			if !exists {
				record = &FailureRecord{IntentID: payload.CommandID}
				s.failures[payload.CommandID] = record
			}
			record.Count++
			record.LastFailure = time.Now()

			// Phase 1 + 3: Full execution result mapping using domain struct
			res := domain.ExecutionResult{
				Success:  false,
				Cause:    payload.Cause,
				Progress: payload.Progress,
				Action:   record.Action,
			}

			directive := s.ctrlManager.EvaluateFailure(res, record.Count)
			actionToRetry := record.Action
			s.failuresMu.Unlock()

			// Keep the planner in the loop with the actual failure reason
			s.ctrlManager.RecordResult(res)

			s.logger.Warn("Task failed, applying adaptation strategy",
				slog.String("task_id", payload.CommandID),
				slog.String("cause", payload.Cause),
				slog.Float64("progress", payload.Progress),
				slog.String("strategy", string(directive.Strategy)),
				slog.Duration("delay", directive.Delay),
			)

			// Close the current failed instance so the queue unblocks
			s.engine.OnTaskEnd(payload.CommandID, false)
			_ = s.taskManager.Complete(ctx, payload.CommandID, false)

			if actionToRetry.ID == "" {
				s.logger.Warn("Task dropped, missing action details for retry", slog.String("task_id", payload.CommandID))
				return
			}

			switch directive.Strategy {
			case StrategyRetrySame, StrategyRetryDifferent:
				executeAt := time.Now().Add(directive.Delay)
				_ = s.taskManager.EnqueueScheduled(ctx, actionToRetry, executeAt)

			case StrategyDegrade:
				actionToRetry.Action = directive.Fallback
				actionToRetry.ID = actionToRetry.ID + "-degraded"
				executeAt := time.Now().Add(directive.Delay)
				_ = s.taskManager.EnqueueScheduled(ctx, actionToRetry, executeAt)

			case StrategyAbort:
				// Already aborted and completed above
			}

		} else {
			// Clean up success states
			s.failuresMu.Lock()
			delete(s.failures, payload.CommandID)
			s.failuresMu.Unlock()

			if success {
				s.ctrlManager.RecordResult(domain.ExecutionResult{Success: true, Progress: 1.0, Cause: ""})
			}

			s.engine.OnTaskEnd(payload.CommandID, success)
			_ = s.taskManager.Complete(ctx, payload.CommandID, success)
		}
	}
}

func (s *ControlService) clearAllWatchdogs() {
	s.timersMu.Lock()
	defer s.timersMu.Unlock()
	for id, timer := range s.activeTimers {
		timer.Stop()
		delete(s.activeTimers, id)
	}
}
