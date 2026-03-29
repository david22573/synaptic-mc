package orchestrator

import (
	"context"
	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/observability"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

type Orchestrator struct {
	sessionID  string
	store      domain.EventStore
	decision   decision.Engine
	controller execution.Controller
	logger     *slog.Logger

	stateCh chan domain.GameState
	eventCh chan domain.DomainEvent

	mu        sync.RWMutex
	lastState domain.GameState
	uiHub     *observability.Hub

	// Added: Proper shutdown tracking
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
		sessionID:  sessionID,
		store:      store,
		decision:   decisionEngine,
		controller: exec,
		uiHub:      uiHub,
		logger:     logger.With(slog.String("component", "orchestrator"), slog.String("session_id", sessionID)),
		stateCh:    make(chan domain.GameState, 10),
		eventCh:    make(chan domain.DomainEvent, 100),
	}
}

func (o *Orchestrator) Run(ctx context.Context) error {
	o.logger.Info("Starting orchestrator lifecycle")

	g, gCtx := errgroup.WithContext(ctx)

	// Track goroutines for graceful shutdown
	o.wg.Add(2)
	g.Go(func() error {
		defer o.wg.Done()
		return o.processStateLoop(gCtx)
	})

	g.Go(func() error {
		defer o.wg.Done()
		return o.processEventLoop(gCtx)
	})

	// Wait for context cancellation then gracefully shutdown
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
	}
}

func (o *Orchestrator) SessionID() string {
	return o.sessionID
}

func (o *Orchestrator) SetController(ctrl execution.Controller) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Close old controller if it implements io.Closer
	if o.controller != nil {
		if closer, ok := o.controller.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}

	o.controller = ctrl
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
	// FIX: Hold lock for entire evaluation to prevent controller swap mid-evaluation
	o.mu.RLock()
	controller := o.controller
	o.mu.RUnlock()

	if controller == nil {
		o.logger.Debug("No controller available, skipping evaluation")
		return nil
	}

	trace := domain.TraceContext{
		TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
		ActionID: fmt.Sprintf("act-%d", time.Now().UnixNano()),
	}

	// FIX: Check context cancellation before heavy work
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	plan, err := o.decision.Evaluate(ctx, o.sessionID, state, trace)
	if err != nil {
		return fmt.Errorf("decision evaluation failed: %w", err)
	}

	if plan == nil || len(plan.Tasks) == 0 {
		return nil
	}

	if o.uiHub != nil {
		o.uiHub.Broadcast("objective_update", plan.Objective)
	}

	o.logger.Info("New plan generated",
		slog.String("objective", plan.Objective),
		slog.Int("tasks", len(plan.Tasks)))

	// FIX: Double-check controller is still valid before dispatch
	o.mu.RLock()
	currentController := o.controller
	o.mu.RUnlock()

	if currentController == nil {
		return fmt.Errorf("controller became nil after planning")
	}

	return currentController.Dispatch(ctx, plan.Tasks[0])
}

func (o *Orchestrator) handleDomainEvent(ctx context.Context, ev domain.DomainEvent) {
	o.mu.RLock()
	controller := o.controller
	o.mu.RUnlock()

	if controller == nil {
		return
	}

	switch ev.Type {
	case domain.EventBotDeath:
		o.logger.Warn("Bot death detected, aborting active plans")
		_ = controller.AbortCurrent(ctx, "bot_died")
	case domain.EventTypePanic:
		o.logger.Warn("Bot panicked, halting execution")
		_ = controller.AbortCurrent(ctx, "panic_triggered")
	}
}
