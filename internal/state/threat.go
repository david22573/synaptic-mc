package state

import (
	"fmt"
	"math"
	"sync"

	"david22573/synaptic-mc/internal/domain"
)

// ThreatHeatmap tracks the spatial density of danger over time.
type ThreatHeatmap struct {
	mu    sync.RWMutex
	cells map[string]float64 // key: "x,z" (chunk-sized cells), value: danger level 0.0-1.0
}

func NewThreatHeatmap() *ThreatHeatmap {
	return &ThreatHeatmap{
		cells: make(map[string]float64),
	}
}

func (h *ThreatHeatmap) RecordThreat(pos domain.Vec3, danger float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := getHeatmapKey(pos)
	h.cells[key] = math.Min(1.0, h.cells[key]+danger)
}

func (h *ThreatHeatmap) Decay() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for k, v := range h.cells {
		h.cells[k] = v * 0.95
		if h.cells[k] < 0.01 {
			delete(h.cells, k)
		}
	}
}

func (h *ThreatHeatmap) GetDanger(pos domain.Vec3) float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cells[getHeatmapKey(pos)]
}

func getHeatmapKey(pos domain.Vec3) string {
	// 8x8 meter cells for heatmap
	cx := int(pos.X) >> 3
	cz := int(pos.Z) >> 3
	return fmt.Sprintf("%d,%d", cx, cz)
}

// EnemyPredictor forecasts mob movement to avoid collisions.
type EnemyPredictor struct {
	lastPositions map[string]domain.Vec3
	mu            sync.Mutex
}

func NewEnemyPredictor() *EnemyPredictor {
	return &EnemyPredictor{
		lastPositions: make(map[string]domain.Vec3),
	}
}

func (p *EnemyPredictor) Predict(threats []domain.ThreatInfo) map[string]domain.Vec3 {
	p.mu.Lock()
	defer p.mu.Unlock()

	predictions := make(map[string]domain.Vec3)
	// Mock prediction logic: assumes mobs continue in same direction
	// In a real impl, we'd need entity IDs from the state update
	return predictions
}
