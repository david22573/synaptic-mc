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
	ctrlManager *execution.ControllerManager
	taskManager *TaskManager
	logger      *slog.Logger

	stateCh chan domain.VersionedState
	eventCh chan domain.DomainEvent

	mu              sync.RWMutex
	currentSnapshot domain.EvaluationSnapshot
	pendingFeedback []domain.Feedback
	reflexLock      bool
	evalCancel      context.CancelFunc
	evalRunID       uint64

	uiHub        *observability.Hub
	stateVersion atomic.Uint64
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
	cm := execution.NewControllerManager()
	if exec != nil {
		cm.SetController("initial", exec)
	}

	tm := NewTaskManager(cm, nil, logger)

	o := &Orchestrator{
		sessionID:   sessionID,
		store:       store,
		memory:      memStore,
		decision:    decisionEngine,
		ctrlManager: cm,
		taskManager: tm,
		uiHub:       uiHub,
		logger:      logger.With(slog.String("component", "orchestrator"), slog.String("session_id", sessionID)),
		stateCh:     make(chan domain.VersionedState, 10),
		eventCh:     make(chan domain.DomainEvent, 100),
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
	o.mu.RLock()
	snap := o.currentSnapshot
	o.mu.RUnlock()

	if snap.ActivePlan != nil {
		o.mu.Lock()
		if o.currentSnapshot.ActivePlan.Status == domain.PlanStatusActive || o.currentSnapshot.ActivePlan.Status == domain.PlanStatusPending {
			o.currentSnapshot.ActivePlan.Status = domain.PlanStatusCompleted

			event := domain.DomainEvent{
				SessionID: o.sessionID,
				Trace:     domain.TraceContext{TraceID: "sys-drain", ActionID: o.currentSnapshot.ActivePlan.ID},
				Type:      domain.EventTypePlanCompleted,
				Payload:   marshalJSON(map[string]string{"plan_id": o.currentSnapshot.ActivePlan.ID}),
			}
			o.mu.Unlock()
			o.IngestEvent(context.Background(), event)
		} else {
			o.mu.Unlock()
		}
	}

	_ = o.evaluate(context.Background(), snap)
}

func (o *Orchestrator) processStateLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case vState := <-o.stateCh:
			o.mu.Lock()

			if o.evalCancel != nil {
				o.evalCancel()
				o.evalCancel = nil
			}

			if len(o.pendingFeedback) > 0 {
				vState.State.Feedback = append(vState.State.Feedback, o.pendingFeedback...)
				o.pendingFeedback = nil
			}

			o.currentSnapshot.State = vState
			snap := o.currentSnapshot

			o.mu.Unlock()

			if o.uiHub != nil {
				o.uiHub.Broadcast("state_update", vState.State)
			}

			if err := o.evaluate(ctx, snap); err != nil {
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

func generatePlanID(plan *domain.Plan) string {
	h := sha1.New()
	for _, t := range plan.Tasks {
		h.Write([]byte(t.Action + ":" + t.Target.Name + "|"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func marshalJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (o *Orchestrator) evaluate(ctx context.Context, snap domain.EvaluationSnapshot) error {
	o.mu.RLock()
	tm := o.taskManager
	isLocked := o.reflexLock
	o.mu.RUnlock()

	if tm == nil || isLocked || !tm.IsIdle() {
		return nil
	}

	evalCtx, cancel := context.WithCancel(ctx)

	o.mu.Lock()
	if o.evalCancel != nil {
		o.evalCancel()
	}
	o.evalCancel = cancel
	o.evalRunID++
	runID := o.evalRunID
	o.mu.Unlock()

	trace := domain.TraceContext{
		TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
		ActionID: fmt.Sprintf("act-%d", time.Now().UnixNano()),
	}

	go func(evalCtx context.Context, snapshot domain.EvaluationSnapshot, evalID uint64) {
		defer func() {
			o.mu.Lock()
			if o.evalRunID == evalID {
				o.evalCancel = nil
			}
			o.mu.Unlock()
			cancel()
		}()

		plan, err := o.decision.Evaluate(evalCtx, o.sessionID, snapshot.State.State, trace)
		if err != nil {
			if evalCtx.Err() != nil {
				return
			}
			o.logger.Error("decision evaluation failed", slog.Any("error", err))
			o.mu.Lock()
			o.pendingFeedback = append(o.pendingFeedback, domain.Feedback{
				Type:  "PLAN_REJECTED",
				Cause: err.Error(),
			})
			o.mu.Unlock()
			return
		}

		if plan == nil || len(plan.Tasks) == 0 {
			return
		}

		plan.StateVersion = snapshot.State.Version
		plan.CreatedAt = time.Now()
		plan.Status = domain.PlanStatusPending

		if plan.ID == "" {
			plan.ID = generatePlanID(plan)
		}

		o.mu.Lock()
		isLoop := false
		if o.currentSnapshot.ActivePlan != nil {
			isLoop = o.currentSnapshot.ActivePlan.ID == plan.ID
			if o.currentSnapshot.ActivePlan.ID != plan.ID {
				if o.currentSnapshot.ActivePlan.Status == domain.PlanStatusPending || o.currentSnapshot.ActivePlan.Status == domain.PlanStatusActive {
					now := time.Now()
					o.currentSnapshot.ActivePlan.Status = domain.PlanStatusInvalidated
					o.currentSnapshot.ActivePlan.InvalidatedAt = &now
					plan.ParentPlanID = o.currentSnapshot.ActivePlan.ID

					o.IngestEvent(ctx, domain.DomainEvent{
						SessionID: o.sessionID,
						Trace:     trace,
						Type:      domain.EventTypePlanInvalidated,
						Payload:   marshalJSON(map[string]string{"plan_id": o.currentSnapshot.ActivePlan.ID, "superseded_by": plan.ID}),
					})
				}
			}
		}

		o.currentSnapshot.ActivePlan = plan
		o.mu.Unlock()

		o.IngestEvent(ctx, domain.DomainEvent{
			SessionID: o.sessionID,
			Trace:     trace,
			Type:      domain.EventTypePlanCreated,
			Payload:   marshalJSON(plan),
		})

		if isLoop {
			o.logger.Warn("plan_loop_detected", slog.String("id", plan.ID))
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
			slog.String("id", plan.ID),
			slog.Uint64("state_version", plan.StateVersion),
			slog.String("objective", plan.Objective),
			slog.Int("tasks", len(plan.Tasks)))

		o.mu.RLock()
		freshTM := o.taskManager
		o.mu.RUnlock()

		if freshTM != nil {
			o.mu.Lock()
			if o.currentSnapshot.ActivePlan != nil && o.currentSnapshot.ActivePlan.ID == plan.ID {
				o.currentSnapshot.ActivePlan.Status = domain.PlanStatusActive
			}
			o.mu.Unlock()
			_ = freshTM.Enqueue(ctx, plan.Tasks...)
		} else {
			o.logger.Warn("Dropping generated plan: TaskManager is offline")
		}
	}(evalCtx, snap, runID)

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
				if o.currentSnapshot.ActivePlan != nil && o.currentSnapshot.ActivePlan.Status != domain.PlanStatusFailed {
					o.currentSnapshot.ActivePlan.Status = domain.PlanStatusFailed

					event := domain.DomainEvent{
						SessionID: o.sessionID,
						Trace:     ev.Trace,
						Type:      domain.EventTypePlanFailed,
						Payload:   marshalJSON(map[string]string{"plan_id": o.currentSnapshot.ActivePlan.ID, "cause": payload.Cause}),
					}

					o.mu.Unlock()
					o.IngestEvent(ctx, event)
					o.mu.Lock()
				}

				o.pendingFeedback = append(o.pendingFeedback, domain.Feedback{
					Type:   "TASK_FAILED",
					Action: payload.Action,
					Cause:  payload.Cause,
					Hint:   hint,
				})
				o.mu.Unlock()

			} else if success {
				parts := strings.SplitN(payload.Action, " ", 2)
				if len(parts) == 2 {
					actionType := parts[0]
					targetName := parts[1]

					if (actionType == "gather" || actionType == "mine") && o.memory != nil {
						o.mu.RLock()
						pos := o.currentSnapshot.State.State.Position
						o.mu.RUnlock()
						_ = o.memory.MarkWorldNode(ctx, targetName, "resource", pos)
					}

					if actionType == "mark_location" && o.memory != nil {
						o.mu.RLock()
						pos := o.currentSnapshot.State.State.Position
						o.mu.RUnlock()

						_ = o.memory.MarkWorldNode(ctx, targetName, "user_marked", pos)
						o.logger.Info("Location marked", slog.String("name", targetName))

						o.mu.Lock()
						o.pendingFeedback = append(o.pendingFeedback, domain.Feedback{
							Type:  "SYSTEM",
							Cause: fmt.Sprintf("Successfully marked location as '%s'", targetName),
						})
						o.mu.Unlock()

					} else if actionType == "recall_location" && o.memory != nil {
						o.mu.RLock()
						pos := o.currentSnapshot.State.State.Position
						o.mu.RUnlock()

						knownWorld, _ := o.memory.GetKnownWorld(ctx, pos)
						o.logger.Info("Location recalled", slog.String("world", knownWorld))

						o.mu.Lock()
						o.pendingFeedback = append(o.pendingFeedback, domain.Feedback{
							Type:  "SYSTEM",
							Cause: "RECALL: " + knownWorld,
						})
						o.mu.Unlock()
					}
				}
			}

			_ = tm.Complete(ctx, payload.CommandID, success)
		}
	}
}
