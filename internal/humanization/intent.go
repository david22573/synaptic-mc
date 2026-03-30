package humanization

import "david22573/synaptic-mc/internal/domain"

type IntentModel struct{}

func NewIntentModel() *IntentModel {
	return &IntentModel{}
}

func (m *IntentModel) Apply(tasks []domain.Action, state *State, ctx Context) []domain.Action {
	if len(tasks) == 0 {
		return tasks
	}

	if ctx.IsStuck {
		state.Intent.Frustration += 0.2
	} else {
		state.Intent.Frustration -= 0.05
		if state.Intent.Frustration < 0 {
			state.Intent.Frustration = 0
		}
	}

	if state.Intent.Frustration > 0.8 {
		state.Intent.CurrentGoal = ""
		state.Intent.Commitment = 0.0
		state.Intent.Frustration = 0.0
		return []domain.Action{}
	}

	primaryTask := tasks[0]
	if state.Intent.CurrentGoal != primaryTask.Action {
		state.Intent.CurrentGoal = primaryTask.Action
		state.Intent.Commitment = 1.0
		state.Intent.Frustration = 0.0
	}

	return tasks
}
