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
	// Grab the event stream to build projections.
	// In a heavily loaded system, this would be periodically cached rather than computed per-tick.
	events, err := p.store.GetStream(ctx, p.sessionID)
	if err != nil {
		return Decision{IsApproved: true} // Fail open if event store is unreachable
	}

	stats := learning.CalculateActionStats(events)

	for _, task := range input.Plan.Tasks {
		stat, exists := stats[task.Action]

		// If we've tried this action at least 3 times and it fails > 70% of the time
		if exists && stat.Attempts >= 3 && stat.SuccessRate < 0.3 {
			maxCount := 0
			topCause := "unknown"
			for cause, count := range stat.FailureCauses {
				if count > maxCount {
					maxCount = count
					topCause = cause
				}
			}

			reason := fmt.Sprintf(
				"Action '%s' has a historically low success rate (%.0f%%) over %d attempts. Primary failure cause: %s. Try a completely different approach.",
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
