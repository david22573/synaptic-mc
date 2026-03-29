package decision

import (
	"errors"
	"fmt"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

// RuleValidator ensures a plan is physically possible according to game mechanics.
// It performs a forward-simulation of the inventory state across the action chain.
type RuleValidator struct{}

func NewRuleValidator() *RuleValidator {
	return &RuleValidator{}
}

func (v *RuleValidator) Validate(plan *domain.Plan, state domain.GameState) error {
	if plan == nil || len(plan.Tasks) == 0 {
		return errors.New("empty plan")
	}

	// Create a local shadow copy of the inventory for forward simulation
	simInv := make(map[string]int)
	for _, item := range state.Inventory {
		simInv[item.Name] += item.Count
	}

	for i, task := range plan.Tasks {
		if err := v.validateTask(task, state, simInv); err != nil {
			return fmt.Errorf("task %d (%s) rejected: %w", i+1, task.Action, err)
		}

		// Simulate state mutation for the next tasks in the chain
		switch task.Action {
		case "craft", "gather", "mine":
			simInv[task.Target.Name]++
		case "eat":
			simInv[task.Target.Name]--
		}
	}

	return nil
}

func (v *RuleValidator) validateTask(task domain.Action, state domain.GameState, simInv map[string]int) error {
	switch task.Action {
	case "explore":
		if task.Target.Name != "none" {
			return errors.New("explore action must have target 'none'")
		}
	case "eat":
		if simInv[task.Target.Name] <= 0 {
			return fmt.Errorf("cannot eat %s: not in inventory", task.Target.Name)
		}
	case "mine":
		// Enforce tool requirements strictly
		isHardBlock := strings.Contains(task.Target.Name, "stone") ||
			strings.Contains(task.Target.Name, "coal") ||
			strings.Contains(task.Target.Name, "iron")

		if isHardBlock {
			hasPick := simInv["wooden_pickaxe"] > 0 || simInv["stone_pickaxe"] > 0 || simInv["iron_pickaxe"] > 0
			if !hasPick {
				return fmt.Errorf("mining %s requires a pickaxe", task.Target.Name)
			}
		}
		fallthrough // mining shares spatial checks with gathering
	case "gather":
		// Spatial awareness check: Is the target actually nearby?
		visible := false
		for _, poi := range state.POIs {
			if strings.Contains(poi.Name, task.Target.Name) || strings.Contains(task.Target.Name, poi.Name) {
				visible = true
				break
			}
		}
		if !visible && task.Target.Name != "wood" {
			return fmt.Errorf("target '%s' is not in visual range", task.Target.Name)
		}
	case "craft":
		if strings.HasSuffix(task.Target.Name, "_planks") {
			hasLogs := false
			for k, v := range simInv {
				if strings.HasSuffix(k, "_log") && v > 0 {
					hasLogs = true
					break
				}
			}
			if !hasLogs {
				return errors.New("cannot craft planks without logs")
			}
		}
	}
	return nil
}
