package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Planner interface {
	GenerateMilestone(ctx context.Context, state json.RawMessage, sessionID string) (*MilestonePlan, error)
	GenerateTactics(ctx context.Context, state json.RawMessage, sessionID, systemOverride string) (*LLMPlan, error)
	RecordStall() bool
	ResetStall()
	ClearMilestone()
	GetActiveMilestone() *MilestonePlan
}

const maxMilestoneStalls = 5

type LLMPlanner struct {
	brain     Brain
	logger    *slog.Logger
	uiHub     *UIHub
	memory    MemoryBank
	telemetry *Telemetry

	mu                  sync.RWMutex
	activeMilestone     *MilestonePlan
	milestoneStallCount int
}

func NewLLMPlanner(b Brain, uiHub *UIHub, memory MemoryBank, tel *Telemetry, baseLogger *slog.Logger, sessionID string) *LLMPlanner {
	return &LLMPlanner{
		brain:     b,
		uiHub:     uiHub,
		memory:    memory,
		telemetry: tel,
		logger:    baseLogger.With(slog.String("component", "planner"), slog.String("session_id", sessionID)),
	}
}

func (p *LLMPlanner) GenerateMilestone(ctx context.Context, state json.RawMessage, sessionID string) (*MilestonePlan, error) {
	p.logger.Info("Generating new milestone...")

	milestone, err := p.brain.GenerateMilestone(ctx, Tick{State: state}, sessionID)
	if err != nil {
		return nil, err
	}

	p.telemetry.RecordMilestoneGenerated()

	p.mu.Lock()
	p.activeMilestone = milestone
	p.milestoneStallCount = 0
	p.mu.Unlock()

	p.logger.Info("New milestone set",
		slog.String("id", milestone.ID),
		slog.String("description", milestone.Description),
	)
	p.uiHub.Broadcast(map[string]interface{}{
		"type":    "milestone_update",
		"payload": milestone,
	})

	go func(desc string) {
		sCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.memory.SetSummary(sCtx, sessionID, "Active Milestone", desc)
	}(milestone.Description)

	return milestone, nil
}

func (p *LLMPlanner) GenerateTactics(ctx context.Context, state json.RawMessage, sessionID, systemOverride string) (*LLMPlan, error) {
	p.mu.RLock()
	milestone := p.activeMilestone
	p.mu.RUnlock()

	if milestone == nil {
		return nil, fmt.Errorf("cannot generate tactics without an active milestone")
	}

	plan, err := p.brain.EvaluatePlan(ctx, Tick{State: state}, sessionID, systemOverride, milestone)
	if err != nil {
		return nil, err
	}

	if plan != nil && plan.MilestoneComplete {
		p.logger.Info("Milestone marked complete by tactical planner",
			slog.String("milestone", milestone.Description),
		)
		go p.memory.LogEvent("milestone_complete", milestone.Description,
			EventMeta{SessionID: sessionID, Status: "COMPLETED"},
		)

		go func(desc string) {
			sCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.memory.SetSummary(sCtx, sessionID, "Last Completed Milestone", desc)
		}(milestone.Description)

		p.ClearMilestone()
	}

	return plan, nil
}

func (p *LLMPlanner) RecordStall() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.milestoneStallCount++
	if p.milestoneStallCount >= maxMilestoneStalls {
		p.logger.Warn("Milestone stalled — clearing for regeneration",
			slog.Int("stall_count", p.milestoneStallCount),
			slog.String("milestone", p.milestoneDesc(p.activeMilestone)),
		)
		p.activeMilestone = nil
		p.milestoneStallCount = 0
		return true
	}
	return false
}

func (p *LLMPlanner) ResetStall() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.milestoneStallCount = 0
}

func (p *LLMPlanner) ClearMilestone() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeMilestone = nil
	p.milestoneStallCount = 0
}

func (p *LLMPlanner) GetActiveMilestone() *MilestonePlan {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.activeMilestone
}

func (p *LLMPlanner) milestoneDesc(m *MilestonePlan) string {
	if m == nil {
		return "<none>"
	}
	return m.Description
}
