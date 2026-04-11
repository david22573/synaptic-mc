package decision

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

var (
	ErrInvalidTransition = errors.New("invalid plan state transition")
	ErrNoActivePlan      = errors.New("no active plan")
)

type PlanManager struct {
	current *domain.Plan
	mu      sync.RWMutex
}

func NewPlanManager() *PlanManager {
	return &PlanManager{}
}

func (pm *PlanManager) GetCurrent() *domain.Plan {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	return clonePlan(pm.current)
}

func clonePlan(src *domain.Plan) *domain.Plan {
	if src == nil {
		return nil
	}

	// Shallow copy top-level fields
	dst := *src

	// Deep copy slices
	dst.Tasks = cloneActions(src.Tasks)
	dst.Fallbacks = make([][]domain.Action, len(src.Fallbacks))
	for i, fb := range src.Fallbacks {
		dst.Fallbacks[i] = cloneActions(fb)
	}

	// Deep copy pointer fields
	if src.InvalidatedAt != nil {
		t := *src.InvalidatedAt
		dst.InvalidatedAt = &t
	}

	return &dst
}

func cloneActions(src []domain.Action) []domain.Action {
	if src == nil {
		return nil
	}
	dst := make([]domain.Action, len(src))
	for i, a := range src {
		// Explicit deep copy to prevent data races if pointers are added later
		dst[i] = domain.Action{
			ID:           a.ID,
			ControllerID: a.ControllerID,
			Source:       a.Source,
			Trace: domain.TraceContext{
				TraceID:     a.Trace.TraceID,
				ActionID:    a.Trace.ActionID,
				MilestoneID: a.Trace.MilestoneID,
			},
			Type:   a.Type,
			Action: a.Action,
			Target: domain.Target{
				Type: a.Target.Type,
				Name: a.Target.Name,
			},
			Count:     a.Count,
			Rationale: a.Rationale,
			Priority:  a.Priority,
			Timeout:   a.Timeout,
		}
	}
	return dst
}

func (pm *PlanManager) SetPlan(plan *domain.Plan) {
	if plan == nil {
		return
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Perform deep copy before storing to prevent outside mutation
	newPlan := clonePlan(plan)
	newPlan.Status = domain.PlanStatusPending
	pm.current = newPlan
}

// FIX: Safe, locked mutation for task progression
func (pm *PlanManager) PopTask(taskID string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.current == nil || len(pm.current.Tasks) == 0 {
		return false
	}

	if pm.current.Tasks[0].ID != taskID {
		return false // stale event, don't advance
	}

	pm.current.Tasks = pm.current.Tasks[1:]
	return len(pm.current.Tasks) > 0
}

func (pm *PlanManager) NextFallback() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.current == nil || len(pm.current.Fallbacks) == 0 {
		return false
	}

	// Promote the first fallback candidate to the main task list
	pm.current.Tasks = pm.current.Fallbacks[0]
	// Remove the promoted candidate from the fallbacks slice
	pm.current.Fallbacks = pm.current.Fallbacks[1:]
	pm.current.Status = domain.PlanStatusPending
	return true
}

func (pm *PlanManager) Transition(to domain.PlanStatus) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.current == nil {
		return ErrNoActivePlan
	}

	from := pm.current.Status
	valid := false

	switch from {
	case domain.PlanStatusPending:
		valid = (to == domain.PlanStatusActive || to == domain.PlanStatusInvalidated || to == domain.PlanStatusBlocked)
	case domain.PlanStatusActive:
		valid = (to == domain.PlanStatusCompleted || to == domain.PlanStatusFailed || to == domain.PlanStatusInvalidated || to == domain.PlanStatusBlocked)
	case domain.PlanStatusBlocked:
		valid = (to == domain.PlanStatusActive || to == domain.PlanStatusInvalidated || to == domain.PlanStatusFailed)
	case domain.PlanStatusCompleted, domain.PlanStatusFailed, domain.PlanStatusInvalidated:
		valid = false
	}

	if !valid {
		return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidTransition, from, to)
	}

	pm.current.Status = to
	if to == domain.PlanStatusInvalidated {
		now := time.Now()
		pm.current.InvalidatedAt = &now
	}

	return nil
}

func (pm *PlanManager) Clear() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.current = nil
}

func (pm *PlanManager) HasActivePlan() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if pm.current == nil {
		return false
	}

	status := pm.current.Status
	return status == domain.PlanStatusPending || status == domain.PlanStatusActive || status == domain.PlanStatusBlocked
}
