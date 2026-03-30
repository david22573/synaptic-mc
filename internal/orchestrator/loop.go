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

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/learning"
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

			if count := o.eventCount.Add(1); count%500 == 0 {
				go o.takeSnapshot(context.Background())
			}
		}
	}
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

	// Fix: Now actually uses the parent context passed in the arguments
	evalCtx, cancel := context.WithCancel(ctx)
	o.evalCancel = cancel
	o.mu.Unlock()

	go func() {
		defer cancel()

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

		o.mu.Lock()
		o.activeIntent = intent
		o.beforeState = state
		o.mu.Unlock()

		trace := domain.TraceContext{
			TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
			ActionID: intent.ID,
		}

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
			Payload:   o.marshalJSON(intent),
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
		_ = tm.Halt(ctx, "bot_died")
		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()

	case domain.EventTypePanic:
		_ = tm.Halt(ctx, "panic_triggered")
		o.mu.Lock()
		o.reflexLock = true
		o.mu.Unlock()

	case domain.EventTypePanicResolved:
		o.mu.Lock()
		o.reflexLock = false
		o.mu.Unlock()
		o.handleQueueDrain()

	case domain.EventTypeTaskEnd:
		var payload struct {
			Status    string `json:"status"`
			CommandID string `json:"command_id"`
			Cause     string `json:"cause"`
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
				if payload.Status != "COMPLETED" {
					success = false
					critique = fmt.Sprintf("TS Execution Failed: %s. %s", payload.Cause, critique)
				}

				o.mu.Lock()
				o.taskHistory = append(o.taskHistory, domain.TaskHistory{
					Intent:   *intent,
					Success:  success,
					Critique: critique,
				})
				o.mu.Unlock()
			}

			_ = tm.Complete(ctx, payload.CommandID, payload.Status == "COMPLETED")
		}
	}
}

func (o *Orchestrator) marshalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
