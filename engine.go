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
	Health   float64 `json:"health"`
	Food     float64 `json:"food"`
	Position Vec3    `json:"position"`
	Threats  []struct {
		Name string `json:"name"`
	} `json:"threats"`
	Inventory []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"inventory"`
}

type Engine struct {
	brain     Brain
	conn      *websocket.Conn
	memory    MemoryBank
	telemetry *Telemetry
	uiHub     *UIHub
	logger    *slog.Logger

	mu      sync.Mutex
	writeMu sync.Mutex

	taskQueue    []Action
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
}

func NewEngine(
	b Brain,
	c *websocket.Conn,
	mem MemoryBank,
	tel *Telemetry,
	uiHub *UIHub,
	baseLogger *slog.Logger,
) *Engine {
	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())

	return &Engine{
		brain:      b,
		conn:       c,
		memory:     mem,
		telemetry:  tel,
		uiHub:      uiHub,
		logger:     baseLogger.With(slog.String("session_id", sessionID)),
		taskQueue:  make([]Action, 0),
		planEpoch:  0,
		lastHealth: 20.0,
		sessionID:  sessionID,
	}
}

func (e *Engine) Run(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.cancelPlanningLocked()
		e.mu.Unlock()
		_ = e.conn.Close()
	}()

	for {
		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}

		if err := e.conn.ReadJSON(&msg); err != nil {
			e.logger.Warn("Bot disconnected or read error", slog.Any("error", err))
			return
		}

		switch msg.Type {
		case "event":
			e.handleEvent(ctx, msg.Payload)
		case "state":
			e.handleState(ctx, msg.Payload)
		default:
			e.logger.Warn("Ignoring unknown message type", slog.String("type", msg.Type))
		}
	}
}

func (e *Engine) handleEvent(ctx context.Context, payload json.RawMessage) {
	var eventPayload struct {
		Event     string `json:"event"`
		Cause     string `json:"cause"`
		Action    string `json:"action"`
		CommandID string `json:"command_id"`
		Duration  int    `json:"duration_ms"`
	}

	if err := json.Unmarshal(payload, &eventPayload); err != nil {
		e.logger.Warn("Failed to decode event payload", slog.Any("error", err))
		return
	}

	e.uiHub.Broadcast(map[string]interface{}{"type": "event_stream", "payload": eventPayload})

	e.mu.Lock()

	meta := EventMeta{
		SessionID: e.sessionID,
		X:         e.lastPos.X,
		Y:         e.lastPos.Y,
		Z:         e.lastPos.Z,
	}

	logCtx := []any{
		slog.String("action", eventPayload.Action),
		slog.String("command_id", eventPayload.CommandID),
		slog.Int("duration_ms", eventPayload.Duration),
	}

	switch eventPayload.Event {
	case "death":
		e.telemetry.RecordTaskStatus("FAILED")
		e.resetExecutionStateLocked()

		// Set high-priority recovery directive
		e.systemOverride = fmt.Sprintf("CRITICAL OVERRIDE: You have died at X:%.1f Y:%.1f Z:%.1f. Cause: %s. Your items dropped here and will despawn in 5 minutes. Formulate a recovery plan immediately.", e.lastPos.X, e.lastPos.Y, e.lastPos.Z, eventPayload.Cause)
		e.lastReplan = time.Time{} // Force immediate replan

		meta.Status = "FAILED"
		e.memory.LogEvent("death", "Died due to: "+eventPayload.Cause, meta)
		e.logger.Warn("Bot died, triggering emergency recovery replan", slog.String("cause", eventPayload.Cause))

	case "panic_retreat":
		e.telemetry.RecordPanic()
		e.resetExecutionStateLocked()
		e.panicCooldown = time.Now().Add(8 * time.Second)
		meta.Status = "PANIC"
		e.memory.LogEvent("evasion", "Fled from threat: "+eventPayload.Cause, meta)
		e.logger.Warn("Reflex triggered by client", append(logCtx, slog.String("cause", eventPayload.Cause))...)

	case "task_completed":
		if !e.matchesInFlightLocked(eventPayload.CommandID) {
			e.mu.Unlock()
			return
		}
		e.telemetry.RecordTaskStatus("COMPLETED")
		meta.Status = "COMPLETED"
		e.memory.LogEvent(eventPayload.Action, "Finished successfully", meta)
		e.logger.Info("Task completed", logCtx...)
		e.inFlightTask = nil
		e.mu.Unlock()
		go e.processNextTask()
		return

	case "task_failed", "task_aborted":
		if !e.matchesInFlightLocked(eventPayload.CommandID) {
			e.mu.Unlock()
			return
		}
		status := strings.ToUpper(strings.Split(eventPayload.Event, "_")[1])
		e.telemetry.RecordTaskStatus(status)
		meta.Status = status
		e.memory.LogEvent(eventPayload.Action, "Task "+status, meta)
		e.logger.Warn("Task incomplete", append(logCtx, slog.String("event", eventPayload.Event))...)

		e.resetExecutionStateLocked()
		go e.setSummaryAsync("Last Failure", eventPayload.Action+" ("+eventPayload.Event+")")
	}

	e.mu.Unlock()
}

func (e *Engine) handleState(ctx context.Context, payload json.RawMessage) {
	var state GameState
	if err := json.Unmarshal(payload, &state); err != nil {
		return
	}

	e.uiHub.Broadcast(map[string]interface{}{"type": "state_update", "payload": state})

	e.mu.Lock()
	e.lastPos = state.Position
	topThreat := ""
	if len(state.Threats) > 0 {
		topThreat = state.Threats[0].Name
	}

	healthDropped := state.Health < e.lastHealth && state.Health < 15
	newThreat := topThreat != "" && topThreat != e.lastThreat
	criticalOverride := healthDropped || newThreat

	e.lastHealth = state.Health
	e.lastThreat = topThreat

	if time.Now().Before(e.panicCooldown) {
		e.mu.Unlock()
		return
	}

	if e.planning && criticalOverride {
		e.cancelPlanningLocked()
	}

	busy := e.inFlightTask != nil || len(e.taskQueue) > 0
	timeSinceReplan := time.Since(e.lastReplan)
	needsReplan := e.lastReplan.IsZero() || timeSinceReplan > 10*time.Second || criticalOverride || e.systemOverride != ""

	if busy && !criticalOverride {
		e.mu.Unlock()
		return
	}

	if criticalOverride && busy {
		e.resetExecutionStateLocked()
		go e.sendControlMessage("noop", "critical state interrupt")
	}

	if !needsReplan {
		e.mu.Unlock()
		return
	}

	epochAtStart := e.planEpoch
	sessionID := e.sessionID
	sysOverride := e.systemOverride
	e.systemOverride = "" // Clear after injecting

	planCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	e.planCancel = cancel
	e.planning = true
	e.lastReplan = time.Now()

	e.mu.Unlock()

	e.telemetry.RecordReplan()
	go e.evaluateAndQueuePlan(planCtx, payload, epochAtStart, sessionID, sysOverride)
}

func (e *Engine) evaluateAndQueuePlan(ctx context.Context, payload json.RawMessage, epochAtStart int, sessionID, sysOverride string) {
	plan, err := e.brain.EvaluatePlan(ctx, Tick{State: payload}, sessionID, sysOverride)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.planCancel != nil {
		e.planCancel = nil
	}
	e.planning = false

	if e.planEpoch != epochAtStart {
		return
	}

	if err != nil {
		if ctx.Err() != context.Canceled {
			e.logger.Error("Planning failed", slog.Any("error", err))
			go e.sendControlMessage("planning_error", "Failed to generate valid plan")
		}
		return
	}

	if plan == nil || len(plan.Tasks) == 0 {
		go e.sendControlMessage("noop", "No actionable tasks generated")
		return
	}

	for i := range plan.Tasks {
		if plan.Tasks[i].ID == "" {
			plan.Tasks[i].ID = fmt.Sprintf("cmd-llm-%d-%d", time.Now().UnixNano(), i)
		}
	}

	e.uiHub.Broadcast(map[string]interface{}{"type": "objective_update", "payload": plan.Objective})
	go e.setSummaryAsync("Current Objective", plan.Objective)

	e.taskQueue = append(e.taskQueue[:0], plan.Tasks...)
	e.mu.Unlock()
	e.processNextTask()
	e.mu.Lock() // Re-lock for defer
}

// processNextTask handles execution delegation (Internal Go Memory tasks vs JS Client tasks)
func (e *Engine) processNextTask() {
	e.mu.Lock()
	task := e.dequeueNextTaskLocked()
	if task == nil {
		e.mu.Unlock()
		return
	}

	// INTERCEPT: Go-Side Internal Tasks
	if task.Action == "mark_location" {
		locName := task.Target.Name
		err := e.memory.MarkLocation(context.Background(), locName, e.lastPos.X, e.lastPos.Y, e.lastPos.Z)
		if err == nil {
			msg := fmt.Sprintf("Marked %s at X:%.1f, Y:%.1f, Z:%.1f", locName, e.lastPos.X, e.lastPos.Y, e.lastPos.Z)
			e.memory.LogEvent("spatial_memory", msg, EventMeta{SessionID: e.sessionID, X: e.lastPos.X, Y: e.lastPos.Y, Z: e.lastPos.Z, Status: "COMPLETED"})
			e.logger.Info("Location marked in spatial memory", slog.String("name", locName))
		}
		e.inFlightTask = nil
		e.mu.Unlock()
		go e.processNextTask() // Immediately grab next task
		return
	}

	if task.Action == "recall_location" {
		locName := task.Target.Name
		loc, err := e.memory.GetLocation(context.Background(), locName)
		if err == nil {
			msg := fmt.Sprintf("Recalled %s at X:%.1f, Y:%.1f, Z:%.1f", locName, loc.X, loc.Y, loc.Z)
			e.memory.LogEvent("spatial_memory", msg, EventMeta{SessionID: e.sessionID, Status: "COMPLETED"})
			go e.setSummaryAsync("Known Location: "+locName, msg)
		}
		e.inFlightTask = nil
		e.mu.Unlock()
		go e.processNextTask()
		return
	}

	// External Task -> Send to JS Client
	e.mu.Unlock()
	_ = e.sendCommand(*task)
}

func (e *Engine) resetExecutionStateLocked() {
	e.planEpoch++
	e.taskQueue = nil
	e.inFlightTask = nil
	e.lastReplan = time.Time{}
	e.cancelPlanningLocked()
}

func (e *Engine) dequeueNextTaskLocked() *Action {
	if e.inFlightTask != nil || len(e.taskQueue) == 0 {
		return nil
	}
	nextTask := e.taskQueue[0]
	e.taskQueue = e.taskQueue[1:]
	e.inFlightTask = &nextTask
	return &nextTask
}

func (e *Engine) matchesInFlightLocked(commandID string) bool {
	if e.inFlightTask == nil {
		return false
	}
	return commandID == "" || e.inFlightTask.ID == commandID
}

func (e *Engine) cancelPlanningLocked() {
	if e.planCancel != nil {
		e.planCancel()
		e.planCancel = nil
	}
	e.planning = false
}

func (e *Engine) sendCommand(action Action) error {
	payload, _ := json.Marshal(action)
	e.uiHub.Broadcast(map[string]interface{}{"type": "command_dispatch", "payload": action})

	if err := e.writeJSON(map[string]interface{}{"type": "command", "payload": json.RawMessage(payload)}); err != nil {
		e.mu.Lock()
		if e.inFlightTask != nil && e.inFlightTask.ID == action.ID {
			e.inFlightTask = nil
			e.lastReplan = time.Time{}
		}
		e.mu.Unlock()
		return err
	}
	return nil
}

func (e *Engine) sendControlMessage(msgType, reason string) {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	_ = e.writeJSON(map[string]interface{}{"type": msgType, "payload": json.RawMessage(payload)})
}

func (e *Engine) writeJSON(v interface{}) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.conn.WriteJSON(v)
}

func (e *Engine) setSummaryAsync(key, value string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = e.memory.SetSummary(ctx, e.sessionID, key, value)
	}()
}
