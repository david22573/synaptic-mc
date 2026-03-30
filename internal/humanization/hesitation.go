package humanization

import (
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// CalculateHesitation determines how long the bot should pause before acting.
func CalculateHesitation(action domain.Action, ctx Context, state *State, cfg Config) time.Duration {
	base := float64(cfg.HesitationBase.Milliseconds())

	// 1. Complexity Scaling
	// High-stakes or complex physical actions require more "thinking" time.
	switch action.Action {
	case "hunt", "combat", "retreat", "build":
		base += 600.0
	case "craft", "smelt", "store":
		base += 300.0
	case "explore":
		base += 100.0 // Wandering is instinctual, low hesitation
	}

	// 2. Confusion Scaling
	// If the bot is stuck, it hesitates longer, simulating confusion.
	if ctx.IsStuck {
		base += 1200.0
	}

	// 3. Attention Scaling
	// Lower attention = slower reaction times.
	attentionModifier := 1.0 + (1.0 - state.GetAttention())
	base *= attentionModifier

	// 4. Natural Jitter
	// Add up to 50% random jitter so the delay is never exactly the same.
	jitterLimit := int64(base * 0.5)
	var jitter int64
	if jitterLimit > 0 {
		jitter = rand.Int63n(jitterLimit)
	}

	finalMs := int64(base) + jitter
	return time.Duration(finalMs) * time.Millisecond
}
