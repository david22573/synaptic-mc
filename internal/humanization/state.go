package humanization

import (
	"sync"
	"time"
)

// State tracks the evolving internal conditions of the agent.
type State struct {
	mu             sync.Mutex
	AttentionLevel float64
	Fatigue        float64
	Intent         IntentState // NEW: Hooked in from intent.go
	cfg            Config
}

func NewState(cfg Config) *State {
	return &State{
		AttentionLevel: 1.0,
		Fatigue:        0.0,
		Intent:         IntentState{},
		cfg:            cfg,
	}
}

func (s *State) Evolve(ctx Context, dt time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	decay := s.cfg.AttentionDecay * dt.Seconds()
	s.AttentionLevel -= decay

	if ctx.State.Health < 15 || ctx.IsStuck {
		s.AttentionLevel = 1.0
	}

	if s.AttentionLevel < 0.2 {
		s.AttentionLevel = 0.2
	}
}

func (s *State) GetAttention() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.AttentionLevel
}
