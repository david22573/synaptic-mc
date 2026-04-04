// internal/execution/task.go
package execution

import (
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type TaskStatus string

const (
	StatusQueued     TaskStatus = "QUEUED"
	StatusDispatched TaskStatus = "DISPATCHED"
	StatusRunning    TaskStatus = "RUNNING"
	StatusCompleted  TaskStatus = "COMPLETED"
	StatusFailed     TaskStatus = "FAILED"
	StatusTimedOut   TaskStatus = "TIMED_OUT"
	StatusAborted    TaskStatus = "ABORTED"
)

// ExecutionResult closes the loop between Go's cognition and TS's execution.
type ExecutionResult struct {
	Success  bool    `json:"success"`
	Cause    Cause   `json:"cause,omitempty"`
	Progress float64 `json:"progress"` // 0.0 -> 1.0
	Action   string  // e.g., "explore", "mine"
}

type ExecutionTask struct {
	Action      domain.Action    `json:"action"`
	Status      TaskStatus       `json:"status"`
	EnqueueTime time.Time        `json:"enqueue_time"`
	StartTime   *time.Time       `json:"start_time,omitempty"`
	EndTime     *time.Time       `json:"end_time,omitempty"`
	Error       string           `json:"error,omitempty"`
	Result      *ExecutionResult `json:"result,omitempty"`
}

type ActivePlan struct {
	Plan         domain.Plan
	CurrentIndex int
}
