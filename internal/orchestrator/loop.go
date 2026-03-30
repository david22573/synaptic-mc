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
)

type Orchestrator struct {
	sessionID string
	store     domain.EventStore
	memory    memory.Store
	flags     config.FeatureFlags

	curriculum voyager.Curriculum
	critic     voyager.Critic
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

	// Safely scoped pointers to avoid race conditions during read/write
	activeIntent atomic.Pointer[domain.ActionIntent]
	beforeState  atomic.Pointer[domain.GameState]

	reflexLock   bool
	evalCancel   context.CancelFunc
	evalInFlight atomic.Bool

	uiHub        *observability.Hub
	stateVersion atomic.Uint64
	eventCount   atomic.Int64
}

func New(
	sessionID string,
	store domain.EventStore,
	memStore memory.Store,
	curriculum voyager.Curriculum,
	critic voyager.Critic,
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
	tm := NewTaskManager(engine, nil, logger)
	humanizer := humanization.NewEngine(humanCfg)

	o := &Orchestrator{
		sessionID:   sessionID,
		store:       store,
		memory:      memStore,
		flags:       flags,
		curriculum:  curriculum,
		critic:      critic,
		ctrlManager: cm,
		engine:      engine,
		taskManager: tm,
		humanizer:   humanizer,
		uiHub:       uiHub,
		logger:      logger.With(slog.String("component", "orchestrator"), slog.String("session_id", sessionID)),
		eventCh:     make(chan domain.DomainEvent, 100),
		taskHistory: make([]domain.TaskHistory, 0),
	}

	o.planTracker = NewPlanTracker(tm, humanizer, o.buildHumanizationContext, logger)
	tm.OnDrain = o.handleQueueDrain

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

	<-gCtx.Done()
	o.logger.Info("Orchestrator shutting down, waiting for loops...")
	return g.Wait()
}

func (o *Orchestrator) IngestState(ctx context.Context, state domain.GameState) {
	vState := domain.VersionedState{
		Version: o.stateVersion.Add(1),
		State:   state,
	}

	o.stateBuffer.Store(&vState)

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

func (o *Orchestrator) handleQueueDrain() {
	if !o.planTracker.HasActivePlan() {
		// Use the orchestrator's base context to prevent detached goroutine evaluation leaks
		if o.baseCtx != nil {
			_ = o.evaluateNextTask(o.baseCtx)
		}
	}
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

			if tm != nil && !o.planTracker.HasActivePlan() && tm.IsIdle() && !isLocked && !o.evalInFlight.Load() {
				go o.handleQueueDrain()
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
			if err := o.store.Append(ctx, o.sessionID, ev.Trace, ev.Type, ev.Payload); err != nil {
				o.logger.Error("Failed to append event to store", slog.Any("error", err))
				continue
			}

			if o.uiHub != nil {
				o.uiHub.Broadcast("event_stream", ev)
			}

			o.handleDomainEvent(ctx, ev)

			if count := o.eventCount.Add(1); count%SnapshotEventInterval == 0 {
				go o.takeSnapshot(context.Background())
			}
		}
	}
}

func (o *Orchestrator) evaluateNextTask(ctx context.Context) error {
	if !o.evalInFlight.CompareAndSwap(false, true) {
		return nil
	}

	o.mu.RLock()
	tm := o.taskManager
	isLocked := o.reflexLock
	state := o.currentSnapshot.State.State
	history := o.taskHistory
	o.mu.RUnlock()

	if tm == nil || isLocked || o.planTracker.HasActivePlan() {
		o.evalInFlight.Store(false)
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
		defer o.evalInFlight.Store(false)
		defer cancel() // Explicitly chains cancellation to prevent leaking after ProposeTask

		intent, err := o.curriculum.ProposeTask(evalCtx, state, history)
		if err != nil {
			if evalCtx.Err() == nil {
				o.logger.Error("Curriculum failed to propose task", slog.Any("error", err))
			}
			return
		}

		if intent == nil {
			return
		}

		intent.ID = fmt.Sprintf("intent-%d", time.Now().UnixNano())

		o.activeIntent.Store(intent)
		o.beforeState.Store(&state)

		trace := domain.TraceContext{
			TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
			ActionID: intent.ID,
		}

		action := domain.Action{
			ID:        intent.ID,
			Trace:     trace,
			Action:    intent.Action,
			Target:    domain.Target{Name: intent.Target, Type: "intent"},
			Count:     intent.Count,
			Rationale: intent.Rationale,
			Priority:  10,
		}

		o.IngestEvent(evalCtx, domain.DomainEvent{
			SessionID: o.sessionID,
			Trace:     trace,
			Type:      domain.EventTypePlanCreated,
			Payload:   o.marshalJSON(intent),
		})

		if o.uiHub != nil {
			o.uiHub.Broadcast("objective_update", intent.Rationale)
		}

		plan := &domain.Plan{
			Objective: intent.Rationale,
			Tasks:     []domain.Action{action},
		}

		o.planTracker.SetPlan(evalCtx, plan)
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
		_ = tm.Halt(ctx, "bot_died")
		o.planTracker.ClearPlan(ctx, "bot_died")
		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()
		observability.Metrics.TaskInterruption.Inc()

	case domain.EventTypePanic:
		// Enhanced recovery logging mechanism captures the TS stack trace correctly
		var payload struct {
			Error string `json:"error"`
			Stack string `json:"stack"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			o.logger.Error("TS Handler Panic", slog.String("error", payload.Error), slog.String("stack", payload.Stack))
		} else {
			o.logger.Error("TS Handler Panic (unknown payload structure)", slog.String("raw", string(ev.Payload)))
		}

		_ = tm.Halt(ctx, "panic_triggered")
		o.planTracker.ClearPlan(ctx, "panic_triggered")
		o.mu.Lock()
		o.reflexLock = true
		o.mu.Unlock()
		observability.Metrics.TaskInterruption.Inc()

	case domain.EventTypePanicResolved:
		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()
		o.handleQueueDrain()

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

			o.mu.Lock()
			after := o.currentSnapshot.State.State

			if intent != nil && beforePtr != nil && intent.ID == payload.CommandID {
				o.activeIntent.Store(nil)
				o.beforeState.Store(nil)

				successCritic, critique := o.critic.Evaluate(*intent, *beforePtr, after)
				if !success {
					successCritic = false
					critique = fmt.Sprintf("TS Execution Failed: %s. %s", payload.Cause, critique)
				}

				o.taskHistory = append(o.taskHistory, domain.TaskHistory{
					Intent:   *intent,
					Success:  successCritic,
					Critique: critique,
				})
			}
			o.mu.Unlock()

			_ = tm.Complete(ctx, payload.CommandID, success)
		}
	}
}

func (o *Orchestrator) marshalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
