package humanization

import "time"

// Config holds the tuning parameters for the humanization engine.
type Config struct {
	// AttentionDecay is the rate at which focus drops per second (0.0 to 1.0)
	AttentionDecay float64

	// HesitationBase is the minimum reaction time before executing an action
	HesitationBase time.Duration

	// NoiseLevel dictates the probability/severity of input jitter (0.0 to 1.0)
	NoiseLevel float64

	// BaseDriftRate dictates the probability of background idle actions
	BaseDriftRate float64

	// MaxDriftDelay bounds how far into the future a drift action is scheduled
	MaxDriftDelay time.Duration

	DistractionThreshold float64 // How close is "close enough" for pathing and interactions
}
