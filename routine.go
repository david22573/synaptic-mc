package main

import (
	"fmt"
	"strings"
	"time"
)

type RoutineManager interface {
	Evaluate(state GameState, inFlightTask *Action, taskQueue []Action) []Action
}

type DefaultRoutineManager struct{}

func NewDefaultRoutineManager() *DefaultRoutineManager {
	return &DefaultRoutineManager{}
}

func (r *DefaultRoutineManager) Evaluate(state GameState, inFlightTask *Action, taskQueue []Action) []Action {
	var routines []Action

	hasRoutineTask := func(action, targetName string) bool {
		if inFlightTask != nil && inFlightTask.Action == action && inFlightTask.Target.Name == targetName {
			return true
		}
		for _, t := range taskQueue {
			if t.Action == action && t.Target.Name == targetName {
				return true
			}
		}
		return false
	}

	// 1. Sleep Routine
	if state.TimeOfDay > 12541 && state.TimeOfDay < 23000 {
		if state.HasBedNearby && !hasRoutineTask("sleep", "bed") {
			routines = append(routines, Action{
				ID:        fmt.Sprintf("routine-sleep-%d", time.Now().UnixNano()),
				Action:    "sleep",
				Target:    Target{Type: "block", Name: "bed"},
				Rationale: "Mandatory daily routine: Sleep to skip the night",
				Priority:  PriRoutine,
			})
		}
	}

	// 2. Inventory Parsing
	hasCraftingTable := false
	hasFurnace := false
	rawMeatCount := 0
	fuelCount := 0
	plankCount := 0
	cobbleCount := 0
	logName := ""

	for _, item := range state.Inventory {
		switch item.Name {
		case "crafting_table":
			hasCraftingTable = true
		case "furnace":
			hasFurnace = true
		case "cobblestone":
			cobbleCount += item.Count
		case "beef", "porkchop", "mutton", "chicken", "rabbit":
			rawMeatCount += item.Count
		case "coal", "charcoal":
			fuelCount += item.Count
		}

		if strings.HasSuffix(item.Name, "_planks") {
			plankCount += item.Count
			fuelCount += item.Count
		}
		if strings.HasSuffix(item.Name, "_log") {
			logName = item.Name
		}
	}

	// 3. Mandatory Tool Routines
	if !hasCraftingTable && plankCount < 4 && logName != "" {
		plankTarget := strings.Replace(logName, "_log", "_planks", 1)
		if !hasRoutineTask("craft", plankTarget) {
			routines = append(routines, Action{
				ID:        fmt.Sprintf("routine-craft-planks-%d", time.Now().UnixNano()),
				Action:    "craft",
				Target:    Target{Type: "recipe", Name: plankTarget},
				Rationale: "Routine: Auto-crafting logs into planks to enable tool crafting",
				Priority:  PriRoutine,
			})
		}
	}

	if !hasCraftingTable && plankCount >= 4 && !hasRoutineTask("craft", "crafting_table") {
		routines = append(routines, Action{
			ID:        fmt.Sprintf("routine-craft-table-%d", time.Now().UnixNano()),
			Action:    "craft",
			Target:    Target{Type: "recipe", Name: "crafting_table"},
			Rationale: "Mandatory tool missing: Auto-crafting since we have planks",
			Priority:  PriRoutine,
		})
	}

	if !hasFurnace && cobbleCount >= 8 && !hasRoutineTask("craft", "furnace") {
		routines = append(routines, Action{
			ID:        fmt.Sprintf("routine-craft-furnace-%d", time.Now().UnixNano()),
			Action:    "craft",
			Target:    Target{Type: "recipe", Name: "furnace"},
			Rationale: "Mandatory tool missing: Auto-crafting since we have cobblestone",
			Priority:  PriRoutine,
		})
	}

	// 4. Auto-Cooking Routine
	if hasFurnace && rawMeatCount > 0 && fuelCount > 0 && !hasRoutineTask("smelt", "food") {
		routines = append(routines, Action{
			ID:        fmt.Sprintf("routine-smelt-%d", time.Now().UnixNano()),
			Action:    "smelt",
			Target:    Target{Type: "category", Name: "food"},
			Rationale: "Routine: Cooking raw food to restore hunger safely",
			Priority:  PriRoutine,
		})
	}

	return routines
}
