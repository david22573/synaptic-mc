package decision

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/pipeline"
	"david22573/synaptic-mc/internal/policy"
)

type TaskManager interface {
	Enqueue(ctx context.Context, tasks ...domain.Action) error
}

type Pipeline struct {
	planner     *AdvancedPlanner
	stages      []pipeline.Stage
	taskManager TaskManager
	logger      *slog.Logger
	currentTask atomic.Pointer[domain.Action]

	// Phase 3.2: State tracking for smart interrupts
	lastState     domain.GameState
	lastStateMu   sync.Mutex
	lastStateTime time.Time
	stuckSince    time.Time
}

func NewPipeline(
	p *AdvancedPlanner,
	policyEngine policy.Engine,
	taskManager TaskManager,
	logger *slog.Logger,
) *Pipeline {
	return &Pipeline{
		planner: p,
		stages: []pipeline.Stage{
			pipeline.NewNormalizeStage(),
			pipeline.NewValidateStage(),
			pipeline.NewSimulateStage(),
			pipeline.NewPolicyStage(policyEngine),
		},
		taskManager: taskManager,
		logger:      logger.With(slog.String("component", "pipeline")),
	}
}

func (p *Pipeline) Process(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) {
	active := p.currentTask.Load()

	if active != nil {
		if !p.shouldInterrupt(state) {
			return
		}
		p.logger.Warn("Task commitment broken by state urgency", slog.String("action", active.Action))
	}

	p.generatePlanAndDispatch(ctx, sessionID, state, trace)
}

// Phase 3.2: Smart Interrupt Logic
func (p *Pipeline) shouldInterrupt(state domain.GameState) bool {
	p.lastStateMu.Lock()
	defer p.lastStateMu.Unlock()

	now := time.Now()

	// Initial assignment guard
	if p.lastStateTime.IsZero() {
		p.lastState = state
		p.lastStateTime = now
		return false
	}

	healthDrop := p.lastState.Health - state.Health
	timeDiff := now.Sub(p.lastStateTime).Seconds()

	droppingRate := 0.0
	if timeDiff > 0 {
		droppingRate = healthDrop / timeDiff
	}

	dx := p.lastState.Position.X - state.Position.X
	dy := p.lastState.Position.Y - state.Position.Y
	dz := p.lastState.Position.Z - state.Position.Z
	distMoved := math.Sqrt(dx*dx + dy*dy + dz*dz)

	active := p.currentTask.Load()
	isMovingTask := active != nil && active.Action != "idle"

	if distMoved < 0.1 && isMovingTask {
		if p.stuckSince.IsZero() {
			p.stuckSince = now
		}
	} else {
		p.stuckSince = time.Time{} // Reset if moving
	}

	isStuck := !p.stuckSince.IsZero() && now.Sub(p.stuckSince) > 3*time.Second
	threatsInView := len(state.Threats) > 0

	p.lastState = state
	p.lastStateTime = now

	return droppingRate > 2.0 || threatsInView || isStuck
}

func (p *Pipeline) generatePlanAndDispatch(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) {
	p.planner.TriggerReplan(state)
	rawPlan := p.planner.FastPlan(state)

	pipeState := pipeline.PipelineState{
		SessionID: sessionID,
		GameState: state,
		Trace:     trace,
		Plan:      &rawPlan,
	}

	for _, stage := range p.stages {
		nextState, err := stage.Process(ctx, pipeState)
		if err != nil {
			p.logger.Error(fmt.Sprintf("Pipeline stage %s failed", stage.Name()), slog.Any("error", err))
			return
		}
		pipeState = nextState
	}

	if pipeState.Validation != nil && !pipeState.Validation.IsValid {
		p.logger.Warn("Plan validation rejected", slog.Any("errors", pipeState.Validation.Errors))
		return
	}

	finalPlan := pipeState.FinalPlan
	if finalPlan == nil || len(finalPlan.Tasks) == 0 {
		return
	}

	p.currentTask.Store(&finalPlan.Tasks[0])

	err := p.taskManager.Enqueue(ctx, finalPlan.Tasks...)
	if err != nil {
		p.logger.Error("Failed to enqueue tasks", slog.Any("error", err))
	}
}
