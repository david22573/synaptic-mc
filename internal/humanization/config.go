package humanization

import "time"

type Config struct {
	AttentionDecay float64
	HesitationBase time.Duration
	NoiseLevel     float64
}

func DefaultConfig() Config {
	return Config{
		AttentionDecay: 0.1,
		HesitationBase: 250 * time.Millisecond,
		NoiseLevel:     0.05,
	}
}
