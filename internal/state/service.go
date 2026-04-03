package state

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type Service struct {
	bus          domain.EventBus
	logger       *slog.Logger
	stateVersion atomic.Uint64
	currentState atomic.Pointer[domain.VersionedState]
}

func NewService(bus domain.EventBus, logger *slog.Logger) *Service {
	s := &Service{
		bus:    bus,
		logger: logger.With(slog.String("component", "state_service")),
	}

	// Initialize with an empty state so loads never panic
	s.currentState.Store(&domain.VersionedState{})

	// Wire up the bus listeners
	bus.Subscribe(domain.EventTypeStateTick, domain.FuncHandler(s.handleStateTick))

	// Catch UI-driven updates if they come through as STATE_UPDATE
	bus.Subscribe(domain.EventType("STATE_UPDATE"), domain.FuncHandler(s.handleStateTick))

	return s
}

func (s *Service) GetCurrentState() domain.VersionedState {
	if ptr := s.currentState.Load(); ptr != nil {
		return *ptr
	}
	return domain.VersionedState{}
}

func (s *Service) handleStateTick(ctx context.Context, event domain.DomainEvent) {
	var newState domain.GameState
	if err := json.Unmarshal(event.Payload, &newState); err != nil {
		s.logger.Error("Failed to unmarshal state payload", slog.Any("error", err))
		return
	}

	vState := domain.VersionedState{
		Version: s.stateVersion.Add(1),
		State:   newState,
	}

	s.currentState.Store(&vState)

	payload, _ := json.Marshal(vState)

	// Emit the normalized internal event so the rest of the system can react
	s.bus.Publish(ctx, domain.DomainEvent{
		SessionID: event.SessionID,
		Trace:     event.Trace,
		Type:      domain.EventTypeStateUpdated,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
}
