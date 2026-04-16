package execution

import (
	"context"
	"log/slog"
	"sync"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/state"
)

type ActionArbiter struct {
	mu sync.Mutex

	supervisor *Supervisor
	analyzer   *state.DangerAnalyzer
	logger     *slog.Logger
	
	lastDangerState state.DangerState
}

func NewActionArbiter(supervisor *Supervisor, logger *slog.Logger) *ActionArbiter {
	return &ActionArbiter{
		supervisor: supervisor,
		analyzer:   state.NewDangerAnalyzer(),
		logger:     logger.With(slog.String("component", "action_arbiter")),
	}
}

func (a *ActionArbiter) UpdateDanger(worldState domain.GameState) state.DangerState {
	a.mu.Lock()
	defer a.mu.Unlock()

	newState := a.analyzer.Update(worldState)
	if newState != a.lastDangerState {
		a.logger.Info("Danger state transitioned", 
			slog.String("from", string(a.lastDangerState)), 
			slog.String("to", string(newState)))
		a.lastDangerState = newState
	}
	return newState
}

func (a *ActionArbiter) Request(ctx context.Context, action domain.Action) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Phase 4: Single Writer Action Bus
	// Priority Order enforced by supervisor's AcquireLease
	
	// Survival Hysteresis check
	if a.lastDangerState == state.DangerEscape && GetPriority(action.Action) < 100 {
		a.logger.Debug("Ignoring non-survival request during active ESCAPE", slog.String("action", action.Action))
		return false
	}

	return a.supervisor.Dispatch(ctx, action)
}

func (a *ActionArbiter) HandleTaskEnd(payload domain.TaskEndPayload) {
	a.supervisor.HandleTaskEnd(payload)
}

func (a *ActionArbiter) GetDangerState() state.DangerState {
	return a.analyzer.GetState()
}
