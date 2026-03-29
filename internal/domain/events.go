package domain

import (
	"context"
	"encoding/json"
	"time"
)

// TraceContext guarantees determinism and observability across the Go/TS boundary.
type TraceContext struct {
	TraceID     string `json:"trace_id"`
	ActionID    string `json:"action_id"`
	MilestoneID string `json:"milestone_id,omitempty"`
}

// EventType strongly types our system's ground truth.
type EventType string

const (
	EventTypeStateTick   EventType = "STATE_TICK"
	EventTypeTaskStart   EventType = "TASK_START"
	EventTypeTaskEnd     EventType = "TASK_END"
	EventTypePlanCreated EventType = "PLAN_CREATED"
	EventTypePanic       EventType = "PANIC_TRIGGERED"
	EventBotDeath        EventType = "BOT_DEATH"
)

// DomainEvent is the immutable record of a state transition.
type DomainEvent struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Trace     TraceContext    `json:"trace"`
	Type      EventType       `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// EventStore is the absolute source of truth. No state mutations happen without an event.
type EventStore interface {
	Append(ctx context.Context, sessionID string, trace TraceContext, eventType EventType, payload any) error
	GetStream(ctx context.Context, sessionID string) ([]DomainEvent, error)
	Close() error
}
