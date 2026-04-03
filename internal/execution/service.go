package execution

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type Service struct {
	bus          domain.EventBus
	engine       *TaskExecutionEngine
	taskManager  *TaskManager
	ctrlManager  *ControllerManager
	logger       *slog.Logger
	activeTimers map[string]*time.Timer
	timersMu     sync.Mutex
}

func NewService(bus domain.EventBus, engine *TaskExecutionEngine, tm *TaskManager, cm *ControllerManager, logger *slog.Logger) *Service {
	s := &Service{
		bus:          bus,
		engine:       engine,
		taskManager:  tm,
		ctrlManager:  cm,
		logger:       logger.With(slog.String("component", "execution_service")),
		activeTimers: make(map[string]*time.Timer),
	}

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

	if len(plan.Tasks) > 0 {
		_ = s.taskManager.Enqueue(ctx, plan.Tasks[0])
	}
}

func (s *Service) handlePlanInvalidated(ctx context.Context, event domain.DomainEvent) {
	_ = s.taskManager.Halt(ctx, "plan_invalidated")
	s.clearAllWatchdogs()
}

func (s *Service) handlePanic(ctx context.Context, event domain.DomainEvent) {
	_ = s.taskManager.Halt(ctx, "panic_triggered")
	s.clearAllWatchdogs()
}

func (s *Service) handleBotDeath(ctx context.Context, event domain.DomainEvent) {
	_ = s.taskManager.Halt(ctx, "bot_died")
	s.clearAllWatchdogs()
}

func (s *Service) handleTaskStart(ctx context.Context, event domain.DomainEvent) {
	var payload struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err == nil {
		s.engine.OnTaskStart(payload.CommandID)

		// Phase 3 Improvement: execution-deadlines Watchdog
		inFlight := s.engine.GetInFlight()
		if inFlight != nil && inFlight.ID == payload.CommandID {
			timeout := inFlight.Timeout
			if timeout == 0 {
				timeout = 45 * time.Second // Fallback default
			}

			timer := time.AfterFunc(timeout, func() {
				s.logger.Warn("Execution deadline exceeded, aborting task", slog.String("task_id", payload.CommandID))

				_ = s.ctrlManager.AbortCurrent(context.Background(), "DEADLINE_EXCEEDED")

				failedPayload, _ := json.Marshal(map[string]string{
					"status":     "FAILED",
					"command_id": payload.CommandID,
					"cause":      "DEADLINE_EXCEEDED",
				})

				s.bus.Publish(context.Background(), domain.DomainEvent{
					SessionID: event.SessionID,
					Trace:     event.Trace,
					Type:      domain.EventTypeTaskEnd,
					Payload:   failedPayload,
					CreatedAt: time.Now(),
				})
			})

			s.timersMu.Lock()
			s.activeTimers[payload.CommandID] = timer
			s.timersMu.Unlock()
		}
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

		// Stop and remove the watchdog timer
		s.timersMu.Lock()
		if timer, ok := s.activeTimers[payload.CommandID]; ok {
			timer.Stop()
			delete(s.activeTimers, payload.CommandID)
		}
		s.timersMu.Unlock()

		s.engine.OnTaskEnd(payload.CommandID, success)
		_ = s.taskManager.Complete(ctx, payload.CommandID, success)
	}
}

func (s *Service) clearAllWatchdogs() {
	s.timersMu.Lock()
	defer s.timersMu.Unlock()
	for id, timer := range s.activeTimers {
		timer.Stop()
		delete(s.activeTimers, id)
	}
}
