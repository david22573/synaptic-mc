// internal/domain/world.go
package domain

import (
	"fmt"
	"sync"
)

// Location represents standard 3D coordinates (adjust to match your existing types)
type Location struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// WorldModel acts as the bot's short-term spatial and tactical memory.
type WorldModel struct {
	mu sync.RWMutex

	// ZonePenalties maps a spatial chunk (e.g., "x,z") to a cost multiplier.
	// High cost = don't path here, it's a death trap or physically blocked.
	ZonePenalties map[string]float64

	// ActionWeights tracks the success rate of specific actions.
	// Useful if the bot needs to learn that hunting is currently failing.
	ActionWeights map[string]float64
}

func NewWorldModel() *WorldModel {
	return &WorldModel{
		ZonePenalties: make(map[string]float64),
		ActionWeights: make(map[string]float64),
	}
}

// RecordSuccess boosts the confidence of an action.
func (w *WorldModel) RecordSuccess(action string, target any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Exponential moving average to gradually favor successful actions
	current := w.ActionWeights[action]
	w.ActionWeights[action] = current*0.8 + 1.0*0.2
}

// RewardPath reduces the penalty of a zone if we managed to make progress through it.
func (w *WorldModel) RewardPath(loc Location, progress float64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	zone := getZoneKey(loc)
	if val, exists := w.ZonePenalties[zone]; exists {
		// Reduce penalty based on how far we got
		w.ZonePenalties[zone] = val * (1.0 - (progress * 0.5))
	}
}

// PenalizeZone marks a 16x16 chunk as dangerous or unnavigable.
func (w *WorldModel) PenalizeZone(loc Location, penalty float64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	zone := getZoneKey(loc)
	w.ZonePenalties[zone] += penalty
}

// PenalizeAction drops the confidence of an action to trigger fallbacks.
func (w *WorldModel) PenalizeAction(action string, penalty float64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	current := w.ActionWeights[action]
	w.ActionWeights[action] = current*0.8 - (penalty * 0.2)
}

// GetZoneCost lets the planner check if a target location is worth trying.
func (w *WorldModel) GetZoneCost(loc Location) float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	zone := getZoneKey(loc)
	return w.ZonePenalties[zone]
}

// getZoneKey buckets exact coordinates into ~16x16 chunk zones (Minecraft standard)
func getZoneKey(loc Location) string {
	chunkX := int(loc.X) >> 4
	chunkZ := int(loc.Z) >> 4
	return fmt.Sprintf("%d,%d", chunkX, chunkZ)
}
