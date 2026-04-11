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
	Feedback       Feedback    // Phase 6: Awareness of execution success/failure
	cfg            Config
}

type Feedback struct {
	Failures    int
	SuccessRate float64
}

func NewState(cfg Config) *State {
	return &State{
		AttentionLevel: 1.0,
		Fatigue:        0.0,
		Intent:         IntentState{},
		Feedback:       Feedback{SuccessRate: 1.0},
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

func (s *State) UpdateFeedback(failures int, successRate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Feedback.Failures = failures
	s.Feedback.SuccessRate = successRate
}

func (s *State) GetAttention() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.AttentionLevel
}

func (s *State) GetFeedback() Feedback {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Feedback
}

func (s *State) GetCommitment() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Intent.Commitment
}
