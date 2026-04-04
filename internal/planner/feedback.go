// internal/planner/feedback.go
package planner

import (
	"log"
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
)

// FeedbackAnalyzer processes the raw execution results from TS
// and mutates the current tactical plan to prevent infinite failure loops.
type FeedbackAnalyzer struct {
	world *domain.WorldModel
}

func NewFeedbackAnalyzer(world *domain.WorldModel) *FeedbackAnalyzer {
	// Seed the RNG for our spatial offsets
	rand.Seed(time.Now().UnixNano())

	return &FeedbackAnalyzer{
		world: world,
	}
}

// Analyze processes the TS execution layer's feedback and returns an adapted intent.
// If it returns nil, the task was successful or should be dropped.
func (f *FeedbackAnalyzer) Analyze(intent domain.ActionIntent, res execution.ExecutionResult) *domain.ActionIntent {
	if res.Success {
		f.world.RecordSuccess(intent.Action, intent.Target)
		return nil
	}

	log.Printf("[FeedbackAnalyzer] Task %s failed: Cause=%s, Progress=%.2f", intent.Action, res.Cause, res.Progress)

	// If we made significant progress before failing, update our local world weight
	// so the planner knows this wasn't a total bust.
	if res.Progress > 0.5 && intent.TargetLocation != nil {
		f.world.RewardPath(*intent.TargetLocation, res.Progress)
	}

	switch res.Cause {
	case execution.CauseBlocked, execution.CauseStuck:
		// Bot is physically trapped or pathing is deadlocking.
		// Mark the zone as high-cost to prevent immediate retries in the same vector.
		if intent.TargetLocation != nil {
			f.world.PenalizeZone(*intent.TargetLocation, 10.0)
		}
		return f.generateFallback(intent)

	case execution.CauseTimeout:
		if res.Progress > 0.1 {
			// We are moving, just slow. Resume the exact same intent.
			return &intent
		}
		// Complete stall. Backoff and try a different angle.
		return f.generateBackoff(intent)

	case execution.CauseInterrupted:
		// Survival system took over in TS. Go policy needs to re-evaluate the world state
		// (e.g., are we still being hunted?) before blindly resuming the task.
		return f.triggerReevaluation()

	case execution.CauseMissingResource:
		// TS realized it doesn't have the mats. Planner needs to inject a gather/craft task.
		return f.generatePrerequisitePlan(intent)

	default:
		// Blind retry as a last resort, but down-weight it so we don't spam it.
		f.world.PenalizeAction(intent.Action, 2.0)
		return &intent
	}
}

// --- Internal Tactical Mutations ---

func (f *FeedbackAnalyzer) generateFallback(failedIntent domain.ActionIntent) *domain.ActionIntent {
	if failedIntent.TargetLocation == nil {
		// Non-spatial task failed structurally. Blind retry.
		return &failedIntent
	}

	// Offset the target by 1.5 to 3.0 blocks to try a different physical angle
	// avoiding the specific block that caused the physics engine to stall.
	offsetX := (rand.Float64() * 3.0) - 1.5
	offsetZ := (rand.Float64() * 3.0) - 1.5

	newLoc := domain.Location{
		X: failedIntent.TargetLocation.X + offsetX,
		Y: failedIntent.TargetLocation.Y, // Keep Y the same so we don't try pathing into the ceiling
		Z: failedIntent.TargetLocation.Z + offsetZ,
	}

	failedIntent.TargetLocation = &newLoc
	failedIntent.Rationale = "FALLBACK: Target shifted slightly to avoid physical blockage."

	return &failedIntent
}

func (f *FeedbackAnalyzer) generateBackoff(failedIntent domain.ActionIntent) *domain.ActionIntent {
	// Drop the current task temporarily and force a micro-explore.
	// This tells the TS layer to run the explore FSM, which raymarches to find open space,
	// effectively "unsticking" the bot before it retries the real intent.
	return &domain.ActionIntent{
		ID:        "backoff_" + failedIntent.ID,
		Action:    "explore",
		Target:    "nearby",
		Count:     1,
		Rationale: "BACKOFF: Pathing timeout detected. Exploring nearby to reset physical state.",
	}
}

func (f *FeedbackAnalyzer) triggerReevaluation() *domain.ActionIntent {
	// Returning nil explicitly drops the micro-plan.
	// The orchestrator sees nil and asks strategy.Evaluator for a fresh Directive.
	log.Println("[FeedbackAnalyzer] Task interrupted by survival gate. Triggering macro re-evaluation.")
	return nil
}

func (f *FeedbackAnalyzer) generatePrerequisitePlan(failedIntent domain.ActionIntent) *domain.ActionIntent {
	// Map end-goals to their immediate raw material dependency.
	// We use "gather" for surface items and "mine" for ores/stone.
	deps := map[string]struct{ action, target string }{
		"planks":         {action: "gather", target: "log"},
		"stick":          {action: "gather", target: "log"}, // log normalizes to planks, which normalizes to sticks
		"crafting_table": {action: "gather", target: "log"},
		"wooden_pickaxe": {action: "gather", target: "log"},
		"stone_pickaxe":  {action: "mine", target: "cobblestone"},
		"iron_pickaxe":   {action: "mine", target: "iron_ore"},
		"furnace":        {action: "mine", target: "cobblestone"},
	}

	dep, exists := deps[failedIntent.Target]
	if !exists {
		// If we don't know the exact dependency here, we drop back to the macro strategy
		// which has the full inventory view.
		log.Printf("[FeedbackAnalyzer] Unknown dependency map for %s, dropping to macro strategy", failedIntent.Target)
		return nil
	}

	return &domain.ActionIntent{
		ID:        "prereq_" + failedIntent.ID,
		Action:    dep.action,
		Target:    dep.target,
		Count:     3, // Grab a few to ensure we have enough for the recipe
		Rationale: "PREREQUISITE: Missing materials for " + failedIntent.Target + ". Diverting to " + dep.action + " " + dep.target + ".",
	}
}
