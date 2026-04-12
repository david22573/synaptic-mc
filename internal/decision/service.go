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
	memStore      memory.Store
	feedback      *planner.FeedbackAnalyzer
	logger        *slog.Logger
	flags         config.FeatureFlags
	skillManager  *voyager.SkillManager

	mu            sync.Mutex
	evalSemaphore chan struct{}
	activeIntent  atomic.Pointer[domain.ActionIntent]
	beforeState   atomic.Pointer[domain.GameState]
	taskHistory   []domain.TaskHistory
	milestones    []domain.ProgressionMilestone

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
	case "use_skill":
		return target != ""
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
	memStore memory.Store,
	feedback *planner.FeedbackAnalyzer,
	skillManager *voyager.SkillManager,
	logger *slog.Logger,
	flags config.FeatureFlags,
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
		memStore:      memStore,
		feedback:      feedback,
		logger:        logger.With(slog.String("component", "decision_service")),
		flags:         flags,
		evalSemaphore: make(chan struct{}, 1),
		taskHistory:   make([]domain.TaskHistory, 0),
		milestones:    make([]domain.ProgressionMilestone, 0),
		skillManager:  skillManager,
	}

	// Load persistent state
	if memStore != nil {
		history, err := memStore.GetTaskHistory(context.Background(), sessionID, 50)
		if err == nil {
			s.taskHistory = history
			s.logger.Info("Loaded task history from persistent store", slog.Int("count", len(history)))
		}

		milestones, err := memStore.GetMilestones(context.Background(), sessionID)
		if err == nil {
			s.milestones = milestones
			s.logger.Info("Loaded milestones from persistent store", slog.Int("count", len(milestones)))
		}
	}

	bus.Subscribe(domain.EventTypeStateUpdated, domain.FuncHandler(s.handleStateUpdated))
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))
	bus.Subscribe(domain.EventTypePlanInvalidated, domain.FuncHandler(s.handlePlanInvalidated))
	bus.Subscribe(domain.EventBotRespawn, domain.FuncHandler(s.handleBotRespawn))

	// Link planner background completions to service evaluations
	advPlanner.SetOnPlanReady(func() {
		s.logger.Info("Background plan ready, requesting evaluation")
		s.RequestEvaluation()
	})

	// Jumpstart background loop: ensures the bot doesn't stay idle if events are missed
	// or if the state is stable but no plan is active.
	go func() {
		ticker := time.NewTicker(12 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !s.planManager.HasActivePlan() && s.activeIntent.Load() == nil && s.execStatus.IsIdle() {
					s.logger.Info("Jumpstart: bot is idle, forcing evaluation")
					s.planner.TriggerReplan(s.stateProvider.GetCurrentState().State)
					go s.evaluateNextPlan(context.Background())
				}
			}
		}
	}()

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
	s.planner.SetMilestoneContext(s.getMilestoneContext())
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

			// Milestone detection
			s.detectMilestones(beforePtr, &after)

			if payload.Success && s.skillManager != nil {
				// Type-assert the curriculum to check if it can synthesize code.
				// This avoids changing the Curriculum interface.
				synth, ok := s.curriculum.(voyager.CodeSynthesizer)
				if ok {
					beforeState := s.beforeState.Load()
					afterState := s.stateProvider.GetCurrentState().State

					if beforeState != nil {
						intentVal := s.activeIntent.Load()
						if intentVal != nil && intentVal.Action != "" && intentVal.Action != "idle" && intentVal.Action != "use_skill" {
							// Run synthesis in background so it doesn't block the planning loop.
							go func(intent domain.ActionIntent, before, after domain.GameState) {
								ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
								defer cancel()

								jsCode, err := synth.SynthesizeCode(ctx, intent, before, after)
								if err != nil {
									s.logger.Warn("Skill synthesis failed", slog.String("action", intent.Action), slog.Any("error", err))
									return
								}
								if jsCode == "" {
									return
								}

								skillName := fmt.Sprintf("%s_%s", intent.Action, sanitizeSkillName(intent.Target))
								skill := voyager.ExecutableSkill{
									Name:        skillName,
									Description: fmt.Sprintf("%s %s (count: %d)", intent.Action, intent.Target, intent.Count),
									JSCode:      jsCode,
								}

								if err := s.skillManager.SaveSkill(ctx, skill); err != nil {
									s.logger.Warn("Failed to persist skill", slog.String("name", skillName), slog.Any("error", err))
									return
								}
								s.logger.Info("Skill synthesized and saved", slog.String("name", skillName))
							}(*intentVal, *beforeState, afterState)
						}
					}
				}
			}
		}

		s.mu.Lock()
		newHistory := domain.TaskHistory{
			Intent: *intent, Success: successCritic, Critique: critique,
		}
		s.taskHistory = append(s.taskHistory, newHistory)

		// Trim in-memory history to 200 entries max
		if len(s.taskHistory) > 200 {
			s.taskHistory = s.taskHistory[len(s.taskHistory)-200:]
		}
		s.mu.Unlock()

		// Persist to SQLite
		if s.memStore != nil {
			go func(h domain.TaskHistory) {
				dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := s.memStore.SaveTaskHistory(dbCtx, s.sessionID, []domain.TaskHistory{h}); err != nil {
					s.logger.Error("Failed to persist task history", slog.Any("error", err))
				}
			}(newHistory)
		}

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
			Payload:   []byte(`{}`), // FIX: Prevent EOF unmarshal error on UI broadcast
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

func (s *Service) getTaskHistory() []domain.TaskHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	historyCopy := make([]domain.TaskHistory, len(s.taskHistory))
	copy(historyCopy, s.taskHistory)
	return historyCopy
}

var techTree = []string{
	"crafting_table",
	"wooden_pickaxe",
	"stone_pickaxe",
	"furnace",
	"iron_pickaxe",
	"iron_ingot",
	"iron_sword",
	"iron_chestplate",
	"diamond_pickaxe",
	"diamond",
}

func (s *Service) detectMilestones(before, after *domain.GameState) {
	beforeItems := make(map[string]bool)
	for _, item := range before.Inventory {
		beforeItems[item.Name] = true
	}

	for _, item := range techTree {
		alreadyUnlocked := false
		s.mu.Lock()
		for _, m := range s.milestones {
			if m.Name == item {
				alreadyUnlocked = true
				break
			}
		}
		s.mu.Unlock()

		if alreadyUnlocked {
			continue
		}

		// Check if it's new in inventory
		hasItNow := false
		for _, inv := range after.Inventory {
			if inv.Name == item {
				hasItNow = true
				break
			}
		}

		if hasItNow && !beforeItems[item] {
			s.logger.Info("Milestone unlocked!", slog.String("name", item))
			m := domain.ProgressionMilestone{Name: item, UnlockedAt: time.Now()}
			s.mu.Lock()
			s.milestones = append(s.milestones, m)
			s.mu.Unlock()

			if s.memStore != nil {
				go func(name string) {
					dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = s.memStore.SaveMilestone(dbCtx, s.sessionID, name)
				}(item)
			}
		}
	}
}

func (s *Service) getMilestoneContext() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.milestones) == 0 {
		return "PROGRESSION STATUS: Just started. Goal: crafting_table, wooden_pickaxe."
	}

	var unlocked []string
	for _, m := range s.milestones {
		unlocked = append(unlocked, m.Name)
	}

	// Simple next goal derivation
	nextGoal := ""
	for _, item := range techTree {
		found := false
		for _, u := range unlocked {
			if u == item {
				found = true
				break
			}
		}
		if !found {
			nextGoal = item
			break
		}
	}

	return fmt.Sprintf("PROGRESSION STATUS:\n  UNLOCKED: %s\n  ACTIVE GOAL: acquire_%s",
		strings.Join(unlocked, ", "), nextGoal)
}

func isProgressionMode(state domain.GameState) bool {
	// Progression mode active when survival is stable:
	// health > 14 (enough to survive a surprise),
	// food > 10 (not starving),
	// no immediate threats.
	return state.Health > 14 && state.Food > 10 && len(state.Threats) == 0
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
	} else if isProgressionMode(state) && s.curriculum != nil {
		s.logger.Info("Stable state detected, curriculum driving progression")

		intent, err := s.curriculum.ProposeTask(ctx, state, s.getTaskHistory(), s.getMilestoneContext(), s.sessionID, s.flags.CurriculumHorizon)
		if err == nil && intent != nil && isValidCurriculumIntent(state, intent) {
			if intent.Action == "use_skill" && len(intent.SkillSteps) > 0 {
				s.logger.Info("Expanding composable skill", slog.String("skill", intent.Target), slog.Int("steps", len(intent.SkillSteps)))
				plan = domain.Plan{
					Objective:  intent.Rationale,
					IsFallback: false,
					Tasks:      make([]domain.Action, len(intent.SkillSteps)),
				}
				for i, step := range intent.SkillSteps {
					plan.Tasks[i] = domain.Action{
						ID:        fmt.Sprintf("%s-step-%d", intent.ID, i),
						Action:    step.Action,
						Target:    domain.Target{Name: step.Target, Type: "skill_step"},
						Count:     step.Count,
						Rationale: step.Rationale,
						Priority:  75 - i, // Slightly declining priority for later steps
					}
				}
			} else {
				plan = domain.Plan{
					Objective:  intent.Rationale,
					IsFallback: false,
					Tasks: []domain.Action{
						{
							ID:        fmt.Sprintf("curr-%d", time.Now().UnixNano()), // FIX: Force unique ID to avoid idempotency drops from LLM hallucinated loops
							Action:    intent.Action,
							Target:    domain.Target{Name: intent.Target, Type: "curriculum"},
							Count:     intent.Count,
							Rationale: intent.Rationale,
							Priority:  70,
						},
					},
				}
			}
		} else {
			if err != nil {
				s.logger.Error("Curriculum failed to propose task, falling back to tactical planner", slog.Any("error", err))
			} else {
				s.logger.Info("Curriculum provided no intent, using tactical planner")
			}
			plan = s.planner.FastPlan(state)
		}
	} else {
		s.logger.Info("Evaluating next tactical objective")
		plan = s.planner.FastPlan(state)
	}

	if !Validate(&plan, state) {
		s.logger.Warn("Generated plan failed validation, falling back to curriculum", slog.String("objective", plan.Objective))
		plan.IsFallback = true
		plan.Tasks = nil
	}

	if (plan.IsFallback || len(plan.Tasks) == 0) && s.curriculum != nil {
		// Only trigger fallback if we didn't just try curriculum (or if it's a critical fallback)
		if plan.Objective != "Curriculum Fallback" && plan.Objective != "Curriculum" {
			s.logger.Info("Planner failed or empty, using curriculum as fallback")

			intent, err := s.curriculum.ProposeTask(ctx, state, s.getTaskHistory(), s.getMilestoneContext(), s.sessionID, s.flags.CurriculumHorizon)
			if err == nil && intent != nil && isValidCurriculumIntent(state, intent) {
				plan = domain.Plan{
					Objective:  "Curriculum Fallback",
					IsFallback: true,
					Tasks: []domain.Action{
						{
							ID:        fmt.Sprintf("curr-fb-%d", time.Now().UnixNano()), // FIX: Force unique ID
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

// sanitizeSkillName converts an arbitrary target string into a valid skill name.
func sanitizeSkillName(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	target = strings.ReplaceAll(target, " ", "_")
	// Remove any characters that aren't alphanumeric or underscore.
	var b strings.Builder
	for _, r := range target {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	result := b.String()
	if result == "" {
		return "unnamed"
	}
	return result
}
