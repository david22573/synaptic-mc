package main

import "strings"

type Strategy struct {
	Goal     string
	Priority int
	Active   bool
}

type StrategyManager struct{}

func NewStrategyManager() *StrategyManager {
	return &StrategyManager{}
}

func (s *StrategyManager) Evaluate(state GameState) Strategy {
	inv := make(map[string]int)
	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
	}

	// Scan the ranked POIs for any type of bed
	hasBedNearby := false
	for _, poi := range state.POIs {
		if strings.Contains(poi.Name, "bed") {
			hasBedNearby = true
			break
		}
	}

	// 1. Critical Survival Triggers
	if state.Health < 10 || state.Food < 6 {
		return Strategy{Goal: "SURVIVAL: Secure food immediately and retreat to safety to regenerate health.", Priority: 100, Active: true}
	}

	// 2. Nightfall / Shelter
	isNight := state.TimeOfDay > 12541 && state.TimeOfDay < 23000
	if isNight && !hasBedNearby {
		return Strategy{Goal: "SHELTER: Survive the night. Avoid open areas, dig a 3-block deep hole and cover the top, or stay near a bed.", Priority: 90, Active: true}
	}

	// 3. Tech Tree Heuristics
	hasWoodenPick := inv["wooden_pickaxe"] > 0
	hasStonePick := inv["stone_pickaxe"] > 0 || inv["iron_pickaxe"] > 0 || inv["diamond_pickaxe"] > 0

	hasLog := false
	for k, v := range inv {
		if v > 0 && strings.HasSuffix(k, "_log") {
			hasLog = true
			break
		}
	}

	if !hasWoodenPick && !hasStonePick {
		if !hasLog && inv["oak_planks"] == 0 && inv["crafting_table"] == 0 {
			return Strategy{Goal: "TECH TIER 1 (Wood): Gather logs. This is the absolute first step.", Priority: 80, Active: true}
		}
		return Strategy{Goal: "TECH TIER 1 (Tools): Use logs to craft planks, sticks, a crafting table, and finally a wooden pickaxe.", Priority: 75, Active: true}
	}

	if !hasStonePick {
		return Strategy{Goal: "TECH TIER 2 (Stone): Use wooden pickaxe to mine at least 3 stone (cobblestone), then craft a stone pickaxe.", Priority: 70, Active: true}
	}

	if inv["furnace"] == 0 {
		return Strategy{Goal: "TECH TIER 3 (Smelting): Mine 8 cobblestone and craft a furnace. Gather coal.", Priority: 65, Active: true}
	}

	// 4. Default Expansion
	return Strategy{Goal: "EXPANSION: Explore the area, map new POIs, stockpile food, and locate iron ore.", Priority: 50, Active: true}
}
