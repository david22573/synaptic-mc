// internal/pipeline/validate.go
package pipeline

import (
	"context"
	"fmt"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

// ValidateStage mechanically verifies if the normalized plan is physically possible.
// It performs a pure forward-simulation of the inventory state.
type ValidateStage struct{}

func NewValidateStage() *ValidateStage {
	return &ValidateStage{}
}

func (s *ValidateStage) Process(ctx context.Context, state *PipelineState) error {
	if state.Normalized == nil || len(state.Normalized.Tasks) == 0 {
		state.Validation = &ValidationResult{IsValid: false, Errors: []error{fmt.Errorf("empty normalized plan")}}
		return nil
	}

	// Create local shadow inventory
	simInv := make(map[string]int)
	for _, item := range state.GameState.Inventory {
		simInv[item.Name] += item.Count
	}

	var validationErrors []error

	for i, task := range state.Normalized.Tasks {
		if err := validateTaskPure(task, state.GameState, simInv); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("task %d (%s) rejected: %w", i+1, task.Action, err))
			break // Fast fail the chain on the first physically impossible task
		}

		// Simulate state mutation for the next tasks in the chain
		switch task.Action {
		case "craft", "gather", "mine":
			simInv[task.Target.Name]++
		case "eat":
			simInv[task.Target.Name]--
		}
	}

	state.Validation = &ValidationResult{
		IsValid: len(validationErrors) == 0,
		Errors:  validationErrors,
	}
	return nil
}

func validateTaskPure(task domain.Action, gameState domain.GameState, simInv map[string]int) error {
	switch task.Action {
	case "explore":
		if task.Target.Name != "none" {
			return fmt.Errorf("explore action must have target 'none'")
		}
	case "eat":
		if simInv[task.Target.Name] <= 0 {
			return fmt.Errorf("cannot eat %s: not in inventory", task.Target.Name)
		}
	case "mine":
		isHardBlock := strings.Contains(task.Target.Name, "stone") ||
			strings.Contains(task.Target.Name, "coal") ||
			strings.Contains(task.Target.Name, "iron")

		if isHardBlock {
			hasPick := simInv["wooden_pickaxe"] > 0 || simInv["stone_pickaxe"] > 0 ||
				simInv["iron_pickaxe"] > 0
			if !hasPick {
				return fmt.Errorf("mining %s requires a pickaxe", task.Target.Name)
			}
		}
		fallthrough // mining shares spatial checks with gathering
	case "gather":
		visible := false
		for _, poi := range gameState.POIs {
			if strings.Contains(poi.Name, task.Target.Name) ||
				strings.Contains(task.Target.Name, poi.Name) {
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
				return fmt.Errorf("cannot craft planks without logs")
			}
		}
	}
	return nil
}
