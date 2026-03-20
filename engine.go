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
	brain        Brain
	conn         *websocket.Conn
	memory       MemoryBank
	telemetry    *Telemetry
	logger       *slog.Logger
	mu           sync.Mutex
	taskQueue    []Action
	inFlightTask *Action
	planEpoch    int
	sessionID    string

	lastReplan time.Time
	lastHealth float64
	lastThreat string
	lastPos    Vec3
}

func NewEngine(b Brain, c *websocket.Conn, mem MemoryBank, tel *Telemetry, baseLogger *slog.Logger) *Engine {
	sessionID := fmt.Sprintf("sess-%d", time.Now().Unix())
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
	defer e.conn.Close()
	var processingMutex sync.Mutex

	for {
		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}

		if err := e.conn.ReadJSON(&msg); err != nil {
			e.logger.Warn("Bot disconnected or read error", slog.Any("error", err))
			return
		}

		if msg.Type == "event" {
			var eventPayload struct {
				Event     string `json:"event"`
				Cause     string `json:"cause"`
				Action    string `json:"action"`
				CommandID string `json:"command_id"`
				Duration  int    `json:"duration_ms"`
			}
			json.Unmarshal(msg.Payload, &eventPayload)

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
				e.planEpoch++
				e.taskQueue = nil
				e.inFlightTask = nil

				meta.Status = "PANIC"
				e.memory.LogEvent("evasion", "Fled from threat: "+eventPayload.Cause, meta)
				e.logger.Warn("Reflex Triggered", append(logCtx, slog.String("cause", eventPayload.Cause))...)

			case "task_started":
				meta.Status = "STARTED"
				e.memory.LogEvent(eventPayload.Action, "Started task ID: "+eventPayload.CommandID, meta)
				e.logger.Debug("Task Started", logCtx...)

			case "task_completed":
				e.telemetry.RecordTaskStatus("COMPLETED")
				meta.Status = "COMPLETED"
				e.memory.LogEvent(eventPayload.Action, "Finished successfully", meta)
				e.logger.Info("Task Completed", logCtx...)

				if e.inFlightTask != nil && e.inFlightTask.ID == eventPayload.CommandID {
					e.inFlightTask = nil
				}
				e.advanceQueueUnsafe()

			case "task_failed", "task_aborted":
				e.telemetry.RecordTaskStatus("FAILED")
				meta.Status = "FAILED"
				e.memory.LogEvent(eventPayload.Action, "Task failed or aborted: "+eventPayload.Event, meta)
				e.memory.SetSummary(ctx, e.sessionID, "Last Failure", eventPayload.Action+" ("+eventPayload.Event+")")
				e.logger.Error("Task Failed", append(logCtx, slog.String("event", eventPayload.Event))...)

				e.planEpoch++
				e.taskQueue = nil
				e.inFlightTask = nil
				e.lastReplan = time.Time{}
			}
			e.mu.Unlock()
			continue
		}

		if msg.Type == "state" {
			var state GameState
			if err := json.Unmarshal(msg.Payload, &state); err != nil {
				continue
			}

			e.mu.Lock()
			e.lastPos = state.Position

			busy := e.inFlightTask != nil || len(e.taskQueue) > 0
			currentEpoch := e.planEpoch

			topThreat := ""
			if len(state.Threats) > 0 {
				topThreat = state.Threats[0].Name
			}

			timeSinceReplan := time.Since(e.lastReplan)
			healthDropped := state.Health < e.lastHealth && state.Health < 15
			newThreat := topThreat != "" && topThreat != e.lastThreat

			needsReplan := !busy && (timeSinceReplan > 10*time.Second || healthDropped || newThreat)

			e.lastHealth = state.Health
			e.lastThreat = topThreat

			if !needsReplan {
				e.mu.Unlock()
				continue
			}

			if state.Health < 10 && topThreat != "" {
				e.telemetry.RecordReplan()
				e.logger.Warn("Deterministic Override: Low health + Threat -> Tactical Retreat")
				e.taskQueue = []Action{{
					ID:        fmt.Sprintf("cmd-det-%d", time.Now().UnixNano()),
					Action:    "retreat",
					Target:    Target{Type: "none", Name: "none"},
					Rationale: "Health critical, threat present",
				}}
				e.advanceQueueUnsafe()
				e.lastReplan = time.Now()
				e.mu.Unlock()
				continue
			}

			e.lastReplan = time.Now()
			e.mu.Unlock()

			if !processingMutex.TryLock() {
				continue
			}

			e.telemetry.RecordReplan()

			go func(payload json.RawMessage, epochAtStart int, sessionID string) {
				defer processingMutex.Unlock()
				evalCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()

				plan, err := e.brain.EvaluatePlan(evalCtx, Tick{State: payload}, sessionID)

				e.mu.Lock()
				defer e.mu.Unlock()

				if e.planEpoch != epochAtStart {
					e.logger.Debug("Discarding stale plan", slog.Int("start_epoch", epochAtStart), slog.Int("current_epoch", e.planEpoch))
					return
				}

				if err != nil || plan == nil || len(plan.Tasks) == 0 {
					e.logger.Error("Planning failed", slog.Any("error", err))
					e.sendControlMessage("planning_error", "Failed to generate valid plan")
					return
				}

				e.logger.Info("New LLM Plan Generated",
					slog.String("objective", plan.Objective),
					slog.Int("task_count", len(plan.Tasks)),
					slog.Int("queue_length", len(e.taskQueue)),
				)
				e.memory.SetSummary(evalCtx, sessionID, "Current Objective", plan.Objective)

				for i := range plan.Tasks {
					plan.Tasks[i].ID = fmt.Sprintf("cmd-llm-%d-%d", time.Now().UnixNano(), i)
				}

				e.taskQueue = plan.Tasks
				e.advanceQueueUnsafe()
			}(msg.Payload, currentEpoch, e.sessionID)
		}
	}
}

func (e *Engine) advanceQueueUnsafe() {
	if e.inFlightTask != nil || len(e.taskQueue) == 0 {
		return
	}

	nextTask := e.taskQueue[0]
	e.taskQueue = e.taskQueue[1:]
	e.inFlightTask = &nextTask

	payload, _ := json.Marshal(nextTask)
	response := map[string]interface{}{
		"type":    "command",
		"payload": json.RawMessage(payload),
	}

	if err := e.conn.WriteJSON(response); err != nil {
		e.logger.Error("Failed to send command to bot", slog.Any("error", err))
		e.inFlightTask = nil
	}
}

func (e *Engine) sendControlMessage(msgType, reason string) {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	response := map[string]interface{}{
		"type":    msgType,
		"payload": json.RawMessage(payload),
	}
	e.conn.WriteJSON(response)
}
