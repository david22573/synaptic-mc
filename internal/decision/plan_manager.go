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
	return pm.current
}

func (pm *PlanManager) SetPlan(plan *domain.Plan) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Enforce initial state on injection
	plan.Status = domain.PlanStatusPending
	pm.current = plan
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
		valid = (to == domain.PlanStatusActive || to == domain.PlanStatusInvalidated)
	case domain.PlanStatusActive:
		valid = (to == domain.PlanStatusCompleted || to == domain.PlanStatusFailed || to == domain.PlanStatusInvalidated)
	// Terminal states cannot transition further
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
	return status == domain.PlanStatusPending || status == domain.PlanStatusActive
}
