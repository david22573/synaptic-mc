package decision

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/planner"
	"david22573/synaptic-mc/internal/voyager"
)

type StateProvider interface {
	GetCurrentState() domain.VersionedState
}

type ExecutionStatus interface {
	IsIdle() bool
}

type Service struct {
	ctx           context.Context
	sessionID     string
	bus           domain.EventBus
	planner       *AdvancedPlanner
	planManager   *PlanManager
	curriculum    voyager.Curriculum
	critic        voyager.Critic
	stateProvider StateProvider
	execStatus    ExecutionStatus
	worldModel    *domain.WorldModel
	memStore      memory.Store
	feedback      *planner.FeedbackAnalyzer
	logger        *slog.Logger
	flags         config.FeatureFlags
	skillManager  *voyager.SkillManager

	predictor     *StrategyPredictor
	routeScorer   *RouteScorer

	mu            sync.Mutex
	evalTrigger   chan struct{}
	activeIntent  atomic.Pointer[domain.ActionIntent]
	beforeState   atomic.Pointer[domain.GameState]
	taskHistory   []domain.TaskHistory
	milestones    []domain.ProgressionMilestone

	synthesisCache map[string]bool
	synthMu        sync.Mutex

	commitment atomic.Pointer[Commitment]
}

func NewService(
	ctx context.Context,
	sessionID string,
	bus domain.EventBus,
	advPlanner *AdvancedPlanner,
	pm *PlanManager,
	curriculum voyager.Curriculum,
	critic voyager.Critic,
	stateProvider StateProvider,
	execStatus ExecutionStatus,
	worldModel *domain.WorldModel,
	memStore memory.Store,
	feedback *planner.FeedbackAnalyzer,
	skillManager *voyager.SkillManager,
	logger *slog.Logger,
	flags config.FeatureFlags,
) *Service {
	s := &Service{
		ctx:            ctx,
		sessionID:      sessionID,
		bus:            bus,
		planner:        advPlanner,
		planManager:    pm,
		curriculum:     curriculum,
		critic:         critic,
		stateProvider:  stateProvider,
		execStatus:     execStatus,
		worldModel:     worldModel,
		memStore:       memStore,
		feedback:       feedback,
		logger:         logger.With(slog.String("component", "decision_service")),
		flags:          flags,
		evalTrigger:    make(chan struct{}, 1),
		taskHistory:    make([]domain.TaskHistory, 0),
		milestones:     make([]domain.ProgressionMilestone, 0),
		skillManager:   skillManager,
		predictor:      NewStrategyPredictor(memStore),
		routeScorer:    &RouteScorer{},
		synthesisCache: make(map[string]bool),
	}

	// Load persistent state
	if memStore != nil {
		history, err := memStore.GetTaskHistory(ctx, sessionID, 50)
		if err == nil {
			s.taskHistory = history
			s.logger.Info("Loaded task history from persistent store", slog.Int("count", len(history)))
		}

		milestones, err := memStore.GetMilestones(ctx, sessionID)
		if err == nil {
			s.milestones = milestones
			s.logger.Info("Loaded milestones from persistent store", slog.Int("count", len(milestones)))
		}

		failures, err := memStore.GetFailureCounts(ctx, sessionID)
		if err == nil {
			s.planner.SetFailures(failures)
			s.logger.Info("Loaded plan failure counts from persistent store", slog.Int("count", len(failures)))
		}
	}

	bus.Subscribe(domain.EventTypeStateUpdated, domain.FuncHandler(s.handleStateUpdated))
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))
	bus.Subscribe(domain.EventTypePlanInvalidated, domain.FuncHandler(s.handlePlanInvalidated))
	bus.Subscribe(domain.EventBotRespawn, domain.FuncHandler(s.handleBotRespawn))

	// Link planner background completions to service evaluations
	advPlanner.SetOnPlanReady(func() {
		s.logger.Info("Background plan ready, requesting evaluation")
		s.evaluateNextPlan()
	})

	// Evaluation background loop
	go s.runEvaluationLoop(ctx)

	// Jumpstart background loop
	go func() {
		ticker := time.NewTicker(12 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !s.planManager.HasActivePlan() && s.activeIntent.Load() == nil && s.execStatus.IsIdle() {
					s.logger.Info("Jumpstart: bot is idle, forcing evaluation")
					s.planner.TriggerReplan(s.stateProvider.GetCurrentState().State)
					s.evaluateNextPlan()
				}
			}
		}
	}()

	return s
}

func (s *Service) RequestEvaluation() {
	s.evaluateNextPlan()
}
