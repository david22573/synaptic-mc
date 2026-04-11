// internal/humanization/hesitation.go
package humanization

import (
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// CalculateHesitation determines how long the bot should pause before acting.
// CRITICAL: This function only calculates the duration. The actual delay MUST be
// implemented at the cognitive decision layer (e.g., in the Go planner) rather
// than the physical execution layer to avoid stalling the bot's world state
// processing and physical movement in TypeScript.
func CalculateHesitation(action domain.Action, ctx Context, state *State, cfg Config) time.Duration {
	base := float64(cfg.HesitationBase.Milliseconds())

	// 1. Complexity Scaling
	switch action.Action {
	case "hunt", "combat", "retreat", "build":
		base += 600.0
	case "craft", "smelt", "store":
		base += 300.0
	case "explore":
		base += 100.0
	}

	// 2. Confusion Scaling
	if ctx.IsStuck {
		base += 1200.0
	}

	// 3. Attention & Commitment Scaling
	attention := state.GetAttention()
	commitment := state.GetCommitment()

	// Attention increases delay (distraction), Commitment reduces it (flow)
	attentionModifier := 1.0 + (1.0 - attention)
	commitmentModifier := 1.0 - (commitment * 0.25) // Up to 25% reduction if fully committed

	base *= attentionModifier
	base *= commitmentModifier

	// 4. Week 5: Risk-Based Hesitation (Caution factor)
	// Base delay + (risk * factor). High risk environments cause the bot to pause
	// and "think" before acting (simulating stealth/caution), unless panic mode overrides.
	risk := 0.0
	if ctx.State.Health < 20 {
		risk += (20.0 - ctx.State.Health) * 0.5
	}
	risk += float64(len(ctx.State.Threats) * 2.0)

	factor := 45.0 // ms added per unit of risk
	base += (risk * factor)

	// 5. Phase 6: Feedback-Aware Hesitation
	// More hesitation after recent failures (representing caution/frustration),
	// less after consistent success (representing confidence).
	feedback := state.GetFeedback()
	if feedback.Failures > 0 {
		// Add 500ms per consecutive failure, capped at 2s
		failurePenalty := float64(feedback.Failures) * 500.0
		if failurePenalty > 2000.0 {
			failurePenalty = 2000.0
		}
		base += failurePenalty
	}

	if feedback.SuccessRate > 0.8 {
		// Reduce base hesitation by up to 20% if we are on a roll
		confidenceBonus := (feedback.SuccessRate - 0.8) * 0.5 // max 0.1 reduction
		base *= (1.0 - confidenceBonus)
	}

	// 6. Natural Jitter
	jitterLimit := int64(base * 0.5)
	var jitter int64
	if jitterLimit > 0 {
		jitter = rand.Int63n(jitterLimit)
	}

	finalMs := int64(base) + jitter
	return time.Duration(finalMs) * time.Millisecond
}
