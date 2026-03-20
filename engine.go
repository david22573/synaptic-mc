package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
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
	wg            sync.WaitGroup

	// ── Two-tier planning state ──────────────────────────────────────────
	// activeMilestone is the current high-level goal. It is generated once
	// and persists until the tactical planner signals completion, the bot
	// dies, or it stalls too many times without progress.
	activeMilestone     *MilestonePlan
	milestoneGenerating bool
	// milestoneStallCount tracks consecutive tactical replans that produced
	// zero completed tasks. Too many stalls trigger a milestone regeneration.
	milestoneStallCount int
	// tasksCompletedThisMilestone resets on each new milestone.
	tasksCompletedThisMilestone int
}

const maxMilestoneStalls = 5

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
		e.wg.Wait()
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

		// Death invalidates the active milestone — the bot may have lost
		// critical items, so the previous goal is likely no longer valid.
		e.activeMilestone = nil
		e.milestoneStallCount = 0
		e.tasksCompletedThisMilestone = 0

		e.systemOverride = fmt.Sprintf(
			"CRITICAL OVERRIDE: You have died at X:%.1f Y:%.1f Z:%.1f. Cause: %s. "+
				"Your items dropped here and will despawn in 5 minutes. Formulate a recovery plan immediately.",
			e.lastPos.X, e.lastPos.Y, e.lastPos.Z, eventPayload.Cause,
		)
		e.lastReplan = time.Time{}

		meta.Status = "FAILED"
		e.memory.LogEvent("death", "Died due to: "+eventPayload.Cause, meta)
		e.logger.Warn("Bot died — milestone cleared, triggering emergency recovery replan",
			slog.String("cause", eventPayload.Cause))

	case "panic_retreat":
		e.telemetry.RecordPanic()
		e.resetExecutionStateLocked()

		e.panicCooldown = time.Now().Add(10 * time.Second)
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

		// Track progress so we can detect stalls.
		e.tasksCompletedThisMilestone++
		e.milestoneStallCount = 0

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

		if time.Now().Before(e.panicCooldown) {
			e.lastReplan = time.Now()
		}

		// Count this replan cycle as a stall if we haven't made progress.
		if e.tasksCompletedThisMilestone == 0 {
			e.milestoneStallCount++
			if e.milestoneStallCount >= maxMilestoneStalls {
				e.logger.Warn("Milestone stalled — clearing for regeneration",
					slog.Int("stall_count", e.milestoneStallCount),
					slog.String("milestone", milestoneDesc(e.activeMilestone)),
				)
				e.activeMilestone = nil
				e.milestoneStallCount = 0
			}
		}
		e.tasksCompletedThisMilestone = 0

		go e.setSummaryAsync("Last Failure", eventPayload.Action+" ("+eventPayload.Event+")")
	}

	e.mu.Unlock()
}

func (e *Engine) handleState(ctx context.Context, payload json.RawMessage) {
	var state GameState
	if err := json.Unmarshal(payload, &state); err != nil {
		e.logger.Error("Failed to decode state", slog.Any("error", err))
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
	criticalOverride := healthDropped

	e.lastHealth = state.Health
	e.lastThreat = topThreat

	e.evaluateRoutinesLocked(state)

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
		// ── Milestone generation can happen in the background even while
		// tasks are running — we just don't want to fire a tactical replan.
		if e.activeMilestone == nil && !e.milestoneGenerating {
			e.startMilestoneGenerationLocked(ctx, payload)
		}
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

	// ── If we don't have a milestone yet, kick one off and wait. ────────
	// We won't do tactical planning until the milestone is available.
	if e.activeMilestone == nil {
		if !e.milestoneGenerating {
			e.startMilestoneGenerationLocked(ctx, payload)
		}
		e.mu.Unlock()
		return
	}

	// ── Normal tactical replan — we have a milestone, proceed. ──────────
	epochAtStart := e.planEpoch
	sessionID := e.sessionID
	sysOverride := e.systemOverride
	milestone := e.activeMilestone
	e.systemOverride = ""

	planCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	e.planCancel = cancel
	e.planning = true
	e.lastReplan = time.Now()

	e.mu.Unlock()

	e.telemetry.RecordReplan()
	go e.evaluateAndQueuePlan(planCtx, payload, epochAtStart, sessionID, sysOverride, milestone)
}

// startMilestoneGenerationLocked fires a background goroutine to ask the
// LLM for a new milestone. Must be called with e.mu held.
func (e *Engine) startMilestoneGenerationLocked(ctx context.Context, payload json.RawMessage) {
	e.milestoneGenerating = true
	sessionID := e.sessionID

	e.logger.Info("Generating new milestone...")

	go func() {
		mCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()

		milestone, err := e.brain.GenerateMilestone(mCtx, Tick{State: payload}, sessionID)

		e.mu.Lock()
		defer e.mu.Unlock()
		e.milestoneGenerating = false

		if err != nil {
			e.logger.Error("Failed to generate milestone", slog.Any("error", err))
			return
		}

		e.activeMilestone = milestone
		e.tasksCompletedThisMilestone = 0
		e.milestoneStallCount = 0
		// Reset replan timer so tactical planning fires on the next tick.
		e.lastReplan = time.Time{}

		e.logger.Info("New milestone set",
			slog.String("id", milestone.ID),
			slog.String("description", milestone.Description),
		)
		e.uiHub.Broadcast(map[string]interface{}{
			"type":    "milestone_update",
			"payload": milestone,
		})
		go e.setSummaryAsync("Active Milestone", milestone.Description)
	}()
}

func (e *Engine) evaluateRoutinesLocked(state GameState) {
	// 1. Sleep Routine (Requires nearby bed to execute)
	if state.TimeOfDay > 12541 && state.TimeOfDay < 23000 {
		if state.HasBedNearby && !e.hasRoutineTaskLocked("sleep", "bed") {
			e.injectTaskLocked(Action{
				ID:        fmt.Sprintf("routine-sleep-%d", time.Now().UnixNano()),
				Action:    "sleep",
				Target:    Target{Type: "block", Name: "bed"},
				Rationale: "Mandatory daily routine: Sleep to skip the night",
				Priority:  PriRoutine,
			})
		}
	}

	// 2. Inventory Parsing
	hasCraftingTable := false
	hasFurnace := false
	rawMeatCount := 0
	fuelCount := 0
	plankCount := 0
	cobbleCount := 0
	logName := "" // Track the exact type of log we have (e.g., oak_log vs birch_log)

	for _, item := range state.Inventory {
		switch item.Name {
		case "crafting_table":
			hasCraftingTable = true
		case "furnace":
			hasFurnace = true
		case "cobblestone":
			cobbleCount += item.Count
		case "beef", "porkchop", "mutton", "chicken", "rabbit":
			rawMeatCount += item.Count
		case "coal", "charcoal":
			fuelCount += item.Count
		}

		if strings.HasSuffix(item.Name, "_planks") {
			plankCount += item.Count
			fuelCount += item.Count
		}
		if strings.HasSuffix(item.Name, "_log") {
			logName = item.Name
		}
	}

	// 3. Mandatory Tool Routines

	// NEW: Auto-craft planks if we need a table but only have raw logs
	if !hasCraftingTable && plankCount < 4 && logName != "" {
		plankTarget := strings.Replace(logName, "_log", "_planks", 1)
		if !e.hasRoutineTaskLocked("craft", plankTarget) {
			e.injectTaskLocked(Action{
				ID:        fmt.Sprintf("routine-craft-planks-%d", time.Now().UnixNano()),
				Action:    "craft",
				Target:    Target{Type: "recipe", Name: plankTarget},
				Rationale: "Routine: Auto-crafting logs into planks to enable tool crafting",
				Priority:  PriRoutine,
			})
		}
	}

	// Existing: Auto-craft table if we have planks
	if !hasCraftingTable && plankCount >= 4 && !e.hasRoutineTaskLocked("craft", "crafting_table") {
		e.injectTaskLocked(Action{
			ID:        fmt.Sprintf("routine-craft-table-%d", time.Now().UnixNano()),
			Action:    "craft",
			Target:    Target{Type: "recipe", Name: "crafting_table"},
			Rationale: "Mandatory tool missing: Auto-crafting since we have planks",
			Priority:  PriRoutine,
		})
	}

	if !hasFurnace && cobbleCount >= 8 && !e.hasRoutineTaskLocked("craft", "furnace") {
		e.injectTaskLocked(Action{
			ID:        fmt.Sprintf("routine-craft-furnace-%d", time.Now().UnixNano()),
			Action:    "craft",
			Target:    Target{Type: "recipe", Name: "furnace"},
			Rationale: "Mandatory tool missing: Auto-crafting since we have cobblestone",
			Priority:  PriRoutine,
		})
	}

	// 4. Auto-Cooking Routine
	if hasFurnace && rawMeatCount > 0 && fuelCount > 0 && !e.hasRoutineTaskLocked("smelt", "food") {
		e.injectTaskLocked(Action{
			ID:        fmt.Sprintf("routine-smelt-%d", time.Now().UnixNano()),
			Action:    "smelt",
			Target:    Target{Type: "category", Name: "food"},
			Rationale: "Routine: Cooking raw food to restore hunger safely",
			Priority:  PriRoutine,
		})
	}
}

func (e *Engine) hasRoutineTaskLocked(action, targetName string) bool {
	if e.inFlightTask != nil && e.inFlightTask.Action == action && e.inFlightTask.Target.Name == targetName {
		return true
	}
	for _, t := range e.taskQueue {
		if t.Action == action && t.Target.Name == targetName {
			return true
		}
	}
	return false
}

func (e *Engine) injectTaskLocked(task Action) {
	e.taskQueue = append(e.taskQueue, task)

	sort.SliceStable(e.taskQueue, func(i, j int) bool {
		return e.taskQueue[i].Priority < e.taskQueue[j].Priority
	})

	if e.inFlightTask != nil && e.inFlightTask.Priority > task.Priority {
		e.logger.Info("Routine interrupting in-flight LLM task", slog.String("routine", task.Action))
		go e.sendControlMessage("abort_task", "Routine interrupt: "+task.Rationale)

		e.inFlightTask = nil
		e.lastReplan = time.Time{}
	}

	if e.inFlightTask == nil {
		go e.processNextTask()
	}
}

func (e *Engine) evaluateAndQueuePlan(
	ctx context.Context,
	payload json.RawMessage,
	epochAtStart int,
	sessionID, sysOverride string,
	milestone *MilestonePlan,
) {
	plan, err := e.brain.EvaluatePlan(ctx, Tick{State: payload}, sessionID, sysOverride, milestone)

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

	if plan == nil {
		go e.sendControlMessage("noop", "Invalid plan payload received")
		return
	}

	// ── FIX: Process Milestone Completion FIRST ────────────────────────
	if plan.MilestoneComplete && e.activeMilestone != nil {
		e.logger.Info("Milestone marked complete by tactical planner",
			slog.String("milestone", e.activeMilestone.Description),
		)
		go e.memory.LogEvent("milestone_complete", e.activeMilestone.Description,
			EventMeta{SessionID: e.sessionID, Status: "COMPLETED"},
		)
		go e.setSummaryAsync("Last Completed Milestone", e.activeMilestone.Description)

		e.activeMilestone = nil
		e.milestoneStallCount = 0
		e.tasksCompletedThisMilestone = 0
	}

	// Now check if we have actionable tasks for the current tick
	if len(plan.Tasks) == 0 {
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

	sort.SliceStable(e.taskQueue, func(i, j int) bool {
		return e.taskQueue[i].Priority < e.taskQueue[j].Priority
	})

	e.mu.Unlock()
	e.processNextTask()
	e.mu.Lock()
}

func (e *Engine) processNextTask() {
	e.mu.Lock()
	task := e.dequeueNextTaskLocked()
	e.mu.Unlock()

	if task == nil {
		return
	}

	if task.Action == "mark_location" {
		locName := task.Target.Name
		err := e.memory.MarkLocation(context.Background(), locName, e.lastPos.X, e.lastPos.Y, e.lastPos.Z)
		if err == nil {
			msg := fmt.Sprintf("Marked %s at X:%.1f, Y:%.1f, Z:%.1f", locName, e.lastPos.X, e.lastPos.Y, e.lastPos.Z)
			e.memory.LogEvent("spatial_memory", msg, EventMeta{SessionID: e.sessionID, X: e.lastPos.X, Y: e.lastPos.Y, Z: e.lastPos.Z, Status: "COMPLETED"})
			e.logger.Info("Location marked in spatial memory", slog.String("name", locName))
		}

		e.inFlightTask = nil
		go e.processNextTask()
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
		go e.processNextTask()
		return
	}

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
	msg := WSMessage{
		Type:    TypeCommand,
		Payload: json.RawMessage(payload),
	}
	return e.writeJSON(msg)
}

func (e *Engine) sendControlMessage(msgType, reason string) {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	msg := WSMessage{
		Type:    WSMessageType(msgType),
		Payload: json.RawMessage(payload),
	}
	_ = e.writeJSON(msg)
}

func (e *Engine) writeJSON(v interface{}) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.conn.WriteJSON(v)
}

func (e *Engine) setSummaryAsync(key, value string) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = e.memory.SetSummary(ctx, e.sessionID, key, value)
	}()
}

// milestoneDesc is a nil-safe helper for logging.
func milestoneDesc(m *MilestonePlan) string {
	if m == nil {
		return "<none>"
	}
	return m.Description
}
