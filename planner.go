package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

const MaxStalls = 3

type Planner interface {
	GetActiveMilestone() *MilestonePlan
	ClearMilestone()
	RecordStall()
	ResetStall()
	GeneratePlan(ctx context.Context, rawState []byte, sessionID, sysOverride string) (*LLMPlan, error)
}

type TacticalPlanner struct {
	brain           Brain
	activeMilestone *MilestonePlan
	stallCount      int
	logger          *slog.Logger
	mu              sync.Mutex
}

func NewTacticalPlanner(brain Brain, logger *slog.Logger) *TacticalPlanner {
	return &TacticalPlanner{
		brain:  brain,
		logger: logger.With(slog.String("component", "TacticalPlanner")),
	}
}

func (p *TacticalPlanner) GetActiveMilestone() *MilestonePlan {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeMilestone == nil {
		return nil
	}
	// Return a copy to prevent race conditions
	ms := *p.activeMilestone
	return &ms
}

func (p *TacticalPlanner) ClearMilestone() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeMilestone != nil {
		p.logger.Info("Clearing active milestone", slog.String("id", p.activeMilestone.ID))
	}
	p.activeMilestone = nil
	p.stallCount = 0
}

func (p *TacticalPlanner) RecordStall() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stallCount++
	p.logger.Warn("Milestone stalled", slog.Int("count", p.stallCount))
}

func (p *TacticalPlanner) ResetStall() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stallCount > 0 {
		p.logger.Debug("Stall count reset")
	}
	p.stallCount = 0
}

func (p *TacticalPlanner) GeneratePlan(ctx context.Context, rawState []byte, sessionID, sysOverride string) (*LLMPlan, error) {
	p.mu.Lock()
	currentMS := p.activeMilestone
	stalls := p.stallCount
	p.mu.Unlock()

	// 1. Break infinite loops on impossible milestones
	if stalls >= MaxStalls {
		p.logger.Warn("Max stalls reached. Forcing milestone drop to prevent thrashing.", slog.String("milestone", currentMS.ID))
		dropOverride := fmt.Sprintf("CRITICAL: You have repeatedly failed to complete the milestone '%s'. It is currently impossible. Drop it entirely, generate a NEW milestone, and try a different approach.\n\n%s", currentMS.Description, sysOverride)

		tick := Tick{State: rawState}
		plan, err := p.brain.GeneratePlan(ctx, tick, sessionID, dropOverride, nil) // Pass nil to force new milestone
		if err != nil {
			return nil, err
		}
		p.updateState(plan)
		return plan, nil
	}

	// 2. Normal planning with partial progress tracking
	contextOverride := sysOverride
	if currentMS != nil && stalls > 0 {
		contextOverride = fmt.Sprintf("You are currently trying to complete milestone '%s' but the last task failed. Evaluate the state, keep the milestone active, and generate the next tasks to recover and proceed.\n\n%s", currentMS.Description, sysOverride)
	}

	tick := Tick{State: rawState}
	plan, err := p.brain.GeneratePlan(ctx, tick, sessionID, contextOverride, currentMS)
	if err != nil {
		return nil, err
	}

	p.updateState(plan)
	return plan, nil
}

func (p *TacticalPlanner) updateState(plan *LLMPlan) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if plan == nil {
		return
	}

	if bool(plan.MilestoneComplete) {
		if p.activeMilestone != nil {
			p.logger.Info("Milestone marked complete by LLM", slog.String("milestone", p.activeMilestone.ID))
		}
		p.activeMilestone = nil
		p.stallCount = 0
		return
	}

	if plan.Milestone != nil && plan.Milestone.ID != "" {
		if p.activeMilestone == nil || p.activeMilestone.ID != plan.Milestone.ID {
			p.logger.Info("Adopting new milestone", slog.String("id", plan.Milestone.ID), slog.String("desc", plan.Milestone.Description))
			p.activeMilestone = plan.Milestone
			p.stallCount = 0
		}
	}
}
