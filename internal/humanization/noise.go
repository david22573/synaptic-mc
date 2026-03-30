package humanization

import (
	"david22573/synaptic-mc/internal/domain"
)

// ApplyNoise lightly fuzzes the intent parameters.
// Currently acts as a passthrough for structural safety, but is hooked up
// for future vector/coordinate fuzzing when actions carry explicit Vec3s.
func ApplyNoise(action domain.Action, ctx Context, cfg Config) domain.Action {
	if cfg.NoiseLevel <= 0.01 {
		return action
	}

	noisyAction := action

	// Example: If the action involves a specific coordinate, we would
	// apply +/- NoiseLevel to X/Z here so the bot doesn't walk perfectly straight.
	// Since our current target types are mostly entity names (e.g., "oak_log"),
	// we rely on the TS client (Movements config) for physical path jitter.

	return noisyAction
}
