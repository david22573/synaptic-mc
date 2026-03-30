package humanization

import (
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type Engine struct {
	attention  *AttentionModel
	intent     *IntentModel
	hesitation *HesitationModel
	noise      *NoiseModel
	state      *State
	config     Config
}

func NewEngine(config Config) *Engine {
	return &Engine{
		attention:  NewAttentionModel(config),
		intent:     NewIntentModel(),
		hesitation: NewHesitationModel(config),
		noise:      NewNoiseModel(config),
		state:      NewState(),
		config:     config,
	}
}

// NEW: Accessor for state evolution
func (e *Engine) State() *State {
	return e.state
}

func (e *Engine) Process(plan domain.Plan, ctx Context) []ScheduledAction {
	now := time.Now()
	if !e.state.LastUpdate.IsZero() {
		dt := now.Sub(e.state.LastUpdate)
		e.state.Evolve(ctx, dt)
	} else {
		e.state.LastUpdate = now
	}

	tasks := plan.Tasks
	tasks = e.attention.Apply(tasks, e.state)
	tasks = e.intent.Apply(tasks, e.state, ctx)
	tasks = e.noise.Apply(tasks, e.state)

	return e.hesitation.Schedule(tasks, e.state)
}
