package engine

import (
	"context"
	"fmt"
	"time"
)

type FallbackBrain struct{}

func NewFallbackBrain() *FallbackBrain {
	return &FallbackBrain{}
}

func (f *FallbackBrain) GeneratePlan(ctx context.Context, t Tick, sessionID, systemOverride string, currentMilestone *MilestonePlan, attempt int) (*LLMPlan, error) {
	// Keep the existing milestone if we have one, otherwise spin up a generic survival one
	milestone := currentMilestone
	if milestone == nil {
		milestone = &MilestonePlan{
			ID:             fmt.Sprintf("fallback-ms-%d", time.Now().UnixNano()),
			Description:    "Survive and stabilize until primary LLM API is restored.",
			CompletionHint: "Primary LLM API comes back online.",
		}
	}

	// Safest bet when the LLM is down is to clear tasks and let the routines (combat, eat, wander) take the wheel
	return &LLMPlan{
		Milestone:         milestone,
		Objective:         "Executing fallback survival protocols. Waiting for API.",
		MilestoneComplete: false,
		Tasks:             []Action{},
	}, nil
}
