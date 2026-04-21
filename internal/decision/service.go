package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthdm/hollywood/actor"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
)

// Planner defines the interface for generating strategic plans.
type Planner interface {
	Generate(ctx context.Context, state domain.GameState) (*domain.Plan, error)
}

// Actor Message Types
type StateUpdateMsg struct{ State domain.GameState }
type TaskEndMsg struct{ Payload domain.TaskEndPayload }
type EvalTriggerMsg struct{}
type PlanReadyMsg struct {
	Plan *domain.Plan
	Err  error
}

type Service struct {
	engine *actor.Engine
	pid    *actor.PID
}

func NewService(engine *actor.Engine, planner Planner, bus domain.EventBus, memStore memory.Store, sessionID string, logger *slog.Logger) *Service {
	pid := engine.Spawn(func() actor.Receiver {
		return &decisionActor{
			planner:   planner,
			bus:       bus,
			memStore:  memStore,
			sessionID: sessionID,
			logger:    logger.With(slog.String("component", "decision_actor")),
		}
	}, "decision_brain")

	// Subscribe the actor directly to the event bus
	if aeb, ok := bus.(*domain.ActorEventBus); ok {
		aeb.SubscribeActor(pid)
	}

	return &Service{
		engine: engine,
		pid:    pid,
	}
}

func (s *Service) RequestEvaluation() {
	s.engine.Send(s.pid, EvalTriggerMsg{})
}

type decisionActor struct {
	planner    Planner
	bus        domain.EventBus
	memStore   memory.Store
	sessionID  string
	logger     *slog.Logger
	state      domain.GameState
	activePlan *domain.Plan
	evaluating bool
	milestones []domain.ProgressionMilestone
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

func (a *decisionActor) Receive(ctx *actor.Context) {
	switch msg := ctx.Message().(type) {

	case actor.Started:
		// Load milestones on start
		if a.memStore != nil {
			m, err := a.memStore.GetMilestones(context.Background(), a.sessionID)
			if err == nil {
				a.milestones = m
			}
		}

	case domain.DomainEvent:
		switch msg.Type {
		case domain.EventTypeStateUpdated:
			var gs domain.GameState
			if err := json.Unmarshal(msg.Payload, &gs); err == nil {
				ctx.Send(ctx.PID(), StateUpdateMsg{State: gs})
			}

		case domain.EventTypeTaskEnd:
			var payload domain.TaskEndPayload
			if err := json.Unmarshal(msg.Payload, &payload); err == nil {
				ctx.Send(ctx.PID(), TaskEndMsg{Payload: payload})
			}
		}

	case StateUpdateMsg:
		a.detectMilestones(&a.state, &msg.State)
		a.state = msg.State
		if a.activePlan == nil {
			ctx.Send(ctx.PID(), EvalTriggerMsg{})
		}

	case TaskEndMsg:
		// Plan progression logic: Remove completed task
		if a.activePlan != nil && len(a.activePlan.Tasks) > 0 {
			if a.activePlan.Tasks[0].ID == msg.Payload.CommandID {
				a.activePlan.Tasks = a.activePlan.Tasks[1:]
				if len(a.activePlan.Tasks) == 0 {
					a.activePlan = nil
				}
			}
		}
		ctx.Send(ctx.PID(), EvalTriggerMsg{})

	case EvalTriggerMsg:
		if a.evaluating {
			return
		}
		a.evaluating = true
		a.logger.Debug("Spawning async planner")

		go func(pid *actor.PID, eng *actor.Engine, st domain.GameState) {
			plan, err := a.planner.Generate(context.Background(), st)
			eng.Send(pid, PlanReadyMsg{Plan: plan, Err: err})
		}(ctx.PID(), ctx.Engine(), a.state)

	case PlanReadyMsg:
		a.evaluating = false
		if msg.Err != nil {
			a.logger.Error("Planner failed", slog.Any("err", msg.Err))
			return
		}

		if msg.Plan != nil {
			a.logger.Info("New plan generated", slog.Int("tasks", len(msg.Plan.Tasks)))
			a.activePlan = msg.Plan

			a.bus.Publish(context.Background(), domain.DomainEvent{
				Type:      domain.EventTypePlanCreated,
				Payload:   domain.MustMarshal(a.activePlan),
				CreatedAt: time.Now(),
			})
		}
	}
}

func (a *decisionActor) detectMilestones(before, after *domain.GameState) {
	beforeItems := make(map[string]bool)
	for _, item := range before.Inventory {
		beforeItems[item.Name] = true
	}

	for _, item := range techTree {
		alreadyUnlocked := false
		for _, m := range a.milestones {
			if m.Name == item {
				alreadyUnlocked = true
				break
			}
		}
		if alreadyUnlocked {
			continue
		}

		hasItNow := false
		for _, inv := range after.Inventory {
			if inv.Name == item {
				hasItNow = true
				break
			}
		}

		if hasItNow && !beforeItems[item] {
			a.logger.Info("Milestone unlocked!", slog.String("name", item))
			m := domain.ProgressionMilestone{Name: item, UnlockedAt: time.Now()}
			a.milestones = append(a.milestones, m)

			if a.memStore != nil {
				go func(sid, name string) {
					dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = a.memStore.SaveMilestone(dbCtx, sid, name)
				}(a.sessionID, item)
			}
		}
	}
}

func (a *decisionActor) getMilestoneContext() string {
	if len(a.milestones) == 0 {
		return "PROGRESSION STATUS: Just started. Goal: crafting_table, wooden_pickaxe."
	}

	var unlocked []string
	for _, m := range a.milestones {
		unlocked = append(unlocked, m.Name)
	}

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
