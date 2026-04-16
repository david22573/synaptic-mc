package state

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
)

type Service struct {
	bus          domain.EventBus
	memStore     memory.Store
	logger       *slog.Logger
	stateVersion atomic.Uint64
	currentState atomic.Pointer[domain.VersionedState]
}

func NewService(bus domain.EventBus, memStore memory.Store, logger *slog.Logger) *Service {
	s := &Service{
		bus:      bus,
		memStore: memStore,
		logger:   logger.With(slog.String("component", "state_service")),
	}

	// Initialize with an empty state so loads never panic
	s.currentState.Store(&domain.VersionedState{})

	// Wire up the bus listeners
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

	// World Memory Graph: Persist significant features
	if s.memStore != nil {
		for _, poi := range newState.POIs {
			nodeType := ""
			switch {
			case strings.Contains(strings.ToLower(poi.Name), "village"):
				nodeType = "village"
			case strings.Contains(strings.ToLower(poi.Name), "cave"):
				nodeType = "cave"
			case strings.Contains(strings.ToLower(poi.Name), "chest") || strings.Contains(strings.ToLower(poi.Name), "bed"):
				nodeType = "safe_base"
			}

			if nodeType != "" {
				_ = s.memStore.MarkWorldNode(ctx, poi.Name, nodeType, poi.Position)
			}
		}
	}

	// Preserve and merge long-lived world memory across ticks.
	curr := s.GetCurrentState()
	newState.DangerZones = mergeDangerZones(curr.State.DangerZones, newState.DangerZones)
	newState.VisitedChunks = mergeVisitedChunks(curr.State.VisitedChunks, newState.VisitedChunks)
	newState.TerrainRoughness = mergeTerrainRoughness(curr.State.TerrainRoughness, newState.TerrainRoughness)

	// Instant Reactions: High-priority survival check
	isLava := false
	isFalling := false
	for _, fb := range newState.Feedback {
		cause := strings.ToLower(fb.Cause)
		if strings.Contains(cause, "lava") {
			isLava = true
		}
		if strings.Contains(cause, "fall") {
			isFalling = true
		}
	}
	lowHealth := newState.Health < 8.0
	surroundedCount := 0
	for _, t := range newState.Threats {
		if t.Distance < 4.0 {
			surroundedCount++
		}
	}
	surrounded := surroundedCount >= 3

	if isLava || lowHealth || isFalling || surrounded {
		s.bus.Publish(ctx, domain.DomainEvent{
			SessionID: event.SessionID,
			Type:      domain.EventTypePanic,
			Payload:   []byte(fmt.Sprintf(`{"cause": "survival_critical", "is_lava": %v, "low_health": %v, "is_falling": %v, "is_surrounded": %v}`, isLava, lowHealth, isFalling, surrounded)),
			CreatedAt: time.Now(),
		})
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

func mergeDangerZones(existing, incoming []domain.DangerZone) []domain.DangerZone {
	if len(existing) == 0 {
		return append([]domain.DangerZone{}, incoming...)
	}
	if len(incoming) == 0 {
		return append([]domain.DangerZone{}, existing...)
	}

	merged := make([]domain.DangerZone, 0, len(incoming)+len(existing))
	seen := make(map[string]int, len(incoming)+len(existing))

	add := func(zone domain.DangerZone) {
		key := fmt.Sprintf(
			"%s:%d:%d:%d",
			zone.Reason,
			int(zone.Center.X)/4,
			int(zone.Center.Y)/4,
			int(zone.Center.Z)/4,
		)

		if idx, ok := seen[key]; ok {
			if zone.Risk > merged[idx].Risk {
				merged[idx] = zone
			}
			return
		}

		seen[key] = len(merged)
		merged = append(merged, zone)
	}

	for _, zone := range incoming {
		add(zone)
	}
	for _, zone := range existing {
		add(zone)
	}

	if len(merged) > 64 {
		merged = merged[:64]
	}

	return merged
}

func mergeVisitedChunks(existing, incoming []domain.ChunkCoord) []domain.ChunkCoord {
	if len(existing) == 0 {
		return append([]domain.ChunkCoord{}, incoming...)
	}
	if len(incoming) == 0 {
		return append([]domain.ChunkCoord{}, existing...)
	}

	merged := make([]domain.ChunkCoord, 0, len(existing)+len(incoming))
	seen := make(map[string]struct{}, len(existing)+len(incoming))

	add := func(chunk domain.ChunkCoord) {
		key := fmt.Sprintf("%d,%d", chunk.X, chunk.Z)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		merged = append(merged, chunk)
	}

	for _, chunk := range existing {
		add(chunk)
	}
	for _, chunk := range incoming {
		add(chunk)
	}

	return merged
}

func mergeTerrainRoughness(existing, incoming map[string]float64) map[string]float64 {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}

	merged := make(map[string]float64, len(existing)+len(incoming))
	for key, value := range existing {
		merged[key] = value
	}
	for key, value := range incoming {
		merged[key] = value
	}

	return merged
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
