// internal/planner/feedback.go
package planner

import (
	"log/slog"
	"math/rand"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

// FeedbackAnalyzer processes the raw execution results from TS
// and mutates the current tactical plan to prevent infinite failure loops.
// It supports Iterative Prompting by enriching the failure reasons.
type FeedbackAnalyzer struct {
	world  *domain.WorldModel
	logger *slog.Logger
}

func NewFeedbackAnalyzer(world *domain.WorldModel, logger *slog.Logger) *FeedbackAnalyzer {
	return &FeedbackAnalyzer{
		world:  world,
		logger: logger.With(slog.String("component", "feedback_analyzer")),
	}
}

// Analyze processes the TS execution layer's feedback and returns an adapted intent.
// If it returns nil, the task was successful or should be dropped.
func (f *FeedbackAnalyzer) Analyze(intent domain.ActionIntent, res domain.ExecutionResult) *domain.ActionIntent {
	if res.Success {
		f.world.RecordSuccess(intent.Action, intent.Target)
		return nil
	}

	f.logger.Info("Task failed, analyzing feedback",
		slog.String("action", intent.Action),
		slog.String("cause", res.Cause),
		slog.Float64("progress", res.Progress))

	// If we made significant progress before failing, update our local world weight
	// so the planner knows this wasn't a total bust.
	if res.Progress > 0.5 && intent.TargetLocation != nil {
		f.world.RewardPath(*intent.TargetLocation, res.Progress)
	}

	// Capture JS stack traces for Iterative Prompting / Debugging
	if strings.Contains(res.Cause, "stack") || strings.Contains(res.Cause, "Error") {
		f.logger.Warn("JS Execution Error detected in feedback", slog.String("error", res.Cause))
		// We don't necessarily mutate the intent here, but the Critic will pick this up
		// and add it to the taskHistory for LLM reflection.
	}

	switch res.Cause {
	case domain.CauseBlocked, domain.CauseStuck:
		// Bot is physically trapped or pathing is deadlocking.
		if intent.TargetLocation != nil {
			f.world.PenalizeZone(*intent.TargetLocation, 10.0)
		}
		return f.generateFallback(intent)

	case domain.CauseTimeout:
		if res.Progress > 0.1 {
			// We are moving, just slow. Resume the exact same intent.
			return &intent
		}
		// Complete stall. Backoff and try a different angle.
		return f.generateBackoff(intent)

	case domain.CauseInterrupted:
		// Survival system took over in TS.
		return f.triggerReevaluation()

	case domain.CauseMissingResource:
		// TS realized it doesn't have the mats.
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
		return &failedIntent
	}

	// Offset the target by 1.5 to 3.0 blocks to try a different physical angle
	offsetX := (rand.Float64() * 3.0) - 1.5
	offsetZ := (rand.Float64() * 3.0) - 1.5

	newLoc := domain.Location{
		X: failedIntent.TargetLocation.X + offsetX,
		Y: failedIntent.TargetLocation.Y,
		Z: failedIntent.TargetLocation.Z + offsetZ,
	}

	failedIntent.TargetLocation = &newLoc
	failedIntent.Rationale = "FALLBACK: Target shifted slightly to avoid physical blockage."

	return &failedIntent
}

func (f *FeedbackAnalyzer) generateBackoff(failedIntent domain.ActionIntent) *domain.ActionIntent {
	return &domain.ActionIntent{
		ID:        "backoff_" + failedIntent.ID,
		Action:    "explore",
		Target:    "nearby",
		Count:     1,
		Rationale: "BACKOFF: Pathing timeout detected. Exploring nearby to reset physical state.",
	}
}

func (f *FeedbackAnalyzer) triggerReevaluation() *domain.ActionIntent {
	f.logger.Info("Task interrupted by survival gate. Triggering macro re-evaluation.")
	return nil
}

func (f *FeedbackAnalyzer) generatePrerequisitePlan(failedIntent domain.ActionIntent) *domain.ActionIntent {
	target := strings.ToLower(failedIntent.Target)

	// Dependency mapping with substring matching
	deps := []struct {
		key    string
		action string
		target string
	}{
		{key: "planks", action: "gather", target: "log"},
		{key: "stick", action: "gather", target: "log"},
		{key: "crafting_table", action: "gather", target: "log"},
		{key: "wooden_pickaxe", action: "gather", target: "log"},
		{key: "stone_pickaxe", action: "mine", target: "cobblestone"},
		{key: "iron_pickaxe", action: "mine", target: "iron_ore"},
		{key: "furnace", action: "mine", target: "cobblestone"},
	}

	var dep *struct{ action, target string }
	for _, d := range deps {
		if strings.Contains(target, d.key) {
			dep = &struct{ action, target string }{action: d.action, target: d.target}
			break
		}
	}

	if dep == nil {
		f.logger.Warn("Unknown dependency map, dropping to macro strategy", slog.String("target", failedIntent.Target))
		return nil
	}

	return &domain.ActionIntent{
		ID:        "prereq_" + failedIntent.ID,
		Action:    dep.action,
		Target:    dep.target,
		Count:     3,
		Rationale: "PREREQUISITE: Missing materials for " + failedIntent.Target + ". Diverting to " + dep.action + " " + dep.target + ".",
	}
}
