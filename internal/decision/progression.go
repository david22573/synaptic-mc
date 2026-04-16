package decision

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

var techTree = []string{
	"crafting_table",
	"wooden_pickaxe",
	"stone_pickaxe",
	"furnace",
	"iron_pickaxe",
	"iron_ingot",
	"iron_sword",
	"iron_chestplate",
	"diamond_pickaxe",
	"diamond",
}

func (s *Service) detectMilestones(before, after *domain.GameState) {
	beforeItems := make(map[string]bool)
	for _, item := range before.Inventory {
		beforeItems[item.Name] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, item := range techTree {
		alreadyUnlocked := false
		for _, m := range s.milestones {
			if m.Name == item {
				alreadyUnlocked = true
				break
			}
		}

		if alreadyUnlocked {
			continue
		}

		// Check if it's new in inventory
		hasItNow := false
		for _, inv := range after.Inventory {
			if inv.Name == item {
				hasItNow = true
				break
			}
		}

		if hasItNow && !beforeItems[item] {
			s.logger.Info("Milestone unlocked!", slog.String("name", item))
			m := domain.ProgressionMilestone{Name: item, UnlockedAt: time.Now()}
			s.milestones = append(s.milestones, m)

			if s.memStore != nil {
				go func(name string) {
					dbCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
					defer cancel()
					_ = s.memStore.SaveMilestone(dbCtx, s.sessionID, name)
				}(item)
			}
		}
	}
}

func (s *Service) getMilestoneContext() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.milestones) == 0 {
		return "PROGRESSION STATUS: Just started. Goal: crafting_table, wooden_pickaxe."
	}

	var unlocked []string
	for _, m := range s.milestones {
		unlocked = append(unlocked, m.Name)
	}

	// Next goal derivation
	nextGoal := ""
	for _, item := range techTree {
		found := false
		for _, u := range unlocked {
			if u == item {
				found = true
				break
			}
		}
		if !found {
			nextGoal = item
			break
		}
	}

	return fmt.Sprintf("PROGRESSION STATUS:\n  UNLOCKED: %s\n  ACTIVE GOAL: acquire_%s",
		strings.Join(unlocked, ", "), nextGoal)
}

func (s *Service) getTaskHistory() []domain.TaskHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	historyCopy := make([]domain.TaskHistory, len(s.taskHistory))
	copy(historyCopy, s.taskHistory)
	return historyCopy
}
