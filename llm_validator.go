package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type PlanValidator struct{}

func NewPlanValidator() *PlanValidator {
	return &PlanValidator{}
}

func (v *PlanValidator) ValidatePlan(plan *LLMPlan, rawState json.RawMessage) error {
	if plan == nil {
		return errors.New("plan is nil")
	}

	var state GameState
	if err := json.Unmarshal(rawState, &state); err != nil {
		return fmt.Errorf("failed to parse state for validation: %w", err)
	}

	// Map inventory for O(1) lookups
	inv := make(map[string]int)
	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
	}

	// Truncate rather than reject if the LLM spits out too many tasks
	if len(plan.Tasks) > 3 {
		plan.Tasks = plan.Tasks[:3]
	}

	for i, task := range plan.Tasks {
		if task.Action == "" {
			return fmt.Errorf("task %d is missing an action", i)
		}
		if task.Target.Type == "" || task.Target.Name == "" {
			return fmt.Errorf("task %d '%s' is missing target type or name", i, task.Action)
		}
		if task.Rationale == "" {
			return fmt.Errorf("task %d '%s' is missing a rationale", i, task.Action)
		}

		// --- GAME LOGIC VALIDATION ---

		switch task.Action {
		case string(ActionExplore):
			if task.Target.Name != "none" {
				return fmt.Errorf("explore action must have target name 'none', got '%s'", task.Target.Name)
			}

		case string(ActionMine):
			target := task.Target.Name
			if strings.Contains(target, "stone") || strings.Contains(target, "coal") || strings.Contains(target, "iron") {
				hasPick := inv["wooden_pickaxe"] > 0 || inv["stone_pickaxe"] > 0 || inv["iron_pickaxe"] > 0
				if !hasPick {
					return fmt.Errorf("invalid action: cannot mine %s without a pickaxe in inventory", target)
				}
			}

		case string(ActionCraft):
			target := task.Target.Name
			if target == "planks" || strings.HasSuffix(target, "_planks") {
				hasLog := false
				for k, v := range inv {
					if strings.HasSuffix(k, "_log") && v > 0 {
						hasLog = true
						break
					}
				}
				if !hasLog {
					return errors.New("invalid action: cannot craft planks without logs in inventory")
				}
			}
			if target == "stick" {
				hasPlanks := false
				for k, v := range inv {
					if strings.HasSuffix(k, "_planks") && v > 0 {
						hasPlanks = true
						break
					}
				}
				if !hasPlanks {
					return errors.New("invalid action: cannot craft sticks without planks in inventory")
				}
			}

		case string(ActionEat):
			if inv[task.Target.Name] == 0 {
				return fmt.Errorf("invalid action: cannot eat %s because it is not in your inventory", task.Target.Name)
			}
		}
	}

	return nil
}
