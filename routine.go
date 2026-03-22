// routine.go
package main

import (
	"fmt"
	"strings"
	"time"
)

// Routine defines a modular check that can inject high-priority tasks.
type Routine interface {
	Name() string
	Check(state GameState, inFlight *Action, queue []Action) *Action
}

type RoutineManager interface {
	Evaluate(state GameState, inFlightTask *Action, taskQueue []Action) []Action
}

type DefaultRoutineManager struct {
	routines []Routine
}

func NewDefaultRoutineManager() *DefaultRoutineManager {
	return &DefaultRoutineManager{
		routines: []Routine{
			&CombatRoutine{}, // Re-added the Combat Routine
			&EatingRoutine{},
			&SleepRoutine{},
			&ToolingRoutine{},
			&CookingRoutine{},
			&WanderRoutine{},
		},
	}
}

func (r *DefaultRoutineManager) Evaluate(state GameState, inFlightTask *Action, taskQueue []Action) []Action {
	var results []Action
	for _, routine := range r.routines {
		if task := routine.Check(state, inFlightTask, taskQueue); task != nil {
			results = append(results, *task)
		}
	}
	return results
}

func isTaskActive(action, targetName string, inFlight *Action, queue []Action) bool {
	if inFlight != nil && inFlight.Action == action && inFlight.Target.Name == targetName {
		return true
	}
	for _, t := range queue {
		if t.Action == action && t.Target.Name == targetName {
			return true
		}
	}
	return false
}

// --- Routine Implementations ---

type CombatRoutine struct{}

func (c *CombatRoutine) Name() string { return "combat" }
func (c *CombatRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	if len(state.Threats) > 0 {
		topThreat := state.Threats[0].Name

		// Check if we have an axe or sword equipped/in inventory
		hasWeapon := false
		for _, item := range state.Inventory {
			if strings.Contains(item.Name, "axe") || strings.Contains(item.Name, "sword") {
				hasWeapon = true
				break
			}
		}

		// Engage ONLY if armed, healthy, facing exactly 1 threat, and it's not a suicide target
		if len(state.Threats) == 1 && hasWeapon && state.Health > 10 && topThreat != "creeper" && topThreat != "warden" {
			if !isTaskActive(string(ActionHunt), topThreat, inFlight, queue) {
				return &Action{
					ID:        fmt.Sprintf("routine-combat-%d", time.Now().UnixNano()),
					Source:    string(SourceRoutine),
					Action:    string(ActionHunt),
					Target:    Target{Type: string(TargetEntity), Name: topThreat},
					Rationale: "Tactical Engage: 1-on-1 combat advantage detected.",
					Priority:  PriReflex, // Instantly preempts gathering/mining
				}
			}
		}
	}
	return nil
}

type EatingRoutine struct{}

func (e *EatingRoutine) Name() string { return "eating" }
func (e *EatingRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	if state.Food >= 16 || isTaskActive(string(ActionEat), "food", inFlight, queue) {
		return nil
	}
	foodPriority := []string{"cooked_beef", "cooked_porkchop", "bread", "apple", "beef", "porkchop"}
	for _, f := range foodPriority {
		for _, inv := range state.Inventory {
			if inv.Name == f {
				return &Action{
					ID:        fmt.Sprintf("routine-eat-%d", time.Now().UnixNano()),
					Source:    string(SourceRoutine),
					Action:    string(ActionEat),
					Target:    Target{Type: string(TargetCategory), Name: f},
					Rationale: "Survival: Restoring hunger to enable healing.",
					Priority:  PriReflex,
				}
			}
		}
	}
	return nil
}

type SleepRoutine struct{}

func (s *SleepRoutine) Name() string { return "sleep" }
func (s *SleepRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	if state.TimeOfDay > 12541 && state.TimeOfDay < 23000 && state.HasBedNearby {
		if !isTaskActive(string(ActionSleep), "bed", inFlight, queue) {
			return &Action{
				ID:        fmt.Sprintf("routine-sleep-%d", time.Now().UnixNano()),
				Source:    string(SourceRoutine),
				Action:    string(ActionSleep),
				Target:    Target{Type: string(TargetBlock), Name: "bed"},
				Rationale: "Routine: Sleeping to skip the night.",
				Priority:  PriRoutine,
			}
		}
	}
	return nil
}

type ToolingRoutine struct{}

func (t *ToolingRoutine) Name() string { return "tooling" }
func (t *ToolingRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	inv := make(map[string]int)
	for _, item := range state.Inventory {
		inv[item.Name] = item.Count
	}

	// --- FIX 3 (Part B): Don't craft a table if one is already nearby ---
	if inv["crafting_table"] == 0 && !state.HasCraftingTableNearby && !isTaskActive(string(ActionCraft), "crafting_table", inFlight, queue) {
		plankCount := 0
		var logName string
		for name, count := range inv {
			if strings.HasSuffix(name, "_planks") {
				plankCount += count
			}
			if strings.HasSuffix(name, "_log") {
				logName = name
			}
		}

		if plankCount >= 4 {
			return &Action{
				ID:        fmt.Sprintf("routine-craft-table-%d", time.Now().UnixNano()),
				Source:    string(SourceRoutine),
				Action:    string(ActionCraft),
				Target:    Target{Type: string(TargetRecipe), Name: "crafting_table"},
				Rationale: "Progression: Crafting essential table.",
				Priority:  PriRoutine,
			}
		} else if logName != "" {
			target := strings.Replace(logName, "_log", "_planks", 1)
			return &Action{
				ID:        fmt.Sprintf("routine-craft-planks-%d", time.Now().UnixNano()),
				Source:    string(SourceRoutine),
				Action:    string(ActionCraft),
				Target:    Target{Type: string(TargetRecipe), Name: target},
				Rationale: "Progression: Converting logs to planks.",
				Priority:  PriRoutine,
			}
		}
	}
	return nil
}

type CookingRoutine struct{}

func (c *CookingRoutine) Name() string { return "cooking" }
func (c *CookingRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	hasFurnace, hasRaw, hasFuel := false, false, false
	for _, item := range state.Inventory {
		if item.Name == "furnace" {
			hasFurnace = true
		}
		if strings.Contains("beef porkchop mutton chicken rabbit", item.Name) {
			hasRaw = true
		}
		if strings.Contains("coal charcoal oak_planks", item.Name) {
			hasFuel = true
		}
	}

	if hasFurnace && hasRaw && hasFuel && !isTaskActive(string(ActionSmelt), "food", inFlight, queue) {
		return &Action{
			ID:        fmt.Sprintf("routine-smelt-%d", time.Now().UnixNano()),
			Source:    string(SourceRoutine),
			Action:    string(ActionSmelt),
			Target:    Target{Type: string(TargetCategory), Name: "food"},
			Rationale: "Efficiency: Smelting raw food for better nutrition.",
			Priority:  PriRoutine,
		}
	}
	return nil
}

type WanderRoutine struct{}

func (w *WanderRoutine) Name() string { return "wander" }
func (w *WanderRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	if inFlight == nil && len(queue) == 0 {
		return &Action{
			ID:        fmt.Sprintf("routine-wander-%d", time.Now().UnixNano()),
			Source:    string(SourceRoutine),
			Action:    string(ActionExplore),
			Target:    Target{Type: string(TargetNone), Name: "none"},
			Rationale: "Idle: Exploring the area to keep chunks loaded and discover resources.",
			Priority:  Priority(3),
		}
	}
	return nil
}
