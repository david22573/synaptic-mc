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

// Orchestrator manages the lifecycle, concurrency, and routing.
// It is the ONLY component allowed to spawn goroutines for the core loop.
type Orchestrator struct {
	sessionID  string
	store      domain.EventStore
	decision   decision.Engine
	controller execution.Controller
	logger     *slog.Logger

	// Channels for managing incoming telemetry and execution events
	stateCh chan domain.GameState
	eventCh chan domain.DomainEvent

	mu        sync.RWMutex
	lastState domain.GameState
	uiHub     *observability.Hub
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

// Run starts the core loop with guaranteed lifecycle teardown via errgroup.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.logger.Info("Starting orchestrator lifecycle")

	g, gCtx := errgroup.WithContext(ctx)

	// Sub-routine: State Processing Loop
	g.Go(func() error {
		return o.processStateLoop(gCtx)
	})

	// Sub-routine: Event Processing Loop
	g.Go(func() error {
		return o.processEventLoop(gCtx)
	})

	return g.Wait()
}

// IngestState is called by the network layer when the TS bot pushes a tick.
func (o *Orchestrator) IngestState(ctx context.Context, state domain.GameState) {
	select {
	case <-ctx.Done():
	case o.stateCh <- state:
	default:
		o.logger.Warn("State channel full, dropping tick")
	}
}

// IngestEvent is called by the network layer for explicit bot actions (deaths, task completion).
func (o *Orchestrator) IngestEvent(ctx context.Context, event domain.DomainEvent) {
	select {
	case <-ctx.Done():
	case o.eventCh <- event:
	}
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
			// Check routines / reflexes immediately (Phase 1: Fast Path)
			// Trigger replan if necessary (Phase 1: Slow Path)
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
			// 1. Commit to ground truth EventStore
			if err := o.store.Append(ctx, o.sessionID, ev.Trace, ev.Type, ev.Payload); err != nil {
				o.logger.Error("Failed to append event to store", slog.Any("error", err))
				continue
			}

			if o.uiHub != nil {
				o.uiHub.Broadcast("event_stream", ev)
			}

			// 2. React to specific domain events
			o.handleDomainEvent(ctx, ev)
		}
	}
}

func (o *Orchestrator) evaluateState(ctx context.Context, state domain.GameState) error {
	// E.g., if we are actively executing, skip replanning unless interrupted.
	// For this refactor, we mock the trace generation.
	trace := domain.TraceContext{
		TraceID: fmt.Sprintf("tr-%d", time.Now().UnixNano()),
	}

	// This is the clean handoff to the pure decision pipeline
	plan, err := o.decision.Evaluate(ctx, o.sessionID, state, trace)
	if err != nil {
		return err
	}

	if plan == nil || len(plan.Tasks) == 0 {
		return nil // No action required
	}

	if o.uiHub != nil {
		o.uiHub.Broadcast("objective_update", plan.Objective)
	}

	o.logger.Info("New plan generated", slog.String("objective", plan.Objective), slog.Int("tasks", len(plan.Tasks)))

	// Dispatch the first task. Queueing logic will be moved to a dedicated ExecutionManager in Phase 2.
	return o.controller.Dispatch(ctx, plan.Tasks[0])
}

func (o *Orchestrator) handleDomainEvent(ctx context.Context, ev domain.DomainEvent) {
	switch ev.Type {
	case domain.EventBotDeath:
		o.logger.Warn("Bot death detected, aborting active plans")
		_ = o.controller.AbortCurrent(ctx, "bot_died")
	case domain.EventTypePanic:
		o.logger.Warn("Bot panicked, halting execution")
		_ = o.controller.AbortCurrent(ctx, "panic_triggered")
	}
}
