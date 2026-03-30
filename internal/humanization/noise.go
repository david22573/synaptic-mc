package humanization

import (
	"math/rand"

	"david22573/synaptic-mc/internal/domain"
)

type NoiseModel struct {
	config Config
}

func NewNoiseModel(config Config) *NoiseModel {
	return &NoiseModel{config: config}
}

func (m *NoiseModel) Apply(tasks []domain.Action, state *State) []domain.Action {
	if len(tasks) < 2 {
		return tasks
	}

	if rand.Float64() < m.config.NoiseLevel {
		if tasks[0].Action != "retreat" && tasks[0].Action != "eat" && tasks[1].Action != "retreat" {
			tasks[0], tasks[1] = tasks[1], tasks[0]
		}
	}

	return tasks
}
