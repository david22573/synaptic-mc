// engine.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	logger    *slog.Logger

	mu      sync.Mutex
	writeMu sync.Mutex

	taskQueue    []Action
	inFlightTask *Action

	planEpoch  int
	planning   bool
	planCancel context.CancelFunc
	sessionID  string

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
	baseLogger *slog.Logger,
) *Engine {
	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())

	return &Engine{
		brain:      b,
		conn:       c,
		memory:     mem,
		telemetry:  tel,
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

	var nextTask *Action
	var summaryKey string
	var summaryValue string

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
		slog.Int("plan_epoch", e.planEpoch),
	}

	switch eventPayload.Event {
	case "panic_retreat":
		e.telemetry.RecordPanic()
		e.resetExecutionStateLocked()

		// The JS SurvivalSystem handles panics for ~8s. Suppress normal planning.
		e.panicCooldown = time.Now().Add(8 * time.Second)

		meta.Status = "PANIC"
		e.memory.LogEvent("evasion", "Fled from threat: "+eventPayload.Cause, meta)

		e.logger.Warn(
			"Reflex triggered by client",
			append(logCtx, slog.String("cause", eventPayload.Cause))...,
		)

	case "task_started":
		if !e.matchesInFlightLocked(eventPayload.CommandID) {
			e.logger.Warn("Ignoring task_started for non-current task", append(logCtx, slog.String("current_in_flight", e.currentTaskIDLocked()))...)
			e.mu.Unlock()
			return
		}

		meta.Status = "STARTED"
		e.memory.LogEvent(eventPayload.Action, "Started task ID: "+eventPayload.CommandID, meta)
		e.logger.Debug("Task started", logCtx...)

	case "task_completed":
		if !e.matchesInFlightLocked(eventPayload.CommandID) {
			e.logger.Warn("Ignoring stale task_completed event", append(logCtx, slog.String("current_in_flight", e.currentTaskIDLocked()))...)
			e.mu.Unlock()
			return
		}

		e.telemetry.RecordTaskStatus("COMPLETED")
		meta.Status = "COMPLETED"
		e.memory.LogEvent(eventPayload.Action, "Finished successfully", meta)
		e.logger.Info("Task completed", logCtx...)

		e.inFlightTask = nil
		nextTask = e.dequeueNextTaskLocked()

	case "task_failed":
		if !e.matchesInFlightLocked(eventPayload.CommandID) {
			e.logger.Warn("Ignoring stale task_failed event", append(logCtx, slog.String("current_in_flight", e.currentTaskIDLocked()))...)
			e.mu.Unlock()
			return
		}

		e.telemetry.RecordTaskStatus("FAILED")
		meta.Status = "FAILED"
		e.memory.LogEvent(eventPayload.Action, "Task failed", meta)
		e.logger.Error("Task failed", append(logCtx, slog.String("event", eventPayload.Event))...)

		summaryKey = "Last Failure"
		summaryValue = eventPayload.Action + " (task_failed)"

		e.resetExecutionStateLocked()

	case "task_aborted":
		if !e.matchesInFlightLocked(eventPayload.CommandID) {
			e.logger.Warn("Ignoring stale task_aborted event", append(logCtx, slog.String("current_in_flight", e.currentTaskIDLocked()))...)
			e.mu.Unlock()
			return
		}

		e.telemetry.RecordTaskStatus("ABORTED")
		meta.Status = "ABORTED"
		e.memory.LogEvent(eventPayload.Action, "Task aborted", meta)
		e.logger.Warn("Task aborted", append(logCtx, slog.String("event", eventPayload.Event))...)

		summaryKey = "Last Failure"
		summaryValue = eventPayload.Action + " (task_aborted)"

		e.resetExecutionStateLocked()

	default:
		e.logger.Warn("Ignoring unknown event type", slog.String("event", eventPayload.Event))
		e.mu.Unlock()
		return
	}

	e.mu.Unlock()

	if summaryKey != "" {
		e.setSummaryAsync(summaryKey, summaryValue)
	}

	if nextTask != nil {
		_ = e.sendCommand(*nextTask)
	}
}

func (e *Engine) handleState(ctx context.Context, payload json.RawMessage) {
	var state GameState
	if err := json.Unmarshal(payload, &state); err != nil {
		e.logger.Warn("Failed to decode state payload", slog.Any("error", err))
		return
	}

	var epochAtStart int
	var sessionID string
	var shouldPlan bool

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

	// Abort if the JS client is currently handling a panic reflex
	if time.Now().Before(e.panicCooldown) {
		e.mu.Unlock()
		return
	}

	// Context Cancellation: Preempt stale LLM calls if the environment changes critically
	if e.planning {
		if criticalOverride {
			e.logger.Warn("Preempting ongoing LLM plan due to critical state change")
			e.cancelPlanningLocked()
		} else {
			e.mu.Unlock()
			return
		}
	}

	busy := e.inFlightTask != nil || len(e.taskQueue) > 0
	timeSinceReplan := time.Since(e.lastReplan)
	needsReplan := e.lastReplan.IsZero() || timeSinceReplan > 10*time.Second || criticalOverride

	// Don't interrupt standard workflows for routine replans
	if busy && !criticalOverride {
		e.mu.Unlock()
		return
	}

	// If critical and busy, flush the execution queue to allow new priorities
	if criticalOverride && busy {
		e.resetExecutionStateLocked()
		go e.sendControlMessage("noop", "critical state interrupt")
	}

	if !needsReplan {
		e.mu.Unlock()
		return
	}

	epochAtStart = e.planEpoch
	sessionID = e.sessionID

	planCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	e.planCancel = cancel
	e.planning = true
	e.lastReplan = time.Now()
	shouldPlan = true

	e.mu.Unlock()

	if !shouldPlan {
		return
	}

	e.telemetry.RecordReplan()
	go e.evaluateAndQueuePlan(planCtx, payload, epochAtStart, sessionID)
}

func (e *Engine) evaluateAndQueuePlan(
	ctx context.Context,
	payload json.RawMessage,
	epochAtStart int,
	sessionID string,
) {
	plan, err := e.brain.EvaluatePlan(ctx, Tick{State: payload}, sessionID)

	var nextTask *Action
	var sendMsgType string
	var sendReason string
	var objective string

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.planCancel != nil {
		e.planCancel = nil
	}
	e.planning = false

	if e.planEpoch != epochAtStart {
		e.logger.Debug(
			"Discarding stale plan",
			slog.Int("start_epoch", epochAtStart),
			slog.Int("current_epoch", e.planEpoch),
		)
		return
	}

	if err != nil {
		if ctx.Err() == context.Canceled {
			e.logger.Debug("Planning cancelled gracefully via preemptive replan")
			return
		}
		e.logger.Error("Planning failed", slog.Any("error", err))
		sendMsgType = "planning_error"
		sendReason = "Failed to generate valid plan"
		go e.sendControlMessage(sendMsgType, sendReason)
		return
	}

	if plan == nil || len(plan.Tasks) == 0 {
		e.logger.Info("Planner returned no tasks")
		sendMsgType = "noop"
		sendReason = "No actionable tasks generated"
		go e.sendControlMessage(sendMsgType, sendReason)
		return
	}

	for i := range plan.Tasks {
		if plan.Tasks[i].ID == "" {
			plan.Tasks[i].ID = fmt.Sprintf("cmd-llm-%d-%d", time.Now().UnixNano(), i)
		}

		if plan.Tasks[i].Target.Type == "" {
			plan.Tasks[i].Target.Type = "none"
		}
		if plan.Tasks[i].Target.Name == "" {
			plan.Tasks[i].Target.Name = "none"
		}
	}

	objective = plan.Objective
	e.taskQueue = append(e.taskQueue[:0], plan.Tasks...)
	nextTask = e.dequeueNextTaskLocked()

	e.logger.Info(
		"New LLM plan generated",
		slog.String("objective", plan.Objective),
		slog.Int("task_count", len(plan.Tasks)),
	)

	go e.setSummaryAsync("Current Objective", objective)

	if nextTask != nil {
		go e.sendCommand(*nextTask)
	}
}

// resetExecutionStateLocked consolidates clearing the queue and bumping the epoch. Must hold e.mu.
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
	if commandID == "" {
		return true
	}
	return e.inFlightTask.ID == commandID
}

func (e *Engine) currentTaskIDLocked() string {
	if e.inFlightTask == nil {
		return ""
	}
	return e.inFlightTask.ID
}

func (e *Engine) cancelPlanningLocked() {
	if e.planCancel != nil {
		e.planCancel()
		e.planCancel = nil
	}
	e.planning = false
}

func (e *Engine) sendCommand(action Action) error {
	payload, err := json.Marshal(action)
	if err != nil {
		e.logger.Error("Failed to marshal command payload", slog.Any("error", err))
		return err
	}

	response := map[string]interface{}{
		"type":    "command",
		"payload": json.RawMessage(payload),
	}

	if err := e.writeJSON(response); err != nil {
		e.logger.Error(
			"Failed to send command to bot",
			slog.Any("error", err),
			slog.String("command_id", action.ID),
			slog.String("action", action.Action),
		)

		e.mu.Lock()
		if e.inFlightTask != nil && e.inFlightTask.ID == action.ID {
			e.inFlightTask = nil
			e.lastReplan = time.Time{}
		}
		e.mu.Unlock()

		return err
	}

	e.logger.Debug(
		"Command dispatched",
		slog.String("command_id", action.ID),
		slog.String("action", action.Action),
		slog.String("target_type", action.Target.Type),
		slog.String("target_name", action.Target.Name),
	)

	return nil
}

func (e *Engine) sendControlMessage(msgType, reason string) {
	payload, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		e.logger.Error("Failed to marshal control payload", slog.Any("error", err))
		return
	}

	response := map[string]interface{}{
		"type":    msgType,
		"payload": json.RawMessage(payload),
	}

	if err := e.writeJSON(response); err != nil {
		e.logger.Error("Failed to send control message", slog.Any("error", err), slog.String("type", msgType))
	}
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

		if err := e.memory.SetSummary(ctx, e.sessionID, key, value); err != nil {
			e.logger.Warn("Failed to persist session summary", slog.String("key", key), slog.Any("error", err))
		}
	}()
}
