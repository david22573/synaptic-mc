package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type PlanStatus string

const (
	PlanStatusPending     PlanStatus = "PENDING"
	PlanStatusActive      PlanStatus = "ACTIVE"
	PlanStatusCompleted   PlanStatus = "COMPLETED"
	PlanStatusFailed      PlanStatus = "FAILED"
	PlanStatusInvalidated PlanStatus = "INVALIDATED"
	PlanStatusBlocked     PlanStatus = "BLOCKED" // Week 3: Plan State Machine
)

// Phase 1 & 5: Execution Result Protocol
const (
	CauseTimeout                 = "TIMEOUT"
	CauseBlocked                 = "BLOCKED"
	CauseStuck                   = "STUCK"
	CausePartial                 = "PARTIAL"
	CauseFailed                  = "FAILED"
	CauseDistracted              = "DISTRACTED"
	CauseAbortedDuringHesitation = "ABORTED_DURING_HESITATION"
	CauseUnknown                 = "UNKNOWN"
)

// Survival & Decision Thresholds
const (
	SurvivalCriticalHealth = 4.0
	SurvivalMinFoodForHunt = 8.0
	SurvivalMaxThreatDist  = 12.0
	DecisionHealthSafe     = 10.0
	DecisionHealthHunt     = 12.0
	DecisionFoodMax        = 20.0
)

type ExecutionResult struct {
	Action   Action  `json:"action"`
	Success  bool    `json:"success"`
	Cause    string  `json:"cause"`
	Progress float64 `json:"progress"`
}

type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

func (v Vec3) DistanceTo(other Vec3) float64 {
	dx := v.X - other.X
	dy := v.Y - other.Y
	dz := v.Z - other.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// Phase 5: World Model Alignment Structs
type ChunkCoord struct {
	X int `json:"x"`
	Z int `json:"z"`
}

type DangerZone struct {
	Center Vec3    `json:"center"`
	Radius float64 `json:"radius"`
	Reason string  `json:"reason"` // e.g., "failed_path", "lava", "mob_cluster"
	Risk   float64 `json:"risk"`   // 0.0 to 1.0
}

type Feedback struct {
	Type   string `json:"type"`
	Cause  string `json:"cause"`
	Action string `json:"action,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type GameState struct {
	Health       float64           `json:"health"`
	Food         float64           `json:"food"`
	TimeOfDay          int               `json:"time_of_day"`
	Experience         float64           `json:"experience"`
	ExperienceProgress float64           `json:"experience_progress"`
	Level              int               `json:"level"`
	HasBedNearby bool              `json:"has_bed_nearby"`
	Position     Vec3              `json:"position"`
	Threats      []Threat          `json:"threats"`
	POIs         []POI             `json:"pois"`
	Inventory    []Item            `json:"inventory"`
	Hotbar       []*Item           `json:"hotbar"`
	Offhand      *Item             `json:"offhand"`
	ActiveSlot   int               `json:"active_slot"`
	KnownChests  map[string][]Item `json:"known_chests,omitempty"`
	Feedback     []Feedback        `json:"feedback,omitempty"`
	CurrentTask  *Action           `json:"current_task,omitempty"`
	TaskProgress float64           `json:"task_progress,omitempty"`

	// Phase 5: State Fidelity
	DangerZones      []DangerZone       `json:"danger_zones,omitempty"`
	VisitedChunks    []ChunkCoord       `json:"visited_chunks,omitempty"`
	TerrainRoughness map[string]float64 `json:"terrain_roughness,omitempty"` // map["x,z"] -> 0.0-1.0
}

// Phase 5.2: Feed execution feedback into state
func (s *GameState) MarkAreaRisky(pos Vec3, reason string, risk float64) {
	s.DangerZones = append(s.DangerZones, DangerZone{
		Center: pos,
		Radius: 8.0,
		Reason: reason,
		Risk:   risk,
	})
}

func (s *GameState) RecordChunkVisit(x, z int) {
	for _, c := range s.VisitedChunks {
		if c.X == x && c.Z == z {
			return // Already tracked
		}
	}
	s.VisitedChunks = append(s.VisitedChunks, ChunkCoord{X: x, Z: z})
}

type VersionedState struct {
	Version uint64
	State   GameState
}

type Threat struct {
	Name     string  `json:"name"`
	Distance float64 `json:"distance"`
}

type Target struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type POI struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Position   Vec3    `json:"position"`
	Distance   float64 `json:"distance"`
	Visibility float64 `json:"visibility"`
	Score      int     `json:"score"`
	Direction  string  `json:"direction"`
}

type Item struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Action struct {
	ID           string        `json:"id"`
	ControllerID string        `json:"controller_id,omitempty"`
	Source       string        `json:"source"`
	Trace        TraceContext  `json:"trace"`
	Type         string        `json:"type,omitempty"`
	Action       string        `json:"action"`
	Target       Target        `json:"target"`
	Count        int           `json:"count"`
	Rationale    string        `json:"rationale"`
	Priority     int           `json:"priority"`
	Timeout      time.Duration `json:"timeout,omitempty"`
}

type Plan struct {
	ID            string     `json:"id"`
	ParentPlanID  string     `json:"parent_plan_id,omitempty"`
	StateVersion  uint64     `json:"state_version"`
	Status        PlanStatus `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	InvalidatedAt *time.Time `json:"invalidated_at,omitempty"`
	Objective     string     `json:"objective"`
	Tasks         []Action   `json:"tasks"`
	Fallbacks     [][]Action `json:"fallbacks,omitempty"` // Week 4: Multi-Plan Fallback
	IsFallback    bool       `json:"is_fallback,omitempty"`
}

type EvaluationSnapshot struct {
	State      VersionedState
	History    []DomainEvent
	ActivePlan *Plan
}

// LLM Formatting Structs
type CompactPOI struct {
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	Distance  float64 `json:"distance"`
	Direction string  `json:"direction"`
}

type CompactThreat struct {
	Name string `json:"name"`
}

type CompactTask struct {
	Action   string  `json:"action"`
	Target   string  `json:"target"`
	Progress float64 `json:"progress"`
}

type CompactDangerZone struct {
	Distance float64 `json:"distance"`
	Reason   string  `json:"reason"`
	Risk     float64 `json:"risk"`
}

func FormatStateForLLM(state GameState) string {
	compact := struct {
		Health                  float64                   `json:"health"`
		Food                    float64                   `json:"food"`
		TimeOfDay               int                       `json:"time_of_day"`
		Experience              float64                   `json:"experience"`
		Level                   int                       `json:"level"`
		HasBedNearby            bool                      `json:"has_bed_nearby"`
		Inventory               map[string]int            `json:"inventory"`
		Threats                 []CompactThreat           `json:"threats"`
		POIs                    []CompactPOI              `json:"pois"`
		HasPickaxe              bool                      `json:"has_pickaxe"`
		HasWeapon               bool                      `json:"has_weapon"`
		KnownChests             map[string]map[string]int `json:"known_chests,omitempty"`
		Feedback                []Feedback                `json:"feedback,omitempty"`
		CurrentTask             *CompactTask              `json:"current_task,omitempty"`
		CurrentTerrainRoughness float64                   `json:"current_terrain_roughness,omitempty"`
		NearbyDangerZones       []CompactDangerZone       `json:"nearby_danger_zones,omitempty"`
		VisitedChunksCount      int                       `json:"visited_chunks_count"`
	}{
		Health:             state.Health,
		Food:               state.Food,
		TimeOfDay:          state.TimeOfDay,
		Experience:         state.Experience,
		Level:              state.Level,
		HasBedNearby:       state.HasBedNearby,
		Inventory:          make(map[string]int),
		Threats:            make([]CompactThreat, 0),
		POIs:               make([]CompactPOI, 0),
		KnownChests:        make(map[string]map[string]int),
		Feedback:           state.Feedback,
		NearbyDangerZones:  make([]CompactDangerZone, 0),
		VisitedChunksCount: len(state.VisitedChunks),
	}

	if state.CurrentTask != nil {
		compact.CurrentTask = &CompactTask{
			Action:   state.CurrentTask.Action,
			Target:   state.CurrentTask.Target.Name,
			Progress: state.TaskProgress,
		}
	}

	// Phase 5: Inject local world context
	chunkKey := fmt.Sprintf("%d,%d", int(state.Position.X)>>4, int(state.Position.Z)>>4)
	if roughness, exists := state.TerrainRoughness[chunkKey]; exists {
		compact.CurrentTerrainRoughness = roughness
	}

	for _, dz := range state.DangerZones {
		dist := state.Position.DistanceTo(dz.Center)
		if dist < 64.0 { // Only burden the prompt with localized danger
			compact.NearbyDangerZones = append(compact.NearbyDangerZones, CompactDangerZone{
				Distance: dist,
				Reason:   dz.Reason,
				Risk:     dz.Risk,
			})
		}
	}

	for _, item := range state.Inventory {
		if item.Count > 0 {
			compact.Inventory[item.Name] += item.Count
			if strings.Contains(item.Name, "pickaxe") {
				compact.HasPickaxe = true
			}
			if strings.Contains(item.Name, "sword") ||
				(strings.Contains(item.Name, "axe") && !strings.Contains(item.Name, "pickaxe")) {
				compact.HasWeapon = true
			}
		}
	}

	for i, t := range state.Threats {
		if i >= 3 {
			break
		}
		compact.Threats = append(compact.Threats, CompactThreat{Name: t.Name})
	}

	for i, p := range state.POIs {
		if i >= 5 {
			break
		}
		compact.POIs = append(compact.POIs, CompactPOI{
			Type:      p.Type,
			Name:      p.Name,
			Distance:  p.Distance,
			Direction: p.Direction,
		})
	}

	for pos, items := range state.KnownChests {
		cInv := make(map[string]int)
		for _, item := range items {
			if item.Count > 0 {
				cInv[item.Name] += item.Count
			}
		}
		compact.KnownChests[pos] = cInv
	}

	b, _ := json.MarshalIndent(compact, "", "  ")
	return string(b)
}

func CleanJSON(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	return strings.TrimSpace(cleaned)
}
