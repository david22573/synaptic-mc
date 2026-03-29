package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/observability"
	"david22573/synaptic-mc/internal/voyager"
)

type Orchestrator struct {
	sessionID string
	store     domain.EventStore
	memory    memory.Store

	curriculum voyager.Curriculum
	critic     voyager.Critic

	ctrlManager *execution.ControllerManager
	taskManager *TaskManager
	logger      *slog.Logger

	stateCh chan domain.VersionedState
	eventCh chan domain.DomainEvent

	mu              sync.RWMutex
	currentSnapshot domain.EvaluationSnapshot
	taskHistory     []domain.TaskHistory
	activeIntent    *domain.ActionIntent
	beforeState     domain.GameState

	reflexLock bool
	evalCancel context.CancelFunc

	uiHub        *observability.Hub
	stateVersion atomic.Uint64
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
) *Orchestrator {
	cm := execution.NewControllerManager()
	if exec != nil {
		cm.SetController("initial", exec)
	}

	tm := NewTaskManager(cm, nil, logger)

	o := &Orchestrator{
		sessionID:   sessionID,
		store:       store,
		memory:      memStore,
		curriculum:  curriculum,
		critic:      critic,
		ctrlManager: cm,
		taskManager: tm,
		uiHub:       uiHub,
		logger:      logger.With(slog.String("component", "orchestrator"), slog.String("session_id", sessionID)),
		stateCh:     make(chan domain.VersionedState, 10),
		eventCh:     make(chan domain.DomainEvent, 100),
		taskHistory: make([]domain.TaskHistory, 0),
	}

	tm.OnDrain = o.handleQueueDrain

	return o
}

func (o *Orchestrator) Run(ctx context.Context) error {
	o.logger.Info("Starting orchestrator lifecycle")

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return o.processStateLoop(gCtx)
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

	select {
	case <-ctx.Done():
		return
	case o.stateCh <- vState:
		return
	default:
		select {
		case <-o.stateCh:
		default:
		}

		select {
		case o.stateCh <- vState:
		default:
			o.logger.Warn("State channel completely backed up, dropping tick")
		}
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
	_ = o.evaluateNextTask(context.Background())
}

func (o *Orchestrator) processStateLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case vState := <-o.stateCh:
			o.mu.Lock()
			o.currentSnapshot.State = vState
			o.mu.Unlock()

			if o.uiHub != nil {
				o.uiHub.Broadcast("state_update", vState.State)
			}

			if err := o.evaluateNextTask(ctx); err != nil {
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

func marshalJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (o *Orchestrator) evaluateNextTask(ctx context.Context) error {
	o.mu.RLock()
	tm := o.taskManager
	isLocked := o.reflexLock
	state := o.currentSnapshot.State.State
	history := o.taskHistory
	o.mu.RUnlock()

	if tm == nil || isLocked || !tm.IsIdle() {
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
		defer cancel()

		intent, err := o.curriculum.ProposeTask(evalCtx, state, history)
		if err != nil {
			o.logger.Error("Curriculum failed to propose task", slog.Any("error", err))
			return
		}

		if intent == nil {
			return
		}

		if intent.ID == "" {
			intent.ID = fmt.Sprintf("int-%d", time.Now().UnixNano())
		}

		trace := domain.TraceContext{
			TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
			ActionID: intent.ID,
		}

		o.mu.Lock()
		o.activeIntent = intent
		o.beforeState = state
		o.mu.Unlock()

		o.logger.Info("New Intent Proposed", slog.String("action", intent.Action), slog.String("target", intent.Target))

		// Map ActionIntent to domain.Action for the TS bridge TaskManager execution constraints
		action := domain.Action{
			ID:        intent.ID,
			Trace:     trace,
			Action:    intent.Action,
			Target:    domain.Target{Name: intent.Target, Type: "intent"},
			Rationale: intent.Rationale,
			Priority:  10,
		}

		o.IngestEvent(evalCtx, domain.DomainEvent{
			SessionID: o.sessionID,
			Trace:     trace,
			Type:      domain.EventTypePlanCreated,
			Payload:   marshalJSON(intent),
		})

		if o.uiHub != nil {
			o.uiHub.Broadcast("objective_update", intent.Rationale)
		}

		_ = tm.Enqueue(evalCtx, action)
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
		o.logger.Warn("Bot death detected, aborting active intents")
		_ = tm.Halt(ctx, "bot_died")

		o.mu.RLock()
		pos := o.currentSnapshot.State.State.Position
		o.mu.RUnlock()

		if o.memory != nil {
			_ = o.memory.MarkWorldNode(ctx, "death_site", "hazard", pos)
		}

		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()

	case domain.EventTypePanic:
		o.logger.Warn("Bot panicked, halting execution and locking curriculum")
		_ = tm.Halt(ctx, "panic_triggered")

		o.mu.Lock()
		o.reflexLock = true
		o.mu.Unlock()

	case domain.EventTypePanicResolved:
		o.logger.Info("Bot survival reflex resolved, unlocking curriculum")
		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()

	case domain.EventTypeTaskEnd:
		var payload struct {
			Status    string `json:"status"`
			CommandID string `json:"command_id"`
			Cause     string `json:"cause"`
			Action    string `json:"action"`
		}

		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			o.mu.Lock()
			intent := o.activeIntent
			before := o.beforeState
			after := o.currentSnapshot.State.State
			o.activeIntent = nil
			o.mu.Unlock()

			if intent != nil && intent.ID == payload.CommandID {
				success, critique := o.critic.Evaluate(*intent, before, after)

				// Natively verify TS-level failures
				if payload.Status != "COMPLETED" {
					success = false
					critique = fmt.Sprintf("TS Execution Failed: %s. Critic note: %s", payload.Cause, critique)
				}

				o.logger.Info("Critic Evaluation",
					slog.Bool("success", success),
					slog.String("critique", critique),
				)

				o.mu.Lock()
				o.taskHistory = append(o.taskHistory, domain.TaskHistory{
					Intent:   *intent,
					Success:  success,
					Critique: critique,
				})
				o.mu.Unlock()

				if success && o.memory != nil {
					parts := strings.SplitN(payload.Action, " ", 2)
					if len(parts) == 2 {
						actionType := parts[0]
						if actionType == "gather" || actionType == "mine" {
							_ = o.memory.MarkWorldNode(ctx, intent.Target, "resource", after.Position)
						}
					}
				}
			}

			_ = tm.Complete(ctx, payload.CommandID, payload.Status == "COMPLETED")

			// Immediately trigger the curriculum for the next move
			go o.evaluateNextTask(context.Background())
		}
	}
}
