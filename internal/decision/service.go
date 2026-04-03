package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/voyager"
)

type StateProvider interface {
	GetCurrentState() domain.VersionedState
}

// Phase 4 Improvement: commitment-lock
type Commitment struct {
	TaskID      string
	MinDuration time.Duration
	StartTime   time.Time
}

type Service struct {
	sessionID     string
	bus           domain.EventBus
	planner       *AdvancedPlanner
	planManager   *PlanManager
	curriculum    voyager.Curriculum
	critic        voyager.Critic
	stateProvider StateProvider
	logger        *slog.Logger

	mu            sync.Mutex
	evalSemaphore chan struct{}
	activeIntent  atomic.Pointer[domain.ActionIntent]
	beforeState   atomic.Pointer[domain.GameState]
	taskHistory   []domain.TaskHistory

	commitment atomic.Pointer[Commitment]
}

func NewService(
	sessionID string,
	bus domain.EventBus,
	planner *AdvancedPlanner,
	pm *PlanManager,
	curriculum voyager.Curriculum,
	critic voyager.Critic,
	stateProvider StateProvider,
	logger *slog.Logger,
) *Service {
	s := &Service{
		sessionID:     sessionID,
		bus:           bus,
		planner:       planner,
		planManager:   pm,
		curriculum:    curriculum,
		critic:        critic,
		stateProvider: stateProvider,
		logger:        logger.With(slog.String("component", "decision_service")),
		evalSemaphore: make(chan struct{}, 1),
		taskHistory:   make([]domain.TaskHistory, 0),
	}

	bus.Subscribe(domain.EventTypeStateUpdated, domain.FuncHandler(s.handleStateUpdated))
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))

	return s
}

func (s *Service) handleStateUpdated(ctx context.Context, event domain.DomainEvent) {
	if !s.planManager.HasActivePlan() {
		go s.evaluateNextPlan(context.Background())
	}
}

func (s *Service) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	var payload struct {
		Status    string `json:"status"`
		CommandID string `json:"command_id"`
		Cause     string `json:"cause"`
	}

	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return
	}

	success := payload.Status == "COMPLETED"

	intent := s.activeIntent.Load()
	beforePtr := s.beforeState.Load()

	if intent != nil && beforePtr != nil && intent.ID == payload.CommandID {
		after := s.stateProvider.GetCurrentState().State
		successCritic, critique := s.critic.Evaluate(*intent, *beforePtr, after)
		if !success {
			successCritic = false
			critique = fmt.Sprintf("TS Failed: %s. %s", payload.Cause, critique)
		}

		s.mu.Lock()
		s.taskHistory = append(s.taskHistory, domain.TaskHistory{
			Intent: *intent, Success: successCritic, Critique: critique,
		})
		s.mu.Unlock()

		s.activeIntent.Store(nil)
		s.beforeState.Store(nil)
	}

	if !s.planManager.HasActivePlan() {
		return
	}

	if !success {
		s.logger.Warn("Plan failed due to task failure", slog.String("failed_task", payload.CommandID))
		_ = s.planManager.Transition(domain.PlanStatusFailed)

		// Clear commitment on failure
		s.commitment.Store(nil)

		s.bus.Publish(ctx, domain.DomainEvent{
			SessionID: s.sessionID,
			Type:      domain.EventTypePlanInvalidated,
			CreatedAt: time.Now(),
		})
		go s.evaluateNextPlan(context.Background())
		return
	}

	_ = s.planManager.Transition(domain.PlanStatusCompleted)
	go s.evaluateNextPlan(context.Background())
}

func (s *Service) evaluateNextPlan(ctx context.Context) {
	select {
	case s.evalSemaphore <- struct{}{}:
	default:
		return
	}
	defer func() { <-s.evalSemaphore }()

	state := s.stateProvider.GetCurrentState().State
	if state.Health <= 0 {
		return
	}

	// Phase 4 Improvement: commitment-lock
	if currCommitment := s.commitment.Load(); currCommitment != nil {
		if time.Since(currCommitment.StartTime) < currCommitment.MinDuration {
			// Allow overriding the lock for CRITICAL severity events (e.g., taking damage, low health)
			if state.Health >= 10 && len(state.Threats) == 0 {
				return
			}
			s.logger.Info("Breaking commitment lock for critical survival event")
		}
	}

	s.logger.Info("Evaluating next objective")
	plan := s.planner.FastPlan(state)

	if plan.Objective == "Reactive Fallback Plan" && s.curriculum != nil {
		s.logger.Info("Planner cache empty, falling back to curriculum")

		s.mu.Lock()
		historyCopy := make([]domain.TaskHistory, len(s.taskHistory))
		copy(historyCopy, s.taskHistory)
		s.mu.Unlock()

		intent, err := s.curriculum.ProposeTask(ctx, state, historyCopy, s.sessionID)
		if err == nil && intent != nil {
			plan = domain.Plan{
				Objective: "Curriculum Fallback",
				Tasks: []domain.Action{
					{
						ID:        intent.ID,
						Action:    intent.Action,
						Target:    domain.Target{Name: intent.Target, Type: "inferred"},
						Count:     intent.Count,
						Rationale: intent.Rationale,
						Priority:  50,
					},
				},
			}
		}
	}

	if len(plan.Tasks) == 0 {
		return
	}

	s.planManager.SetPlan(&plan)
	_ = s.planManager.Transition(domain.PlanStatusActive)

	firstTask := plan.Tasks[0]

	// Set the commitment lock for stable, non-twitchy task execution
	s.commitment.Store(&Commitment{
		TaskID:      firstTask.ID,
		StartTime:   time.Now(),
		MinDuration: 2 * time.Second, // Minimum time before we allow replanning
	})

	s.activeIntent.Store(&domain.ActionIntent{
		ID:        firstTask.ID,
		Action:    firstTask.Action,
		Target:    firstTask.Target.Name,
		Count:     firstTask.Count,
		Rationale: firstTask.Rationale,
	})
	s.beforeState.Store(&state)

	payload, _ := json.Marshal(plan)
	s.bus.Publish(ctx, domain.DomainEvent{
		SessionID: s.sessionID,
		Trace: domain.TraceContext{
			TraceID:  fmt.Sprintf("tr-%d", time.Now().UnixNano()),
			ActionID: firstTask.ID,
		},
		Type:      domain.EventTypePlanCreated,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
}
