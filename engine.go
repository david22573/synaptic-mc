package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
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
	POIs      []POI           `json:"pois"`
	Inventory []InventoryItem `json:"inventory"`
}

type StateSnapshot struct {
	InventoryHash string
	Health        float64
	HasThreat     bool
}

type EventProcessNext struct{}

func (EventProcessNext) isEngineEvent() {}

type Engine struct {
	planner    Planner
	routine    RoutineManager
	exec       Executor
	memory     MemoryBank
	eventStore EventStore
	strategy   *StrategyManager
	learning   *LearningSystem
	telemetry  *Telemetry
	uiHub      *UIHub
	logger     *slog.Logger

	eventCh chan EngineEvent

	queue        *TaskQueue
	inFlightTask *Action

	planEpoch  int
	planning   bool
	planCancel context.CancelFunc

	sessionID       string
	systemOverride  string
	currentStrategy string

	lastReplan      time.Time
	lastSnapshot    StateSnapshot
	lastState       GameState
	tsEmergencyLock bool
	lastHealth      float64
	lastThreat      string
	lastPos         Vec3
	wg              sync.WaitGroup

	tasksCompletedSinceReplan int
	actionFailures            map[string]int // Loop protection tracking
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
		planner:        planner,
		routine:        routine,
		exec:           exec,
		memory:         mem,
		eventStore:     eventStore,
		strategy:       NewStrategyManager(),
		learning:       NewLearningSystem(),
		telemetry:      tel,
		uiHub:          uiHub,
		logger:         baseLogger.With(slog.String("session_id", sessionID)),
		eventCh:        make(chan EngineEvent, 100),
		queue:          NewTaskQueue(),
		planEpoch:      0,
		lastHealth:     20.0,
		sessionID:      sessionID,
		actionFailures: make(map[string]int),
	}
}

func (e *Engine) Run(ctx context.Context, conn *websocket.Conn) {
	runCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		e.wg.Wait()
		close(e.eventCh)
		_ = e.exec.Close()
	}()

	e.wg.Add(1)
	go e.loop(runCtx)

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
				state.POIs = e.learning.PenalizePOIs(state.POIs)
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
}

func (e *Engine) loop(ctx context.Context) {
	defer e.wg.Done()
	defer e.cancelPlanning()

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
	case EventProcessNext:
		e.processNextTask()
	}
}

func (e *Engine) stateChangedSignificantly(old, new StateSnapshot) bool {
	return old.InventoryHash != new.InventoryHash || math.Abs(old.Health-new.Health) > 3 || old.HasThreat != new.HasThreat
}

func (e *Engine) handleStateUpdate(ctx context.Context, ev EventClientState) {
	e.uiHub.Broadcast(map[string]interface{}{"type": "state_update", "payload": ev.State})

	e.lastState = ev.State
	e.lastPos = ev.State.Position
	topThreat := ""
	if len(ev.State.Threats) > 0 {
		topThreat = ev.State.Threats[0].Name
	}

	healthDropped := ev.State.Health < e.lastHealth && ev.State.Health < 5
	criticalOverride := healthDropped && time.Since(e.lastReplan) > 15*time.Second

	e.lastHealth = ev.State.Health
	e.lastThreat = topThreat

	for _, poi := range ev.State.POIs {
		if poi.Name == "crafting_table" || poi.Name == "furnace" || strings.Contains(poi.Name, "bed") {
			go e.memory.MarkWorldNode(context.Background(), poi.Name, "structure", poi.Position.X, poi.Position.Y, poi.Position.Z)
		} else if poi.Name == "water" || poi.Name == "lava" {
			go e.memory.MarkWorldNode(context.Background(), poi.Name, "environment", poi.Position.X, poi.Position.Y, poi.Position.Z)
		} else if strings.Contains(poi.Name, "ore") {
			go e.memory.MarkWorldNode(context.Background(), poi.Name, "resource", poi.Position.X, poi.Position.Y, poi.Position.Z)
		}
	}

	strat := e.strategy.Evaluate(ev.State)
	stratChanged := false
	if strat.Goal != e.currentStrategy {
		e.logger.Info("Strategy Shift", slog.String("old", e.currentStrategy), slog.String("new", strat.Goal))
		e.currentStrategy = strat.Goal
		stratChanged = true
		e.planner.ClearMilestone()
		go e.setSummaryAsync("Current Strategy", strat.Goal)
	}

	var currentTask *Action
	if e.inFlightTask != nil {
		taskCopy := *e.inFlightTask
		currentTask = &taskCopy
	}
	lastFail, _ := e.memory.GetSummaryValue(ctx, e.sessionID, "Last Failure")

	snapshot := DebugSnapshot{
		StateSummary: fmt.Sprintf("H:%.1f F:%.1f P:%.0f,%.0f,%.0f T:%s", ev.State.Health, ev.State.Food, e.lastPos.X, e.lastPos.Y, e.lastPos.Z, e.lastThreat),
		CurrentTask:  currentTask,
		QueueLength:  e.queue.Len(),
		LastFailure:  lastFail,
	}
	e.logger.Debug("Tick Snapshot", slog.Any("snapshot", snapshot))

	type kv struct {
		Name  string
		Count int
	}
	sorted := make([]kv, len(ev.State.Inventory))
	for i, item := range ev.State.Inventory {
		sorted[i] = kv{Name: item.Name, Count: item.Count}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	invStr := ""
	for _, item := range sorted {
		invStr += fmt.Sprintf("%s:%d,", item.Name, item.Count)
	}

	currentSnapshot := StateSnapshot{
		InventoryHash: invStr,
		Health:        ev.State.Health,
		HasThreat:     topThreat != "",
	}

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

	if e.tsEmergencyLock {
		return
	}

	if e.planning {
		if criticalOverride {
			e.cancelPlanning()
		} else {
			return
		}
	}

	isExecutingTactics := false
	if e.inFlightTask != nil && e.inFlightTask.Priority <= PriLLM {
		isExecutingTactics = true
	}

	stateChanged := e.stateChangedSignificantly(e.lastSnapshot, currentSnapshot)
	timeSinceReplan := time.Since(e.lastReplan)

	needsReplan := e.lastReplan.IsZero() || criticalOverride || e.systemOverride != "" || stateChanged || stratChanged || e.planner.GetActiveMilestone() == nil

	if isExecutingTactics && !criticalOverride && !stratChanged {
		if e.systemOverride == "" {
			return
		}
	}

	if criticalOverride && isExecutingTactics {
		e.resetExecutionState()
		go e.exec.SendControl("noop", "critical state interrupt")
	}

	if !needsReplan {
		return
	}

	if timeSinceReplan < 5*time.Second && !criticalOverride && !stratChanged && e.systemOverride == "" && e.inFlightTask != nil {
		return
	}

	e.lastSnapshot = currentSnapshot
	epochAtStart := e.planEpoch

	learnedRules := e.learning.GetRules()
	sysOverride := fmt.Sprintf("CURRENT OVERARCHING STRATEGY: %s\n%s\nAll milestones and tasks MUST align with this strategy.\n\n%s", e.currentStrategy, learnedRules, e.systemOverride)
	e.systemOverride = ""

	updatedPayload, _ := json.Marshal(ev.State)

	planCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	e.planCancel = cancel
	e.planning = true
	e.lastReplan = time.Now()

	e.telemetry.RecordReplan()
	traceID := fmt.Sprintf("trace-%d", time.Now().UnixNano())

	go func() {
		plan, err := e.planner.GeneratePlan(planCtx, updatedPayload, e.sessionID, sysOverride)
		if err != nil {
			if planCtx.Err() != context.Canceled {
				e.eventCh <- EventPlanError{Epoch: epochAtStart, Error: err}
			}
			return
		}
		e.eventCh <- EventPlanReady{Epoch: epochAtStart, TraceID: traceID, Plan: plan}
	}()
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

	if ev.Plan != nil && bool(ev.Plan.MilestoneComplete) && len(ev.Plan.Tasks) == 0 {
		e.lastReplan = time.Time{}
		return
	}

	if ev.Plan == nil || len(ev.Plan.Tasks) == 0 {
		go e.exec.SendControl("noop", "No actionable tasks generated")
		return
	}

	// Phase 3: The Plan Validation Gate
	planRes := ValidatePlan(ev.Plan.Tasks, e.lastState)
	if !planRes.Valid {
		e.logger.Warn("Plan validation failed pre-flight", slog.String("reason", planRes.Reason))
		e.systemOverride = fmt.Sprintf("CRITICAL OVERRIDE: Plan rejected. %s. %s", planRes.Reason, planRes.FixHint)
		e.lastReplan = time.Time{} // Force immediate replan
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
	e.cancelPlanning()

	if e.planEpoch != ev.Epoch {
		return
	}

	e.eventStore.Append(ctx, e.sessionID, "", "TacticalPlanFailed", map[string]interface{}{
		"error": ev.Error.Error(),
	})
	e.logger.Error("Planning failed", slog.Any("error", ev.Error))
	go e.exec.SendControl("planning_error", "Failed to generate valid plan")

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

	targetName := "none"
	if e.inFlightTask != nil {
		meta.TraceID = e.inFlightTask.Trace.TraceID
		targetName = e.inFlightTask.Target.Name
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

		e.systemOverride = "CRITICAL: Bot just died. Inventory is RESET. Treat the current inventory as ground truth and restart basic progression: gather wood first. Do NOT assume any tools exist."
		e.lastReplan = time.Time{}
		e.tsEmergencyLock = false

		meta.Status = string(StatusFailed)
		go e.memory.LogEvent("death", "Died due to: "+ev.Cause, meta)
		go e.memory.MarkWorldNode(context.Background(), "last_death", "event", e.lastPos.X, e.lastPos.Y, e.lastPos.Z)

		e.eventStore.Append(ctx, e.sessionID, "", "Death", map[string]interface{}{
			"position": e.lastPos,
			"cause":    ev.Cause,
		})
		e.logger.Warn("Bot died — milestone cleared", slog.String("cause", ev.Cause))

	case "panic_retreat_start":
		e.telemetry.RecordPanic()
		e.resetExecutionState()
		e.tsEmergencyLock = true

		meta.Status = string(StatusPanic)
		go e.memory.LogEvent("evasion", "Fled from threat: "+ev.Cause, meta)
		e.logger.Warn("Emergency Lock ENGAGED", append(logCtx, slog.String("cause", ev.Cause))...)

	case "panic_retreat_end":
		e.tsEmergencyLock = false
		e.lastReplan = time.Time{}
		e.logger.Info("Emergency Lock RELEASED", logCtx...)

	case "task_completed":
		if !e.matchesInFlight(ev.CommandID) {
			return
		}

		// Reset failure memory on success
		delete(e.actionFailures, ev.Action+"_"+targetName)

		e.learning.RecordOutcome(ev.Action, targetName, "COMPLETED", "")

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
			failCause = string(CauseUnknown)
		}

		e.learning.RecordOutcome(ev.Action, targetName, string(status), failCause)

		e.telemetry.RecordTaskStatus(status)
		meta.Status = string(status)
		go e.memory.LogEvent(ev.Action, "Task "+string(status)+": "+failCause, meta)
		e.logger.Warn("Task incomplete", append(logCtx, slog.String("event", ev.Event), slog.String("cause", failCause))...)

		if e.inFlightTask != nil && e.inFlightTask.Source == string(SourceRoutine) {
			e.routine.RecordFailure(e.inFlightTask.Action, e.inFlightTask.Target.Name)
		}

		e.resetExecutionState()

		if !e.tsEmergencyLock {
			e.lastReplan = time.Now()
		}

		if status == StatusFailed {
			if e.tasksCompletedSinceReplan == 0 {
				e.planner.RecordStall()
			}
		}

		e.tasksCompletedSinceReplan = 0

		var advice string
		switch FailureCause(failCause) {
		case CauseNoBlocks:
			advice = "No reachable blocks or entities found nearby. Use 'explore' first."
		case CauseStuck:
			advice = "Bot got physically stuck. Use 'explore' to escape the area."
		case CausePathFailed:
			advice = "Pathfinding failed. Target might be unreachable. Try a different target or explore."
		case CauseTimeout:
			advice = "Task timed out. Simplify the next step or ensure the target is closer."
		default:
			advice = "Adjust your plan accordingly."
		}

		// Phase 3: Loop Protection Counter
		failureKey := ev.Action + "_" + targetName
		e.actionFailures[failureKey]++

		if e.actionFailures[failureKey] >= 3 {
			advice = "CRITICAL: YOU HAVE FAILED THIS EXACT TASK 3 TIMES. ABORT THIS APPROACH COMPLETELY. Choose a different target or clear the area."
			e.actionFailures[failureKey] = 0 // reset so we don't permalock
			e.planner.ClearMilestone()       // Force higher level reset
		}

		e.systemOverride = fmt.Sprintf("CRITICAL OVERRIDE: Task '%s' %s. Cause: %s. %s", ev.Action, string(status), failCause, advice)
		go e.setSummaryAsync("Last Failure", ev.Action+" ("+ev.Event+"): "+failCause)
	}
}

func (e *Engine) processNextTask() {
	if e.inFlightTask != nil || e.queue.Len() == 0 {
		return
	}

	e.inFlightTask = e.queue.Pop()
	task := e.inFlightTask

	// Phase 3: The Atomic Validation Gate with Severity
	res := ValidateAction(*task, e.lastState)
	if !res.Valid {
		switch res.Severity {
		case SeverityAdvisory:
			e.logger.Info("Task skipped (Advisory)", slog.String("reason", res.Reason))
			e.inFlightTask = nil
			go func() { e.eventCh <- EventProcessNext{} }()
			return
		case SeverityBlocking:
			e.logger.Warn("Task blocked", slog.String("reason", res.Reason))
			e.systemOverride = fmt.Sprintf("Task '%s' blocked: %s. %s", task.Action, res.Reason, res.FixHint)
			e.resetExecutionState()
			return
		case SeverityCritical:
			e.logger.Error("Task critical failure", slog.String("reason", res.Reason))
			e.systemOverride = fmt.Sprintf("CRITICAL FAILURE on '%s': %s. YOU MUST CHANGE STRATEGY. %s", task.Action, res.Reason, res.FixHint)
			e.planner.ClearMilestone()
			e.resetExecutionState()
			return
		}
	}

	if task.Action == string(ActionMarkLocation) {
		locName := task.Target.Name
		e.wg.Add(1)
		go func(name string, x, y, z float64, tID string) {
			defer e.wg.Done()
			err := e.memory.MarkWorldNode(context.Background(), name, "user_marked", x, y, z)
			if err == nil {
				msg := fmt.Sprintf("Marked %s at X:%.1f, Y:%.1f, Z:%.1f", name, x, y, z)
				e.memory.LogEvent("world_model", msg, EventMeta{SessionID: e.sessionID, TraceID: tID, X: x, Y: y, Z: z, Status: "COMPLETED"})
				e.logger.Info("World node mapped", slog.String("name", name))
			}
			e.eventCh <- EventClientAction{Event: "task_completed", Action: string(ActionMarkLocation), CommandID: task.ID}
		}(locName, e.lastPos.X, e.lastPos.Y, e.lastPos.Z, task.Trace.TraceID)
		return
	}

	if task.Action == string(ActionRecallLocation) {
		locName := task.Target.Name
		e.wg.Add(1)
		go func(name string, tID string) {
			defer e.wg.Done()
			node, err := e.memory.GetNode(context.Background(), name)
			if err == nil {
				msg := fmt.Sprintf("Recalled %s at X:%.1f, Y:%.1f, Z:%.1f", name, node.X, node.Y, node.Z)
				e.memory.LogEvent("world_model", msg, EventMeta{SessionID: e.sessionID, TraceID: tID, Status: "COMPLETED"})
				e.setSummaryAsync("Target Node: "+name, msg)
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
	e.queue.ClearBySource(SourceLLM)
	e.inFlightTask = nil
	e.lastReplan = time.Time{}
	e.cancelPlanning()
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

func (e *Engine) setSummaryAsync(key, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.memory.SetSummary(ctx, e.sessionID, key, value)
}
