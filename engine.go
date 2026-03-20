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
	X float64
	Y float64
	Z float64
}

type GameState struct {
	Position     Vec3
	Health       float64
	Threats      []Threat
	HasBedNearby bool
	Inventory    []Item
	TimeOfDay    int
}

type Engine struct {
	planner   Planner
	routine   RoutineManager
	exec      Executor
	memory    MemoryBank
	telemetry *Telemetry
	uiHub     *UIHub
	logger    *slog.Logger

	// Event loop channel replaces e.mu Mutex
	eventCh chan EngineEvent

	queue        *TaskQueue
	inFlightTask *Action

	planEpoch      int
	planning       bool
	planCancel     context.CancelFunc
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
) *Engine {
	return &Engine{
		planner:    planner,
		routine:    routine,
		exec:       exec,
		memory:     mem,
		telemetry:  tel,
		uiHub:      uiHub,
		logger:     baseLogger.With(slog.String("session_id", sessionID)),
		eventCh:    make(chan EngineEvent, 100), // Buffered to handle rapid client bursts
		queue:      NewTaskQueue(),
		planEpoch:  0,
		lastHealth: 20.0,
		sessionID:  sessionID,
	}
}

func (e *Engine) Run(ctx context.Context, conn *websocket.Conn) {
	e.wg.Add(1)
	go e.loop(ctx)

	// Keep the WS reader tight. It parses raw messages and feeds the event loop.
	for {
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

	close(e.eventCh) // Triggers shutdown of the event loop
	e.wg.Wait()
	_ = e.exec.Close()
}

// loop is the single-threaded actor routine. All state mutations happen here.
func (e *Engine) loop(ctx context.Context) {
	defer e.wg.Done()
	defer e.cancelPlanning()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-e.eventCh:
			if !ok {
				return // Channel closed due to disconnect
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
		e.handleClientAction(ev)
	case EventPlanReady:
		e.handlePlanReady(ev)
	case EventPlanError:
		e.handlePlanError(ev)
	}
}

func (e *Engine) handleStateUpdate(ctx context.Context, ev EventClientState) {
	e.uiHub.Broadcast(map[string]interface{}{"type": "state_update", "payload": ev.State})

	e.lastPos = ev.State.Position
	topThreat := ""
	if len(ev.State.Threats) > 0 {
		topThreat = ev.State.Threats[0].Name
	}

	healthDropped := ev.State.Health < e.lastHealth && ev.State.Health < 15
	criticalOverride := healthDropped

	e.lastHealth = ev.State.Health
	e.lastThreat = topThreat

	// Evaluate routines. queue.HasRoutineTarget replaces the old hasRoutineTaskLocked
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
			// milestone generation runs in its own goroutine managed by planner
			go e.planner.GenerateMilestone(ctx, ev.RawPayload, e.sessionID)
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
		go e.planner.GenerateMilestone(ctx, ev.RawPayload, e.sessionID)
		return
	}

	// Trigger async LLM planning
	epochAtStart := e.planEpoch
	sysOverride := e.systemOverride
	e.systemOverride = ""

	planCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	e.planCancel = cancel
	e.planning = true
	e.lastReplan = time.Now()

	e.telemetry.RecordReplan()

	// Spin up async worker to call LLM, ensuring it feeds results back to the eventCh
	go func() {
		plan, err := e.planner.GenerateTactics(planCtx, ev.RawPayload, e.sessionID, sysOverride)
		if err != nil {
			if planCtx.Err() != context.Canceled {
				e.eventCh <- EventPlanError{Epoch: epochAtStart, Error: err}
			}
			return
		}
		e.eventCh <- EventPlanReady{Epoch: epochAtStart, Plan: plan}
	}()
}

func (e *Engine) handlePlanReady(ev EventPlanReady) {
	if e.planCancel != nil {
		e.planCancel = nil
	}
	e.planning = false

	if e.planEpoch != ev.Epoch {
		return // Stale plan
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
	}

	e.uiHub.Broadcast(map[string]interface{}{"type": "objective_update", "payload": ev.Plan.Objective})
	go e.setSummaryAsync("Current Objective", ev.Plan.Objective)

	e.queue.ClearBySource(SourceLLM)
	e.queue.Push(ev.Plan.Tasks...)

	e.tasksCompletedSinceReplan = 0
	e.processNextTask()
}

func (e *Engine) handlePlanError(ev EventPlanError) {
	if e.planCancel != nil {
		e.planCancel = nil
	}
	e.planning = false

	if e.planEpoch != ev.Epoch {
		return
	}

	e.logger.Error("Planning failed", slog.Any("error", ev.Error))
	go e.exec.SendControl("planning_error", "Failed to generate valid plan")
}

func (e *Engine) handleClientAction(ev EventClientAction) {
	meta := EventMeta{
		SessionID: e.sessionID,
		X:         e.lastPos.X,
		Y:         e.lastPos.Y,
		Z:         e.lastPos.Z,
	}

	logCtx := []any{
		slog.String("action", ev.Action),
		slog.String("command_id", ev.CommandID),
		slog.Int("duration_ms", ev.Duration),
	}

	switch ev.Event {
	case "death":
		e.telemetry.RecordTaskStatus("FAILED")
		e.resetExecutionState()
		e.planner.ClearMilestone()

		e.systemOverride = fmt.Sprintf(
			"CRITICAL OVERRIDE: You have died at X:%.1f Y:%.1f Z:%.1f. Cause: %s. "+
				"Your items dropped here and will despawn in 5 minutes. Formulate a recovery plan immediately.",
			e.lastPos.X, e.lastPos.Y, e.lastPos.Z, ev.Cause,
		)
		e.lastReplan = time.Time{}

		meta.Status = "FAILED"
		go e.memory.LogEvent("death", "Died due to: "+ev.Cause, meta)
		e.logger.Warn("Bot died — milestone cleared", slog.String("cause", ev.Cause))

	case "panic_retreat":
		e.telemetry.RecordPanic()
		e.resetExecutionState()

		e.panicCooldown = time.Now().Add(10 * time.Second)
		meta.Status = "PANIC"
		go e.memory.LogEvent("evasion", "Fled from threat: "+ev.Cause, meta)
		e.logger.Warn("Reflex triggered by client", append(logCtx, slog.String("cause", ev.Cause))...)

	case "task_completed":
		if !e.matchesInFlight(ev.CommandID) {
			return
		}
		e.telemetry.RecordTaskStatus("COMPLETED")
		meta.Status = "COMPLETED"
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
		status := strings.ToUpper(strings.Split(ev.Event, "_")[1])
		e.telemetry.RecordTaskStatus(status)
		meta.Status = status
		go e.memory.LogEvent(ev.Action, "Task "+status, meta)
		e.logger.Warn("Task incomplete", append(logCtx, slog.String("event", ev.Event))...)

		e.resetExecutionState()

		if time.Now().Before(e.panicCooldown) {
			e.lastReplan = time.Now()
		}

		if e.tasksCompletedSinceReplan == 0 {
			e.planner.RecordStall()
		}
		e.tasksCompletedSinceReplan = 0

		go e.setSummaryAsync("Last Failure", ev.Action+" ("+ev.Event+")")
	}
}

func (e *Engine) processNextTask() {
	if e.inFlightTask != nil || e.queue.Len() == 0 {
		return
	}

	e.inFlightTask = e.queue.Pop()
	task := e.inFlightTask

	if task.Action == "mark_location" {
		locName := task.Target.Name
		go func(name string, x, y, z float64) {
			err := e.memory.MarkLocation(context.Background(), name, x, y, z)
			if err == nil {
				msg := fmt.Sprintf("Marked %s at X:%.1f, Y:%.1f, Z:%.1f", name, x, y, z)
				e.memory.LogEvent("spatial_memory", msg, EventMeta{SessionID: e.sessionID, X: x, Y: y, Z: z, Status: "COMPLETED"})
				e.logger.Info("Location marked in spatial memory", slog.String("name", name))
			}
			e.eventCh <- EventClientAction{Event: "task_completed", CommandID: task.ID}
		}(locName, e.lastPos.X, e.lastPos.Y, e.lastPos.Z)
		return
	}

	if task.Action == "recall_location" {
		locName := task.Target.Name
		go func(name string) {
			loc, err := e.memory.GetLocation(context.Background(), name)
			if err == nil {
				msg := fmt.Sprintf("Recalled %s at X:%.1f, Y:%.1f, Z:%.1f", name, loc.X, loc.Y, loc.Z)
				e.memory.LogEvent("spatial_memory", msg, EventMeta{SessionID: e.sessionID, Status: "COMPLETED"})
				e.setSummaryAsync("Known Location: "+name, msg)
			}
			e.eventCh <- EventClientAction{Event: "task_completed", CommandID: task.ID}
		}(locName)
		return
	}

	_ = e.exec.Dispatch(*task)
}

func (e *Engine) resetExecutionState() {
	e.planEpoch++
	e.queue.ClearBySource(SourceLLM)
	e.queue.ClearBySource(SourceRoutine) // Or leave routines if you want them to survive interrupts
	e.inFlightTask = nil
	e.lastReplan = time.Time{}
	e.cancelPlanning()
}

func (e *Engine) matchesInFlight(commandID string) bool {
	if e.inFlightTask == nil {
		return false
	}
	return commandID == "" || e.inFlightTask.ID == commandID
}

func (e *Engine) cancelPlanning() {
	if e.planCancel != nil {
		e.planCancel()
		e.planCancel = nil
	}
	e.planning = false
}

func (e *Engine) setSummaryAsync(key, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.memory.SetSummary(ctx, e.sessionID, key, value)
}
