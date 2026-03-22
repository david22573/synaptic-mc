// shared_types.go
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

// TraceContext links Go engine decisions to TS execution logs.
type TraceContext struct {
	TraceID     string `json:"trace_id"`
	ActionID    string `json:"action_id"`
	MilestoneID string `json:"milestone_id,omitempty"`
}

// WSMessage is updated to include trace context.
type WSMessage struct {
	Type    WSMessageType   `json:"type"`
	Trace   TraceContext    `json:"trace"`
	Payload json.RawMessage `json:"payload"`
}
