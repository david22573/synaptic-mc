package engine

import (
	"sort"
)

type TaskSource string

const (
	SourceLLM     TaskSource = "llm"
	SourceRoutine TaskSource = "routine"
	SourceReflex  TaskSource = "reflex"
)

// TaskQueue manages the prioritization and lifecycle of pending actions.
// It is NOT thread-safe by design — it must only be mutated by the Engine's event loop.
type TaskQueue struct {
	items []Action
}

func NewTaskQueue() *TaskQueue {
	return &TaskQueue{
		items: make([]Action, 0, 10),
	}
}

// Push adds one or more tasks to the queue and re-sorts by priority.
// Lower Priority int means higher urgency (e.g., PriRoutine = 1, PriLLM = 2).
func (q *TaskQueue) Push(tasks ...Action) {
	q.items = append(q.items, tasks...)
	sort.SliceStable(q.items, func(i, j int) bool {
		return q.items[i].Priority < q.items[j].Priority
	})
}

// Pop removes and returns the highest priority task.
// Returns nil if empty.
func (q *TaskQueue) Pop() *Action {
	if len(q.items) == 0 {
		return nil
	}
	task := q.items[0]
	q.items = q.items[1:]
	return &task
}

// Peek returns the highest priority task without removing it.
func (q *TaskQueue) Peek() *Action {
	if len(q.items) == 0 {
		return nil
	}
	return &q.items[0]
}

// ClearBySource removes all tasks matching the specified source.
func (q *TaskQueue) ClearBySource(source TaskSource) {
	writeIndex := 0
	for _, task := range q.items {
		if task.Source != string(source) {
			q.items[writeIndex] = task
			writeIndex++
		}
	}

	// Zero out the remaining elements to prevent memory leaks
	for i := writeIndex; i < len(q.items); i++ {
		q.items[i] = Action{}
	}

	// Truncate the slice
	q.items = q.items[:writeIndex]
}

// HasRoutineTarget checks if a routine is already queued for a specific target to prevent duplicates.
func (q *TaskQueue) HasRoutineTarget(action, targetName string) bool {
	for _, t := range q.items {
		if t.Source == string(SourceRoutine) && t.Action == action && t.Target.Name == targetName {
			return true
		}
	}
	return false
}

func (q *TaskQueue) Len() int {
	return len(q.items)
}

// Snapshot returns a copy of the current queue state.
// This prevents routines from holding or mutating the engine's internal slice.
func (q *TaskQueue) Snapshot() []Action {
	snap := make([]Action, len(q.items))
	copy(snap, q.items)
	return snap
}
