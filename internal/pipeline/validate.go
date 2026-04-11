package pipeline

import (
	"context"
	"fmt"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type ValidateStage struct{}

func NewValidateStage() *ValidateStage {
	return &ValidateStage{}
}

func (s *ValidateStage) Name() string {
	return "Validate_Physics_And_State"
}

func (s *ValidateStage) Process(ctx context.Context, input PipelineState) (PipelineState, error) {
	output := input

	if input.Normalized == nil || len(input.Normalized.Tasks) == 0 {
		output.Validation = &ValidationResult{IsValid: false, Errors: []error{fmt.Errorf("empty normalized plan")}}
		return output, nil
	}

	simInv := make(map[string]int)
	for _, item := range input.GameState.Inventory {
		simInv[item.Name] += item.Count
	}

	var validationErrors []error
	spatialShifted := false

	for i, task := range input.Normalized.Tasks {
		// 1. Check logical state (Inventory, Tools, Recipes)
		if err := validateTaskPure(task, input.GameState, simInv); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("task %d (%s) logic rejected: %w", i+1, task.Action, err))
		}

		// 2. Check spatial reality (Perception)
		// Bypass if a previous task in the chain moved the bot
		if !spatialShifted {
			if err := validateSpatialReality(task, input); err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("task %d (%s) physics rejected: %w", i+1, task.Action, err))
			}
		}

		// Simulate changes for chained tasks
		switch task.Action {
		case "explore", "retreat":
			spatialShifted = true
		case "gather", "mine":
			simInv[task.Target.Name]++
		case "eat":
			simInv[task.Target.Name]--
		case "smelt":
			for k, v := range simInv {
				if v > 0 && (k == "raw_iron" || k == "iron_ore" || k == "raw_gold" || k == "porkchop" || k == "beef" || k == "cobblestone" || k == "sand" || strings.HasSuffix(k, "_log")) {
					simInv[k]--
					break
				}
			}
		}
	}

	output.Validation = &ValidationResult{
		IsValid: len(validationErrors) == 0,
		Errors:  validationErrors,
	}
	return output, nil
}

func validateSpatialReality(task domain.Action, input PipelineState) error {
	switch task.Action {
	case "mine", "gather", "hunt", "farm":
		if input.Perception == nil || len(input.Perception.RankedPOIs) == 0 {
			return fmt.Errorf("cannot %s %s: blindness/no POIs detected in local chunk", task.Action, task.Target.Name)
		}

		target := strings.ToLower(task.Target.Name)
		found := false

		for _, poi := range input.Perception.RankedPOIs {
			poiName := strings.ToLower(poi.Name)
			if strings.Contains(poiName, target) || strings.Contains(target, poiName) {
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("target '%s' is not in visual range. Must use 'explore' action first to find it", task.Target.Name)
		}

	case "retrieve", "store":
		target := strings.ToLower(task.Target.Name)
		foundChest := false

		for _, poi := range input.Perception.RankedPOIs {
			if strings.Contains(strings.ToLower(poi.Name), "chest") && poi.Distance < 6.0 {
				foundChest = true
				break
			}
		}

		if !foundChest && len(input.GameState.KnownChests) == 0 {
			return fmt.Errorf("cannot %s %s: no chests are nearby or known", task.Action, target)
		}
	}
	return nil
}

func validateTaskPure(task domain.Action, gameState domain.GameState, simInv map[string]int) error {
	target := strings.ToLower(task.Target.Name)
	hasCraftingTable := simInv["crafting_table"] > 0
	for _, poi := range gameState.POIs {
		if strings.Contains(strings.ToLower(poi.Name), "crafting_table") && poi.Distance < 5.0 {
			hasCraftingTable = true
			break
		}
	}

	switch task.Action {
	case "explore", "retreat", "idle":
		return nil
	case "eat":
		if simInv[task.Target.Name] <= 0 && !domain.IsFood(task.Target.Name) {
			return fmt.Errorf("cannot eat %s: not in inventory", task.Target.Name)
		}
		// If target is "best_food", we should check if ANY food exists
		if task.Target.Name == "best_food" {
			foundFood := false
			for k, v := range simInv {
				if v > 0 && domain.IsFood(k) {
					foundFood = true
					break
				}
			}
			if !foundFood {
				return fmt.Errorf("cannot eat: no food found in inventory")
			}
		}
	case "smelt":
		hasValidInput := false
		hasFuel := false
		validSmeltInputs := []string{
			"raw_iron", "iron_ore", "raw_gold", "gold_ore", "raw_copper", "copper_ore",
			"beef", "porkchop", "mutton", "chicken", "rabbit", "cod", "salmon",
			"sand", "red_sand", "cobblestone", "stone", "potato", "kelp", "clay_ball", "cactus", "netherrack",
		}

		for k, v := range simInv {
			if v > 0 {
				for _, input := range validSmeltInputs {
					if k == input || strings.HasSuffix(k, "_log") {
						hasValidInput = true
						break
					}
				}
				if k == "coal" || k == "charcoal" || strings.HasSuffix(k, "_log") || strings.HasSuffix(k, "_planks") || k == "blaze_rod" {
					hasFuel = true
				}
			}
		}

		if !hasValidInput {
			return fmt.Errorf("cannot smelt: no valid smeltable input found in inventory")
		}
		if !hasFuel {
			return fmt.Errorf("cannot smelt: no valid fuel found in inventory")
		}

	case "mine", "gather":
		isHardBlock := strings.Contains(target, "stone") || strings.Contains(target, "coal") || strings.Contains(target, "iron") || strings.Contains(target, "gold") || strings.Contains(target, "diamond") || strings.Contains(target, "emerald") || strings.Contains(target, "redstone") || strings.Contains(target, "lapis")

		if task.Action == "mine" && isHardBlock {
			hasPick := simInv["wooden_pickaxe"] > 0 || simInv["stone_pickaxe"] > 0 || simInv["iron_pickaxe"] > 0 || simInv["diamond_pickaxe"] > 0 || simInv["netherite_pickaxe"] > 0
			if !hasPick {
				return fmt.Errorf("mining %s requires a pickaxe", task.Target.Name)
			}
		}
	case "craft":
		if strings.HasSuffix(target, "_planks") {
			hasLogs := false
			for k, v := range simInv {
				if strings.HasSuffix(k, "_log") && v > 0 {
					hasLogs = true
					break
				}
			}
			if !hasLogs {
				return fmt.Errorf("cannot craft planks without logs in inventory")
			}
		} else if target == "stick" {
			hasPlanks := false
			for k, v := range simInv {
				if strings.HasSuffix(k, "_planks") && v >= 2 {
					hasPlanks = true
					break
				}
			}
			if !hasPlanks {
				return fmt.Errorf("cannot craft sticks without at least 2 planks")
			}
		} else if strings.Contains(target, "pickaxe") || strings.Contains(target, "sword") || strings.Contains(target, "axe") || strings.Contains(target, "shovel") || strings.Contains(target, "hoe") {
			if !hasCraftingTable {
				return fmt.Errorf("crafting %s requires a crafting table nearby or in inventory", target)
			}
		} else if target == "crafting_table" {
			hasPlanks := false
			for k, v := range simInv {
				if strings.HasSuffix(k, "_planks") && v >= 4 {
					hasPlanks = true
					break
				}
			}
			if !hasPlanks {
				return fmt.Errorf("cannot craft crafting_table without at least 4 planks")
			}
		}
	}
	return nil
}
