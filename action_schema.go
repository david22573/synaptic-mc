package main

// ActionType defines all macro actions the LLM or routines can trigger.
type ActionType string

const (
	ActionGather         ActionType = "gather"
	ActionCraft          ActionType = "craft"
	ActionHunt           ActionType = "hunt"
	ActionExplore        ActionType = "explore"
	ActionBuild          ActionType = "build"
	ActionSmelt          ActionType = "smelt"
	ActionFarm           ActionType = "farm"
	ActionMine           ActionType = "mine"
	ActionMarkLocation   ActionType = "mark_location"
	ActionRecallLocation ActionType = "recall_location"
	ActionIdle           ActionType = "idle"
	ActionSleep          ActionType = "sleep"
	ActionRetreat        ActionType = "retreat"
	ActionEat            ActionType = "eat"
	ActionInteract       ActionType = "interact"
)

// TargetType defines valid schema values for action targets.
type TargetType string

const (
	TargetBlock    TargetType = "block"
	TargetEntity   TargetType = "entity"
	TargetRecipe   TargetType = "recipe"
	TargetLocation TargetType = "location"
	TargetCategory TargetType = "category"
	TargetNone     TargetType = "none"
)

var ValidActions = map[string]bool{
	string(ActionGather):         true,
	string(ActionCraft):          true,
	string(ActionHunt):           true,
	string(ActionExplore):        true,
	string(ActionBuild):          true,
	string(ActionSmelt):          true,
	string(ActionFarm):           true,
	string(ActionMine):           true,
	string(ActionMarkLocation):   true,
	string(ActionRecallLocation): true,
	string(ActionIdle):           true,
	string(ActionSleep):          true,
	string(ActionRetreat):        true,
	string(ActionEat):            true,
	string(ActionInteract):       true,
}

var ValidTargetTypes = map[string]bool{
	string(TargetBlock):    true,
	string(TargetEntity):   true,
	string(TargetRecipe):   true,
	string(TargetLocation): true,
	string(TargetCategory): true,
	string(TargetNone):     true,
}

// IsValidAction checks if a raw string maps to a canonical ActionType.
func IsValidAction(action string) bool {
	return ValidActions[action]
}

// IsValidTargetType checks if a raw string maps to a canonical TargetType.
func IsValidTargetType(t string) bool {
	return ValidTargetTypes[t]
}
