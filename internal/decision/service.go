package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/planner"
	"david22573/synaptic-mc/internal/voyager"
)

type StateProvider interface {
	GetCurrentState() domain.VersionedState
}

type ExecutionStatus interface {
	IsIdle() bool
}

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
	execStatus    ExecutionStatus
	worldModel    *domain.WorldModel
	feedback      *planner.FeedbackAnalyzer
	logger        *slog.Logger

	mu            sync.Mutex
	evalSemaphore chan struct{}
	activeIntent  atomic.Pointer[domain.ActionIntent]
	beforeState   atomic.Pointer[domain.GameState]
	taskHistory   []domain.TaskHistory

	commitment atomic.Pointer[Commitment]
}

func shouldWaitForFreshState(cause string) bool {
	switch cause {
	case domain.CauseSurvivalPanic, domain.CausePanic, domain.CausePanicTriggered, domain.CauseUnlock:
		return true
	default:
		return false
	}
}

func hasImmediateThreat(state domain.GameState) bool {
	for _, threat := range state.Threats {
		if threat.Distance <= domain.SurvivalMaxThreatDist {
			return true
		}
	}
	return false
}

func isValidCurriculumIntent(state domain.GameState, intent *domain.ActionIntent) bool {
	if intent == nil || intent.Action == "" {
		return false
	}

	target := strings.ToLower(strings.TrimSpace(intent.Target))
	switch intent.Action {
	case "gather":
		switch target {
		case "", "none", "air", "water", "lava":
			return false
		}
	case "eat":
		for _, item := range state.Inventory {
			if strings.EqualFold(item.Name, target) && item.Count > 0 && domain.IsFood(item.Name) {
				return true
			}
		}
		return false
	}

	return true
}

func NewService(
	sessionID string,
	bus domain.EventBus,
	advPlanner *AdvancedPlanner,
	pm *PlanManager,
	curriculum voyager.Curriculum,
	critic voyager.Critic,
	stateProvider StateProvider,
	execStatus ExecutionStatus,
	worldModel *domain.WorldModel,
	feedback *planner.FeedbackAnalyzer,
	logger *slog.Logger,
) *Service {
	s := &Service{
		sessionID:     sessionID,
		bus:           bus,
		planner:       advPlanner,
		planManager:   pm,
		curriculum:    curriculum,
		critic:        critic,
		stateProvider: stateProvider,
		execStatus:    execStatus,
		worldModel:    worldModel,
		feedback:      feedback,
		logger:        logger.With(slog.String("component", "decision_service")),
		evalSemaphore: make(chan struct{}, 1),
		taskHistory:   make([]domain.TaskHistory, 0),
	}

	bus.Subscribe(domain.EventTypeStateUpdated, domain.FuncHandler(s.handleStateUpdated))
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))
	bus.Subscribe(domain.EventTypePlanInvalidated, domain.FuncHandler(s.handlePlanInvalidated))
	bus.Subscribe(domain.EventBotRespawn, domain.FuncHandler(s.handleBotRespawn))

	return s
}

func Validate(plan *domain.Plan, state domain.GameState) bool {
	if plan == nil || len(plan.Tasks) == 0 {
		return false
	}

	// Only validate the FIRST task. We re-plan after each task completion anyway.
	// Validating future tasks against the current state is too restrictive.
	task := plan.Tasks[0]

	hasPickaxe := false
	hasCraftingTable := false

	for _, item := range state.Inventory {
		if strings.Contains(item.Name, "pickaxe") {
			hasPickaxe = true
		}
		if item.Name == "crafting_table" {
			hasCraftingTable = true
		}
	}

	for _, poi := range state.POIs {
		if poi.Name == "crafting_table" {
			hasCraftingTable = true
		}
	}

	switch task.Action {
	case "eat":
		if state.Food >= domain.DecisionFoodMax {
			return false
		}
		// Check if we actually have food
		hasFood := false
		for _, item := range state.Inventory {
			if domain.IsFood(item.Name) {
				hasFood = true
				break
			}
		}
		return hasFood
	case "craft":
		if len(state.Inventory) == 0 {
			return false
		}
		if strings.Contains(task.Target.Name, "pickaxe") && !hasCraftingTable {
			return false
		}
	case "mine":
		if !hasPickaxe {
			return false
		}
	case "hunt":
		if state.Health < domain.DecisionHealthHunt {
			return false
		}
	}

	return true
}

func (s *Service) handleStateUpdated(ctx context.Context, event domain.DomainEvent) {
	s.planner.TriggerReplan(s.stateProvider.GetCurrentState().State)
	if !s.planManager.HasActivePlan() {
		go s.evaluateNextPlan(context.Background())
	}
}

func (s *Service) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	var payload domain.TaskEndPayload

	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return
	}

	success := payload.Status == "COMPLETED"
	controlledStop := domain.IsControlledStop(payload.Cause)

	intent := s.activeIntent.Load()
	beforePtr := s.beforeState.Load()

	if intent != nil && beforePtr != nil && intent.ID == payload.CommandID {
		after := s.stateProvider.GetCurrentState().State

		failureCount := 0
		currentPlan := s.planManager.GetCurrent()
		if currentPlan != nil {
			failureCount = s.planner.GetFailureCount(currentPlan.Objective)
		}

		// Phase 5.5: Analyze feedback for tactical adaptation
		res := domain.ExecutionResult{
			Success:  success,
			Cause:    payload.Cause,
			Progress: payload.Progress,
			Action: domain.Action{
				Action: payload.Action,
				Target: domain.Target{Name: payload.Target},
			},
		}
		adaptedIntent := s.feedback.Analyze(*intent, res)

		successCritic, critique := s.critic.Evaluate(*intent, *beforePtr, after, res, failureCount)

		if !success {
			successCritic = false
			critique = fmt.Sprintf("TS Failed: %s. %s", payload.Cause, critique)

			// Learn from failure: penalize the action and the location
			if s.worldModel != nil {
				s.worldModel.PenalizeAction(intent.Action, 1.0)
				loc := domain.Location{X: beforePtr.Position.X, Y: beforePtr.Position.Y, Z: beforePtr.Position.Z}
				s.worldModel.PenalizeZone(loc, 0.5)
			}

			// If analyzer provided a tactical fallback, inject it immediately
			if adaptedIntent != nil && adaptedIntent.ID != intent.ID {
				s.logger.Info("FeedbackAnalyzer injected tactical fallback", slog.String("action", adaptedIntent.Action))
				s.activeIntent.Store(adaptedIntent)
				s.dispatchActiveIntent(ctx, adaptedIntent)
				return
			}
		} else {
			// Learn from success: reward the action
			if s.worldModel != nil {
				s.worldModel.RecordSuccess(intent.Action, intent.Target)
			}
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

	if !success && !controlledStop {
		s.logger.Warn("Plan failed due to task failure", slog.String("failed_task", payload.CommandID))
		s.commitment.Store(nil)

		currentPlan := s.planManager.GetCurrent()
		if currentPlan != nil {
			s.planner.RecordFailure(currentPlan.Objective)
		}

		if s.planManager.NextFallback() {
			s.logger.Info("Attempting fallback plan candidate")
			_ = s.planManager.Transition(domain.PlanStatusActive)
			s.dispatchActivePlan(ctx)
			return
		}

		_ = s.planManager.Transition(domain.PlanStatusFailed)

		s.bus.Publish(ctx, domain.DomainEvent{
			SessionID: s.sessionID,
			Type:      domain.EventTypePlanInvalidated,
			CreatedAt: time.Now(),
		})
		go s.evaluateNextPlan(context.Background())
		return
	}

	if controlledStop {
		s.commitment.Store(nil)
		if !shouldWaitForFreshState(payload.Cause) {
			go s.evaluateNextPlan(context.Background())
		}
		return
	}

	// FIX: Use thread-safe PopTask to progress the plan, avoiding the manual slice race condition
	hasMoreTasks := s.planManager.PopTask(payload.CommandID)
	if hasMoreTasks {
		s.dispatchActivePlan(ctx)
		return
	}

	currentPlan := s.planManager.GetCurrent()
	if currentPlan != nil {
		s.planner.RecordSuccess(currentPlan.Objective)
	}

	// FIX: Tell the planner we finished the plan so it stops serving stale cached pointers
	s.planner.ClearCurrentPlan()

	_ = s.planManager.Transition(domain.PlanStatusCompleted)
	go s.evaluateNextPlan(context.Background())
}

func (s *Service) dispatchActiveIntent(ctx context.Context, intent *domain.ActionIntent) {
	if intent == nil {
		return
	}

	s.commitment.Store(&Commitment{
		TaskID:      intent.ID,
		StartTime:   time.Now(),
		MinDuration: 2 * time.Second,
	})

	// Wrap the single intent into a plan for the wire
	plan := domain.Plan{
		ID:        fmt.Sprintf("adapted-%d", time.Now().UnixNano()),
		Objective: "Tactical Adaptation",
		Tasks: []domain.Action{
			{
				ID:        intent.ID,
				Action:    intent.Action,
				Target:    domain.Target{Name: intent.Target, Type: "adapted"},
				Count:     intent.Count,
				Rationale: intent.Rationale,
				Priority:  100,
			},
		},
	}

	payload, _ := json.Marshal(plan)
	s.bus.Publish(ctx, domain.DomainEvent{
		SessionID: s.sessionID,
		Trace: domain.TraceContext{
			TraceID:  fmt.Sprintf("tr-ad-%d", time.Now().UnixNano()),
			ActionID: intent.ID,
		},
		Type:      domain.EventTypePlanCreated,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
}

func (s *Service) handlePlanInvalidated(ctx context.Context, event domain.DomainEvent) {
	s.commitment.Store(nil)
	s.activeIntent.Store(nil)
	s.beforeState.Store(nil)
	s.planner.ClearCurrentPlan()
	s.planManager.Clear()
}

func (s *Service) handleBotRespawn(ctx context.Context, event domain.DomainEvent) {
	s.handlePlanInvalidated(ctx, event)
	go s.evaluateNextPlan(context.Background())
}

func (s *Service) RequestEvaluation() {
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

	survivalOverride := hasImmediateThreat(state) ||
		(state.Health < domain.SurvivalCriticalHealth) ||
		(state.Health < domain.DecisionHealthSafe && state.Food < domain.SurvivalMinFoodForHunt)

	// Keep the current plan/task flowing unless survival needs to interrupt it.
	if !survivalOverride {
		if s.activeIntent.Load() != nil || s.planManager.HasActivePlan() || !s.execStatus.IsIdle() {
			return
		}
	}

	if currCommitment := s.commitment.Load(); currCommitment != nil {
		if time.Since(currCommitment.StartTime) < currCommitment.MinDuration {
			if state.Health >= domain.DecisionHealthSafe && len(state.Threats) == 0 {
				return
			}
			s.logger.Info("Breaking commitment lock for critical survival event")
		}
	}

	var plan domain.Plan

	if survivalOverride {
		s.logger.Warn("Survival priority override active")
		plan = s.planner.reactivePlan(state)
	} else {
		s.logger.Info("Evaluating next objective")
		plan = s.planner.FastPlan(state)
	}

	if !Validate(&plan, state) {
		s.logger.Warn("Generated plan failed validation, falling back to curriculum", slog.String("objective", plan.Objective))
		plan.IsFallback = true
		plan.Tasks = nil
	}

	if (plan.IsFallback || len(plan.Tasks) == 0) && s.curriculum != nil {
		s.logger.Info("Planner cache empty, falling back to curriculum")

		s.mu.Lock()
		historyCopy := make([]domain.TaskHistory, len(s.taskHistory))
		copy(historyCopy, s.taskHistory)
		s.mu.Unlock()

		intent, err := s.curriculum.ProposeTask(ctx, state, historyCopy, s.sessionID)
		if err == nil && intent != nil && isValidCurriculumIntent(state, intent) {
			plan = domain.Plan{
				Objective:  "Curriculum Fallback",
				IsFallback: true,
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
		} else if err == nil && intent != nil {
			s.logger.Warn("Rejected invalid curriculum intent",
				slog.String("action", intent.Action),
				slog.String("target", intent.Target),
			)
		}
	}

	if len(plan.Tasks) == 0 {
		if !survivalOverride {
			s.logger.Debug("Planner warming up, waiting for background or curiosity loop")
		}
		return
	}

	s.planManager.SetPlan(&plan)
	_ = s.planManager.Transition(domain.PlanStatusActive)
	s.dispatchActivePlan(ctx)
}

func (s *Service) dispatchActivePlan(ctx context.Context) {
	plan := s.planManager.GetCurrent()
	if plan == nil || len(plan.Tasks) == 0 {
		return
	}

	firstTask := plan.Tasks[0]

	s.commitment.Store(&Commitment{
		TaskID:      firstTask.ID,
		StartTime:   time.Now(),
		MinDuration: 2 * time.Second,
	})

	s.activeIntent.Store(&domain.ActionIntent{
		ID:        firstTask.ID,
		Action:    firstTask.Action,
		Target:    firstTask.Target.Name,
		Count:     firstTask.Count,
		Rationale: firstTask.Rationale,
	})

	currentState := s.stateProvider.GetCurrentState().State
	s.beforeState.Store(&currentState)

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
