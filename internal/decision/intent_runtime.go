package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/observability"
	"david22573/synaptic-mc/internal/state"
	"david22573/synaptic-mc/internal/voyager"
)

func (s *Service) handleStateUpdated(ctx context.Context, event domain.DomainEvent) {
	gs := s.stateProvider.GetCurrentState().State

	// Phase 3: Danger State Machine
	dangerState := s.arbiter.UpdateDanger(gs)

	// PROACTIVE: Break commitment lock for survival panics only if cooldown allows
	if dangerState == state.DangerEscape || dangerState == state.DangerAlert {
		s.mu.Lock()
		if time.Since(s.lastOverrideTime) >= s.overrideCooldown {
			s.lastOverrideTime = time.Now()
			s.mu.Unlock()
			s.logger.Warn("Survival priority override active (Arbiter), breaking lock", slog.String("danger", string(dangerState)))
			s.commitment.Store(nil)
			s.evaluateNextPlan()
			return
		}
		s.mu.Unlock()
	}

	// Trigger Discipline: Only replan on meaningful state changes
	if s.shouldReplan(gs) {
		s.planner.SetMilestoneContext(s.getMilestoneContext())
		s.planner.TriggerReplan(gs)

		if !s.planManager.HasActivePlan() && s.execStatus.IsIdle() {
			s.evaluateNextPlan()
		}
	}
}

func (s *Service) shouldReplan(gs domain.GameState) bool {
	before := s.beforeState.Load()
	if before == nil {
		return true
	}

	// 1. Danger resolved
	if len(before.Threats) > 0 && len(gs.Threats) == 0 {
		return true
	}

	// 2. Health/Food milestone (bucketed)
	if int(gs.Health)/2 != int(before.Health)/2 || int(gs.Food)/2 != int(before.Food)/2 {
		return true
	}

	// 3. TimeOfDay change (significant - transition to night/day)
	if (gs.TimeOfDay < 12000 && before.TimeOfDay >= 12000) || (gs.TimeOfDay >= 12000 && before.TimeOfDay < 12000) {
		return true
	}

	// 4. Large position change (> 50 blocks)
	if gs.Position.DistanceTo(before.Position) > 50 {
		return true
	}

	// 5. Significant inventory change (new item type)
	if len(gs.Inventory) != len(before.Inventory) {
		return true
	}

	return false
}

func (s *Service) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	var payload domain.TaskEndPayload

	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return
	}

	// Performance Budgeting: Track task runtime
	if payload.DurationMs > 0 {
		observability.Metrics.TaskExecDuration.Observe(float64(payload.DurationMs))
	}

	success := payload.Status == "COMPLETED"
	preempted := payload.Status == "PREEMPTED"

	if success || preempted {
		s.modeManager.RecordSuccess()
		if preempted {
			observability.Metrics.IncPreemption()
		}
	} else if !domain.IsControlledStop(payload.Cause) {
		s.modeManager.RecordFailure()
	}

	// Phase 4: Single Writer Action Bus
	s.arbiter.HandleTaskEnd(payload)

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

		res := domain.ExecutionResult{
			Success:  success,
			Cause:    payload.Cause,
			Progress: payload.Progress,
			Action: domain.Action{
				Action: payload.Action,
				Target: domain.Target{Name: payload.Target},
			},
		}
		var adaptedIntent *domain.ActionIntent
		if s.feedback != nil {
			adaptedIntent = s.feedback.Analyze(*intent, res)
		}

		successCritic, reflection := s.critic.Evaluate(*intent, *beforePtr, after, res, failureCount)
		critique := ""
		if reflection != nil {
			critique = reflection.Failure
			if reflection.Cause != "" {
				critique += " (Cause: " + reflection.Cause + ")"
			}
		}

		if success {
			observability.Metrics.IncTask()
			if intent.Action == "use_skill" {
				observability.Metrics.IncSkillReuse()
			}

			// Track resources gathered
			beforeInv := inventoryMap(*beforePtr)
			afterInv := inventoryMap(after)
			for item, count := range afterInv {
				if diff := count - beforeInv[item]; diff > 0 {
					observability.Metrics.AddResource(uint64(diff))
				}
			}
		} else {
			if res.Cause == domain.CauseBlocked || res.Cause == domain.CauseStuck || res.Cause == domain.CauseStuckTerrain {
				observability.Metrics.IncPathFailure()
				observability.Metrics.IncStuck()
			}
		}

		if !success {
			successCritic = false
			critique = fmt.Sprintf("TS Failed: %s. %s", payload.Cause, critique)

			if s.worldModel != nil {
				s.worldModel.PenalizeAction(intent.Action, 1.0)
				loc := domain.Location{X: beforePtr.Position.X, Y: beforePtr.Position.Y, Z: beforePtr.Position.Z}
				s.worldModel.PenalizeZone(loc, 0.5)
			}

			if intent.Action == "use_skill" && s.skillManager != nil {
				_ = s.skillManager.RecordSkillResult(ctx, intent.Target, false, payload.DurationMs, payload.Cause)
			}

			currentPlan := s.planManager.GetCurrent()
			if currentPlan != nil && s.memStore != nil {
				obj := currentPlan.Objective
				count := s.planner.GetFailureCount(obj)
				go func(sid, o string, c int) {
					dbCtx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
					defer cancel()
					_ = s.memStore.SaveFailureCount(dbCtx, sid, o, c)
				}(s.sessionID, obj, count)
			}

			if adaptedIntent != nil && adaptedIntent.ID != intent.ID {
				s.logger.Info("FeedbackAnalyzer injected tactical fallback", slog.String("action", adaptedIntent.Action))
				s.activeIntent.Store(adaptedIntent)
				s.dispatchActiveIntent(ctx, adaptedIntent)
				return
			}
		} else {
			if s.worldModel != nil {
				s.worldModel.RecordSuccess(intent.Action, intent.Target)
			}

			if intent.Action == "use_skill" && s.skillManager != nil {
				_ = s.skillManager.RecordSkillResult(ctx, intent.Target, true, payload.DurationMs, payload.Cause)
			}

			s.detectMilestones(beforePtr, &after)

			if payload.Success && s.skillManager != nil {
				synth, ok := s.curriculum.(voyager.CodeSynthesizer)
				if ok {
					beforeState := s.beforeState.Load()
					afterState := s.stateProvider.GetCurrentState().State

					if beforeState != nil {
						intentVal := s.activeIntent.Load()
						if intentVal != nil && intentVal.Action != "" && intentVal.Action != "idle" && intentVal.Action != "use_skill" {
							// Synthesis Deduplication: Only synthesize a skill once per action/target pair per session
							key := fmt.Sprintf("%s:%s", intentVal.Action, intentVal.Target)
							s.synthMu.Lock()
							if s.synthesisCache[key] {
								s.synthMu.Unlock()
								return
							}
							s.synthesisCache[key] = true
							s.synthMu.Unlock()

							go func(intent domain.ActionIntent, before, after domain.GameState) {
								ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
								defer cancel()

								result, err := synth.SynthesizeCode(ctx, intent, before, after)
								if err != nil {
									s.logger.Warn("Skill synthesis failed", slog.String("action", intent.Action), slog.Any("error", err))
									return
								}
								if result.JSCode == "" {
									return
								}

								skillName := fmt.Sprintf("%s_%s", intent.Action, sanitizeSkillName(intent.Target))
								skill := voyager.ExecutableSkill{
									Name:          skillName,
									Description:   fmt.Sprintf("%s %s (count: %d)", intent.Action, intent.Target, intent.Count),
									JSCode:        result.JSCode,
									RequiredItems: result.RequiredItems,
									Version:       1,
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
			Intent: *intent, Success: successCritic, Critique: critique, Reflection: reflection,
		}
		s.taskHistory = append(s.taskHistory, newHistory)

		if len(s.taskHistory) > 200 {
			s.taskHistory = s.taskHistory[len(s.taskHistory)-200:]
		}
		s.mu.Unlock()

		if s.memStore != nil {
			go func(h domain.TaskHistory) {
				dbCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
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
			Payload:   []byte(`{}`),
			CreatedAt: time.Now(),
		})
		go s.evaluateNextPlan()
		return
	}

	if controlledStop {
		s.commitment.Store(nil)
		if !shouldWaitForFreshState(payload.Cause) {
			go s.evaluateNextPlan()
		}
		return
	}

	// Fetch current plan before popping to check length for WarmNextPlan
	currentActivePlan := s.planManager.GetCurrent()

	hasMoreTasks, matched := s.planManager.PopTask(payload.CommandID)
	if !matched {
		return // Stale event, ignore to prevent accidental plan abandonment
	}

	if hasMoreTasks {
		s.dispatchActivePlan(ctx)
		return
	}

	if currentActivePlan != nil {
		s.planner.RecordSuccess(currentActivePlan.Objective)
		
		// Only warm the next plan if this was a substantial (multi-task) plan that just finished.
		// Single-task reactive/curriculum plans trigger immediate re-evaluations anyway.
		if len(currentActivePlan.Tasks) > 1 {
			s.planner.WarmNextPlan(ctx, s.sessionID, s.stateProvider.GetCurrentState().State)
		}

		if s.memStore != nil {
			go func(sid, o string) {
				dbCtx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
				defer cancel()
				_ = s.memStore.SaveFailureCount(dbCtx, sid, o, 0)
			}(s.sessionID, currentActivePlan.Objective)
		}
	}

	s.planner.ClearCurrentPlan()
	_ = s.planManager.Transition(domain.PlanStatusCompleted)
	go s.evaluateNextPlan()
}

func (s *Service) dispatchActivePlan(ctx context.Context) {
	plan := s.planManager.GetCurrent()
	if plan == nil || len(plan.Tasks) == 0 {
		return
	}

	firstTask := plan.Tasks[0]

	s.logger.Info("Dispatching active plan",
		slog.String("strategic_goal", plan.StrategicGoal),
		slog.Any("subgoals", plan.Subgoals),
		slog.String("objective", plan.Objective),
		slog.String("task_action", firstTask.Action),
		slog.String("task_target", firstTask.Target.Name))

	s.commitment.Store(&Commitment{
		TaskID:      firstTask.ID,
		Objective:   plan.Objective,
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

	// Phase 4: Single Writer Action Bus
	if !s.arbiter.Request(ctx, firstTask) {
		s.logger.Debug("Arbiter denied plan dispatch", slog.String("task_id", firstTask.ID))
		return
	}

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

func (s *Service) dispatchActiveIntent(ctx context.Context, intent *domain.ActionIntent) {
	if intent == nil {
		return
	}

	s.commitment.Store(&Commitment{
		TaskID:      intent.ID,
		Objective:   "Tactical Adaptation",
		StartTime:   time.Now(),
		MinDuration: 2 * time.Second,
	})

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

	// Phase 4: Single Writer Action Bus
	if !s.arbiter.Request(ctx, plan.Tasks[0]) {
		s.logger.Debug("Arbiter denied adapted intent dispatch", slog.String("task_id", intent.ID))
		return
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
	// Performance Budgeting: Track interruptions
	if s.activeIntent.Load() != nil {
		observability.Metrics.IncPreemption()
	}

	s.commitment.Store(nil)
	s.activeIntent.Store(nil)
	s.beforeState.Store(nil)
	s.planner.ClearCurrentPlan()
	s.planManager.Clear()
}

func (s *Service) handleBotRespawn(ctx context.Context, event domain.DomainEvent) {
	s.handlePlanInvalidated(ctx, event)
	
	// Phase 5: Death / Respawn Recovery
	s.modeManager.mu.Lock()
	s.modeManager.current = ModeRecovery
	s.modeManager.mu.Unlock()
	
	s.logger.Warn("Bot respawned, entering recovery mode for 5s")
	
	go func() {
		// Assess threats/heal during freeze if needed
		time.Sleep(5 * time.Second)
		s.evaluateNextPlan()
	}()
}

func sanitizeSkillName(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	target = strings.ReplaceAll(target, " ", "_")
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

func inventoryMap(state domain.GameState) map[string]int {
	m := make(map[string]int)
	for _, item := range state.Inventory {
		m[strings.ToLower(item.Name)] += item.Count
	}
	return m
}
