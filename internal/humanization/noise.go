package humanization

import (
	"fmt"
	"david22573/synaptic-mc/internal/domain"
)

// ApplyNoise lightly fuzzes the intent parameters.
func ApplyNoise(action domain.Action, ctx Context, state *State, cfg Config) domain.Action {
	if cfg.NoiseLevel <= 0.01 {
		return action
	}

	noisyAction := action

	// If attention is low and noise is high, simulate "cognitive drift" 
	// by injecting noise/hesitation markers into the rationale.
	// This makes the bot's internal state visible in the UI logs.
	attention := state.GetAttention()
	if attention < 0.4 && cfg.NoiseLevel > 0.05 {
		noisyAction.Rationale = fmt.Sprintf("%s... (Wait, what was I doing? Focus is drifting...)", action.Rationale)
	}

	return noisyAction
}
