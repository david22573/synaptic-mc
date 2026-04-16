// internal/domain/world.go
package domain

import (
	"fmt"
	"strings"
	"sync"
)

// Location represents standard 3D coordinates (adjust to match your existing types)
type Location struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type WorldNode struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Kind  string  `json:"kind"` // cave, village, tree_cluster, furnace_base, safe_base, resource_zone, danger_area
	Pos   Vec3    `json:"pos"`
	Score float64 `json:"score"`
}

type Edge struct {
	FromID string  `json:"from_id"`
	ToID   string  `json:"to_id"`
	Cost   float64 `json:"cost"` // e.g., representing travel route difficulty
	Risk   float64 `json:"risk"`
}

type Region struct {
	Name  string   `json:"name"`
	Nodes []string `json:"nodes"`
}

// WorldModel acts as the bot's short-term spatial and tactical memory.
type WorldModel struct {
	mu sync.RWMutex

	// ZonePenalties maps a spatial chunk (e.g., "x,z") to a cost multiplier.
	ZonePenalties map[string]float64

	// ActionWeights tracks the success rate of specific actions.
	ActionWeights map[string]float64

	// Phase 6: World Model Memory
	LastThreats        []ThreatInfo
	SafeZones          []WorldNode
	RecentDamageSource string
	FailedPaths        []Location
}

func NewWorldModel() *WorldModel {
	return &WorldModel{
		ZonePenalties: make(map[string]float64),
		ActionWeights: make(map[string]float64),
		LastThreats:   make([]ThreatInfo, 0),
		SafeZones:     make([]WorldNode, 0),
		FailedPaths:   make([]Location, 0),
	}
}

// RecordSuccess boosts the confidence of an action.
func (w *WorldModel) RecordSuccess(action string, target any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// M-5: Exponential decay 0.95 + add 1.0, clamp to [-5.0, 5.0]
	current := w.ActionWeights[action]
	val := current*0.95 + 1.0
	if val > 5.0 {
		val = 5.0
	} else if val < -5.0 {
		val = -5.0
	}
	w.ActionWeights[action] = val
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

	// M-5: Exponential decay 0.95 - penalty, clamp to [-5.0, 5.0]
	current := w.ActionWeights[action]
	val := current*0.95 - penalty
	if val > 5.0 {
		val = 5.0
	} else if val < -5.0 {
		val = -5.0
	}
	w.ActionWeights[action] = val
}

// GetZoneCost lets the planner check if a target location is worth trying.
func (w *WorldModel) GetZoneCost(loc Location) float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	zone := getZoneKey(loc)
	return w.ZonePenalties[zone]
}

// GetActionWeight returns the confidence score for a specific action.
func (w *WorldModel) GetActionWeight(action string) float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ActionWeights[action]
}

// GetTacticalFeedback returns a summary of penalized actions and zones for the LLM.
func (w *WorldModel) GetTacticalFeedback() string {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var feedback []string

	// Report significantly failing actions
	for action, weight := range w.ActionWeights {
		if weight < -0.2 {
			feedback = append(feedback, fmt.Sprintf("Action '%s' is failing recently (confidence: %.2f)", action, weight))
		}
	}

	// Report dangerous zones
	for zone, penalty := range w.ZonePenalties {
		if penalty > 1.0 {
			feedback = append(feedback, fmt.Sprintf("Zone %s is dangerous or blocked (penalty: %.1f)", zone, penalty))
		}
	}

	if len(feedback) == 0 {
		return ""
	}

	return "TACTICAL FEEDBACK (Avoid these failures):\n- " + strings.Join(feedback, "\n- ")
}

// getZoneKey buckets exact coordinates into ~16x16 chunk zones (Minecraft standard)
func getZoneKey(loc Location) string {
	chunkX := int(loc.X) >> 4
	chunkZ := int(loc.Z) >> 4
	return fmt.Sprintf("%d,%d", chunkX, chunkZ)
}
