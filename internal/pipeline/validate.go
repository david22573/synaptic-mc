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
	return "Validate"
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

	for i, task := range input.Normalized.Tasks {
		if err := validateTaskPure(task, input.GameState, simInv); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("task %d (%s) rejected: %w", i+1, task.Action, err))
		}

		switch task.Action {
		case "gather", "mine":
			simInv[task.Target.Name]++
		case "eat":
			simInv[task.Target.Name]--
		case "smelt":
			var consumedInput string
			validMeats := []string{"beef", "porkchop", "mutton", "chicken", "rabbit"}

			for k, v := range simInv {
				if v > 0 {
					if strings.HasPrefix(k, "raw_") {
						simInv[k]--
						consumedInput = k
						break
					}
					isMeat := false
					for _, m := range validMeats {
						if k == m {
							isMeat = true
							break
						}
					}
					if isMeat {
						simInv[k]--
						consumedInput = k
						break
					}
				}
			}

			fuelConsumed := false
			for _, f := range []string{"coal", "charcoal"} {
				if simInv[f] > 0 {
					simInv[f]--
					fuelConsumed = true
					break
				}
			}
			if !fuelConsumed {
				for k, v := range simInv {
					if v > 0 && (strings.HasSuffix(k, "_log") || strings.HasSuffix(k, "_planks")) {
						simInv[k]--
						break
					}
				}
			}

			if consumedInput != "" {
				if strings.HasPrefix(consumedInput, "raw_") {
					base := strings.TrimPrefix(consumedInput, "raw_")
					simInv[base+"_ingot"]++
				} else {
					simInv["cooked_"+consumedInput]++
				}
			}

		case "craft":
			target := strings.ToLower(task.Target.Name)
			if strings.HasSuffix(target, "_planks") {
				for k := range simInv {
					if strings.HasSuffix(k, "_log") && simInv[k] > 0 {
						simInv[k]--
						break
					}
				}
				simInv[target] += 4
			} else if target == "stick" {
				for k := range simInv {
					if strings.HasSuffix(k, "_planks") && simInv[k] >= 2 {
						simInv[k] -= 2
						break
					}
				}
				simInv["stick"] += 4
			} else if target == "crafting_table" {
				for k := range simInv {
					if strings.HasSuffix(k, "_planks") && simInv[k] >= 4 {
						simInv[k] -= 4
						break
					}
				}
				simInv[target]++
			} else if target == "wooden_pickaxe" {
				simInv["stick"] -= 2
				for k := range simInv {
					if strings.HasSuffix(k, "_planks") && simInv[k] >= 3 {
						simInv[k] -= 3
						break
					}
				}
				simInv[target]++
			} else if target == "stone_pickaxe" {
				simInv["stick"] -= 2
				if simInv["cobblestone"] >= 3 {
					simInv["cobblestone"] -= 3
				}
				simInv[target]++
			} else {
				simInv[target]++
			}
		}
	}

	output.Validation = &ValidationResult{
		IsValid: len(validationErrors) == 0,
		Errors:  validationErrors,
	}
	return output, nil
}

func validateTaskPure(task domain.Action, gameState domain.GameState, simInv map[string]int) error {
	switch task.Action {
	case "explore":
		return nil
	case "eat":
		if simInv[task.Target.Name] <= 0 {
			return fmt.Errorf("cannot eat %s: not in inventory", task.Target.Name)
		}
	case "smelt":
		hasRawMeat := false
		hasFuel := false
		validMeats := []string{"beef", "porkchop", "mutton", "chicken", "rabbit"}

		for k, v := range simInv {
			if v > 0 {
				if strings.HasPrefix(k, "raw_") {
					hasRawMeat = true
				} else {
					for _, meat := range validMeats {
						if k == meat {
							hasRawMeat = true
							break
						}
					}
				}

				if k == "coal" || k == "charcoal" || strings.HasSuffix(k, "_log") || strings.HasSuffix(k, "_planks") {
					hasFuel = true
				}
			}
		}

		if !hasRawMeat {
			return fmt.Errorf("cannot smelt: no raw meat or raw ores found in inventory")
		}
		if !hasFuel {
			return fmt.Errorf("cannot smelt: no valid fuel (coal, charcoal, logs, or planks) found in inventory")
		}

	case "mine", "gather":
		target := strings.ToLower(task.Target.Name)

		if target == "item" || target == "block" || target == "none" || target == "" || target == "inventory" {
			return fmt.Errorf("target must be a specific resource (e.g., 'oak_log', 'dirt'), got generic '%s'", task.Target.Name)
		}

		isHardBlock := strings.Contains(target, "stone") || strings.Contains(target, "coal") || strings.Contains(target, "iron")

		if task.Action == "mine" && isHardBlock {
			hasPick := simInv["wooden_pickaxe"] > 0 || simInv["stone_pickaxe"] > 0 || simInv["iron_pickaxe"] > 0
			if !hasPick {
				return fmt.Errorf("mining %s requires a pickaxe", task.Target.Name)
			}
		}
	case "craft":
		target := strings.ToLower(task.Target.Name)

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
		} else if target == "wooden_pickaxe" {
			hasPlanks := false
			for k, v := range simInv {
				if strings.HasSuffix(k, "_planks") && v >= 3 {
					hasPlanks = true
					break
				}
			}
			if !hasPlanks || simInv["stick"] < 2 {
				return fmt.Errorf("wooden_pickaxe requires at least 3 planks and 2 sticks in inventory")
			}
			if simInv["crafting_table"] <= 0 {
				return fmt.Errorf("wooden_pickaxe requires a crafting_table in inventory to place/use")
			}
		} else if target == "stone_pickaxe" {
			if simInv["cobblestone"] < 3 || simInv["stick"] < 2 {
				return fmt.Errorf("stone_pickaxe requires at least 3 cobblestone and 2 sticks in inventory")
			}
			if simInv["crafting_table"] <= 0 {
				return fmt.Errorf("stone_pickaxe requires a crafting_table in inventory to place/use")
			}
		}
	}
	return nil
}
