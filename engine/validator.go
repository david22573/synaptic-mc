package engine

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Severity int

const (
	SeverityNone Severity = iota
	SeverityAdvisory
	SeverityBlocking
	SeverityCritical
)

type ValidationResult struct {
	Valid    bool
	Severity Severity
	Reason   string
	FixHint  string
}

type Validator struct{}

func NewValidator() *Validator {
	return &Validator{}
}

// ValidateLLMPlan enforces schema, truncates excessive tasks, and runs the forward simulation.
// This replaces the old llm_validator.go logic.
func (v *Validator) ValidateLLMPlan(plan *LLMPlan, rawState json.RawMessage) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}

	var state GameState
	if err := json.Unmarshal(rawState, &state); err != nil {
		return fmt.Errorf("failed to parse state for validation: %w", err)
	}

	// Truncate rather than reject if the LLM spits out too many tasks
	if len(plan.Tasks) > 3 {
		plan.Tasks = plan.Tasks[:3]
	}

	// Basic schema checks
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
	}

	// Run the unified forward simulation
	res := v.ValidateActionChain(plan.Tasks, state)
	if !res.Valid {
		return fmt.Errorf("%s. %s", res.Reason, res.FixHint)
	}

	return nil
}

// ValidateActionChain simulates state changes across a sequence of actions.
func (v *Validator) ValidateActionChain(plan []Action, state GameState) ValidationResult {
	simInv := make(map[string]int)
	for _, item := range state.Inventory {
		simInv[item.Name] += item.Count
	}

	for i, action := range plan {
		res := v.validateSingle(action, state, simInv)
		if !res.Valid {
			res.Severity = SeverityBlocking
			res.Reason = fmt.Sprintf("Step %d (%s) is invalid: %s", i+1, action.Action, res.Reason)
			return res
		}

		// Simulate state mutation for the next tasks in the chain
		if action.Action == string(ActionCraft) || action.Action == string(ActionGather) || action.Action == string(ActionMine) {
			simInv[action.Target.Name]++
		}
	}
	return ValidationResult{Valid: true, Severity: SeverityNone}
}

// ValidateAction checks the immediate task right before execution.
func (v *Validator) ValidateAction(action Action, state GameState) ValidationResult {
	simInv := make(map[string]int)
	for _, item := range state.Inventory {
		simInv[item.Name] += item.Count
	}
	return v.validateSingle(action, state, simInv)
}

func (v *Validator) validateSingle(a Action, s GameState, simInv map[string]int) ValidationResult {
	switch ActionType(a.Action) {
	case ActionExplore:
		if a.Target.Name != "none" {
			return ValidationResult{
				Valid:    false,
				Severity: SeverityBlocking,
				Reason:   fmt.Sprintf("explore action must have target name 'none', got '%s'", a.Target.Name),
				FixHint:  "Change target name to 'none'.",
			}
		}
	case ActionGather, ActionMine:
		return v.validateGatherMine(a, s, simInv)
	case ActionCraft:
		return v.validateCraft(a, s, simInv)
	case ActionEat:
		return v.validateEat(a, simInv)
	case ActionInteract:
		return v.validateInteract(a, s)
	}
	return ValidationResult{Valid: true, Severity: SeverityNone}
}

func (v *Validator) validateGatherMine(a Action, s GameState, simInv map[string]int) ValidationResult {
	// 1. Redundancy Check
	if count, ok := simInv[a.Target.Name]; ok && count >= 32 {
		return ValidationResult{
			Valid:    false,
			Severity: SeverityAdvisory,
			Reason:   fmt.Sprintf("already have %d of %s", count, a.Target.Name),
			FixHint:  "skip gathering and move to the next task",
		}
	}

	// 2. Spatial/POI Awareness
	targetVisible := false
	for _, poi := range s.POIs {
		if strings.Contains(poi.Name, a.Target.Name) || strings.Contains(a.Target.Name, poi.Name) {
			targetVisible = true
			break
		}
	}

	if a.Target.Name == "wood" || strings.HasSuffix(a.Target.Name, "_log") {
		for _, poi := range s.POIs {
			if strings.HasSuffix(poi.Name, "_log") {
				targetVisible = true
				break
			}
		}
	}

	if !targetVisible {
		return ValidationResult{
			Valid:    false,
			Severity: SeverityBlocking,
			Reason:   fmt.Sprintf("target '%s' is not in visual range", a.Target.Name),
			FixHint:  "use 'recall_location' if it's in the KNOWN WORLD, otherwise 'explore'",
		}
	}

	// 3. Tool Requirements
	if a.Action == string(ActionMine) && (strings.Contains(a.Target.Name, "stone") || strings.Contains(a.Target.Name, "coal") || strings.Contains(a.Target.Name, "iron")) {
		hasPick := simInv["wooden_pickaxe"] > 0 || simInv["stone_pickaxe"] > 0 || simInv["iron_pickaxe"] > 0 || simInv["diamond_pickaxe"] > 0
		if !hasPick {
			return ValidationResult{
				Valid:    false,
				Severity: SeverityBlocking,
				Reason:   fmt.Sprintf("mining %s requires a pickaxe", a.Target.Name),
				FixHint:  "craft a pickaxe first",
			}
		}
	}

	return ValidationResult{Valid: true, Severity: SeverityNone}
}

func (v *Validator) validateCraft(a Action, s GameState, simInv map[string]int) ValidationResult {
	if a.Target.Name == "crafting_table" && simInv["crafting_table"] > 0 {
		return ValidationResult{
			Valid:    false,
			Severity: SeverityAdvisory,
			Reason:   "already have a crafting_table in inventory",
			FixHint:  "skip crafting and place/use the existing one",
		}
	}
	if strings.Contains(a.Target.Name, "pickaxe") && simInv[a.Target.Name] > 0 {
		return ValidationResult{
			Valid:    false,
			Severity: SeverityAdvisory,
			Reason:   fmt.Sprintf("already have a %s", a.Target.Name),
			FixHint:  "skip crafting and use the existing tool",
		}
	}

	target := a.Target.Name
	if target == "planks" || strings.HasSuffix(target, "_planks") {
		hasLog := false
		for k, val := range simInv {
			if strings.HasSuffix(k, "_log") && val > 0 {
				hasLog = true
				break
			}
		}
		if !hasLog {
			return ValidationResult{
				Valid:    false,
				Severity: SeverityBlocking,
				Reason:   "cannot craft planks without logs",
				FixHint:  "gather logs first",
			}
		}
	}

	if target == "stick" {
		hasPlanks := false
		for k, val := range simInv {
			if strings.HasSuffix(k, "_planks") && val > 0 {
				hasPlanks = true
				break
			}
		}
		if !hasPlanks {
			return ValidationResult{
				Valid:    false,
				Severity: SeverityBlocking,
				Reason:   "cannot craft sticks without planks",
				FixHint:  "craft planks first",
			}
		}
	}

	return ValidationResult{Valid: true, Severity: SeverityNone}
}

func (v *Validator) validateEat(a Action, simInv map[string]int) ValidationResult {
	if simInv[a.Target.Name] == 0 {
		return ValidationResult{
			Valid:    false,
			Severity: SeverityBlocking,
			Reason:   fmt.Sprintf("cannot eat %s because it is not in your inventory", a.Target.Name),
			FixHint:  "gather or cook food first",
		}
	}
	return ValidationResult{Valid: true, Severity: SeverityNone}
}

func (v *Validator) validateInteract(a Action, s GameState) ValidationResult {
	targetVisible := false
	for _, poi := range s.POIs {
		if strings.Contains(poi.Name, a.Target.Name) || strings.Contains(a.Target.Name, poi.Name) {
			targetVisible = true
			break
		}
	}

	if !targetVisible {
		return ValidationResult{
			Valid:    false,
			Severity: SeverityBlocking,
			Reason:   fmt.Sprintf("target '%s' is not in visual range to interact with", a.Target.Name),
			FixHint:  "If it is in your KNOWN WORLD memory, use 'recall_location' to navigate to it first.",
		}
	}
	return ValidationResult{Valid: true, Severity: SeverityNone}
}
