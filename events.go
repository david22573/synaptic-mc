package main

import "encoding/json"

// EngineEvent is the base interface for all messages routed to the Engine's single-threaded loop.
type EngineEvent interface {
	isEngineEvent()
}

// EventClientState represents a telemetry tick from the TS bot.
type EventClientState struct {
	State      GameState
	RawPayload json.RawMessage
}

func (e EventClientState) isEngineEvent() {}

// EventClientAction represents a task lifecycle update (completed, failed, aborted).
type EventClientAction struct {
	Event     string // "task_completed", "task_failed", "death", "panic_retreat"
	Action    string
	CommandID string
	Cause     string
	Duration  int
}

func (e EventClientAction) isEngineEvent() {}

// EventPlanReady represents a successfully generated tactical plan from the LLM goroutine.
type EventPlanReady struct {
	Epoch   int
	TraceID string
	Plan    *LLMPlan
}

func (e EventPlanReady) isEngineEvent() {}

// EventPlanError represents a failure in the LLM planning goroutine.
type EventPlanError struct {
	Epoch int
	Error error
}

func (e EventPlanError) isEngineEvent() {}

// EventMilestoneReady represents a successfully generated milestone plan.
type EventMilestoneReady struct {
	Epoch     int
	Milestone *MilestonePlan
}

func (e EventMilestoneReady) isEngineEvent() {}

// EventMilestoneError represents a failure in the milestone generation goroutine.
type EventMilestoneError struct {
	Epoch int
	Error error
}

func (e EventMilestoneError) isEngineEvent() {}
