package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

type Planner interface {
	GeneratePlan(ctx context.Context, state json.RawMessage, sessionID, systemOverride string) (*LLMPlan, error)
	RecordStall() bool
	ResetStall()
	ClearMilestone()
	GetActiveMilestone() *MilestonePlan
}

// Raised from 5 to 8 to give the bot more breathing room
const maxMilestoneStalls = 8

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
	p := &LLMPlanner{
		brain:     b,
		uiHub:     uiHub,
		memory:    memory,
		telemetry: tel,
		logger:    baseLogger.With(slog.String("component", "planner"), slog.String("session_id", sessionID)),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id, _ := memory.GetSummaryValue(ctx, "global", "active_milestone_id")
	desc, _ := memory.GetSummaryValue(ctx, "global", "active_milestone_desc")

	if id != "" && desc != "" {
		p.activeMilestone = &MilestonePlan{ID: id, Description: desc}
		p.logger.Info("Restored active milestone from global memory", slog.String("id", id))
	}

	return p
}

func (p *LLMPlanner) GeneratePlan(ctx context.Context, state json.RawMessage, sessionID, systemOverride string) (*LLMPlan, error) {
	p.mu.RLock()
	currentMilestone := p.activeMilestone
	p.mu.RUnlock()

	plan, err := p.brain.GeneratePlan(ctx, Tick{State: state}, sessionID, systemOverride, currentMilestone)
	if err != nil {
		return nil, err
	}

	if plan.Milestone != nil {
		p.telemetry.RecordMilestoneGenerated()

		p.mu.Lock()
		p.activeMilestone = plan.Milestone
		p.milestoneStallCount = 0
		p.mu.Unlock()

		p.logger.Info("New milestone set",
			slog.String("id", plan.Milestone.ID),
			slog.String("description", plan.Milestone.Description),
		)
		p.uiHub.Broadcast(map[string]interface{}{
			"type":    "milestone_update",
			"payload": plan.Milestone,
		})

		go func(m *MilestonePlan) {
			sCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.memory.SetSummary(sCtx, sessionID, "Active Milestone", m.Description)
			_ = p.memory.SetSummary(sCtx, "global", "active_milestone_id", m.ID)
			_ = p.memory.SetSummary(sCtx, "global", "active_milestone_desc", m.Description)
		}(plan.Milestone)
	}

	// Cast the FlexBool to standard bool here
	if bool(plan.MilestoneComplete) {
		p.logger.Info("Milestone marked complete by tactical planner")

		desc := "Unknown"
		if p.activeMilestone != nil {
			desc = p.activeMilestone.Description
		}

		go p.memory.LogEvent("milestone_complete", desc,
			EventMeta{SessionID: sessionID, Status: "COMPLETED"},
		)

		go func(d string) {
			sCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.memory.SetSummary(sCtx, sessionID, "Last Completed Milestone", d)
		}(desc)

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

		go func() {
			sCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.memory.SetSummary(sCtx, "global", "active_milestone_id", "")
			_ = p.memory.SetSummary(sCtx, "global", "active_milestone_desc", "")
		}()
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

	go func() {
		sCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.memory.SetSummary(sCtx, "global", "active_milestone_id", "")
		_ = p.memory.SetSummary(sCtx, "global", "active_milestone_desc", "")
	}()
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
