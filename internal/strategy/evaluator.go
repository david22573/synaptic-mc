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

// Evaluate defines the core progression logic deterministically. [cite: 172, 175, 177]
func (e *Evaluator) Evaluate(state domain.GameState) Directive {
	inv := make(map[string]int)
	hasWeapon := false

	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
		if strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") {
			hasWeapon = true
		}
	}

	// 1. Survival First
	if state.Health < 10 || state.Food < 6 {
		return Directive{
			PrimaryGoal:   "SURVIVAL: Secure food immediately and retreat to safety.",
			SecondaryGoal: "DEFENSE: Eliminate immediate threats preventing safe regeneration.",
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

	// 3. Tech Progression (Deterministic checks)
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

	// 4. Autonomy Handoff
	return Directive{
		PrimaryGoal:   "AUTONOMY: Basic needs are met. Evaluate inventory and known world, set long-term macro strategy.",
		SecondaryGoal: "MAINTENANCE: Ensure food stays above 10 and tools are repaired.",
		IsAutonomous:  true,
	}
}
