package engine

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
	validator  *Validator
	logger     *slog.Logger

	eventCh chan EngineEvent

	queue        *TaskQueue
	inFlightTask *Action

	planEpoch  int
	planning   bool
	planCancel context.CancelFunc

	sessionID        string
	systemOverride   string
	currentStrategy  string
	isAutonomousMode bool

	lastReplan      time.Time
	lastSnapshot    StateSnapshot
	lastState       GameState
	tsEmergencyLock bool
	lastHealth      float64
	lastThreat      string
	lastPos         Vec3
	wg              sync.WaitGroup

	tasksCompletedSinceReplan int
	actionFailures            map[string]int
}

func NewEngine(
	planner Planner,
	routine RoutineManager,
	exec Executor,
	mem MemoryBank,
	tel *Telemetry,
	uiHub *UIHub,
	learning *LearningSystem,
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
		learning:       learning,
		telemetry:      tel,
		uiHub:          uiHub,
		validator:      NewValidator(),
		logger:         baseLogger.With(slog.String("session_id", sessionID)),
		eventCh:        make(chan EngineEvent, 100),
		queue:          NewTaskQueue(),
		planEpoch:      0,
		lastHealth:     20.0,
		sessionID:      sessionID,
		actionFailures: make(map[string]int),
	}
}

// safeGo wraps a goroutine in the engine's WaitGroup for lifecycle tracking.
func (e *Engine) safeGo(fn func()) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		fn()
	}()
}

// safeSendEvent ensures we don't block indefinitely on a full channel
// if the engine is shutting down, preventing wait group deadlocks.
func (e *Engine) safeSendEvent(ctx context.Context, ev EngineEvent) {
	select {
	case <-ctx.Done():
	case e.eventCh <- ev:
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

	e.safeGo(func() {
		e.loop(runCtx)
	})

	e.safeGo(func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_ = e.memory.ConsolidateSession(runCtx, e.sessionID)
			}
		}
	})

	for {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

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
				e.safeSendEvent(runCtx, EventClientState{State: state, RawPayload: msg.Payload})
			}
		case "event":
			var act EventClientAction
			if err := json.Unmarshal(msg.Payload, &act); err == nil {
				e.safeSendEvent(runCtx, act)
				e.uiHub.Broadcast(map[string]interface{}{"type": "event_stream", "payload": act})
			}
		default:
			e.logger.Warn("Ignoring unknown message type", slog.String("type", msg.Type))
		}
	}
}

func (e *Engine) loop(ctx context.Context) {
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
		e.processNextTask(ctx)
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

	e.lastHealth = ev.State.Health
	e.lastThreat = topThreat

	for _, poi := range ev.State.POIs {
		if poi.Name == "crafting_table" || poi.Name == "furnace" || strings.Contains(poi.Name, "bed") {
			poiName, pX, pY, pZ := poi.Name, poi.Position.X, poi.Position.Y, poi.Position.Z
			e.safeGo(func() { e.memory.MarkWorldNode(context.Background(), poiName, "structure", pX, pY, pZ) })
		} else if poi.Name == "water" || poi.Name == "lava" {
			poiName, pX, pY, pZ := poi.Name, poi.Position.X, poi.Position.Y, poi.Position.Z
			e.safeGo(func() { e.memory.MarkWorldNode(context.Background(), poiName, "environment", pX, pY, pZ) })
		} else if strings.Contains(poi.Name, "ore") {
			poiName, pX, pY, pZ := poi.Name, poi.Position.X, poi.Position.Y, poi.Position.Z
			e.safeGo(func() { e.memory.MarkWorldNode(context.Background(), poiName, "resource", pX, pY, pZ) })
		}
	}

	directive := e.strategy.Evaluate(ev.State, time.Since(e.lastReplan))
	strat := directive.Strategy
	stratChanged := false

	overrideActive := strat.IsAutonomous && e.isAutonomousMode && e.currentStrategy != strat.PrimaryGoal

	if strat.PrimaryGoal != e.currentStrategy && !overrideActive {
		e.logger.Info("Strategy Shift", slog.String("primary", strat.PrimaryGoal), slog.String("secondary", strat.SecondaryGoal))
		e.currentStrategy = strat.PrimaryGoal
		e.isAutonomousMode = strat.IsAutonomous
		stratChanged = true
		e.planner.ClearMilestone()
		e.resetExecutionState()
		e.safeGo(func() { e.setSummary("Current Strategy", strat.PrimaryGoal) })
	} else if !strat.IsAutonomous {
		e.isAutonomousMode = false
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

	newRoutines := e.routine.Evaluate(ev.State, e.inFlightTask, e.queue.Snapshot())
	if len(newRoutines) > 0 {
		e.queue.Push(newRoutines...)

		if e.inFlightTask != nil && e.inFlightTask.Priority > newRoutines[0].Priority {
			e.logger.Info("Routine interrupting in-flight task", slog.String("routine", newRoutines[0].Action))
			e.safeGo(func() { e.exec.SendControl("abort_task", "routine interrupt") })
			e.inFlightTask = nil
			e.lastReplan = time.Time{}
		}

		if e.inFlightTask == nil {
			e.processNextTask(ctx)
		}
	}

	if e.tsEmergencyLock {
		return
	}

	if e.planning {
		if directive.InterruptCurrent {
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
	queueExhausted := e.inFlightTask == nil && e.queue.Len() == 0

	needsReplan := e.lastReplan.IsZero() || directive.TriggerReplan || e.systemOverride != "" || stateChanged || stratChanged || e.planner.GetActiveMilestone() == nil || queueExhausted

	if isExecutingTactics && !directive.TriggerReplan && !stratChanged {
		if e.systemOverride == "" && directive.CriticalOverride == "" {
			return
		}
	}

	if directive.InterruptCurrent && isExecutingTactics {
		e.resetExecutionState()
		e.safeGo(func() { e.exec.SendControl("noop", "critical state interrupt") })
	}

	if !needsReplan {
		return
	}

	if timeSinceReplan < 5*time.Second && !directive.TriggerReplan && !stratChanged && e.systemOverride == "" && directive.CriticalOverride == "" && e.inFlightTask != nil {
		return
	}

	e.lastSnapshot = currentSnapshot
	epochAtStart := e.planEpoch

	primaryForPrompt := strat.PrimaryGoal
	secondaryForPrompt := strat.SecondaryGoal
	if e.isAutonomousMode && e.currentStrategy != strat.PrimaryGoal {
		primaryForPrompt = e.currentStrategy
	}

	learnedRules := e.learning.GetRules()

	finalOverride := e.systemOverride
	if directive.CriticalOverride != "" {
		finalOverride = directive.CriticalOverride + "\n" + finalOverride
	}

	sysOverride := fmt.Sprintf("PRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\n%s\nAll milestones and tasks MUST align with these strategies.\n\n%s", primaryForPrompt, secondaryForPrompt, learnedRules, finalOverride)
	e.systemOverride = ""

	updatedPayload, _ := json.Marshal(ev.State)

	planCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	e.planCancel = cancel
	e.planning = true
	e.lastReplan = time.Now()

	e.telemetry.RecordReplan()
	traceID := fmt.Sprintf("trace-%d", time.Now().UnixNano())

	e.safeGo(func() {
		plan, err := e.planner.GeneratePlan(planCtx, updatedPayload, e.sessionID, sysOverride)
		if err != nil {
			if planCtx.Err() != context.Canceled {
				e.safeSendEvent(ctx, EventPlanError{Epoch: epochAtStart, Error: err})
			}
			return
		}
		e.safeSendEvent(ctx, EventPlanReady{Epoch: epochAtStart, TraceID: traceID, Plan: plan})
	})
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
		e.logger.Warn("LLM plan contained zero actionable tasks. Recording stall.")
		e.planner.RecordStall()
		e.lastReplan = time.Time{}
		e.safeGo(func() { e.exec.SendControl("noop", "No actionable tasks generated") })
		return
	}

	if ev.Plan.ProposedStrategy != "" && e.isAutonomousMode {
		if e.currentStrategy != ev.Plan.ProposedStrategy {
			e.logger.Info("LLM Autonomous Strategy Override", slog.String("new_strategy", ev.Plan.ProposedStrategy))
			e.currentStrategy = ev.Plan.ProposedStrategy
			e.planner.ClearMilestone()
			e.safeGo(func() { e.setSummary("Current Strategy", ev.Plan.ProposedStrategy) })
		}
	}

	planRes := e.validator.ValidateActionChain(ev.Plan.Tasks, e.lastState)
	if !planRes.Valid {
		e.logger.Warn("Plan validation failed pre-flight", slog.String("reason", planRes.Reason))
		e.systemOverride = fmt.Sprintf("CRITICAL OVERRIDE: Plan rejected. %s. %s", planRes.Reason, planRes.FixHint)
		e.lastReplan = time.Time{}
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
	e.safeGo(func() { e.setSummary("Current Objective", ev.Plan.Objective) })

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
	e.processNextTask(ctx)
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
	e.safeGo(func() { e.exec.SendControl("planning_error", "Failed to generate valid plan") })

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
		e.learning.Reset()

		e.systemOverride = "CRITICAL: Bot just died. Inventory is RESET. Treat the current inventory as ground truth and restart basic progression: gather wood first. Do NOT assume any tools exist."
		e.lastReplan = time.Time{}
		e.tsEmergencyLock = false

		meta.Status = string(StatusFailed)
		e.safeGo(func() { e.memory.LogEvent("death", "Died due to: "+ev.Cause, meta) })

		pX, pY, pZ := e.lastPos.X, e.lastPos.Y, e.lastPos.Z
		e.safeGo(func() {
			e.memory.MarkWorldNode(context.Background(), fmt.Sprintf("death_zone_%d", time.Now().Unix()), "hazard", pX, pY, pZ)
		})

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
		e.safeGo(func() { e.memory.LogEvent("evasion", "Fled from threat: "+ev.Cause, meta) })
		e.logger.Warn("Emergency Lock ENGAGED", append(logCtx, slog.String("cause", ev.Cause))...)

	case "panic_retreat_end":
		e.tsEmergencyLock = false
		e.lastReplan = time.Time{}
		e.logger.Info("Emergency Lock RELEASED", logCtx...)

	case "task_completed":
		if !e.matchesInFlight(ev.CommandID) {
			return
		}

		delete(e.actionFailures, ev.Action+"_"+targetName)

		e.learning.RecordOutcome(ev.Action, targetName, "COMPLETED", "")

		e.telemetry.RecordTaskStatus(StatusCompleted)
		meta.Status = string(StatusCompleted)
		e.safeGo(func() { e.memory.LogEvent(ev.Action, "Finished successfully", meta) })
		e.logger.Info("Task completed", logCtx...)
		e.inFlightTask = nil

		e.tasksCompletedSinceReplan++
		e.planner.ResetStall()
		e.processNextTask(ctx)

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
		e.safeGo(func() { e.memory.LogEvent(ev.Action, "Task "+string(status)+": "+failCause, meta) })
		e.logger.Warn("Task incomplete", append(logCtx, slog.String("event", ev.Event), slog.String("cause", failCause))...)

		if e.inFlightTask != nil && e.inFlightTask.Source == string(SourceRoutine) {
			e.routine.RecordFailure(e.inFlightTask.Action, e.inFlightTask.Target.Name)
		}

		e.resetExecutionState()

		if !e.tsEmergencyLock {
			e.lastReplan = time.Now()
		}

		if (status == StatusFailed || status == StatusAborted) && e.tasksCompletedSinceReplan == 0 {
			e.planner.RecordStall()
		}

		e.tasksCompletedSinceReplan = 0

		var advice string
		switch FailureCause(failCause) {
		case CauseNoBlocks:
			advice = "No reachable blocks or entities found nearby. Use 'explore' first."
		case "NO_ENTITY": // Mapped from the CauseNoEntity fix requirement
			advice = "The target entity could not be found, despawned, or moved out of range. Find a new target or explore."
		case CauseStuck:
			advice = "Bot got physically stuck. Use 'explore' to escape the area."
		case CausePathFailed:
			advice = "Pathfinding failed. Target might be unreachable. Try a different target or explore."
		case CauseTimeout:
			advice = "Task timed out. Simplify the next step or ensure the target is closer."
		default:
			advice = "Adjust your plan accordingly."
		}

		failureKey := ev.Action + "_" + targetName
		e.actionFailures[failureKey]++

		if e.actionFailures[failureKey] >= 3 {
			advice = "CRITICAL: YOU HAVE FAILED THIS EXACT TASK 3 TIMES. ABORT THIS APPROACH COMPLETELY. Choose a different target or clear the area."
			e.actionFailures[failureKey] = 0
			e.planner.ClearMilestone()
		}

		e.systemOverride = fmt.Sprintf("CRITICAL OVERRIDE: Task '%s' %s. Cause: %s. %s", ev.Action, string(status), failCause, advice)
		e.safeGo(func() { e.setSummary("Last Failure", ev.Action+" ("+ev.Event+"): "+failCause) })
	}
}

func (e *Engine) processNextTask(ctx context.Context) {
	if e.inFlightTask != nil || e.queue.Len() == 0 {
		return
	}

	e.inFlightTask = e.queue.Pop()
	task := e.inFlightTask

	res := e.validator.ValidateAction(*task, e.lastState)
	if !res.Valid {
		switch res.Severity {
		case SeverityAdvisory:
			e.logger.Info("Task skipped (Advisory)", slog.String("reason", res.Reason))
			e.inFlightTask = nil
			e.safeGo(func() { e.safeSendEvent(ctx, EventProcessNext{}) })
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
		pX, pY, pZ := e.lastPos.X, e.lastPos.Y, e.lastPos.Z
		tID := task.Trace.TraceID
		cID := task.ID
		e.safeGo(func() {
			err := e.memory.MarkWorldNode(context.Background(), locName, "user_marked", pX, pY, pZ)
			if err == nil {
				msg := fmt.Sprintf("Marked %s at X:%.1f, Y:%.1f, Z:%.1f", locName, pX, pY, pZ)
				e.memory.LogEvent("world_model", msg, EventMeta{SessionID: e.sessionID, TraceID: tID, X: pX, Y: pY, Z: pZ, Status: "COMPLETED"})
				e.logger.Info("World node mapped", slog.String("name", locName))
			}
			e.safeSendEvent(ctx, EventClientAction{Event: "task_completed", Action: string(ActionMarkLocation), CommandID: cID})
		})
		return
	}

	if task.Action == string(ActionRecallLocation) {
		locName := task.Target.Name
		tID := task.Trace.TraceID
		cID := task.ID
		e.safeGo(func() {
			node, err := e.memory.GetNode(context.Background(), locName)
			if err == nil {
				msg := fmt.Sprintf("Recalled %s at X:%.1f, Y:%.1f, Z:%.1f", locName, node.X, node.Y, node.Z)
				e.memory.LogEvent("world_model", msg, EventMeta{SessionID: e.sessionID, TraceID: tID, Status: "COMPLETED"})
				e.setSummary("Target Node: "+locName, msg)
			}
			time.Sleep(1 * time.Second)
			e.safeSendEvent(ctx, EventClientAction{Event: "task_completed", Action: string(ActionRecallLocation), CommandID: cID})
		})
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

func (e *Engine) setSummary(key, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.memory.SetSummary(ctx, e.sessionID, key, value)
}
