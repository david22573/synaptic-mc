package state

import (
	"encoding/json"
	"fmt"

	"david22573/synaptic-mc/internal/domain"
)

// Reduce applies a domain event to the current state to produce a new authoritative state.
// This centralizes all world memory merging and derived state logic.
func Reduce(current domain.GameState, event domain.DomainEvent) domain.GameState {
	switch event.Type {
	case domain.EventTypeStateTick:
		var newState domain.GameState
		if err := json.Unmarshal(event.Payload, &newState); err != nil {
			return current
		}

		newState.Initialized = true

		// Track chunks visited from heartbeat
		newState.RecordChunkVisit(int(newState.Position.X)>>4, int(newState.Position.Z)>>4)

		// Preserve and merge long-lived world memory across ticks.
		newState.DangerZones = mergeDangerZones(current.DangerZones, newState.DangerZones)
		newState.VisitedChunks = mergeVisitedChunks(current.VisitedChunks, newState.VisitedChunks)
		newState.TerrainRoughness = mergeTerrainRoughness(current.TerrainRoughness, newState.TerrainRoughness)

		return newState

	case domain.EventTypeTaskEnd:
		var result struct {
			Success  bool    `json:"success"`
			Cause    string  `json:"cause"`
			Progress float64 `json:"progress"`
		}
		if err := json.Unmarshal(event.Payload, &result); err != nil {
			return current
		}

		// If we got stuck or pathfinding failed, permanently mark that area as bad for the planner
		if !result.Success && (result.Cause == domain.CauseBlocked || result.Cause == domain.CauseStuck) {
			// Copy slice to avoid race condition during mutation
			next := current
			next.DangerZones = append([]domain.DangerZone{}, current.DangerZones...)
			next.VisitedChunks = append([]domain.ChunkCoord{}, current.VisitedChunks...)

			next.MarkAreaRisky(next.Position, result.Cause, 0.85)
			return next
		}
	}

	return current
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
