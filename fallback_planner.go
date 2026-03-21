package main

import (
	"context"
	"fmt"
	"time"
)

// FallbackBrain provides deterministic, hardcoded survival plans
// when the primary LLM is unavailable or repeatedly hallucinating.
type FallbackBrain struct{}

func NewFallbackBrain() *FallbackBrain {
	return &FallbackBrain{}
}

func (f *FallbackBrain) GenerateMilestone(ctx context.Context, t Tick, sessionID string) (*MilestonePlan, error) {
	return &MilestonePlan{
		ID:             fmt.Sprintf("fallback-milestone-%d", time.Now().Unix()),
		Description:    "Survive and gather basic resources while AI is disconnected.",
		CompletionHint: "Has 10 oak_logs and 10 cobblestone.",
	}, nil
}

func (f *FallbackBrain) EvaluatePlan(ctx context.Context, t Tick, sessionID, systemOverride string, milestone *MilestonePlan) (*LLMPlan, error) {
	// A simple, deterministic loop: if we don't have wood, get wood.
	// We rely on the RoutineManager to handle combat and sleep reflexes.
	return &LLMPlan{
		Objective:         "Execute deterministic fallback survival loop",
		MilestoneComplete: false,
		Tasks: []Action{
			{
				Action:    string(ActionGather),
				Target:    Target{Type: string(TargetBlock), Name: "wood"},
				Rationale: "Fallback mode: Gathering generic wood to stay productive.",
				Priority:  PriLLM,
			},
			{
				Action:    string(ActionIdle),
				Target:    Target{Type: string(TargetNone), Name: "none"},
				Rationale: "Fallback mode: Yielding to routines.",
				Priority:  PriLLM,
			},
		},
	}, nil
}
