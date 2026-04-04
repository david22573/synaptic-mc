// internal/humanization/attention.go
package humanization

import (
	"fmt"
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type AttentionModel struct {
	FocusLevel float64
	LastUpdate time.Time
}

func NewAttentionModel() *AttentionModel {
	return &AttentionModel{
		FocusLevel: 1.0,
		LastUpdate: time.Now(),
	}
}

func (a *AttentionModel) Decay() {
	elapsed := time.Since(a.LastUpdate).Seconds()
	a.FocusLevel -= (elapsed / 60.0) * 0.05
	if a.FocusLevel < 0.2 {
		a.FocusLevel = 0.2
	}
	a.LastUpdate = time.Now()
}

func (a *AttentionModel) CheckDistraction() bool {
	a.Decay()
	return rand.Float64() < (1.0-a.FocusLevel)*0.25
}

func (a *AttentionModel) Refocus() {
	a.FocusLevel = 1.0
	a.LastUpdate = time.Now()
}

// ProcessAttentionDrift generates idle drift actions when the bot's attention
// has degraded enough to cause a lapse in focus.
func ProcessAttentionDrift(ctx Context, state *State, now time.Time) []ScheduledAction {
	attention := state.GetAttention()

	// Scale distraction probability inversely with attention
	distractionChance := (1.0 - attention) * 0.3
	if rand.Float64() >= distractionChance {
		return nil
	}

	action := domain.Action{
		ID:       fmt.Sprintf("attn-drift-%d", now.UnixNano()),
		Priority: -1,
	}

	switch roll := rand.Float64(); {
	case roll < 0.5:
		action.Action = "look"
		action.Target = domain.Target{Type: "relative", Name: "random_yaw"}
		action.Rationale = "Attention drift: unfocused gaze"
	case roll < 0.8:
		action.Action = "look"
		action.Target = domain.Target{Type: "relative", Name: "random_pitch"}
		action.Rationale = "Attention drift: zoning out"
	default:
		// Not every drift manifests as an action
		return nil
	}

	delay := time.Duration(rand.Int63n(int64(2 * time.Second)))
	return []ScheduledAction{{Action: action, ExecuteAt: now.Add(delay)}}
}
