package execution

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/humanization"
)

type StateProvider interface {
	GetCurrentState() domain.VersionedState
}

type ControlService struct {
	bus         domain.EventBus
	engine      *TaskExecutionEngine
	taskManager *TaskManager
	ctrlManager *ControllerManager
	humanizer   *humanization.Engine
	stateProv   StateProvider
	logger      *slog.Logger

	// Control Layer State
	failures    map[string]*FailureRecord
	failuresMu  sync.RWMutex
	stability   StabilityState
	stabilityMu sync.RWMutex

	activeTimers map[string]*time.Timer
	timersMu     sync.Mutex

	lastEvolve time.Time
	evolveMu   sync.Mutex
}

func NewControlService(
	bus domain.EventBus,
	engine *TaskExecutionEngine,
	tm *TaskManager,
	cm *ControllerManager,
	humanizer *humanization.Engine,
	sp StateProvider,
	logger *slog.Logger,
) *ControlService {
	s := &ControlService{
		bus:          bus,
		engine:       engine,
		taskManager:  tm,
		ctrlManager:  cm,
		humanizer:    humanizer,
		stateProv:    sp,
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
	isStuck := s.stability.IsStuck
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
			// Check if the specific task is currently in a retry backoff window
			backoff := time.Duration(record.Count) * time.Second
			if time.Since(record.LastFailure) < backoff {
				s.failuresMu.RUnlock()
				s.logger.Warn("Plan backoff active: skipping task", slog.String("task_id", task.ID))
				return
			}
		}
		s.failuresMu.RUnlock()

		// Update psychological state tick
		s.evolveMu.Lock()
		if s.lastEvolve.IsZero() {
			s.lastEvolve = time.Now()
		}
		dt := time.Since(s.lastEvolve)
		s.lastEvolve = time.Now()
		s.evolveMu.Unlock()

		gameState := s.stateProv.GetCurrentState().State
		hCtx := humanization.BuildContext(gameState, isStuck)
		s.humanizer.State().Evolve(hCtx, dt)

		// Process the task through the humanizer to add natural delays/noise
		singleTaskPlan := plan
		singleTaskPlan.Tasks = []domain.Action{task}
		scheduled := s.humanizer.Process(singleTaskPlan, hCtx)

		for _, sa := range scheduled {
			if sa.ExecuteAt.IsZero() || sa.ExecuteAt.Before(time.Now()) {
				_ = s.taskManager.Enqueue(ctx, sa.Action)
			} else {
				_ = s.taskManager.EnqueueScheduled(ctx, sa.Action, sa.ExecuteAt)
			}
		}
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

			// Default timeout: 45s base + 15s grace for network/server lag
			timeout := 60 * time.Second
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
		Progress  float64 `json:"progress"`
	}

	if err := json.Unmarshal(event.Payload, &payload); err == nil {
		success := payload.Status == "COMPLETED"

		s.timersMu.Lock()
		timer, isActive := s.activeTimers[payload.CommandID]
		if isActive {
			timer.Stop()
			delete(s.activeTimers, payload.CommandID)
		}
		s.timersMu.Unlock()

		if !isActive {
			s.logger.Debug("Ignoring duplicate or untracked TASK_END event", slog.String("task_id", payload.CommandID))
			return
		}

		if !success && payload.Cause != "preempted_by_priority" && payload.Cause != "plan_invalidated" {
			s.failuresMu.Lock()
			record, exists := s.failures[payload.CommandID]
			if !exists {
				record = &FailureRecord{IntentID: payload.CommandID}
				s.failures[payload.CommandID] = record
			}
			record.Count++
			record.LastFailure = time.Now()

			res := domain.ExecutionResult{
				Success:  false,
				Cause:    payload.Cause,
				Progress: payload.Progress,
				Action:   record.Action,
			}

			// Evaluate if we should retry, degrade, or abort based on failure cause/progress
			directive := s.ctrlManager.EvaluateFailure(res, record.Count)
			actionToRetry := record.Action
			s.failuresMu.Unlock()

			s.ctrlManager.RecordResult(res)

			s.logger.Warn("Task failed, applying adaptation strategy",
				slog.String("task_id", payload.CommandID),
				slog.String("cause", payload.Cause),
				slog.Float64("progress", payload.Progress),
				slog.String("strategy", string(directive.Strategy)),
				slog.Duration("delay", directive.Delay),
			)

			// Signal task engine and manager that the current attempt is finished
			s.engine.OnTaskEnd(payload.CommandID, false)
			_ = s.taskManager.Complete(ctx, payload.CommandID, false)

			if actionToRetry.ID == "" {
				return
			}

			// Execute the adaptation directive
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
				// No further action, task is dropped from execution pipeline
			}

		} else {
			// Clean up failure history for this specific ID on success or controlled stop
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
