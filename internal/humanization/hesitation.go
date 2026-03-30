package humanization

import (
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type ScheduledAction struct {
	domain.Action
	ExecuteAt time.Time
}

type HesitationModel struct {
	config Config
}

func NewHesitationModel(config Config) *HesitationModel {
	return &HesitationModel{config: config}
}

func (m *HesitationModel) Schedule(tasks []domain.Action, state *State) []ScheduledAction {
	scheduled := make([]ScheduledAction, len(tasks))
	now := time.Now()

	for i, task := range tasks {
		delay := m.config.HesitationBase

		delay += time.Duration(state.Personality.Fatigue * float64(m.config.HesitationBase))

		if state.Personality.Caution > 0.7 {
			delay = time.Duration(float64(delay) * 1.5)
		}

		jitter := time.Duration(rand.Float64() * float64(m.config.HesitationBase) * 0.2)
		delay += jitter

		if task.Action == "retreat" || task.Action == "eat" {
			delay = 0
		}

		scheduled[i] = ScheduledAction{
			Action:    task,
			ExecuteAt: now.Add(delay),
		}

		now = now.Add(delay)
	}

	return scheduled
}
