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
	bus.Subscribe(domain.EventTypeStateTick, domain.FuncHandler(s.handleStateTick))

	// Phase 5: Listen for task completion to alter the world model
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))

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

	newState.Initialized = true

	// Phase 5: Build world model - Track chunks visited from heartbeat
	newState.RecordChunkVisit(int(newState.Position.X)>>4, int(newState.Position.Z)>>4)

	// Preserve existing DangerZones and VisitedChunks across ticks
	curr := s.GetCurrentState()
	newState.DangerZones = curr.State.DangerZones
	if len(curr.State.VisitedChunks) > len(newState.VisitedChunks) {
		newState.VisitedChunks = curr.State.VisitedChunks
	}

	s.saveState(ctx, newState, event)
}

// Phase 5: Mutate state actively based on execution feedback
func (s *Service) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	var result struct {
		Success  bool    `json:"success"`
		Cause    string  `json:"cause"`
		Progress float64 `json:"progress"`
	}
	if err := json.Unmarshal(event.Payload, &result); err != nil {
		return
	}

	// If we got stuck or pathfinding failed, permanently mark that area as bad for the planner
	if !result.Success && (result.Cause == domain.CauseBlocked || result.Cause == domain.CauseStuck) {
		curr := s.GetCurrentState()

		// Copy slice to avoid race condition during mutation
		newState := curr.State
		newState.DangerZones = append([]domain.DangerZone{}, curr.State.DangerZones...)
		newState.VisitedChunks = append([]domain.ChunkCoord{}, curr.State.VisitedChunks...)

		s.logger.Info("Marking area as risky due to execution failure", slog.String("cause", result.Cause))
		newState.MarkAreaRisky(newState.Position, result.Cause, 0.85)

		s.saveState(ctx, newState, event)
	}
}

func (s *Service) saveState(ctx context.Context, newState domain.GameState, triggerEvent domain.DomainEvent) {
	vState := domain.VersionedState{
		Version: s.stateVersion.Add(1),
		State:   newState,
	}

	s.currentState.Store(&vState)

	payload, _ := json.Marshal(vState)

	// Emit the normalized internal event so the rest of the system can react
	s.bus.Publish(ctx, domain.DomainEvent{
		SessionID: triggerEvent.SessionID,
		Trace:     triggerEvent.Trace,
		Type:      domain.EventTypeStateUpdated,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
}
