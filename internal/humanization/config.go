package humanization

import (
	"time"

	"david22573/synaptic-mc/internal/config"
)

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

	TaskSpacing time.Duration

	MinAttentionLevel       float64
	CriticalHealthThreshold float64

	DriftCuriosityThreshold float64
	DriftIdleLookThreshold  float64
	DriftInventoryThreshold float64
}

func MapToHumanizationConfig(c config.HumanizationConfig) Config {
	return Config{
		AttentionDecay:          c.AttentionDecay,
		HesitationBase:          time.Duration(c.HesitationBaseMs) * time.Millisecond,
		NoiseLevel:              c.NoiseLevel,
		BaseDriftRate:           c.BaseDriftRate,
		MaxDriftDelay:           time.Duration(c.MaxDriftDelayMs) * time.Millisecond,
		DistractionThreshold:    c.DistractionThreshold,
		TaskSpacing:             time.Duration(c.TaskSpacingMs) * time.Millisecond,
		MinAttentionLevel:       0.2,
		CriticalHealthThreshold: 12.0,
		DriftCuriosityThreshold: 0.4,
		DriftIdleLookThreshold:  0.7,
		DriftInventoryThreshold: 0.85,
	}
}
