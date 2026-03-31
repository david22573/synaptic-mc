package policy

import (
	"context"
	"fmt"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/learning"
)

// IntelligencePolicy evaluates plans against historical performance projections.
type IntelligencePolicy struct {
	store     domain.EventStore
	sessionID string
}

func NewIntelligencePolicy(store domain.EventStore, sessionID string) *IntelligencePolicy {
	return &IntelligencePolicy{
		store:     store,
		sessionID: sessionID,
	}
}

func (p *IntelligencePolicy) Decide(ctx context.Context, input DecisionInput) Decision {
	stats, _, err := learning.GetProjectedStats(ctx, p.store, p.sessionID)
	if err != nil {
		return Decision{IsApproved: true}
	}

	for _, task := range input.Plan.Tasks {
		// NEVER reject survival actions based on intelligence stats.
		// If we fail to eat, we should keep trying to eat.
		if task.Action == "eat" || task.Action == "retreat" {
			continue
		}

		stat, exists := stats[task.Action]

		// Relaxed Thresholds:
		// Attempts: 3 -> 5
		// Success Rate: 0.3 -> 0.15
		if exists && stat.Attempts >= 5 && stat.SuccessRate < 0.15 {
			maxCount := 0
			topCause := "unknown"
			for cause, count := range stat.FailureCauses {
				if count > maxCount {
					maxCount = count
					topCause = cause
				}
			}

			reason := fmt.Sprintf(
				"Action '%s' has a critically low success rate (%.0f%%) over %d attempts. Primary failure: %s. Switching strategy recommended.",
				task.Action, stat.SuccessRate*100, stat.Attempts, topCause,
			)

			return Decision{
				IsApproved: false,
				Reason:     reason,
			}
		}
	}

	return Decision{IsApproved: true}
}
