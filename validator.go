package main

import (
	"fmt"
	"strings"
)

type Severity int

const (
	SeverityNone     Severity = iota
	SeverityAdvisory          // Skip this task, move to the next one in the queue
	SeverityBlocking          // Abort the plan, tell the LLM to fix it
	SeverityCritical          // Wipe everything, force a massive strategy shift
)

type ValidationResult struct {
	Valid    bool
	Severity Severity
	Reason   string
	FixHint  string
}

// ValidatePlan runs a lightweight forward simulation of the action chain
func ValidatePlan(plan []Action, state GameState) ValidationResult {
	simInv := make(map[string]int)
	for _, item := range state.Inventory {
		simInv[item.Name] += item.Count
	}

	for i, action := range plan {
		res := validateActionContext(action, state, simInv)
		if !res.Valid {
			// Elevate everything to blocking at the plan level so it replans immediately
			res.Severity = SeverityBlocking
			res.Reason = fmt.Sprintf("Step %d (%s) is invalid: %s", i+1, action.Action, res.Reason)
			return res
		}

		// Simulate state changes for the next step in the chain
		if action.Action == "craft" {
			simInv[action.Target.Name]++
		} else if action.Action == "gather" || action.Action == "mine" {
			simInv[action.Target.Name]++
		}
	}
	return ValidationResult{Valid: true, Severity: SeverityNone}
}

// ValidateAction checks the immediate task right before execution
func ValidateAction(action Action, state GameState) ValidationResult {
	simInv := make(map[string]int)
	for _, item := range state.Inventory {
		simInv[item.Name] += item.Count
	}
	return validateActionContext(action, state, simInv)
}

func validateActionContext(a Action, s GameState, simInv map[string]int) ValidationResult {
	switch a.Action {
	case "gather", "mine":
		return validateGather(a, s, simInv)
	case "craft":
		return validateCraft(a, s, simInv)
	case "interact":
		return validateInteract(a, s)
	default:
		return ValidationResult{Valid: true, Severity: SeverityNone}
	}
}

func validateGather(a Action, s GameState, simInv map[string]int) ValidationResult {
	// 1. Redundancy Check (Advisory)
	if count, ok := simInv[a.Target.Name]; ok && count >= 32 {
		return ValidationResult{
			Valid:    false,
			Severity: SeverityAdvisory,
			Reason:   fmt.Sprintf("already have %d of %s", count, a.Target.Name),
			FixHint:  "skip gathering and move to the next task",
		}
	}

	// 2. Spatial/POI Awareness (Blocking)
	targetVisible := false
	for _, poi := range s.POIs {
		if strings.Contains(poi.Name, a.Target.Name) || strings.Contains(a.Target.Name, poi.Name) {
			targetVisible = true
			break
		}
	}

	// Handle generic "wood" requests mapping to specific logs
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
			FixHint:  "use 'explore' action to locate it first",
		}
	}

	// 3. Tool Requirements (Blocking)
	if a.Target.Name == "stone" || a.Target.Name == "coal_ore" || a.Target.Name == "iron_ore" {
		hasPick := false
		for k, v := range simInv {
			if strings.Contains(k, "pickaxe") && v > 0 {
				hasPick = true
				break
			}
		}
		if !hasPick {
			return ValidationResult{
				Valid:    false,
				Severity: SeverityBlocking,
				Reason:   fmt.Sprintf("mining %s requires a pickaxe", a.Target.Name),
				FixHint:  "craft a wooden_pickaxe first",
			}
		}
	}

	return ValidationResult{Valid: true, Severity: SeverityNone}
}

func validateCraft(a Action, s GameState, simInv map[string]int) ValidationResult {
	// Redundancy: Stop making multiple crafting tables or tools we already have
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
	return ValidationResult{Valid: true, Severity: SeverityNone}
}

func validateInteract(a Action, s GameState) ValidationResult {
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
			FixHint:  "move closer or place it first",
		}
	}
	return ValidationResult{Valid: true, Severity: SeverityNone}
}
