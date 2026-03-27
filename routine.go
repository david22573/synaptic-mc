package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type Routine interface {
	Name() string
	Check(state GameState, inFlight *Action, queue []Action) *Action
}

type RoutineManager interface {
	Evaluate(state GameState, inFlightTask *Action, taskQueue []Action) []Action
	RecordFailure(action, target string)
}

type DefaultRoutineManager struct {
	routines     []Routine
	failCooldown map[string]time.Time
	mu           sync.Mutex
}

func NewDefaultRoutineManager() *DefaultRoutineManager {
	return &DefaultRoutineManager{
		routines: []Routine{
			&CombatRoutine{},
			&EatingRoutine{},
			&SleepRoutine{},
			&ProgressionRoutine{},
			&CookingRoutine{},
			&WanderRoutine{},
		},
		failCooldown: make(map[string]time.Time),
	}
}

func (r *DefaultRoutineManager) RecordFailure(action, target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failCooldown[action+":"+target] = time.Now()
}

func (r *DefaultRoutineManager) isCoolingDown(action, target string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.failCooldown[action+":"+target]
	return ok && time.Since(t) < 30*time.Second
}

func (r *DefaultRoutineManager) Evaluate(state GameState, inFlightTask *Action, taskQueue []Action) []Action {
	var results []Action
	for _, routine := range r.routines {
		if task := routine.Check(state, inFlightTask, taskQueue); task != nil {
			if !r.isCoolingDown(task.Action, task.Target.Name) {
				results = append(results, *task)
			}
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

		hasWeapon := false
		for _, item := range state.Inventory {
			if strings.Contains(item.Name, "axe") || strings.Contains(item.Name, "sword") {
				hasWeapon = true
				break
			}
		}

		if len(state.Threats) == 1 && hasWeapon && state.Health > 10 && topThreat != "creeper" && topThreat != "warden" {
			if !isTaskActive(string(ActionHunt), topThreat, inFlight, queue) {
				return &Action{
					ID:        fmt.Sprintf("routine-combat-%d", time.Now().UnixNano()),
					Source:    string(SourceRoutine),
					Action:    string(ActionHunt),
					Target:    Target{Type: string(TargetEntity), Name: topThreat},
					Rationale: "Tactical Engage: 1-on-1 combat advantage detected.",
					Priority:  PriReflex,
				}
			}
		}
	}
	return nil
}

type EatingRoutine struct{}

func (e *EatingRoutine) Name() string { return "eating" }
func (e *EatingRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	// 15 = 7.5 drumsticks
	if state.Food >= 15 || isTaskActive(string(ActionEat), "food", inFlight, queue) {
		return nil
	}
	foodPriority := []string{"cooked_beef", "cooked_porkchop", "bread", "apple", "beef", "porkchop", "rotten_flesh"}
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
	hasBedNearby := false
	for _, poi := range state.POIs {
		if strings.Contains(poi.Name, "bed") {
			hasBedNearby = true
			break
		}
	}

	if state.TimeOfDay > 12541 && state.TimeOfDay < 23000 && hasBedNearby {
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

type ProgressionRoutine struct{}

func (p *ProgressionRoutine) Name() string { return "progression" }
func (p *ProgressionRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	inv := make(map[string]int)
	var logName string
	hasWeapon := false

	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
		if strings.HasSuffix(item.Name, "_log") {
			logName = item.Name
		}
		if strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") {
			hasWeapon = true
		}
	}

	planks := 0
	for name, count := range inv {
		if strings.HasSuffix(name, "_planks") {
			planks += count
		}
	}

	hasCraftingTableNearby := false
	for _, poi := range state.POIs {
		if poi.Name == "crafting_table" {
			hasCraftingTableNearby = true
			break
		}
	}

	isActive := func(target string) bool {
		return isTaskActive(string(ActionCraft), target, inFlight, queue)
	}

	craft := func(target, rationale string) *Action {
		return &Action{
			ID:        fmt.Sprintf("routine-prog-%d", time.Now().UnixNano()),
			Source:    string(SourceRoutine),
			Action:    string(ActionCraft),
			Target:    Target{Type: string(TargetRecipe), Name: target},
			Rationale: rationale,
			Priority:  PriRoutine,
		}
	}

	if logName != "" && planks < 8 {
		target := strings.Replace(logName, "_log", "_planks", 1)
		if !isActive(target) {
			return craft(target, "Progression: Converting logs to planks.")
		}
	}

	if inv["crafting_table"] == 0 && !hasCraftingTableNearby && planks >= 4 {
		if !isActive("crafting_table") {
			return craft("crafting_table", "Progression: Crafting essential table.")
		}
	}

	needsTool := (inv["iron_pickaxe"] == 0 && inv["iron_ingot"] >= 3) ||
		(inv["iron_pickaxe"] == 0 && inv["stone_pickaxe"] == 0 && (inv["cobblestone"] >= 3 || inv["stone"] >= 3)) ||
		(inv["iron_pickaxe"] == 0 && inv["stone_pickaxe"] == 0 && inv["wooden_pickaxe"] == 0 && planks >= 3)

	needsWeapon := !hasWeapon && ((inv["iron_ingot"] >= 2) || (inv["cobblestone"] >= 2 || inv["stone"] >= 2) || (planks >= 2))

	if (needsTool || needsWeapon) && inv["stick"] < 2 && planks >= 1 {
		if !isActive("stick") {
			return craft("stick", "Progression: Crafting sticks for tools and weapons.")
		}
	}

	hasCraftingTable := hasCraftingTableNearby || inv["crafting_table"] > 0

	// Auto-craft weapons
	if !hasWeapon && inv["stick"] >= 1 && hasCraftingTable {
		if inv["iron_ingot"] >= 2 && !isActive("iron_sword") {
			return craft("iron_sword", "Armament: Crafting an iron sword for defense.")
		}
		if (inv["cobblestone"] >= 2 || inv["stone"] >= 2) && !isActive("stone_sword") {
			return craft("stone_sword", "Armament: Crafting a stone sword for defense.")
		}
		if planks >= 2 && !isActive("wooden_sword") {
			return craft("wooden_sword", "Armament: Crafting a wooden sword for defense.")
		}
	}

	// Auto-craft tools
	if inv["stick"] >= 2 {
		if inv["iron_pickaxe"] == 0 && inv["iron_ingot"] >= 3 {
			if !isActive("iron_pickaxe") {
				return craft("iron_pickaxe", "Progression: Upgrading to Iron Pickaxe.")
			}
		}

		if inv["iron_pickaxe"] == 0 && inv["stone_pickaxe"] == 0 && (inv["cobblestone"] >= 3 || inv["stone"] >= 3) && hasCraftingTable {
			if !isActive("stone_pickaxe") {
				return craft("stone_pickaxe", "Progression: Upgrading to Stone Pickaxe.")
			}
		}

		if inv["iron_pickaxe"] == 0 && inv["stone_pickaxe"] == 0 && inv["wooden_pickaxe"] == 0 && planks >= 3 {
			if !isActive("wooden_pickaxe") {
				return craft("wooden_pickaxe", "Progression: Crafting first wooden pickaxe.")
			}
		}
	}

	return nil
}

type CookingRoutine struct{}

func (c *CookingRoutine) Name() string { return "cooking" }
func (c *CookingRoutine) Check(state GameState, inFlight *Action, queue []Action) *Action {
	hasFurnace, hasRaw, hasFuel := false, false, false
	rawFood := map[string]bool{"beef": true, "porkchop": true, "mutton": true, "chicken": true, "rabbit": true, "cod": true, "salmon": true}
	fuelTypes := map[string]bool{"coal": true, "charcoal": true, "oak_planks": true}

	for _, item := range state.Inventory {
		if item.Name == "furnace" {
			hasFurnace = true
		}
		if rawFood[item.Name] {
			hasRaw = true
		}
		if fuelTypes[item.Name] {
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
			Priority:  PriIdle,
		}
	}
	return nil
}
