// internal/humanization/intent.go
package humanization

import (
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// IntentState tracks the bot's emotional commitment to its current goal.
type IntentState struct {
	CurrentGoal    string
	Commitment     float64
	Frustration    float64
	CommitmentTime time.Time
}

type IntentModel struct{}

func NewIntentModel() *IntentModel {
	return &IntentModel{}
}

// Apply evaluates frustration and decides whether to proceed with the tasks or abandon them.
func (m *IntentModel) Apply(tasks []domain.Action, state *State, ctx Context) []domain.Action {
	if len(tasks) == 0 {
		return tasks
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	// Week 5: Panic Mode
	// If survival is threatened, drop all frustration and lock strictly into the plan
	if ctx.State.Health < 12 || len(ctx.State.Threats) > 0 {
		state.Intent.Frustration = 0.0
		state.Intent.Commitment = 1.0
		return tasks
	}

	// 1. Calculate Frustration
	if ctx.IsStuck {
		state.Intent.Frustration += 0.01
	} else {
		state.Intent.Frustration -= 0.05
		if state.Intent.Frustration < 0 {
			state.Intent.Frustration = 0
		}
	}

	// 2. Rage Quit Threshold
	if state.Intent.Frustration > 0.8 {
		state.Intent.CurrentGoal = ""
		state.Intent.Commitment = 0.0
		state.Intent.Frustration = 0.0
		return []domain.Action{}
	}

	// 3. Lock in new goals
	primaryTask := tasks[0]
	if state.Intent.CurrentGoal != primaryTask.Action {
		state.Intent.CurrentGoal = primaryTask.Action
		state.Intent.Commitment = 1.0
		state.Intent.Frustration = 0.0
		state.Intent.CommitmentTime = time.Now()
	} else {
		// Strengthen emotional commitment the longer a task runs
		if time.Since(state.Intent.CommitmentTime) > 3*time.Second {
			state.Intent.Commitment = 1.0
		}
	}

	return tasks
}
