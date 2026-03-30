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
	EventTypeStateTick       EventType = "STATE_TICK"
	EventTypeTaskStart       EventType = "TASK_START"
	EventTypeTaskEnd         EventType = "TASK_END"
	EventTypePlanCreated     EventType = "PLAN_CREATED"
	EventTypePlanInvalidated EventType = "PLAN_INVALIDATED"
	EventTypePlanCompleted   EventType = "PLAN_COMPLETED"
	EventTypePlanFailed      EventType = "PLAN_FAILED"
	EventTypePanic           EventType = "PANIC_TRIGGERED"
	EventTypePanicResolved   EventType = "PANIC_RESOLVED"
	EventBotDeath            EventType = "BOT_DEATH"
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

// SessionSnapshot holds pre-computed projections to prevent heavy event replays.
type SessionSnapshot struct {
	SessionID   string          `json:"session_id"`
	LastEventID int64           `json:"last_event_id"`
	Data        json.RawMessage `json:"data"`
	CreatedAt   time.Time       `json:"created_at"`
}

// EventStore is the absolute source of truth.
type EventStore interface {
	Append(ctx context.Context, sessionID string, trace TraceContext, eventType EventType, payload any) error
	GetStream(ctx context.Context, sessionID string) ([]DomainEvent, error)
	GetRecentStream(ctx context.Context, sessionID string, limit int) ([]DomainEvent, error)
	GetStreamSince(ctx context.Context, sessionID string, sinceID int64) ([]DomainEvent, error)
	SaveSnapshot(ctx context.Context, snap SessionSnapshot) error
	GetLatestSnapshot(ctx context.Context, sessionID string) (*SessionSnapshot, error)
	Close() error
}
