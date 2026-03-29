package strategy

import (
	"david22573/synaptic-mc/internal/domain"
	"strings"
)

type Directive struct {
	PrimaryGoal   string
	SecondaryGoal string
	IsAutonomous  bool
}

type Evaluator struct{}

func NewEvaluator() *Evaluator {
	return &Evaluator{}
}

func isFood(itemName string) bool {
	foodItems := []string{
		"beef", "porkchop", "mutton", "chicken", "rabbit",
		"cooked_beef", "cooked_porkchop", "cooked_mutton", "cooked_chicken", "cooked_rabbit",
		"apple", "sweet_berries", "bread", "carrot", "potato", "baked_potato", "kelp", "dried_kelp",
	}
	for _, f := range foodItems {
		if strings.Contains(itemName, f) {
			return true
		}
	}
	return false
}

func (e *Evaluator) Evaluate(state domain.GameState) Directive {
	inv := make(map[string]int)
	hasWeapon := false
	hasFood := false

	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
		if strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") {
			hasWeapon = true
		}
		if isFood(item.Name) {
			hasFood = true
		}
	}

	// 1. Survival First (Now Food-Aware)
	if state.Health < 10 || state.Food < 6 {
		if hasFood {
			return Directive{
				PrimaryGoal:   "SURVIVAL: You have food in your inventory. Use the 'eat' action immediately to regenerate health.",
				SecondaryGoal: "DEFENSE: Retreat to a safe location while healing.",
				IsAutonomous:  false,
			}
		}

		return Directive{
			PrimaryGoal:   "SURVIVAL: You are starving and have NO food. You CANNOT hunt (health too low). Use 'gather' for passive food (sweet_berries, apples) or 'explore' to find a village.",
			SecondaryGoal: "DEFENSE: Avoid all combat. Retreat if threatened.",
			IsAutonomous:  false,
		}
	}

	// 2. Nightfall Policy
	isNight := state.TimeOfDay > 12541 && state.TimeOfDay < 23000
	if isNight && !state.HasBedNearby {
		return Directive{
			PrimaryGoal:   "SHELTER: Survive the night. Avoid open areas, dig a 3-block deep hole and cover the top.",
			SecondaryGoal: "TECH: While sheltered, use any available materials to craft tools or smelt items.",
			IsAutonomous:  false,
		}
	}

	// 3. Tech Progression
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
			return Directive{
				PrimaryGoal:   "TECH TIER 1 (Wood): Gather logs. This is the absolute first step.",
				SecondaryGoal: "AWARENESS: Note locations of stone and coal for the next tier.",
			}
		}
		return Directive{
			PrimaryGoal:   "TECH TIER 1 (Tools): Use logs to craft planks, sticks, a crafting table, and a wooden pickaxe.",
			SecondaryGoal: "GATHER: Continue gathering excess wood if near trees.",
		}
	}

	if !hasStonePick {
		return Directive{
			PrimaryGoal:   "TECH TIER 2 (Stone): Use wooden pickaxe to mine at least 3 cobblestone, then craft a stone pickaxe.",
			SecondaryGoal: "ARMAMENT: Upgrade to a stone sword or axe immediately after the pickaxe.",
		}
	}

	if !hasWeapon && (inv["cobblestone"] >= 2 || inv["stone"] >= 2) {
		return Directive{
			PrimaryGoal:   "ARMAMENT: Craft a stone sword or stone axe to defend against threats.",
			SecondaryGoal: "TECH: Mine coal if spotted.",
		}
	}

	// 4. Autonomy Handoff (3.3 FIX: Added sub-goals to bridge the gap)
	cookedFoodCount := 0
	for k, v := range inv {
		if strings.HasPrefix(k, "cooked_") || k == "bread" || k == "baked_potato" {
			cookedFoodCount += v
		}
	}

	if cookedFoodCount < 5 {
		return Directive{
			PrimaryGoal:   "SUSTENANCE: Hunt and smelt meat until you have 5+ cooked food items.",
			SecondaryGoal: "MAINTENANCE: Ensure tools are repaired and ready.",
			IsAutonomous:  false,
		}
	}

	if inv["coal"] == 0 {
		return Directive{
			PrimaryGoal:   "RESOURCES: Find and mine coal_ore. You need it for smelting and torches.",
			SecondaryGoal: "AWARENESS: Note locations of iron_ore for the next tier.",
			IsAutonomous:  false,
		}
	}

	if inv["iron_pickaxe"] == 0 {
		return Directive{
			PrimaryGoal:   "IRON_TIER: Mine iron_ore (needs stone_pickaxe), smelt iron_ingots, craft an iron_pickaxe.",
			SecondaryGoal: "MAINTENANCE: Maintain food and coal stockpiles.",
			IsAutonomous:  false,
		}
	}

	return Directive{
		PrimaryGoal:   "AUTONOMY: Basic needs are met. Evaluate inventory and known world, set long-term macro strategy.",
		SecondaryGoal: "MAINTENANCE: Ensure food stays above 10 and tools are repaired.",
		IsAutonomous:  true,
	}
}
