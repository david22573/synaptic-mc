package voyager

import (
	"fmt"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type Critic interface {
	Evaluate(intent domain.ActionIntent, before, after domain.GameState) (bool, string)
}

type StateCritic struct{}

func NewStateCritic() *StateCritic {
	return &StateCritic{}
}

func (c *StateCritic) Evaluate(intent domain.ActionIntent, before, after domain.GameState) (bool, string) {
	if after.Health <= 0 {
		return false, "Critique: Bot died while executing the task. Re-evaluate threat assessment."
	}

	switch intent.Action {
	case "mine", "gather":
		bCount := countItem(before.Inventory, intent.Target)
		aCount := countItem(after.Inventory, intent.Target)

		if aCount >= bCount+intent.Count {
			return true, fmt.Sprintf("Success: Gathered %d %s. Inventory went from %d to %d.", aCount-bCount, intent.Target, bCount, aCount)
		}
		return false, fmt.Sprintf("Critique: Expected to gather %d %s, but inventory went from %d to %d. The bot may have gotten stuck, the tool might have broken, or the item fell into lava/unreachable area.", intent.Count, intent.Target, bCount, aCount)

	case "craft":
		bCount := countItem(before.Inventory, intent.Target)
		aCount := countItem(after.Inventory, intent.Target)

		if aCount > bCount {
			return true, fmt.Sprintf("Success: Crafted %s.", intent.Target)
		}
		return false, fmt.Sprintf("Critique: Failed to craft %s. Ensure ingredients are actually in the inventory and a crafting table is nearby if required.", intent.Target)

	case "smelt":
		expectedOutput := getSmeltOutput(intent.Target)
		bCount := countItem(before.Inventory, expectedOutput)
		aCount := countItem(after.Inventory, expectedOutput)

		if aCount > bCount {
			return true, fmt.Sprintf("Success: Smelted %s into %s.", intent.Target, expectedOutput)
		}
		return false, fmt.Sprintf("Critique: Failed to smelt %s. Ensure fuel is in inventory and a furnace is reachable.", intent.Target)

	case "hunt":
		if after.Health < before.Health && after.Health < 10 {
			return true, "Warning: Survived the hunt, but took heavy damage. Consider eating or crafting better armor."
		}
		return true, "Success: Engaged and survived the hunt."
	}

	return true, fmt.Sprintf("Success: %s completed natively without strict state diff violations.", intent.Action)
}

func countItem(inv []domain.Item, name string) int {
	for _, item := range inv {
		if strings.EqualFold(item.Name, name) {
			return item.Count
		}
	}
	return 0
}

func getSmeltOutput(input string) string {
	if strings.HasPrefix(input, "raw_") {
		return strings.TrimPrefix(input, "raw_") + "_ingot"
	}
	return "cooked_" + input
}
