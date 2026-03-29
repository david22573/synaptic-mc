package orchestrator

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/observability"
)

type Orchestrator struct {
	sessionID   string
	store       domain.EventStore
	memory      memory.Store
	decision    decision.Engine
	controller  execution.Controller
	taskManager *TaskManager
	logger      *slog.Logger

	stateCh chan domain.GameState
	eventCh chan domain.DomainEvent

	mu               sync.RWMutex
	lastState        domain.GameState
	reflexLock       bool
	lastPlannerError string
	lastPlanHash     string
	systemFeedback   []string
	uiHub            *observability.Hub
	planningInFlight atomic.Bool
}

func New(
	sessionID string,
	store domain.EventStore,
	memStore memory.Store,
	decisionEngine decision.Engine,
	exec execution.Controller,
	uiHub *observability.Hub,
	logger *slog.Logger,
) *Orchestrator {
	tm := NewTaskManager(exec, nil, logger)

	o := &Orchestrator{
		sessionID:   sessionID,
		store:       store,
		memory:      memStore,
		decision:    decisionEngine,
		controller:  exec,
		taskManager: tm,
		uiHub:       uiHub,
		logger:      logger.With(slog.String("component", "orchestrator"), slog.String("session_id", sessionID)),
		stateCh:     make(chan domain.GameState, 10),
		eventCh:     make(chan domain.DomainEvent, 100),
	}

	tm.OnDrain = func() {
		o.mu.RLock()
		state := o.lastState
		o.mu.RUnlock()
		_ = o.evaluateState(context.Background(), state)
	}

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
	newTM := NewTaskManager(ctrl, nil, o.logger)

	newTM.OnDrain = func() {
		o.mu.RLock()
		state := o.lastState
		o.mu.RUnlock()
		_ = o.evaluateState(context.Background(), state)
	}
	o.taskManager = newTM

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

func hashPlan(plan *domain.Plan) string {
	h := sha1.New()
	for _, t := range plan.Tasks {
		h.Write([]byte(t.Action + ":" + t.Target.Name + "|"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (o *Orchestrator) evaluateState(ctx context.Context, state domain.GameState) error {
	o.mu.RLock()
	tm := o.taskManager
	isLocked := o.reflexLock
	lastErr := o.lastPlannerError
	sysFeedback := append([]string(nil), o.systemFeedback...)
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
		return nil
	}

	if lastErr != "" {
		state.Feedback = append(state.Feedback, "PREVIOUS PLAN REJECTED: "+lastErr)
	}
	if len(sysFeedback) > 0 {
		state.Feedback = append(state.Feedback, sysFeedback...)
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

	go func(evalState domain.GameState) {
		defer o.planningInFlight.Store(false)

		plan, err := o.decision.Evaluate(ctx, o.sessionID, evalState, trace)
		if err != nil {
			o.logger.Error("decision evaluation failed", slog.Any("error", err))
			o.mu.Lock()
			o.lastPlannerError = err.Error()
			o.mu.Unlock()
			return
		}

		o.mu.Lock()
		o.lastPlannerError = ""
		o.systemFeedback = nil // Clear feedback once consumed by the LLM
		o.mu.Unlock()

		if plan == nil || len(plan.Tasks) == 0 {
			return
		}

		currentHash := hashPlan(plan)

		o.mu.Lock()
		isLoop := o.lastPlanHash == currentHash
		o.lastPlanHash = currentHash
		o.mu.Unlock()

		if isLoop {
			o.logger.Warn("plan_loop_detected", slog.String("hash", currentHash))
			exploreTask := domain.Action{
				ID:       fmt.Sprintf("cmd-%s-explore-break", trace.ActionID),
				Trace:    trace,
				Action:   "explore",
				Target:   domain.Target{Type: "location", Name: "unknown"},
				Priority: 50,
			}
			plan.Tasks = append([]domain.Action{exploreTask}, plan.Tasks...)
		}

		if o.uiHub != nil {
			o.uiHub.Broadcast("objective_update", plan.Objective)
		}

		o.logger.Info("New plan generated",
			slog.String("objective", plan.Objective),
			slog.Int("tasks", len(plan.Tasks)))

		o.mu.RLock()
		freshTM := o.taskManager
		o.mu.RUnlock()

		if freshTM != nil {
			_ = freshTM.Enqueue(ctx, plan.Tasks...)
		} else {
			o.logger.Warn("Dropping generated plan: TaskManager is offline")
		}
	}(state)

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
			Cause     string `json:"cause"`
			Action    string `json:"action"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			success := payload.Status == "COMPLETED"

			if !success && payload.Cause != "" {
				o.logger.Warn("Task execution failed natively, injecting feedback", slog.String("cause", payload.Cause))

				hint := "Execution failed. Adjust plan and avoid repeating the same sequence."
				switch payload.Cause {
				case "PATH_FAILED":
					hint = "Target unreachable. Try explore first or choose a closer target."
				case "TIMEOUT":
					hint = "Task stalled. Retry once; if recurs, choose a different approach."
				case "NO_BLOCKS":
					hint = "Resource not found nearby. Check POI list or explore first."
				case "NO_ENTITY":
					hint = "Entity not found. Check if any are visible in POIs before hunting."
				case "NO_TOOL":
					hint = "Missing required tool. Craft it before retrying this action."
				}

				o.mu.Lock()
				o.lastPlannerError = fmt.Sprintf("TASK_FAILED: %s | CAUSE: %s | HINT: %s", payload.Action, payload.Cause, hint)
				o.mu.Unlock()
			} else if success {
				parts := strings.SplitN(payload.Action, " ", 2)
				if len(parts) == 2 {
					actionType := parts[0]
					targetName := parts[1]

					if actionType == "mark_location" && o.memory != nil {
						o.mu.RLock()
						pos := o.lastState.Position
						o.mu.RUnlock()

						_ = o.memory.MarkWorldNode(ctx, targetName, "user_marked", pos)
						o.logger.Info("Location marked", slog.String("name", targetName))

						o.mu.Lock()
						o.systemFeedback = append(o.systemFeedback, fmt.Sprintf("SYSTEM: Successfully marked current location as '%s'", targetName))
						o.mu.Unlock()

					} else if actionType == "recall_location" && o.memory != nil {
						o.mu.RLock()
						pos := o.lastState.Position
						o.mu.RUnlock()

						knownWorld, _ := o.memory.GetKnownWorld(ctx, pos)
						o.logger.Info("Location recalled", slog.String("world", knownWorld))

						o.mu.Lock()
						o.systemFeedback = append(o.systemFeedback, "SYSTEM RECALL: "+knownWorld)
						o.mu.Unlock()
					}
				}
			}

			_ = tm.Complete(ctx, payload.CommandID, success)
		}
	}
}
