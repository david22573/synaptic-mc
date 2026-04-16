// internal/execution/service.go
package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/humanization"
	"david22573/synaptic-mc/internal/observability"
)

type StateProvider interface {
	GetCurrentState() domain.VersionedState
}

type ControlService struct {
	rtBus       *domain.RealtimeBus
	evBus       domain.EventBus
	engine      *TaskExecutionEngine
	taskManager *TaskManager
	ctrlManager *ControllerManager
	supervisor  *Supervisor
	arbiter     *ActionArbiter
	humanizer   *humanization.Engine
	stateProv   StateProvider
	logger      *slog.Logger

	// Control Layer State
	stability   StabilityState
	stabilityMu sync.RWMutex

	activeTimers map[string]*time.Timer
	timersMu     sync.Mutex

	lastEvolve time.Time
	evolveMu   sync.Mutex

	prep *PrepOrchestrator
}

func NewControlService(
	rtBus *domain.RealtimeBus,
	evBus domain.EventBus,
	engine *TaskExecutionEngine,
	tm *TaskManager,
	cm *ControllerManager,
	supervisor *Supervisor,
	arbiter *ActionArbiter,
	humanizer *humanization.Engine,
	sp StateProvider,
	logger *slog.Logger,
) *ControlService {
	s := &ControlService{
		rtBus:        rtBus,
		evBus:        evBus,
		engine:       engine,
		taskManager:  tm,
		ctrlManager:  cm,
		supervisor:   supervisor,
		arbiter:      arbiter,
		humanizer:    humanizer,
		stateProv:    sp,
		logger:       logger.With(slog.String("component", "control_service")),
		activeTimers: make(map[string]*time.Timer),
		prep:         NewPrepOrchestrator(engine, arbiter, logger),
	}

	// Plans arrive from the slower, async pipeline
	evBus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(s.handlePlanCreated))
	evBus.Subscribe(domain.EventTypePlanInvalidated, domain.FuncHandler(s.handlePlanInvalidated))
	evBus.Subscribe(domain.EventTypeStateUpdated, domain.FuncHandler(s.handleStateUpdated))

	// Execution events bypass queues via the realtime pipeline
	rtBus.Subscribe(domain.EventTypeTaskStart, domain.FuncHandler(s.handleTaskStart))
	rtBus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))
	rtBus.Subscribe(domain.EventTypePanic, domain.FuncHandler(s.handlePanic))
	rtBus.Subscribe(domain.EventTypePanicResolved, domain.FuncHandler(s.handlePanicResolved))
	rtBus.Subscribe(domain.EventBotDeath, domain.FuncHandler(s.handleBotDeath))
	rtBus.Subscribe(domain.EventBotRespawn, domain.FuncHandler(s.handleBotRespawn))

	return s
}

func (s *ControlService) handleStateUpdated(ctx context.Context, event domain.DomainEvent) {
	state := s.stateProv.GetCurrentState().State

	// Near-Cheating: Proactive Prep
	s.prep.CheckProactiveFarming(ctx, state)
	s.prep.PreEscape(ctx, state)
}

func (s *ControlService) SetReflexActive(active bool) {
	s.stabilityMu.Lock()
	defer s.stabilityMu.Unlock()
	s.stability.ReflexActive = active
}

func (s *ControlService) IsIdle() bool {
	return s.taskManager.IsIdle()
}

func (s *ControlService) handlePlanCreated(ctx context.Context, event domain.DomainEvent) {
	s.stabilityMu.RLock()
	reflexActive := s.stability.ReflexActive
	isStuck := s.stability.IsStuck
	deathCount := s.stability.DeathCount
	lastDeath := s.stability.LastDeath
	s.stabilityMu.RUnlock()

	if reflexActive {
		s.logger.Warn("Reflex active: dropping new plan from decision layer")
		return
	}

	s.engine.mu.RLock()
	execCfg := s.engine.cfg
	s.engine.mu.RUnlock()

	// Death loop protection: If we died recently and many times, ignore plans for a bit
	deathLoopThreshold := time.Duration(execCfg.DeathLoopThresholdMs) * time.Millisecond
	if deathCount >= 3 && time.Since(lastDeath) < deathLoopThreshold {
		s.logger.Warn("Death loop protection active: dropping new plan", slog.Int("death_count", deathCount))
		return
	}

	var plan domain.Plan
	if err := json.Unmarshal(event.Payload, &plan); err != nil {
		s.logger.Error("Failed to unmarshal plan", slog.Any("error", err))
		return
	}

	if len(plan.Tasks) > 0 {
		task := plan.Tasks[0]

		// Phase 4: Single Writer Action Bus
		if !s.arbiter.Request(ctx, task) {
			s.logger.Debug("Arbiter denied task request", slog.String("task_id", task.ID))
			return
		}

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
	observability.Metrics.IncInterrupt()
	_ = s.taskManager.Halt(ctx, "panic_triggered")
	s.clearAllWatchdogs()

	// Run Emergency Policy (Reflexive)
	emergencyAction := domain.Action{
		ID:        fmt.Sprintf("panic-reflex-%d", time.Now().UnixNano()),
		Action:    "emergency_reflex",
		Target:    domain.Target{Name: "survival"},
		Priority:  1000, // Absolute highest priority
		Rationale: "High-priority survival reflex triggered by sensor data",
	}
	
	// Phase 4: Single Writer Action Bus
	s.arbiter.Request(ctx, emergencyAction)
}

func (s *ControlService) handleBotDeath(ctx context.Context, event domain.DomainEvent) {
	observability.Metrics.IncDeath()
	s.stabilityMu.Lock()
	s.stability.ReflexActive = false
	s.stability.DeathCount++
	s.stability.LastDeath = time.Now()
	deathCount := s.stability.DeathCount
	s.stabilityMu.Unlock()

	s.logger.Warn("Bot death detected", slog.Int("death_count", deathCount))

	_ = s.taskManager.Halt(ctx, "bot_died")
	s.clearAllWatchdogs()

	if deathCount >= 3 {
		s.logger.Error("CRITICAL: Bot in death loop, invalidating current plan")
		s.evBus.Publish(ctx, domain.DomainEvent{
			SessionID: event.SessionID,
			Type:      domain.EventTypePlanInvalidated,
			Payload:   []byte(`{"reason": "death_loop"}`),
			CreatedAt: time.Now(),
		})
	}
}

func (s *ControlService) handleBotRespawn(ctx context.Context, event domain.DomainEvent) {
	s.stabilityMu.Lock()
	defer s.stabilityMu.Unlock()

	s.engine.mu.RLock()
	execCfg := s.engine.cfg
	s.engine.mu.RUnlock()

	// If it's been a while since the last death, reset the count
	resetThreshold := time.Duration(execCfg.DeathCountResetMs) * time.Millisecond
	if time.Since(s.stability.LastDeath) > resetThreshold {
		s.stability.DeathCount = 0
	}
	s.logger.Info("Bot respawned")
}

func (s *ControlService) handlePanicResolved(ctx context.Context, event domain.DomainEvent) {
	s.SetReflexActive(false)
}

func (s *ControlService) handleTaskStart(ctx context.Context, event domain.DomainEvent) {
	var payload struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		s.logger.Error("Failed to unmarshal TaskStart payload", slog.Any("error", err), slog.String("event_id", string(event.Type)))
		return
	}

	s.engine.OnTaskStart(payload.CommandID)

	inFlight := s.engine.GetInFlight()
	if inFlight != nil && inFlight.ID == payload.CommandID {
		s.engine.mu.RLock()
		execCfg := s.engine.cfg
		s.engine.mu.RUnlock()

		timeout := time.Duration(execCfg.ExecutionWatchdogTimeoutMs) * time.Millisecond
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

func (s *ControlService) triggerRecovery(sessionID string, taskID string, cause string) {
	_ = s.ctrlManager.AbortCurrent(context.Background(), cause)

	failedPayload, _ := json.Marshal(map[string]interface{}{
		"status":     "FAILED",
		"command_id": taskID,
		"cause":      cause,
		"progress":   0.0,
	})

	ev := domain.DomainEvent{
		SessionID: sessionID,
		Type:      domain.EventTypeTaskEnd,
		Payload:   failedPayload,
		CreatedAt: time.Now(),
	}

	s.rtBus.Publish(context.Background(), ev)
	s.evBus.Publish(context.Background(), ev)
}

func (s *ControlService) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	var payload domain.TaskEndPayload

	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		s.logger.Error("Failed to unmarshal TaskEnd payload", slog.Any("error", err), slog.String("event_id", string(event.Type)))
		return
	}

	success := payload.Status == "COMPLETED"

	s.timersMu.Lock()
	timer, isActive := s.activeTimers[payload.CommandID]
	if isActive {
		timer.Stop()
		delete(s.activeTimers, payload.CommandID)
	}
	s.timersMu.Unlock()

	// ALWAYS signal task engine and manager that the current attempt is finished
	// to prevent deadlocks in the dispatch queue.
	s.engine.OnTaskEnd(payload.CommandID, success)
	_ = s.taskManager.Complete(ctx, payload.CommandID, success, payload.Cause)

	if !isActive {
		s.logger.Debug("Cleaning up untracked or timed-out task", slog.String("task_id", payload.CommandID))
		// We still continue to clean up failure records, but we don't trigger new adaptation
		// if it was already handled by the watchdog or is a duplicate.
	}

	if !success && !domain.IsControlledStop(payload.Cause) {
		if !isActive {
			return // already handled by watchdog or duplicate
		}

		s.supervisor.HandleTaskEnd(payload)

		// Get retry stats from supervisor
		attempts, _ := s.supervisor.state.GetRetryStats(payload.Action)

		res := domain.ExecutionResult{
			Success:  false,
			Cause:    payload.Cause,
			Progress: payload.Progress,
			Action: domain.Action{
				Action: payload.Action,
				Target: domain.Target{Name: payload.Target},
			},
		}

		// Evaluate if we should retry, degrade, or abort based on failure cause/progress
		directive := s.ctrlManager.EvaluateFailure(res, attempts)
		actionToRetry := res.Action
		actionToRetry.ID = payload.CommandID

		s.ctrlManager.RecordResult(res)

		// Phase 6: Update humanizer with performance feedback
		successRate := s.ctrlManager.GetSuccessRate()
		s.humanizer.State().UpdateFeedback(attempts, successRate)

		s.logger.Warn("Task failed, applying adaptation strategy",
			slog.String("task_id", payload.CommandID),
			slog.String("cause", payload.Cause),
			slog.Float64("progress", payload.Progress),
			slog.String("strategy", string(directive.Strategy)),
			slog.Duration("delay", directive.Delay),
		)

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
		if success {
			s.ctrlManager.RecordResult(domain.ExecutionResult{Success: true, Progress: 1.0, Cause: ""})

			// Phase 6: Update humanizer with success feedback
			successRate := s.ctrlManager.GetSuccessRate()
			s.humanizer.State().UpdateFeedback(0, successRate)
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

// IngestControlInput satisfies the observability.ControlOrchestrator interface.
func (s *ControlService) IngestControlInput(ctx context.Context, input domain.ControlInput) {
	// Nuke manual camera movement entirely to prevent queue flooding
	if input.Action == "camera_move" {
		return
	}

	s.logger.Debug("Received control input from UI", slog.String("action", input.Action))

	if s.ctrlManager == nil || !s.ctrlManager.HasActiveController() {
		return
	}

	targetData := map[string]float64{
		"yaw":   input.Yaw,
		"pitch": input.Pitch,
	}

	payloadBytes, err := json.Marshal(targetData)
	if err != nil {
		s.logger.Error("Failed to marshal control input target data", slog.Any("error", err))
		return
	}

	action := domain.Action{
		ID:        fmt.Sprintf("ctrl-%d", time.Now().UnixNano()),
		Source:    "ui_direct_control",
		Action:    input.Action,
		Target:    domain.Target{Type: "direct_input", Name: string(payloadBytes)},
		Priority:  1000, // Absolute highest priority
		Rationale: "Direct user control input",
	}

	// Phase 4: Single Writer Action Bus
	if !s.arbiter.Request(ctx, action) {
		s.logger.Debug("Arbiter denied direct control request", slog.String("action", action.Action))
	}
}
