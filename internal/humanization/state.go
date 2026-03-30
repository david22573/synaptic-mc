package humanization

import "time"

type State struct {
	Attention   AttentionState
	Intent      IntentState
	Personality PersonalityState
	LastUpdate  time.Time
}

type PersonalityState struct {
	Aggression float64
	Curiosity  float64
	Caution    float64
	Fatigue    float64
}

type AttentionState struct {
	CurrentTarget string
	FocusStrength float64
}

type IntentState struct {
	CurrentGoal string
	Commitment  float64
	Frustration float64
}

func NewState() *State {
	return &State{
		Attention: AttentionState{FocusStrength: 1.0},
		Intent:    IntentState{Commitment: 1.0},
		Personality: PersonalityState{
			Aggression: 0.5,
			Curiosity:  0.5,
			Caution:    0.5,
			Fatigue:    0.0,
		},
		LastUpdate: time.Now(),
	}
}

func (s *State) Evolve(ctx Context, dt time.Duration) {
	s.Attention.FocusStrength -= 0.05 * dt.Seconds()
	if s.Attention.FocusStrength < 0 {
		s.Attention.FocusStrength = 0
	}

	if ctx.CurrentTask != nil {
		s.Personality.Fatigue += 0.01 * dt.Seconds()
	} else {
		s.Personality.Fatigue -= 0.05 * dt.Seconds()
	}
	if s.Personality.Fatigue < 0 {
		s.Personality.Fatigue = 0
	} else if s.Personality.Fatigue > 1 {
		s.Personality.Fatigue = 1
	}

	if ctx.Health < 10 || ctx.NearbyThreats > 0 {
		s.Personality.Caution += 0.1 * dt.Seconds()
	} else {
		s.Personality.Caution -= 0.02 * dt.Seconds()
	}
	if s.Personality.Caution < 0 {
		s.Personality.Caution = 0
	} else if s.Personality.Caution > 1 {
		s.Personality.Caution = 1
	}

	s.LastUpdate = time.Now()
}
