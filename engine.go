package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type GameState struct {
	Health       float64 `json:"health"`
	Food         float64 `json:"food"`
	TimeOfDay    int     `json:"time_of_day"`
	HasBedNearby bool    `json:"has_bed_nearby"`
	Position     Vec3    `json:"position"`
	Threats      []struct {
		Name string `json:"name"`
	} `json:"threats"`
	Inventory []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"inventory"`
}

type Engine struct {
	planner    Planner
	routine    RoutineManager
	exec       Executor
	memory     MemoryBank
	eventStore EventStore
	telemetry  *Telemetry
	uiHub      *UIHub
	logger     *slog.Logger

	eventCh chan EngineEvent

	queue        *TaskQueue
	inFlightTask *Action

	planEpoch         int
	planning          bool
	planCancel        context.CancelFunc
	milestoneEpoch    int
	milestonePlanning bool
	milestoneCancel   context.CancelFunc

	sessionID      string
	systemOverride string

	lastReplan    time.Time
	panicCooldown time.Time
	lastHealth    float64
	lastThreat    string
	lastPos       Vec3
	wg            sync.WaitGroup

	tasksCompletedSinceReplan int
}

func NewEngine(
	planner Planner,
	routine RoutineManager,
	exec Executor,
	mem MemoryBank,
	tel *Telemetry,
	uiHub *UIHub,
	baseLogger *slog.Logger,
	sessionID string,
	eventStore EventStore,
) *Engine {
	return &Engine{
		planner:    planner,
		routine:    routine,
		exec:       exec,
		memory:     mem,
		eventStore: eventStore,
		telemetry:  tel,
		uiHub:      uiHub,
		logger:     baseLogger.With(slog.String("session_id", sessionID)),
		eventCh:    make(chan EngineEvent, 100),
		queue:      NewTaskQueue(),
		planEpoch:  0,
		lastHealth: 20.0,
		sessionID:  sessionID,
	}
}

func (e *Engine) Run(ctx context.Context, conn *websocket.Conn) {
	e.wg.Add(1)
	go e.loop(ctx)

	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}

		if err := conn.ReadJSON(&msg); err != nil {
			e.logger.Warn("Bot disconnected or read error", slog.Any("error", err))
			break
		}

		switch msg.Type {
		case "state":
			var state GameState
			if err := json.Unmarshal(msg.Payload, &state); err == nil {
				e.eventCh <- EventClientState{State: state, RawPayload: msg.Payload}
			}
		case "event":
			var act EventClientAction
			if err := json.Unmarshal(msg.Payload, &act); err == nil {
				e.eventCh <- act
				e.uiHub.Broadcast(map[string]interface{}{"type": "event_stream", "payload": act})
			}
		default:
			e.logger.Warn("Ignoring unknown message type", slog.String("type", msg.Type))
		}
	}

	close(e.eventCh)
	e.wg.Wait()
	_ = e.exec.Close()
}

func (e *Engine) loop(ctx context.Context) {
	defer e.wg.Done()
	defer e.cancelPlanning()
	defer e.cancelMilestonePlanning()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-e.eventCh:
			if !ok {
				return
			}
			e.processEvent(ctx, event)
		}
	}
}

func (e *Engine) processEvent(ctx context.Context, event EngineEvent) {
	switch ev := event.(type) {
	case EventClientState:
		e.handleStateUpdate(ctx, ev)
	case EventClientAction:
		e.handleClientAction(ctx, ev)
	case EventPlanReady:
		e.handlePlanReady(ctx, ev)
	case EventPlanError:
		e.handlePlanError(ctx, ev)
	case EventMilestoneReady:
		e.handleMilestoneReady(ctx, ev)
	case EventMilestoneError:
		e.handleMilestoneError(ctx, ev)
	}
}

func (e *Engine) handleStateUpdate(ctx context.Context, ev EventClientState) {
	e.uiHub.Broadcast(map[string]interface{}{"type": "state_update", "payload": ev.State})

	e.lastPos = ev.State.Position
	topThreat := ""
	if len(ev.State.Threats) > 0 {
		topThreat = ev.State.Threats[0].Name
	}

	healthDropped := ev.State.Health < e.lastHealth && ev.State.Health < 5
	criticalOverride := healthDropped && time.Since(e.lastReplan) > 15*time.Second

	e.lastHealth = ev.State.Health
	e.lastThreat = topThreat

	newRoutines := e.routine.Evaluate(ev.State, e.inFlightTask, e.queue.items)
	if len(newRoutines) > 0 {
		e.queue.Push(newRoutines...)

		if e.inFlightTask != nil && e.inFlightTask.Priority > newRoutines[0].Priority {
			e.logger.Info("Routine interrupting in-flight task", slog.String("routine", newRoutines[0].Action))
			go e.exec.SendControl("abort_task", "Routine interrupt: "+newRoutines[0].Rationale)
			e.inFlightTask = nil
			e.lastReplan = time.Time{}
		}

		if e.inFlightTask == nil {
			e.processNextTask()
		}
	}

	if time.Now().Before(e.panicCooldown) {
		return
	}

	if e.planning && criticalOverride {
		e.cancelPlanning()
	}

	busy := e.inFlightTask != nil || e.queue.Len() > 0
	timeSinceReplan := time.Since(e.lastReplan)
	needsReplan := e.lastReplan.IsZero() || timeSinceReplan > 10*time.Second || criticalOverride || e.systemOverride != ""

	if busy && !criticalOverride {
		if e.planner.GetActiveMilestone() == nil {
			e.triggerMilestoneGeneration(ctx, ev.RawPayload)
		}
		return
	}

	if criticalOverride && busy {
		e.resetExecutionState()
		go e.exec.SendControl("noop", "critical state interrupt")
	}

	if !needsReplan {
		return
	}

	if e.planner.GetActiveMilestone() == nil {
		e.triggerMilestoneGeneration(ctx, ev.RawPayload)
		return
	}

	epochAtStart := e.planEpoch
	sysOverride := e.systemOverride
	e.systemOverride = ""

	// Increased timeout from 15s to 60s to account for LLM latency
	planCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	e.planCancel = cancel
	e.planning = true
	e.lastReplan = time.Now()

	e.telemetry.RecordReplan()
	traceID := fmt.Sprintf("trace-%d", time.Now().UnixNano())

	go func() {
		plan, err := e.planner.GenerateTactics(planCtx, ev.RawPayload, e.sessionID, sysOverride)
		if err != nil {
			if planCtx.Err() != context.Canceled {
				e.eventCh <- EventPlanError{Epoch: epochAtStart, Error: err}
			}
			return
		}
		e.eventCh <- EventPlanReady{Epoch: epochAtStart, TraceID: traceID, Plan: plan}
	}()
}

func (e *Engine) triggerMilestoneGeneration(ctx context.Context, statePayload json.RawMessage) {
	if e.milestonePlanning {
		return
	}

	epochAtStart := e.milestoneEpoch
	msCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	e.milestoneCancel = cancel
	e.milestonePlanning = true

	go func() {
		milestone, err := e.planner.GenerateMilestone(msCtx, statePayload, e.sessionID)
		if err != nil {
			if msCtx.Err() != context.Canceled {
				e.eventCh <- EventMilestoneError{Epoch: epochAtStart, Error: err}
			}
			return
		}
		e.eventCh <- EventMilestoneReady{Epoch: epochAtStart, Milestone: milestone}
	}()
}

func (e *Engine) handleMilestoneReady(ctx context.Context, ev EventMilestoneReady) {
	if e.milestoneCancel != nil {
		e.milestoneCancel = nil
	}
	e.milestonePlanning = false

	if e.milestoneEpoch != ev.Epoch {
		e.telemetry.RecordStalePlan()
		return
	}

	e.eventStore.Append(ctx, e.sessionID, "", "MilestoneGenerated", map[string]interface{}{
		"id":          ev.Milestone.ID,
		"description": ev.Milestone.Description,
	})

	e.lastReplan = time.Time{}
}

func (e *Engine) handleMilestoneError(ctx context.Context, ev EventMilestoneError) {
	if e.milestoneCancel != nil {
		e.milestoneCancel = nil
	}
	e.milestonePlanning = false

	if e.milestoneEpoch != ev.Epoch {
		return
	}

	e.eventStore.Append(ctx, e.sessionID, "", "MilestoneGenerationFailed", map[string]interface{}{
		"error": ev.Error.Error(),
	})
	e.logger.Error("Milestone generation failed", slog.Any("error", ev.Error))
}

func (e *Engine) handlePlanReady(ctx context.Context, ev EventPlanReady) {
	if e.planCancel != nil {
		e.planCancel = nil
	}
	e.planning = false

	if e.planEpoch != ev.Epoch {
		e.telemetry.RecordStalePlan()
		return
	}

	if ev.Plan != nil && ev.Plan.MilestoneComplete && len(ev.Plan.Tasks) == 0 {
		e.lastReplan = time.Time{}
		return
	}

	if ev.Plan == nil || len(ev.Plan.Tasks) == 0 {
		go e.exec.SendControl("noop", "No actionable tasks generated")
		return
	}

	for i := range ev.Plan.Tasks {
		if ev.Plan.Tasks[i].ID == "" {
			ev.Plan.Tasks[i].ID = fmt.Sprintf("cmd-llm-%d-%d", time.Now().UnixNano(), i)
		}
		ev.Plan.Tasks[i].Source = string(SourceLLM)

		ev.Plan.Tasks[i].Trace = TraceContext{
			TraceID:  ev.TraceID,
			ActionID: ev.Plan.Tasks[i].ID,
		}
		if ms := e.planner.GetActiveMilestone(); ms != nil {
			ev.Plan.Tasks[i].Trace.MilestoneID = ms.ID
		}
	}

	e.uiHub.Broadcast(map[string]interface{}{"type": "objective_update", "payload": ev.Plan.Objective})
	go e.setSummaryAsync("Current Objective", ev.Plan.Objective)

	e.queue.ClearBySource(SourceLLM)
	e.queue.Push(ev.Plan.Tasks...)

	e.eventStore.Append(ctx, e.sessionID, ev.TraceID, "TacticalPlanGenerated", map[string]interface{}{
		"objective":  ev.Plan.Objective,
		"task_count": len(ev.Plan.Tasks),
		"milestone_id": func() string {
			if len(ev.Plan.Tasks) > 0 {
				return ev.Plan.Tasks[0].Trace.MilestoneID
			}
			return ""
		}(),
	})

	e.tasksCompletedSinceReplan = 0
	e.processNextTask()
}

func (e *Engine) handlePlanError(ctx context.Context, ev EventPlanError) {
	if e.planCancel != nil {
		e.planCancel = nil
	}
	e.planning = false

	if e.planEpoch != ev.Epoch {
		return
	}

	e.eventStore.Append(ctx, e.sessionID, "", "TacticalPlanFailed", map[string]interface{}{
		"error": ev.Error.Error(),
	})
	e.logger.Error("Planning failed", slog.Any("error", ev.Error))
	go e.exec.SendControl("planning_error", "Failed to generate valid plan")

	// Enforce 10-second backoff cooldown before retrying to prevent API spam
	e.lastReplan = time.Now()
}

func (e *Engine) handleClientAction(ctx context.Context, ev EventClientAction) {
	meta := EventMeta{
		SessionID: e.sessionID,
		X:         e.lastPos.X,
		Y:         e.lastPos.Y,
		Z:         e.lastPos.Z,
	}

	e.eventStore.Append(ctx, e.sessionID, meta.TraceID, strings.ToUpper(ev.Event), map[string]interface{}{
		"action": ev.Action,
		"cause":  ev.Cause,
		"status": meta.Status,
	})

	if e.inFlightTask != nil {
		meta.TraceID = e.inFlightTask.Trace.TraceID
	}

	logCtx := []any{
		slog.String("action", ev.Action),
		slog.String("command_id", ev.CommandID),
		slog.Int("duration_ms", ev.Duration),
	}

	switch ev.Event {
	case "death":
		e.telemetry.RecordTaskStatus(StatusFailed)
		e.resetExecutionState()
		e.planner.ClearMilestone()

		e.systemOverride = fmt.Sprintf(
			"CRITICAL OVERRIDE: You have died at X:%.1f Y:%.1f Z:%.1f. Cause: %s. Formulate a recovery plan immediately.",
			e.lastPos.X, e.lastPos.Y, e.lastPos.Z, ev.Cause,
		)
		e.lastReplan = time.Time{}

		meta.Status = string(StatusFailed)
		go e.memory.LogEvent("death", "Died due to: "+ev.Cause, meta)
		e.eventStore.Append(ctx, e.sessionID, "", "Death", map[string]interface{}{
			"position": e.lastPos,
			"cause":    ev.Cause,
		})
		e.logger.Warn("Bot died — milestone cleared", slog.String("cause", ev.Cause))

	case "panic_retreat":
		e.telemetry.RecordPanic()
		e.resetExecutionState()

		e.panicCooldown = time.Now().Add(10 * time.Second)
		meta.Status = string(StatusPanic)
		go e.memory.LogEvent("evasion", "Fled from threat: "+ev.Cause, meta)
		e.logger.Warn("Reflex triggered by client", append(logCtx, slog.String("cause", ev.Cause))...)

	case "task_completed":
		if !e.matchesInFlight(ev.CommandID) {
			return
		}
		e.telemetry.RecordTaskStatus(StatusCompleted)
		meta.Status = string(StatusCompleted)
		go e.memory.LogEvent(ev.Action, "Finished successfully", meta)
		e.logger.Info("Task completed", logCtx...)
		e.inFlightTask = nil

		e.tasksCompletedSinceReplan++
		e.planner.ResetStall()
		e.processNextTask()

	case "task_failed", "task_aborted":
		if !e.matchesInFlight(ev.CommandID) {
			return
		}
		statusStr := strings.ToUpper(strings.Split(ev.Event, "_")[1])
		status := TaskStatus(statusStr)

		failCause := ev.Cause
		if failCause == "" {
			failCause = "unknown reason"
		}

		e.telemetry.RecordTaskStatus(status)
		meta.Status = string(status)
		go e.memory.LogEvent(ev.Action, "Task "+string(status)+": "+failCause, meta)
		e.logger.Warn("Task incomplete", append(logCtx, slog.String("event", ev.Event), slog.String("cause", failCause))...)

		e.resetExecutionState()

		if time.Now().Before(e.panicCooldown) {
			e.lastReplan = time.Now()
		}

		if e.tasksCompletedSinceReplan == 0 {
			e.planner.RecordStall()
		}
		e.tasksCompletedSinceReplan = 0

		e.systemOverride = fmt.Sprintf("CRITICAL OVERRIDE: Previous task '%s' %s. Cause: %s. Adjust your plan accordingly.", ev.Action, string(status), failCause)
		go e.setSummaryAsync("Last Failure", ev.Action+" ("+ev.Event+"): "+failCause)
	}
}

func (e *Engine) processNextTask() {
	if e.inFlightTask != nil || e.queue.Len() == 0 {
		return
	}

	e.inFlightTask = e.queue.Pop()
	task := e.inFlightTask

	if task.Action == string(ActionMarkLocation) {
		locName := task.Target.Name
		go func(name string, x, y, z float64, tID string) {
			err := e.memory.MarkLocation(context.Background(), name, x, y, z)
			if err == nil {
				msg := fmt.Sprintf("Marked %s at X:%.1f, Y:%.1f, Z:%.1f", name, x, y, z)
				e.memory.LogEvent("spatial_memory", msg, EventMeta{SessionID: e.sessionID, TraceID: tID, X: x, Y: y, Z: z, Status: "COMPLETED"})
				e.logger.Info("Location marked in spatial memory", slog.String("name", name))
			}
			e.eventCh <- EventClientAction{Event: "task_completed", Action: string(ActionMarkLocation), CommandID: task.ID}
		}(locName, e.lastPos.X, e.lastPos.Y, e.lastPos.Z, task.Trace.TraceID)
		return
	}

	if task.Action == string(ActionRecallLocation) {
		locName := task.Target.Name
		go func(name string, tID string) {
			loc, err := e.memory.GetLocation(context.Background(), name)
			if err == nil {
				msg := fmt.Sprintf("Recalled %s at X:%.1f, Y:%.1f, Z:%.1f", name, loc.X, loc.Y, loc.Z)
				e.memory.LogEvent("spatial_memory", msg, EventMeta{SessionID: e.sessionID, TraceID: tID, Status: "COMPLETED"})
				e.setSummaryAsync("Known Location: "+name, msg)
			}
			e.eventCh <- EventClientAction{Event: "task_completed", Action: string(ActionRecallLocation), CommandID: task.ID}
		}(locName, task.Trace.TraceID)
		return
	}

	if err := e.exec.Dispatch(*task); err != nil {
		e.telemetry.RecordDispatchFailure()
		e.logger.Error("Dispatch failed", slog.Any("error", err), slog.String("action", task.Action))
		e.inFlightTask = nil
		e.lastReplan = time.Time{}
		e.systemOverride = "CRITICAL OVERRIDE: Executor dispatch failed with error: " + err.Error()
	}
}

func (e *Engine) resetExecutionState() {
	e.planEpoch++
	e.milestoneEpoch++
	e.queue.ClearBySource(SourceLLM)
	// Explicitly removed e.queue.ClearBySource(SourceRoutine) so we don't clear panic reflexes
	e.inFlightTask = nil
	e.lastReplan = time.Time{}
	e.cancelPlanning()
	e.cancelMilestonePlanning()
}

func (e *Engine) matchesInFlight(commandID string) bool {
	if e.inFlightTask == nil {
		return false
	}
	return e.inFlightTask.ID == commandID
}

func (e *Engine) cancelPlanning() {
	if e.planCancel != nil {
		e.planCancel()
		e.planCancel = nil
	}
	e.planning = false
}

func (e *Engine) cancelMilestonePlanning() {
	if e.milestoneCancel != nil {
		e.milestoneCancel()
		e.milestoneCancel = nil
	}
	e.milestonePlanning = false
}

func (e *Engine) setSummaryAsync(key, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.memory.SetSummary(ctx, e.sessionID, key, value)
}
