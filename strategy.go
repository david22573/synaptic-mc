package main

import (
	"strings"
	"sync"
	"time"
)

type Strategy struct {
	Goal     string
	Priority int
	Active   bool
}

type StrategyManager struct {
	currentGoal string
	lastShift   time.Time
	mu          sync.Mutex
}

func NewStrategyManager() *StrategyManager {
	return &StrategyManager{}
}

func (s *StrategyManager) Evaluate(state GameState) Strategy {
	s.mu.Lock()
	defer s.mu.Unlock()

	inv := make(map[string]int)
	hasWeapon := false
	readyFoodCount := 0

	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
		if strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") {
			hasWeapon = true
		}
		// Count food that provides decent saturation
		if strings.Contains(item.Name, "cooked") || strings.Contains(item.Name, "bread") || strings.Contains(item.Name, "apple") {
			readyFoodCount += item.Count
		}
	}

	var nextGoal string
	var priority int

	// 1. Critical Survival Triggers (Overrides hysteresis)
	if state.Health < 10 || state.Food < 6 {
		nextGoal = "SURVIVAL: Secure food immediately and retreat to safety to regenerate health."
		priority = 100
		return s.shiftIfNeeded(nextGoal, priority)
	}

	// Hysteresis: prevent thrashing for non-critical shifts
	if s.currentGoal != "" && time.Since(s.lastShift) < 30*time.Second {
		return Strategy{Goal: s.currentGoal, Priority: 50, Active: true}
	}

	// 2. Nightfall / Shelter
	isNight := state.TimeOfDay > 12541 && state.TimeOfDay < 23000
	if isNight && !state.HasBedNearby {
		nextGoal = "SHELTER: Survive the night. Avoid open areas, dig a 3-block deep hole and cover the top, or stay near a bed."
		priority = 90
		return s.shiftIfNeeded(nextGoal, priority)
	}

	// 3. Proactive Upkeep (The Baseline Necessities)
	if readyFoodCount < 3 && !isNight {
		nextGoal = "PROVISIONING: Hunt animals or harvest crops to build a stockpile of at least 3 food items before exploring."
		priority = 85
		return s.shiftIfNeeded(nextGoal, priority)
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

	// 4. Tech Tree Heuristics
	if !hasWoodenPick && !hasStonePick {
		if !hasLog && inv["oak_planks"] == 0 && inv["crafting_table"] == 0 {
			nextGoal = "TECH TIER 1 (Wood): Gather logs. This is the absolute first step."
			priority = 80
			return s.shiftIfNeeded(nextGoal, priority)
		}
		nextGoal = "TECH TIER 1 (Tools): Use logs to craft planks, sticks, a crafting table, and finally a wooden pickaxe."
		priority = 75
		return s.shiftIfNeeded(nextGoal, priority)
	}

	if !hasStonePick {
		nextGoal = "TECH TIER 2 (Stone): Use wooden pickaxe to mine at least 3 stone (cobblestone), then craft a stone pickaxe."
		priority = 70
		return s.shiftIfNeeded(nextGoal, priority)
	}

	// Ensure we are armed as soon as we hit the stone age
	if !hasWeapon && (inv["cobblestone"] >= 2 || inv["stone"] >= 2) {
		nextGoal = "ARMAMENT: Craft a stone sword or stone axe to defend against threats."
		priority = 68
		return s.shiftIfNeeded(nextGoal, priority)
	}

	if inv["furnace"] == 0 {
		nextGoal = "TECH TIER 3 (Smelting): Mine 8 cobblestone and craft a furnace. Gather coal."
		priority = 65
		return s.shiftIfNeeded(nextGoal, priority)
	}

	// 5. Default Expansion
	nextGoal = "EXPANSION: Explore the area, map new POIs, stockpile resources, and locate iron ore."
	priority = 50
	return s.shiftIfNeeded(nextGoal, priority)
}

func (s *StrategyManager) shiftIfNeeded(goal string, priority int) Strategy {
	if s.currentGoal != goal {
		s.currentGoal = goal
		s.lastShift = time.Now()
	}
	return Strategy{Goal: s.currentGoal, Priority: priority, Active: true}
}
