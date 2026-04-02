package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/humanization"
	"david22573/synaptic-mc/internal/learning"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/observability"
	"david22573/synaptic-mc/internal/voyager"
)

const (
	SnapshotEventInterval = 500
	DefaultTickRate       = 50 * time.Millisecond
	PanicLockTimeout      = 15 * time.Second
	RespawnCooldown       = 10 * time.Second
)

type Orchestrator struct {
	sessionID string
	store     domain.EventStore
	memory    memory.Store
	flags     config.FeatureFlags

	curriculum voyager.Curriculum
	critic     voyager.Critic
	planner    *decision.AdvancedPlanner
	humanizer  *humanization.Engine

	ctrlManager *execution.ControllerManager
	engine      *execution.TaskExecutionEngine
	taskManager *TaskManager
	planTracker *PlanTracker
	logger      *slog.Logger

	baseCtx context.Context
	cancel  context.CancelFunc

	stateBuffer atomic.Pointer[domain.VersionedState]
	ticker      *time.Ticker
	eventCh     chan domain.DomainEvent

	mu              sync.RWMutex
	currentSnapshot domain.EvaluationSnapshot
	taskHistory     []domain.TaskHistory

	activeIntent atomic.Pointer[domain.ActionIntent]
	beforeState  atomic.Pointer[domain.GameState]

	reflexLock    bool
	reflexTimer   *time.Timer
	evalCancel    context.CancelFunc
	evalSemaphore chan struct{}

	drainSignal chan struct{}

	uiHub        *observability.Hub
	stateVersion atomic.Uint64
	eventCount   atomic.Int64

	botDead       atomic.Bool
	lastDeathTime atomic.Int64
}

func New(
	sessionID string,
	store domain.EventStore,
	memStore memory.Store,
	curriculum voyager.Curriculum,
	critic voyager.Critic,
	planner *decision.AdvancedPlanner,
	exec execution.Controller,
	uiHub *observability.Hub,
	logger *slog.Logger,
	flags config.FeatureFlags,
	humanCfg humanization.Config,
) *Orchestrator {
	cm := execution.NewControllerManager()
	if exec != nil {
		cm.SetController("initial", exec)
	}

	engine := execution.NewTaskExecutionEngine(cm, logger)
	tm := NewTaskManager(engine, cm, nil, logger)
	humanizer := humanization.NewEngine(humanCfg)

	o := &Orchestrator{
		sessionID:     sessionID,
		store:         store,
		memory:        memStore,
		flags:         flags,
		curriculum:    curriculum,
		critic:        critic,
		planner:       planner,
		ctrlManager:   cm,
		engine:        engine,
		taskManager:   tm,
		humanizer:     humanizer,
		uiHub:         uiHub,
		logger:        logger.With(slog.String("component", "orchestrator"), slog.String("session_id", sessionID)),
		eventCh:       make(chan domain.DomainEvent, 100),
		taskHistory:   make([]domain.TaskHistory, 0),
		evalSemaphore: make(chan struct{}, 1),
		drainSignal:   make(chan struct{}, 1),
	}

	o.planTracker = NewPlanTracker(tm, humanizer, o.buildHumanizationContext, logger)

	// FIX: Don't spawn a goroutine directly, signal the worker instead
	tm.OnDrain = func() {
		select {
		case o.drainSignal <- struct{}{}:
		default:
		}
	}

	o.taskManager.SetReadyChecker(func() bool {
		if o.botDead.Load() {
			return false
		}

		o.mu.RLock()
		state := o.currentSnapshot.State.State
		o.mu.RUnlock()

		if state.Position.X == 0 && state.Position.Z == 0 && state.Health <= 0 {
			return false
		}
		return true
	})

	return o
}

func (o *Orchestrator) Run(ctx context.Context) error {
	o.logger.Info("Starting orchestrator lifecycle")

	o.mu.Lock()
	o.baseCtx, o.cancel = context.WithCancel(ctx)
	o.mu.Unlock()

	o.ticker = time.NewTicker(DefaultTickRate)
	defer o.ticker.Stop()

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		o.taskManager.Run(gCtx)
		return nil
	})

	g.Go(func() error {
		return o.processStateLoop(gCtx, DefaultTickRate)
	})

	g.Go(func() error {
		return o.processEventLoop(gCtx)
	})

	// Start planner slow replan loop in background
	g.Go(func() error {
		o.logger.Info("Starting planner slow replan loop")
		o.planner.SlowReplanLoop(gCtx, o.sessionID)
		return nil
	})

	// Single worker to handle evaluation drains
	g.Go(func() error {
		for {
			select {
			case <-gCtx.Done():
				return nil
			case <-o.drainSignal:
				if !o.planTracker.HasActivePlan() {
					o.mu.RLock()
					ctx := o.baseCtx
					o.mu.RUnlock()
					if ctx != nil {
						_ = o.evaluateNextTask(ctx)
					}
				}
			}
		}
	})

	<-gCtx.Done()
	return g.Wait()
}

func (o *Orchestrator) IngestState(ctx context.Context, state domain.GameState) {
	vState := domain.VersionedState{
		Version: o.stateVersion.Add(1),
		State:   state,
	}

	o.stateBuffer.Store(&vState)

	// Trigger planner replan on new state
	o.planner.TriggerReplan(state)

	if o.uiHub != nil {
		go o.uiHub.Broadcast("state_update", state)
	}
}

func (o *Orchestrator) IngestEvent(ctx context.Context, event domain.DomainEvent) {
	select {
	case <-ctx.Done():
	case o.eventCh <- event:
	default:
		o.logger.Warn("Event channel full, dropping event", slog.String("type", string(event.Type)))
	}
}

func (o *Orchestrator) SessionID() string {
	return o.sessionID
}

func (o *Orchestrator) SetController(id string, ctrl execution.Controller) {
	o.mu.Lock()
	if o.taskManager != nil {
		_ = o.taskManager.Halt(context.Background(), "controller_swapped")
	}
	o.mu.Unlock()

	o.ctrlManager.SetController(id, ctrl)
	o.logger.Info("Execution controller updated", slog.String("controller_id", id))
}

func (o *Orchestrator) takeSnapshot(ctx context.Context) {
	stats, lastID, err := learning.GetProjectedStats(ctx, o.store, o.sessionID)
	if err != nil || lastID == 0 {
		return
	}

	data, err := json.Marshal(stats)
	if err != nil {
		o.logger.Error("Failed to marshal snapshot data", slog.Any("error", err))
		return
	}

	if s, ok := o.store.(interface {
		SaveSnapshot(context.Context, domain.SessionSnapshot) error
	}); ok {
		err = s.SaveSnapshot(ctx, domain.SessionSnapshot{
			SessionID:   o.sessionID,
			LastEventID: lastID,
			Data:        data,
		})

		if err != nil {
			o.logger.Error("Failed to persist background snapshot", slog.Any("error", err))
		} else {
			o.logger.Info("CQRS read-model snapshot saved", slog.Int64("last_event_id", lastID))
		}
	}
}

func (o *Orchestrator) processStateLoop(ctx context.Context, targetTick time.Duration) error {
	lastTick := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-o.ticker.C:
			actualTick := t.Sub(lastTick)
			jitter := float64(actualTick.Milliseconds() - targetTick.Milliseconds())
			if jitter < 0 {
				jitter = -jitter
			}
			observability.Metrics.DecisionJitter.Observe(jitter)

			hctx := o.buildHumanizationContext()
			o.humanizer.State().Evolve(hctx, t.Sub(lastTick))
			lastTick = t

			vState := o.stateBuffer.Load()
			if vState == nil {
				continue
			}

			o.mu.Lock()
			o.currentSnapshot.State = *vState
			o.mu.Unlock()

			o.mu.RLock()
			tm := o.taskManager
			isLocked := o.reflexLock
			o.mu.RUnlock()

			isEvaluating := len(o.evalSemaphore) > 0

			if tm != nil && !o.planTracker.HasActivePlan() && tm.IsIdle() && !isLocked && !isEvaluating {
				select {
				case o.drainSignal <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (o *Orchestrator) processEventLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-o.eventCh:
			// 1. Persist first to ensure database consistency
			if err := o.store.Append(ctx, o.sessionID, ev.Trace, ev.Type, ev.Payload); err != nil {
				o.logger.Error("Failed to append event to store", slog.Any("error", err))
				continue
			}

			// 2. Broadcast with a UI-friendly map
			if o.uiHub != nil {
				var payloadObj interface{}
				_ = json.Unmarshal(ev.Payload, &payloadObj)

				broadcastEv := map[string]interface{}{
					"id":         ev.ID,
					"session_id": ev.SessionID,
					"type":       ev.Type,
					"trace":      ev.Trace,
					"payload":    payloadObj,
					"created_at": ev.CreatedAt,
				}
				o.uiHub.Broadcast("event_stream", broadcastEv)
			}

			o.handleDomainEvent(ctx, ev)

			if count := o.eventCount.Add(1); count%SnapshotEventInterval == 0 {
				o.mu.RLock()
				baseCtx := o.baseCtx
				o.mu.RUnlock()
				if baseCtx != nil {
					go o.takeSnapshot(baseCtx)
				}
			}
		}
	}
}

func (o *Orchestrator) evaluateNextTask(ctx context.Context) error {
	select {
	case o.evalSemaphore <- struct{}{}:
	default:
		return nil
	}

	release := func() {
		select {
		case <-o.evalSemaphore:
		default:
		}
	}

	o.mu.RLock()
	tm := o.taskManager
	isLocked := o.reflexLock
	state := o.currentSnapshot.State.State
	o.mu.RUnlock()

	if o.botDead.Load() || state.Health <= 0 || tm == nil || isLocked || o.planTracker.HasActivePlan() {
		release()
		return nil
	}

	if !o.ctrlManager.HasActiveController() {
		release()
		return nil
	}

	o.mu.Lock()
	if o.evalCancel != nil {
		o.evalCancel()
	}

	evalCtx, cancel := context.WithCancel(ctx)
	o.evalCancel = cancel
	o.mu.Unlock()

	go func() {
		defer release()
		defer cancel()

		o.logger.Info("Evaluating next objective")

		// Get fast plan from planner
		plan := o.planner.FastPlan(state)

		// Fallback to curriculum if planner only has the reactive fallback
		if plan.Objective == "Reactive Fallback Plan" && o.curriculum != nil {
			o.logger.Info("Planner cache empty, falling back to curriculum")

			// FIX: Extract history safely to prevent data race
			o.mu.RLock()
			historyCopy := make([]domain.TaskHistory, len(o.taskHistory))
			copy(historyCopy, o.taskHistory)
			o.mu.RUnlock()

			// FIX: Release semaphore so curriculum isn't holding it hostage during LLM calls
			release()
			intent, err := o.curriculum.ProposeTask(evalCtx, state, historyCopy, o.sessionID)

			// Reacquire immediately after
			select {
			case o.evalSemaphore <- struct{}{}:
			case <-evalCtx.Done():
				return
			}

			if err != nil {
				o.logger.Error("Curriculum fallback failed", slog.Any("error", err))
				return
			}

			if intent != nil {
				plan = domain.Plan{
					Objective: "Curriculum Fallback",
					Tasks: []domain.Action{
						{
							ID:        intent.ID,
							Action:    intent.Action,
							Target:    domain.Target{Name: intent.Target, Type: "inferred"},
							Count:     intent.Count,
							Rationale: intent.Rationale,
							Priority:  50,
						},
					},
				}
			}
		}

		if len(plan.Tasks) == 0 {
			o.logger.Debug("Final plan is empty")
			return
		}

		// Create trace for plan execution
		trace := domain.TraceContext{
			TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
			ActionID: plan.Tasks[0].ID,
		}

		// Emit plan creation event
		o.IngestEvent(evalCtx, domain.DomainEvent{
			SessionID: o.sessionID,
			Trace:     trace,
			Type:      domain.EventTypePlanCreated,
			Payload:   o.marshalJSON(plan),
			CreatedAt: time.Now(),
		})

		// Set plan for execution
		o.planTracker.SetPlan(evalCtx, &plan)

		// Store first task as active intent for critic evaluation
		if len(plan.Tasks) > 0 {
			firstTask := plan.Tasks[0]
			intent := &domain.ActionIntent{
				ID:        firstTask.ID,
				Action:    firstTask.Action,
				Target:    firstTask.Target.Name,
				Count:     firstTask.Count,
				Rationale: firstTask.Rationale,
			}
			o.activeIntent.Store(intent)
			o.beforeState.Store(&state)
		}
	}()

	return nil
}

func (o *Orchestrator) buildHumanizationContext() humanization.Context {
	o.mu.RLock()
	state := o.currentSnapshot.State.State
	o.mu.RUnlock()

	isStuck := false
	if state.CurrentTask != nil {
		isStuck = state.TaskProgress < 0.01
	}

	return humanization.BuildContext(state, isStuck)
}

func (o *Orchestrator) handleDomainEvent(ctx context.Context, ev domain.DomainEvent) {
	o.mu.RLock()
	tm := o.taskManager
	o.mu.RUnlock()

	if tm == nil {
		return
	}

	switch ev.Type {
	case domain.EventBotDeath:
		o.botDead.Store(true)
		o.lastDeathTime.Store(time.Now().UnixNano())
		_ = tm.Halt(ctx, "bot_died")
		o.planTracker.ClearPlan(ctx, "bot_died")
		o.mu.Lock()
		o.reflexLock = false
		if o.reflexTimer != nil {
			o.reflexTimer.Stop()
		}
		o.reflexTimer = time.AfterFunc(RespawnCooldown, func() {
			o.mu.Lock()
			o.reflexLock = false
			o.mu.Unlock()
			select {
			case o.drainSignal <- struct{}{}:
			default:
			}
		})
		o.mu.Unlock()

	case domain.EventBotRespawn:
		o.logger.Info("Bot respawn detected, resetting death state")
		o.botDead.Store(false)
		o.lastDeathTime.Store(0)

		// Reset reflex lock to allow normal operation
		o.mu.Lock()
		o.reflexLock = false
		if o.reflexTimer != nil {
			o.reflexTimer.Stop()
			o.reflexTimer = nil
		}
		o.mu.Unlock()

		select {
		case o.drainSignal <- struct{}{}:
		default:
		}

	case domain.EventTypePanic:
		o.mu.Lock()
		if !o.reflexLock {
			_ = tm.Halt(ctx, "panic_triggered")
			o.planTracker.ClearPlan(ctx, "panic_triggered")
		}
		o.reflexLock = true
		if o.reflexTimer != nil {
			o.reflexTimer.Stop()
		}
		o.reflexTimer = time.AfterFunc(PanicLockTimeout, func() {
			o.mu.Lock()
			o.reflexLock = false
			o.mu.Unlock()
			select {
			case o.drainSignal <- struct{}{}:
			default:
			}
		})
		o.mu.Unlock()

	case domain.EventTypePanicResolved:
		o.mu.Lock()
		o.reflexLock = false
		if o.reflexTimer != nil {
			o.reflexTimer.Stop()
		}
		o.mu.Unlock()
		select {
		case o.drainSignal <- struct{}{}:
		default:
		}

	case domain.EventTypeTaskStart:
		var payload struct {
			CommandID string `json:"command_id"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			o.engine.OnTaskStart(payload.CommandID)
		}

	case domain.EventTypeTaskEnd:
		var payload struct {
			Status    string `json:"status"`
			CommandID string `json:"command_id"`
			Cause     string `json:"cause"`
		}

		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			success := payload.Status == "COMPLETED"

			o.engine.OnTaskEnd(payload.CommandID, success)
			o.planTracker.OnTaskComplete(ctx, payload.CommandID, success)

			intent := o.activeIntent.Load()
			beforePtr := o.beforeState.Load()

			if intent != nil && beforePtr != nil && intent.ID == payload.CommandID {
				o.mu.Lock()
				after := o.currentSnapshot.State.State
				successCritic, critique := o.critic.Evaluate(*intent, *beforePtr, after)
				if !success {
					successCritic = false
					critique = fmt.Sprintf("TS Failed: %s. %s", payload.Cause, critique)
				}
				o.taskHistory = append(o.taskHistory, domain.TaskHistory{
					Intent: *intent, Success: successCritic, Critique: critique,
				})
				o.activeIntent.Store(nil)
				o.beforeState.Store(nil)
				o.mu.Unlock()
			}

			_ = tm.Complete(ctx, payload.CommandID, success)
		}
	}
}

func (o *Orchestrator) marshalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
