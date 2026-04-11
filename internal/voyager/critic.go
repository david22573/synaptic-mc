package voyager

import (
	"context"
	"fmt"
	"math"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type Critic interface {
	Evaluate(intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) (bool, string)
}

type StateCritic struct{}

func NewStateCritic() *StateCritic {
	return &StateCritic{}
}

func (c *StateCritic) Evaluate(intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) (bool, string) {
	// If execution failed, start by capturing the reason and any potential stack trace.
	if !result.Success {
		critique := fmt.Sprintf("Critique: Task '%s' failed. Reason: %s.", intent.Action, result.Cause)
		if strings.Contains(result.Cause, "Error") || strings.Contains(result.Cause, "stack") {
			critique += " DEBUG INFO: Review the JavaScript execution error and ensure the code aligns with Mineflayer API requirements."
		}
		
		if failureCount >= 2 {
			critique += fmt.Sprintf(" This is failure #%d. The current approach is deadlocking; you MUST rethink the entire tactical sequence.", failureCount)
		}
		return false, critique
	}

	if after.Health <= 0 {
		return false, "Critique: Bot died while executing the task. Re-evaluate threat assessment and survival priorities."
	}

	// Mathematical comparison of GameState diffs
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
			return true, fmt.Sprintf("Success: Verified GameState diff. Gathered %d %s (Target reached).", aCount-bCount, intent.Target)
		}
		if aCount > bCount {
			return true, fmt.Sprintf("Partial Success: Gathered %d %s. Expected %d. Inventory increased as expected.", aCount-bCount, intent.Target, intent.Count)
		}
		return false, fmt.Sprintf("Critique: Execution reported success but GameState verification FAILED. Inventory count for '%s' remained at %d. Possible cause: item dropped but was never collected by the bot.", intent.Target, bCount)

	case "craft":
		bCount := beforeInv[target]
		aCount := afterInv[target]

		if aCount > bCount {
			return true, fmt.Sprintf("Success: Verified GameState diff. Crafted %s (Inventory: %d -> %d).", intent.Target, bCount, aCount)
		}
		return false, fmt.Sprintf("Critique: Failed to verify craft in GameState. Item '%s' count did not increase. Ensure prerequisites were in inventory and bot reached a crafting table.", intent.Target)

	case "smelt":
		expectedOutput := getSmeltOutput(target)
		bCount := beforeInv[expectedOutput]
		aCount := afterInv[expectedOutput]

		if aCount > bCount {
			return true, fmt.Sprintf("Success: Smelted %s. Output '%s' verified in inventory.", intent.Target, expectedOutput)
		}
		return false, fmt.Sprintf("Critique: Smelting output '%s' not found in GameState after execution. Verify furnace proximity and fuel availability.", expectedOutput)

	case "hunt":
		// Check for specific mob drops if possible, otherwise check survival
		if after.Health < before.Health && after.Health < 10 {
			return true, "Warning: Hunt successful, but Health diff shows critical damage taken. High risk encounter."
		}
		return true, "Success: Encounter resolved. Survival verified by health delta."

	case "store":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount < bCount || target == "all" || target == "dump" {
			return true, "Success: State diff shows items removed from local inventory and placed in container."
		}
		return false, fmt.Sprintf("Critique: Store failed. Inventory count for '%s' remained unchanged at %d.", intent.Target, bCount)

	case "retrieve":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount > bCount {
			return true, fmt.Sprintf("Success: Retrieved %s. State diff confirmed inventory increase.", intent.Target)
		}
		return false, fmt.Sprintf("Critique: Retrieve failed. GameState shows no increase in '%s' count.", intent.Target)

	case "eat":
		if after.Food > before.Food || after.Health > before.Health {
			return true, "Success: Metabolic state verified. Food/Health delta is positive."
		}
		return false, "Critique: Eat command failed. GameState metadata (Food/Health) shows no change."

	case "build":
		// Build consumes resources
		beforeTotal := 0
		for _, v := range beforeInv {
			beforeTotal += v
		}
		afterTotal := 0
		for _, v := range afterInv {
			afterTotal += v
		}
		if afterTotal < beforeTotal {
			return true, fmt.Sprintf("Success: Construction verified. Resources consumed: %d blocks.", beforeTotal-afterTotal)
		}
		return false, "Critique: Build failure. No resource consumption detected in GameState diff."

	case "explore", "retreat":
		dx := after.Position.X - before.Position.X
		dy := after.Position.Y - before.Position.Y
		dz := after.Position.Z - before.Position.Z
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)

		if dist > 3.0 {
			return true, fmt.Sprintf("Success: Displacement verified. Bot moved %.1f blocks from origin.", dist)
		}
		return false, "Critique: Stagnant position detected. GameState shows bot moved less than 3 blocks. Pathing may be obstructed."
	}

	return true, fmt.Sprintf("Success: %s executed. General state integrity maintained.", intent.Action)
}

func getSmeltOutput(input string) string {
	input = strings.ToLower(input)
	if strings.HasPrefix(input, "raw_") {
		return strings.TrimPrefix(input, "raw_") + "_ingot"
	}
	if strings.HasSuffix(input, "_ore") {
		return strings.TrimSuffix(input, "_ore") + "_ingot"
	}
	if strings.HasSuffix(input, "_log") || strings.HasSuffix(input, "_wood") {
		return "charcoal"
	}
	switch input {
	case "cobblestone":
		return "stone"
	case "stone":
		return "smooth_stone"
	case "sand", "red_sand":
		return "glass"
	case "beef":
		return "cooked_beef"
	case "porkchop":
		return "cooked_porkchop"
	case "chicken":
		return "cooked_chicken"
	case "mutton":
		return "cooked_mutton"
	case "rabbit":
		return "cooked_rabbit"
	case "cod":
		return "cooked_cod"
	case "salmon":
		return "cooked_salmon"
	case "potato":
		return "baked_potato"
	case "kelp":
		return "dried_kelp"
	case "clay_ball":
		return "brick"
	case "cactus":
		return "green_dye"
	case "netherrack":
		return "nether_brick"
	}
	if !strings.HasPrefix(input, "cooked_") && (input == "beef" || input == "porkchop" || input == "chicken" || input == "mutton" || input == "rabbit" || input == "cod" || input == "salmon") {
		return "cooked_" + input
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
