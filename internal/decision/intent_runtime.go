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
	"david22573/synaptic-mc/internal/voyager"
)

func (s *Service) handleStateUpdated(ctx context.Context, event domain.DomainEvent) {
	// Performance Budgeting: Track state age
	age := time.Since(event.CreatedAt).Milliseconds()
	observability.Metrics.StateAgeMs.Observe(float64(age))

	s.planner.SetMilestoneContext(s.getMilestoneContext())
	s.planner.TriggerReplan(s.stateProvider.GetCurrentState().State)
	if !s.planManager.HasActivePlan() {
		s.evaluateNextPlan()
	}
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

	hasMoreTasks := s.planManager.PopTask(payload.CommandID)
	if hasMoreTasks {
		s.dispatchActivePlan(ctx)
		return
	}

	currentPlan := s.planManager.GetCurrent()
	if currentPlan != nil {
		s.planner.RecordSuccess(currentPlan.Objective)
		if s.memStore != nil {
			go func(sid, o string) {
				dbCtx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
				defer cancel()
				_ = s.memStore.SaveFailureCount(dbCtx, sid, o, 0)
			}(s.sessionID, currentPlan.Objective)
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

	if len(plan.Tasks) == 1 {
		s.planner.WarmNextPlan(ctx, s.sessionID, s.stateProvider.GetCurrentState().State)
	}

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

func (s *Service) dispatchActiveIntent(ctx context.Context, intent *domain.ActionIntent) {
	if intent == nil {
		return
	}

	s.commitment.Store(&Commitment{
		TaskID:      intent.ID,
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
		observability.Metrics.IncInterrupt()
	}

	s.commitment.Store(nil)
	s.activeIntent.Store(nil)
	s.beforeState.Store(nil)
	s.planner.ClearCurrentPlan()
	s.planManager.Clear()
}

func (s *Service) handleBotRespawn(ctx context.Context, event domain.DomainEvent) {
	s.handlePlanInvalidated(ctx, event)
	go func() {
		time.Sleep(1500 * time.Millisecond)
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
