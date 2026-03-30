package humanization

import "david22573/synaptic-mc/internal/domain"

type AttentionModel struct {
	config Config
}

func NewAttentionModel(config Config) *AttentionModel {
	return &AttentionModel{config: config}
}

func (m *AttentionModel) Apply(tasks []domain.Action, state *State) []domain.Action {
	if len(tasks) == 0 {
		return tasks
	}

	primaryTask := tasks[0]
	newTarget := primaryTask.Target.Name

	if newTarget != state.Attention.CurrentTarget && state.Attention.FocusStrength > 0.7 {
		if primaryTask.Action != "retreat" && primaryTask.Action != "eat" {
			for i, t := range tasks {
				if t.Target.Name == state.Attention.CurrentTarget {
					tasks[0], tasks[i] = tasks[i], tasks[0]
					return tasks
				}
			}
		}
	}

	if newTarget != state.Attention.CurrentTarget {
		state.Attention.CurrentTarget = newTarget
		state.Attention.FocusStrength = 1.0
	}

	return tasks
}
