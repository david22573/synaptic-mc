package main

import "encoding/json"

// ClientEventType defines all events emitted by the TS client.
type ClientEventType string

const (
	EventTaskCompleted     ClientEventType = "task_completed"
	EventTaskFailed        ClientEventType = "task_failed"
	EventTaskAborted       ClientEventType = "task_aborted"
	EventDeath             ClientEventType = "death"
	EventPanicRetreatStart ClientEventType = "panic_retreat_start"
	EventPanicRetreatEnd   ClientEventType = "panic_retreat_end"
)

// TaskStatus unifies execution states.
type TaskStatus string

const (
	StatusRunning   TaskStatus = "RUNNING"
	StatusCompleted TaskStatus = "COMPLETED"
	StatusFailed    TaskStatus = "FAILED"
	StatusAborted   TaskStatus = "ABORTED"
	StatusPanic     TaskStatus = "PANIC"
)

// FailureCause standardizes reasons for task failures across TS and Go
type FailureCause string

const (
	CauseNoBlocks   FailureCause = "NO_BLOCKS"
	CausePathFailed FailureCause = "PATH_FAILED"
	CauseTimeout    FailureCause = "TIMEOUT"
	CauseStuck      FailureCause = "STUCK"
	CauseUnknown    FailureCause = "UNKNOWN"
)

// TraceContext links Go engine decisions to TS execution logs.
type TraceContext struct {
	TraceID     string `json:"trace_id"`
	ActionID    string `json:"action_id"`
	MilestoneID string `json:"milestone_id,omitempty"`
}

// DebugSnapshot captures the engine state at a point in time for observability
type DebugSnapshot struct {
	StateSummary string  `json:"state_summary"`
	CurrentTask  *Action `json:"current_task,omitempty"`
	QueueLength  int     `json:"queue_length"`
	LastFailure  string  `json:"last_failure"`
}

// WSMessage is updated to include trace context.
type WSMessage struct {
	Type    WSMessageType   `json:"type"`
	Trace   TraceContext    `json:"trace"`
	Payload json.RawMessage `json:"payload"`
}

// POI represents a Point of Interest from the perception system with FOV scoring
type POI struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Position   Vec3    `json:"position"`
	Distance   float64 `json:"distance"`
	Visibility float64 `json:"visibility"`
	Score      int     `json:"score"`
	Direction  string  `json:"direction"`
}

// InventoryItem represents an item stack in the bot's inventory
type InventoryItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
