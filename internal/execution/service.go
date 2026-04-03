package execution

import (
	"context"
	"encoding/json"
	"log/slog"

	"david22573/synaptic-mc/internal/domain"
)

type Service struct {
	bus         domain.EventBus
	engine      *TaskExecutionEngine
	taskManager *TaskManager
	ctrlManager *ControllerManager
	logger      *slog.Logger
}

func NewService(bus domain.EventBus, engine *TaskExecutionEngine, tm *TaskManager, cm *ControllerManager, logger *slog.Logger) *Service {
	s := &Service{
		bus:         bus,
		engine:      engine,
		taskManager: tm,
		ctrlManager: cm,
		logger:      logger.With(slog.String("component", "execution_service")),
	}

	// Subscribe to the bus
	bus.Subscribe(domain.EventTypePlanCreated, domain.FuncHandler(s.handlePlanCreated))
	bus.Subscribe(domain.EventTypePlanInvalidated, domain.FuncHandler(s.handlePlanInvalidated))
	bus.Subscribe(domain.EventTypeTaskStart, domain.FuncHandler(s.handleTaskStart))
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))
	bus.Subscribe(domain.EventTypePanic, domain.FuncHandler(s.handlePanic))
	bus.Subscribe(domain.EventBotDeath, domain.FuncHandler(s.handleBotDeath))

	return s
}

func (s *Service) handlePlanCreated(ctx context.Context, event domain.DomainEvent) {
	var plan domain.Plan
	if err := json.Unmarshal(event.Payload, &plan); err != nil {
		s.logger.Error("Failed to unmarshal plan", slog.Any("error", err))
		return
	}

	// For Phase 1, we just enqueue the first task immediately to keep the agent moving.
	// In Phase 4 (Humanization), this is where we'll inject the execution noise.
	if len(plan.Tasks) > 0 {
		_ = s.taskManager.Enqueue(ctx, plan.Tasks[0])
	}
}

func (s *Service) handlePlanInvalidated(ctx context.Context, event domain.DomainEvent) {
	_ = s.taskManager.Halt(ctx, "plan_invalidated")
}

func (s *Service) handlePanic(ctx context.Context, event domain.DomainEvent) {
	_ = s.taskManager.Halt(ctx, "panic_triggered")
}

func (s *Service) handleBotDeath(ctx context.Context, event domain.DomainEvent) {
	_ = s.taskManager.Halt(ctx, "bot_died")
}

func (s *Service) handleTaskStart(ctx context.Context, event domain.DomainEvent) {
	var payload struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err == nil {
		s.engine.OnTaskStart(payload.CommandID)
	}
}

func (s *Service) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	var payload struct {
		Status    string `json:"status"`
		CommandID string `json:"command_id"`
		Cause     string `json:"cause"`
	}

	if err := json.Unmarshal(event.Payload, &payload); err == nil {
		success := payload.Status == "COMPLETED"
		s.engine.OnTaskEnd(payload.CommandID, success)
		_ = s.taskManager.Complete(ctx, payload.CommandID, success)
	}
}
