package execution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"david22573/synaptic-mc/internal/domain"
)

var ErrDraining = errors.New("controller is draining")

type VersionedController struct {
	ID       string
	Ctrl     Controller
	draining atomic.Bool
	active   sync.WaitGroup
}

// ControllerManager orchestrates graceful swaps between bot connections
// preventing in-flight task corruption and ghost execution.
type ControllerManager struct {
	current atomic.Pointer[VersionedController]
	mu      sync.Mutex
}

func NewControllerManager() *ControllerManager {
	return &ControllerManager{}
}

func (m *ControllerManager) SetController(id string, ctrl Controller) {
	m.mu.Lock()
	defer m.mu.Unlock()

	old := m.current.Load()
	if old != nil {
		old.draining.Store(true)
		// Drain in-flight dispatches/aborts before swapping
		old.active.Wait()
		_ = old.Ctrl.Close()
	}

	vc := &VersionedController{
		ID:   id,
		Ctrl: ctrl,
	}
	m.current.Store(vc)
}

func (m *ControllerManager) Dispatch(ctx context.Context, action domain.Action) error {
	vc := m.current.Load()
	if vc == nil {
		return errors.New("no active controller")
	}
	if vc.draining.Load() {
		return ErrDraining
	}

	vc.active.Add(1)
	defer vc.active.Done()

	// Tag the action to prevent ghost execution execution loops
	action.ControllerID = vc.ID
	return vc.Ctrl.Dispatch(ctx, action)
}

func (m *ControllerManager) AbortCurrent(ctx context.Context, reason string) error {
	vc := m.current.Load()
	if vc == nil {
		return nil
	}

	vc.active.Add(1)
	defer vc.active.Done()

	return vc.Ctrl.AbortCurrent(ctx, reason)
}

func (m *ControllerManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vc := m.current.Load()
	if vc != nil {
		vc.draining.Store(true)
		vc.active.Wait()
		return vc.Ctrl.Close()
	}
	return nil
}
