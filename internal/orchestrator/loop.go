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

	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/observability"
)

type Orchestrator struct {
	sessionID   string
	store       domain.EventStore
	decision    decision.Engine
	controller  execution.Controller
	taskManager *TaskManager
	logger      *slog.Logger

	stateCh chan domain.GameState
	eventCh chan domain.DomainEvent

	mu               sync.RWMutex
	lastState        domain.GameState
	reflexLock       bool
	uiHub            *observability.Hub
	planningInFlight atomic.Bool

	wg sync.WaitGroup
}

func New(
	sessionID string,
	store domain.EventStore,
	decisionEngine decision.Engine,
	exec execution.Controller,
	uiHub *observability.Hub,
	logger *slog.Logger,
) *Orchestrator {
	return &Orchestrator{
		sessionID:   sessionID,
		store:       store,
		decision:    decisionEngine,
		controller:  exec,
		taskManager: NewTaskManager(exec, logger),
		uiHub:       uiHub,
		logger:      logger.With(slog.String("component", "orchestrator"), slog.String("session_id", sessionID)),
		stateCh:     make(chan domain.GameState, 10),
		eventCh:     make(chan domain.DomainEvent, 100),
	}
}

func (o *Orchestrator) Run(ctx context.Context) error {
	o.logger.Info("Starting orchestrator lifecycle")

	g, gCtx := errgroup.WithContext(ctx)

	o.wg.Add(2)
	g.Go(func() error {
		defer o.wg.Done()
		return o.processStateLoop(gCtx)
	})

	g.Go(func() error {
		defer o.wg.Done()
		return o.processEventLoop(gCtx)
	})

	<-gCtx.Done()
	o.logger.Info("Orchestrator shutting down, waiting for loops...")
	o.wg.Wait()
	return g.Wait()
}

func (o *Orchestrator) IngestState(ctx context.Context, state domain.GameState) {
	select {
	case <-ctx.Done():
	case o.stateCh <- state:
	default:
		o.logger.Warn("State channel full, dropping tick")
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

func (o *Orchestrator) SetController(ctrl execution.Controller) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.controller != nil {
		if closer, ok := o.controller.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}

	o.controller = ctrl
	o.taskManager = NewTaskManager(ctrl, o.logger)
	o.logger.Info("Execution controller updated")
}

func (o *Orchestrator) processStateLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case state := <-o.stateCh:
			o.mu.Lock()
			o.lastState = state
			o.mu.Unlock()

			if o.uiHub != nil {
				o.uiHub.Broadcast("state_update", state)
			}

			if err := o.evaluateState(ctx, state); err != nil {
				o.logger.Error("Failed to evaluate state", slog.Any("error", err))
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
		}
	}
}

func (o *Orchestrator) evaluateState(ctx context.Context, state domain.GameState) error {
	o.mu.RLock()
	tm := o.taskManager
	isLocked := o.reflexLock
	o.mu.RUnlock()

	if tm == nil {
		return nil
	}

	if isLocked {
		return nil
	}

	if !tm.IsIdle() {
		return nil
	}

	if !o.planningInFlight.CompareAndSwap(false, true) {
		return nil // Already planning
	}

	trace := domain.TraceContext{
		TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
		ActionID: fmt.Sprintf("act-%d", time.Now().UnixNano()),
	}

	select {
	case <-ctx.Done():
		o.planningInFlight.Store(false)
		return ctx.Err()
	default:
	}

	go func() {
		defer o.planningInFlight.Store(false)
		plan, err := o.decision.Evaluate(ctx, o.sessionID, state, trace)
		if err != nil {
			o.logger.Error("decision evaluation failed", slog.Any("error", err))
			return
		}

		if plan == nil || len(plan.Tasks) == 0 {
			return
		}

		if o.uiHub != nil {
			o.uiHub.Broadcast("objective_update", plan.Objective)
		}

		o.logger.Info("New plan generated",
			slog.String("objective", plan.Objective),
			slog.Int("tasks", len(plan.Tasks)))

		_ = tm.Enqueue(ctx, plan.Tasks...)
	}()

	return nil
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
		o.logger.Warn("Bot death detected, aborting active plans")
		_ = tm.Halt(ctx, "bot_died")

		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()

	case domain.EventTypePanic:
		o.logger.Warn("Bot panicked, halting execution and locking planner")
		_ = tm.Halt(ctx, "panic_triggered")

		o.mu.Lock()
		o.reflexLock = true
		o.mu.Unlock()

	case domain.EventTypePanicResolved:
		o.logger.Info("Bot survival reflex resolved, unlocking planner")
		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()

	case domain.EventTypeTaskEnd:
		var payload struct {
			Status    string `json:"status"`
			CommandID string `json:"command_id"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			success := payload.Status == "COMPLETED"
			_ = tm.Complete(ctx, payload.CommandID, success)
		}
	}
}
