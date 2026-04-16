package domain

import (
	"strings"
)

// GoalUtility defines the base rewards and unlock values for items.
type GoalUtility struct {
	BaseReward  float64
	UnlockValue float64
	IsResource  bool
}

var itemUtilities = map[string]GoalUtility{
	"log":             {BaseReward: 5, UnlockValue: 10, IsResource: true},
	"oak_log":         {BaseReward: 5, UnlockValue: 10, IsResource: true},
	"birch_log":       {BaseReward: 5, UnlockValue: 10, IsResource: true},
	"planks":          {BaseReward: 2, UnlockValue: 15, IsResource: true},
	"stick":           {BaseReward: 1, UnlockValue: 5, IsResource: true},
	"crafting_table":  {BaseReward: 0, UnlockValue: 50, IsResource: false},
	"wooden_pickaxe":  {BaseReward: 0, UnlockValue: 60, IsResource: false},
	"cobblestone":     {BaseReward: 4, UnlockValue: 20, IsResource: true},
	"stone_pickaxe":   {BaseReward: 0, UnlockValue: 80, IsResource: false},
	"iron_ore":        {BaseReward: 10, UnlockValue: 40, IsResource: true},
	"raw_iron":        {BaseReward: 10, UnlockValue: 0, IsResource: true},
	"iron_ingot":      {BaseReward: 20, UnlockValue: 100, IsResource: true},
	"iron_pickaxe":    {BaseReward: 0, UnlockValue: 150, IsResource: false},
	"diamond_ore":     {BaseReward: 50, UnlockValue: 200, IsResource: true},
	"diamond":         {BaseReward: 100, UnlockValue: 500, IsResource: true},
	"coal_ore":        {BaseReward: 5, UnlockValue: 10, IsResource: true},
	"coal":            {BaseReward: 5, UnlockValue: 0, IsResource: true},
	"beef":            {BaseReward: 8, UnlockValue: 0, IsResource: true},
	"cooked_beef":     {BaseReward: 15, UnlockValue: 0, IsResource: true},
	"porkchop":        {BaseReward: 8, UnlockValue: 0, IsResource: true},
	"cooked_porkchop": {BaseReward: 15, UnlockValue: 0, IsResource: true},
	"mutton":          {BaseReward: 8, UnlockValue: 0, IsResource: true},
	"cooked_mutton":   {BaseReward: 15, UnlockValue: 0, IsResource: true},
	"chicken":         {BaseReward: 5, UnlockValue: 0, IsResource: true},
	"cooked_chicken":  {BaseReward: 10, UnlockValue: 0, IsResource: true},
	"apple":           {BaseReward: 5, UnlockValue: 0, IsResource: true},
}

// GetItemUtility returns the reward and unlock value for a given target name.
func GetItemUtility(target string) (float64, float64) {
	target = strings.ToLower(strings.TrimSpace(target))
	if u, ok := itemUtilities[target]; ok {
		return u.BaseReward, u.UnlockValue
	}
	
	// Fallback for general categories
	if strings.Contains(target, "log") || strings.Contains(target, "wood") {
		return 5, 10
	}
	if strings.Contains(target, "ore") {
		return 10, 20
	}
	if strings.Contains(target, "pickaxe") {
		return 0, 50
	}
	
	return 1, 0
}
