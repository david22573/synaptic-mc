// internal/humanization/engine.go
package humanization

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type ScheduledAction struct {
	Action    domain.Action
	ExecuteAt time.Time
}

type Engine struct {
	cfg         Config
	state       *State
	intentModel *IntentModel
}

func NewEngine(cfg Config) *Engine {
	return &Engine{
		cfg:         cfg,
		state:       NewState(cfg),
		intentModel: NewIntentModel(),
	}
}

func (e *Engine) State() *State {
	return e.state
}

func (e *Engine) Config() Config {
	return e.cfg
}

func (e *Engine) Process(plan domain.Plan, ctx Context) []ScheduledAction {
	var scheduled []ScheduledAction
	now := time.Now()
	currentDelay := time.Duration(0)

	approvedTasks := e.intentModel.Apply(plan.Tasks, e.state, ctx)
	if len(approvedTasks) == 0 {
		return scheduled
	}

	// Week 5: Panic Mode (Pure reflex + escape, disable humanization noise)
	isCritical := false
	if ctx.State.CurrentTask != nil {
		action := ctx.State.CurrentTask.Action
		if action == "combat" || action == "retreat" || action == "eat" || action == "mine" {
			isCritical = true
		}
	}
	if ctx.State.Health < 12 || len(ctx.State.Threats) > 0 {
		isCritical = true
	}

	// Only drift if we aren't in a life-or-death situation
	if !isCritical {
		driftActions := ProcessAttentionDrift(ctx, e.state, now)
		scheduled = append(scheduled, driftActions...)

		if rand.Float64() < e.cfg.BaseDriftRate {
			driftAction := e.generateDrift(ctx)
			if driftAction != nil {
				driftDelay := time.Duration(rand.Int63n(int64(e.cfg.MaxDriftDelay)))
				scheduled = append(scheduled, ScheduledAction{
					Action:    *driftAction,
					ExecuteAt: now.Add(driftDelay),
				})
			}
		}
	}

	for _, task := range approvedTasks {
		if task.Priority == 0 {
			task.Priority = 5
		}

		noisyTask := ApplyNoise(task, ctx, e.state, e.cfg)

		// Phase 7: Hesitation moved to cognitive decision layer (planner.go)
		// to avoid stalling physical execution with stale world states.
		// We still use currentDelay for sequential task spacing (100ms).

		scheduled = append(scheduled, ScheduledAction{
			Action:    noisyTask,
			ExecuteAt: now.Add(currentDelay),
		})

		currentDelay += 100 * time.Millisecond
	}

	return scheduled
}

func (e *Engine) generateDrift(ctx Context) *domain.Action {
	roll := rand.Float64()

	action := domain.Action{
		ID:       fmt.Sprintf("drift-%d", time.Now().UnixNano()),
		Priority: -1,
	}

	// Dynamic curiosity: Look at interesting things if they exist
	if roll < 0.4 && len(ctx.State.POIs) > 0 {
		poi := ctx.State.POIs[rand.Intn(len(ctx.State.POIs))]
		
		// Marshal POI coordinates for the TS look handler
		poiData, _ := json.Marshal(map[string]float64{
			"x": poi.Position.X,
			"y": poi.Position.Y + 1.0, // Look at eye level
			"z": poi.Position.Z,
		})

		action.Action = "look"
		action.Target = domain.Target{Type: "location", Name: string(poiData)}
		action.Rationale = fmt.Sprintf("Humanization: curious about %s", poi.Name)
		return &action
	}

	if roll < 0.7 {
		action.Action = "look"
		action.Target = domain.Target{Type: "relative", Name: "random_yaw"}
		action.Rationale = "Humanization: idle looking around"
	} else if roll < 0.85 {
		action.Action = "inventory"
		action.Target = domain.Target{Type: "ui", Name: "open_close"}
		action.Rationale = "Humanization: nervously checking inventory"
	} else {
		return nil
	}

	return &action
}
