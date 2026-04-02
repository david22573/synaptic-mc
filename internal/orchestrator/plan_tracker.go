package orchestrator

import (
	"context"
	"log/slog"
	"sync"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/humanization"
)

type PlanTracker struct {
	mu           sync.Mutex
	activePlan   *domain.Plan
	currentIndex int
	taskManager  *TaskManager
	humanizer    *humanization.Engine
	logger       *slog.Logger
	hctxBuilder  func() humanization.Context
}

func NewPlanTracker(tm *TaskManager, h *humanization.Engine, hctxBuilder func() humanization.Context, logger *slog.Logger) *PlanTracker {
	return &PlanTracker{
		taskManager: tm,
		humanizer:   h,
		hctxBuilder: hctxBuilder,
		logger:      logger.With(slog.String("component", "plan_tracker")),
	}
}

func (pt *PlanTracker) SetPlan(ctx context.Context, plan *domain.Plan) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.activePlan != nil {
		pt.logger.Info("Replacing active plan", slog.String("old_objective", pt.activePlan.Objective), slog.String("new_objective", plan.Objective))
		pt.taskManager.Halt(ctx, "plan_superseded")
	}

	pt.activePlan = plan
	pt.currentIndex = 0
	pt.logger.Info("New plan active", slog.String("objective", plan.Objective), slog.Int("tasks", len(plan.Tasks)))

	pt.enqueueNextLocked(ctx)
}

func (pt *PlanTracker) OnTaskComplete(ctx context.Context, taskID string, success bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.activePlan == nil {
		return
	}

	if !success {
		pt.logger.Warn("Plan failed due to task failure", slog.String("failed_task", taskID), slog.String("objective", pt.activePlan.Objective))
		pt.activePlan = nil
		pt.currentIndex = 0
		return
	}

	// Direct ID match to avoid index desyncs and empty string bugs
	foundIndex := -1
	for i, task := range pt.activePlan.Tasks {
		if task.ID == taskID {
			foundIndex = i
			break
		}
	}

	if foundIndex == -1 {
		pt.logger.Warn("Task completion not found in active plan", slog.String("task_id", taskID), slog.String("objective", pt.activePlan.Objective))
		return
	}

	// Advance past the completed task
	pt.currentIndex = foundIndex + 1

	if pt.currentIndex >= len(pt.activePlan.Tasks) {
		pt.logger.Info("Plan completed successfully", slog.String("objective", pt.activePlan.Objective))
		pt.activePlan = nil
		pt.currentIndex = 0
	} else {
		pt.enqueueNextLocked(ctx)
	}
}

func (pt *PlanTracker) ClearPlan(ctx context.Context, reason string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.activePlan != nil {
		pt.logger.Info("Clearing active plan", slog.String("reason", reason), slog.String("objective", pt.activePlan.Objective))
		pt.taskManager.Halt(ctx, reason)
		pt.activePlan = nil
		pt.currentIndex = 0
	}
}

func (pt *PlanTracker) HasActivePlan() bool {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.activePlan != nil
}

func (pt *PlanTracker) enqueueNextLocked(ctx context.Context) {
	if pt.currentIndex < len(pt.activePlan.Tasks) {
		nextTask := pt.activePlan.Tasks[pt.currentIndex]

		// Apply humanization context to this specific task step
		singleTaskPlan := domain.Plan{Objective: pt.activePlan.Objective, Tasks: []domain.Action{nextTask}}
		scheduledActions := pt.humanizer.Process(singleTaskPlan, pt.hctxBuilder())

		for _, sa := range scheduledActions {
			_ = pt.taskManager.EnqueueScheduled(ctx, sa.Action, sa.ExecuteAt)
		}
	}
}
