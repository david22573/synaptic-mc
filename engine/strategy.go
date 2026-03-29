package engine

import (
	"strings"
	"sync"
	"time"
)

type Strategy struct {
	PrimaryGoal   string
	SecondaryGoal string
	Priority      int
	Active        bool
	IsAutonomous  bool
}

type SystemDirective struct {
	Strategy         Strategy
	CriticalOverride string
	InterruptCurrent bool
	TriggerReplan    bool
}

type StrategyManager struct {
	currentPrimary   string
	currentSecondary string
	lastShift        time.Time
	lastHealth       float64
	mu               sync.Mutex
}

func NewStrategyManager() *StrategyManager {
	return &StrategyManager{
		lastHealth: 20.0,
	}
}

func (s *StrategyManager) Evaluate(state GameState, timeSinceReplan time.Duration) SystemDirective {
	s.mu.Lock()
	defer s.mu.Unlock()

	directive := SystemDirective{}

	// 1. Critical Health Policy (Pulled from Engine)
	healthDropped := state.Health < s.lastHealth && state.Health < 5
	if healthDropped && timeSinceReplan > 15*time.Second {
		directive.TriggerReplan = true
		directive.InterruptCurrent = true
		directive.CriticalOverride = "CRITICAL OVERRIDE: Health is critically low and actively dropping. Retreat to safety and eat immediately. Do not engage in combat."
	}
	s.lastHealth = state.Health

	inv := make(map[string]int)
	hasWeapon := false
	readyFoodCount := 0

	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
		if strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") {
			hasWeapon = true
		}
		if strings.Contains(item.Name, "cooked") || strings.Contains(item.Name, "bread") || strings.Contains(item.Name, "apple") {
			readyFoodCount += item.Count
		}
	}

	var primary, secondary string
	var priority int

	// 2. Critical Survival Triggers
	if state.Health < 10 || state.Food < 6 {
		primary = "SURVIVAL: Secure food immediately and retreat to safety to regenerate health."
		secondary = "DEFENSE: Eliminate immediate threats preventing safe regeneration."
		priority = 100
		directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
		return directive
	}

	if s.currentPrimary != "" && time.Since(s.lastShift) < 30*time.Second {
		directive.Strategy = Strategy{
			PrimaryGoal:   s.currentPrimary,
			SecondaryGoal: s.currentSecondary,
			Priority:      50,
			Active:        true,
			IsAutonomous:  strings.HasPrefix(s.currentPrimary, "AUTONOMY") || !strings.Contains(s.currentPrimary, ":"),
		}
		return directive
	}

	// 3. Nightfall / Shelter
	isNight := state.TimeOfDay > 12541 && state.TimeOfDay < 23000
	if isNight && !state.HasBedNearby {
		primary = "SHELTER: Survive the night. Avoid open areas, dig a 3-block deep hole and cover the top, or stay near a bed."
		secondary = "TECH: While sheltered, use any available materials to craft tools or smelt items."
		priority = 90
		directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
		return directive
	}

	// 4. Proactive Upkeep
	if readyFoodCount < 3 && !isNight {
		primary = "PROVISIONING: Hunt animals or harvest crops to build a stockpile of at least 3 food items."
		secondary = "EXPLORATION: Keep moving to locate new resources while hunting."
		priority = 85
		directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
		return directive
	}

	hasWoodenPick := inv["wooden_pickaxe"] > 0
	hasStonePick := inv["stone_pickaxe"] > 0 || inv["iron_pickaxe"] > 0 || inv["diamond_pickaxe"] > 0

	hasLog := false
	for k, v := range inv {
		if v > 0 && strings.HasSuffix(k, "_log") {
			hasLog = true
			break
		}
	}

	// 5. Tech Tree Heuristics
	if !hasWoodenPick && !hasStonePick {
		if !hasLog && inv["oak_planks"] == 0 && inv["crafting_table"] == 0 {
			primary = "TECH TIER 1 (Wood): Gather logs. This is the absolute first step."
			secondary = "AWARENESS: Note locations of stone and coal for the next tier."
			priority = 80
			directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
			return directive
		}
		primary = "TECH TIER 1 (Tools): Use logs to craft planks, sticks, a crafting table, and finally a wooden pickaxe."
		secondary = "GATHER: Continue gathering excess wood if near trees."
		priority = 75
		directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
		return directive
	}

	if !hasStonePick {
		primary = "TECH TIER 2 (Stone): Use wooden pickaxe to mine at least 3 stone (cobblestone), then craft a stone pickaxe."
		secondary = "ARMAMENT: Upgrade to a stone sword or axe immediately after the pickaxe."
		priority = 70
		directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
		return directive
	}

	if !hasWeapon && (inv["cobblestone"] >= 2 || inv["stone"] >= 2) {
		primary = "ARMAMENT: Craft a stone sword or stone axe to defend against threats."
		secondary = "TECH: Mine coal if spotted."
		priority = 68
		directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
		return directive
	}

	if inv["furnace"] == 0 {
		primary = "TECH TIER 3 (Smelting): Mine 8 cobblestone and craft a furnace."
		secondary = "PROVISIONING: Gather raw meat and coal to utilize the new furnace."
		priority = 65
		directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, false)
		return directive
	}

	// 6. True Autonomy Handover
	primary = "AUTONOMY: Basic needs are met. You are now in full autonomous mode. Evaluate your inventory and the known world, and set a new long-term macro strategy."
	secondary = "MAINTENANCE: Ensure food stays above 10 and tools are repaired."
	priority = 50
	directive.Strategy = s.shiftIfNeeded(primary, secondary, priority, true)
	return directive
}

func (s *StrategyManager) shiftIfNeeded(primary, secondary string, priority int, isAuto bool) Strategy {
	if s.currentPrimary != primary || s.currentSecondary != secondary {
		s.currentPrimary = primary
		s.currentSecondary = secondary
		s.lastShift = time.Now()
	}
	return Strategy{PrimaryGoal: s.currentPrimary, SecondaryGoal: s.currentSecondary, Priority: priority, Active: true, IsAutonomous: isAuto}
}
