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

	if pm.current == nil {
		return nil
	}

	// FIX: Return a deep copy to prevent slice mutation data races
	cp := *pm.current
	cp.Tasks = make([]domain.Action, len(pm.current.Tasks))
	copy(cp.Tasks, pm.current.Tasks)

	// Clone fallbacks too
	cp.Fallbacks = make([][]domain.Action, len(pm.current.Fallbacks))
	for i, fb := range pm.current.Fallbacks {
		cp.Fallbacks[i] = make([]domain.Action, len(fb))
		copy(cp.Fallbacks[i], fb)
	}

	return &cp
}

func (pm *PlanManager) SetPlan(plan *domain.Plan) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	plan.Status = domain.PlanStatusPending
	pm.current = plan
}

// FIX: Safe, locked mutation for task progression
func (pm *PlanManager) PopTask(taskID string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.current == nil || len(pm.current.Tasks) == 0 {
		return false
	}

	if pm.current.Tasks[0].ID == taskID {
		pm.current.Tasks = pm.current.Tasks[1:]
	}

	return len(pm.current.Tasks) > 0
}

func (pm *PlanManager) NextFallback() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.current == nil || len(pm.current.Fallbacks) == 0 {
		return false
	}

	pm.current.Tasks = pm.current.Fallbacks[0]
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
