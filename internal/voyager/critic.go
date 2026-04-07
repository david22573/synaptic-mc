package voyager

import (
	"context"
	"fmt"
	"math"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type Critic interface {
	Evaluate(intent domain.ActionIntent, before, after domain.GameState, failureCount int) (bool, string)
}

type StateCritic struct{}

func NewStateCritic() *StateCritic {
	return &StateCritic{}
}

func (c *StateCritic) Evaluate(intent domain.ActionIntent, before, after domain.GameState, failureCount int) (bool, string) {
	// If the planner has failed multiple times, explicitly tell the LLM it's stuck.
	if failureCount >= 2 {
		return false, fmt.Sprintf("Critique: Task '%s' has failed %d times in a row. The environment is blocking execution. ABANDON this task and choose a completely different approach.", intent.Action, failureCount)
	}

	if after.Health <= 0 {
		return false, "Critique: Bot died while executing the task. Re-evaluate threat assessment and survival priorities."
	}

	beforeInv := make(map[string]int)
	for _, item := range before.Inventory {
		beforeInv[strings.ToLower(item.Name)] += item.Count
	}

	afterInv := make(map[string]int)
	for _, item := range after.Inventory {
		afterInv[strings.ToLower(item.Name)] += item.Count
	}

	target := strings.ToLower(intent.Target)

	switch intent.Action {
	case "mine", "gather", "farm":
		bCount := beforeInv[target]
		aCount := afterInv[target]

		if aCount >= bCount+intent.Count {
			return true, fmt.Sprintf("Success: Gathered %d %s. Inventory went from %d to %d.", aCount-bCount, intent.Target, bCount, aCount)
		}
		if aCount > bCount {
			return true, fmt.Sprintf("Partial Success: Gathered %d %s (requested %d).", aCount-bCount, intent.Target, intent.Count)
		}
		return false, fmt.Sprintf("Critique: Expected to gather %d %s, but inventory remained at %d. The bot may have gotten stuck, lacked the correct tool, or the dropped item was unreachable.", intent.Count, intent.Target, bCount)

	case "craft":
		bCount := beforeInv[target]
		aCount := afterInv[target]

		if aCount > bCount {
			return true, fmt.Sprintf("Success: Crafted %s.", intent.Target)
		}
		return false, fmt.Sprintf("Critique: Failed to craft %s. Ensure ingredients are actually in the inventory and a crafting table is reachable if required.", intent.Target)

	case "smelt":
		expectedOutput := getSmeltOutput(target)
		bCount := beforeInv[expectedOutput]
		aCount := afterInv[expectedOutput]

		if aCount > bCount {
			return true, fmt.Sprintf("Success: Smelted %s into %s.", intent.Target, expectedOutput)
		}
		return false, fmt.Sprintf("Critique: Failed to smelt %s. Ensure both the raw material and fuel (coal/wood) are in the inventory, and a furnace is placed nearby.", intent.Target)

	case "hunt":
		if after.Health < before.Health && after.Health < 10 {
			return true, "Warning: Survived the hunt, but took heavy damage. Consider retreating, eating, or crafting better armor."
		}
		return true, "Success: Engaged target and survived the encounter."

	case "store":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount < bCount || target == "all" || target == "dump" {
			return true, "Success: Stored items in chest."
		}
		return false, fmt.Sprintf("Critique: Failed to store %s. Inventory count remained at %d. Chest might be full or pathfinding failed.", intent.Target, bCount)

	case "retrieve":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount > bCount {
			return true, fmt.Sprintf("Success: Retrieved %s from chest.", intent.Target)
		}
		return false, fmt.Sprintf("Critique: Failed to retrieve %s. The chest might be empty of this item or unreachable.", intent.Target)

	case "eat":
		if after.Food > before.Food || after.Health > before.Health {
			return true, "Success: Consumed food and restored stats."
		}
		return false, "Critique: Failed to eat. Ensure the specified food item is actually present in the inventory."

	case "build":
		beforeTotal := 0
		for _, v := range beforeInv {
			beforeTotal += v
		}
		afterTotal := 0
		for _, v := range afterInv {
			afterTotal += v
		}
		if afterTotal < beforeTotal {
			return true, "Success: Constructed structure and consumed blocks."
		}
		return false, "Critique: Failed to build structure. The bot might have lacked sufficient dirt/cobblestone/planks, or terrain blocked placement."

	case "explore", "retreat":
		dx := after.Position.X - before.Position.X
		dy := after.Position.Y - before.Position.Y
		dz := after.Position.Z - before.Position.Z
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)

		if dist > 3.0 {
			return true, fmt.Sprintf("Success: Relocated %.1f blocks successfully.", dist)
		}
		return false, "Critique: Failed to move significantly. The bot might be trapped in a hole, blocked by water/lava, or pathfinding failed."
	}

	return true, fmt.Sprintf("Success: %s completed natively without strict state violations.", intent.Action)
}

func getSmeltOutput(input string) string {
	if strings.HasPrefix(input, "raw_") {
		return strings.TrimPrefix(input, "raw_") + "_ingot"
	}
	if strings.HasSuffix(input, "_ore") {
		return strings.TrimSuffix(input, "_ore") + "_ingot"
	}
	if strings.HasSuffix(input, "_log") {
		return "charcoal"
	}
	if input == "cobblestone" {
		return "stone"
	}
	if input == "stone" {
		return "smooth_stone"
	}
	if input == "sand" || input == "red_sand" {
		return "glass"
	}
	return "cooked_" + input
}

// GenerateRules implements the RuleExtractor interface
func (c *StateCritic) GenerateRules(ctx context.Context, sessionID string) string {
	var rules strings.Builder
	rules.WriteString("CRITICAL SURVIVAL RULES:\n")
	rules.WriteString("- If health < 10, prioritize 'eat' or 'retreat' actions\n")
	rules.WriteString("- Avoid 'hunt' when health < 12\n")
	rules.WriteString("- Always ensure food is available before dangerous activities\n\n")

	rules.WriteString("TOOL & PREREQUISITE RULES:\n")
	rules.WriteString("- Cannot mine stone without wooden_pickaxe or better\n")
	rules.WriteString("- Crafting table required for most tool recipes\n")
	rules.WriteString("- Verify materials are in inventory before crafting\n\n")

	rules.WriteString("ACTION VALIDATION RULES:\n")
	rules.WriteString("- 'store' requires accessible chest with space\n")
	rules.WriteString("- 'retrieve' requires item exists in known chest\n")
	rules.WriteString("- 'build' requires ≥20 blocks (dirt/planks/cobblestone)\n")
	rules.WriteString("- 'smelt' requires raw material + fuel (coal/wood)\n")
	rules.WriteString("- Movement actions should result in >3 block displacement\n")
	rules.WriteString("- If an action fails repeatedly, do NOT try it again. Choose a different approach.\n")

	return rules.String()
}
