package decision

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/pipeline"
	"david22573/synaptic-mc/internal/policy"
)

type Pipeline struct {
	planner     *AdvancedPlanner
	stages      []pipeline.Stage
	executor    *execution.ControllerManager
	logger      *slog.Logger
	currentTask atomic.Pointer[domain.Action]
}

func NewPipeline(
	p *AdvancedPlanner,
	policyEngine policy.Engine,
	executor *execution.ControllerManager,
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
		executor: executor,
		logger:   logger.With(slog.String("component", "pipeline")),
	}
}

// Process replaces Evaluate, now fully asynchronous and managing task commitments natively
func (p *Pipeline) Process(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) {
	active := p.currentTask.Load()

	// Task Commitment Window
	if active != nil {
		if !p.shouldInterrupt(state) {
			return // Task committed and running safely. Skip replanning.
		}
		p.logger.Warn("Task commitment broken by state urgency", slog.String("action", active.Action))
	}

	p.generatePlanAndDispatch(ctx, sessionID, state, trace)
}

func (p *Pipeline) shouldInterrupt(state domain.GameState) bool {
	// Interrupt active committed tasks if health falls into critical margins
	return state.Health < 6.0
}

func (p *Pipeline) generatePlanAndDispatch(ctx context.Context, sessionID string, state domain.GameState, trace domain.TraceContext) {
	// 1. Kick off the slow background LLM process with fresh context
	p.planner.TriggerReplan(state)

	// 2. Instant grab of the fastest available plan
	rawPlan := p.planner.FastPlan(state)

	pipeState := pipeline.PipelineState{
		SessionID: sessionID,
		GameState: state,
		Trace:     trace,
		Plan:      &rawPlan,
	}

	// 3. Pure stage execution (Normalize -> Validate -> Simulate -> Policy)
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

	// 4. Action Queue Prefill
	// Take the current goal and aggressively pre-queue the next 2 steps so TS doesn't idle
	for i := 0; i < 2 && i < len(finalPlan.Tasks); i++ {
		task := finalPlan.Tasks[i]

		// Set the top-level pointer so we know we're committed to this execution trace
		if i == 0 {
			p.currentTask.Store(&task)
		}

		err := p.executor.Dispatch(ctx, task)
		if err != nil {
			p.logger.Error("Failed to prefill execution queue", slog.String("task_id", task.ID), slog.Any("error", err))
		}
	}
}
