package humanization

import (
	"fmt"
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// ScheduledAction encapsulates an action and its calculated execution time.
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

// Process wraps the planner's output and applies human-like behavior (drift, hesitation, rage-quits).
func (e *Engine) Process(plan domain.Plan, ctx Context) []ScheduledAction {
	var scheduled []ScheduledAction
	now := time.Now()
	currentDelay := time.Duration(0)

	// 1. Pass the tasks through the Intent filter to check for rage-quits
	approvedTasks := e.intentModel.Apply(plan.Tasks, e.state, ctx)
	if len(approvedTasks) == 0 {
		return scheduled
	}

	// Determine if the current state is critical (no drift allowed, minimal hesitation)
	isCritical := false
	if ctx.State.CurrentTask != nil {
		action := ctx.State.CurrentTask.Action
		if action == "combat" || action == "retreat" || action == "eat" || action == "mine" {
			isCritical = true
		}
	}
	if ctx.State.Health < 10 {
		isCritical = true
	}

	// 2. Inject ambient attention drift ONLY if safe
	if !isCritical {
		driftActions := ProcessAttentionDrift(ctx, e.state, now)
		scheduled = append(scheduled, driftActions...)

		// Plus explicit background drift
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

	// 3. Process the actual objective
	for i, task := range approvedTasks {
		// Ensure standard tasks have appropriate priority if not set
		if task.Priority == 0 {
			task.Priority = 5 // Base PLAN priority
		}

		noisyTask := ApplyNoise(task, ctx, e.cfg)
		hesitation := time.Duration(0)

		// Only hesitate if it's not a critical panic state
		if i == 0 || !isCritical {
			hesitation = CalculateHesitation(noisyTask, ctx, e.state, e.cfg)
		}

		currentDelay += hesitation

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
		Priority: -1, // BACKGROUND priority (TaskExecutionEngine will drop this if busy)
	}

	if roll < 0.5 {
		action.Action = "look"
		action.Target = domain.Target{Type: "relative", Name: "random_yaw"}
		action.Rationale = "Humanization: idle looking around"
	} else if roll < 0.8 {
		action.Action = "inventory"
		action.Target = domain.Target{Type: "ui", Name: "open_close"}
		action.Rationale = "Humanization: nervously checking inventory"
	} else {
		return nil // No drift
	}

	return &action
}
