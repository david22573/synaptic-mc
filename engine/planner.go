package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const MaxStalls = 3
const MaxMilestoneAge = 25 * time.Minute // Phase 3: Replanning if milestone gets too stale

type Planner interface {
	GetActiveMilestone() *MilestonePlan
	ClearMilestone()
	RecordStall()
	ResetStall()
	GeneratePlan(ctx context.Context, rawState []byte, sessionID, sysOverride string) (*LLMPlan, error)
}

type TacticalPlanner struct {
	brain           Brain
	memory          MemoryBank // Phase 2: Persistence
	activeMilestone *MilestonePlan
	stallCount      int
	logger          *slog.Logger
	mu              sync.Mutex
	sessionID       string
}

func NewTacticalPlanner(brain Brain, memory MemoryBank, sessionID string, logger *slog.Logger) *TacticalPlanner {
	p := &TacticalPlanner{
		brain:     brain,
		memory:    memory,
		sessionID: sessionID,
		logger:    logger.With(slog.String("component", "TacticalPlanner")),
	}

	// Phase 2: Attempt to restore milestone across restarts/crashes
	if ms, err := memory.LoadMilestone(context.Background(), sessionID); err == nil && ms != nil {
		p.activeMilestone = ms
		p.logger.Info("Restored active milestone from persistence", slog.String("id", ms.ID))
	}

	return p
}

func (p *TacticalPlanner) GetActiveMilestone() *MilestonePlan {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeMilestone == nil {
		return nil
	}
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
	_ = p.memory.SaveMilestone(context.Background(), p.sessionID, nil)
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

	contextOverride := sysOverride

	if currentMS != nil {
		// Phase 3: Milestone Age Metric
		age := time.Since(currentMS.StartedAt)
		if age > MaxMilestoneAge {
			p.logger.Warn("Milestone age exceeded threshold", slog.Float64("minutes", age.Minutes()))
			contextOverride = fmt.Sprintf("CRITICAL: Milestone '%s' has been active for %.0f minutes. It is heavily stale. You MUST evaluate if this is still the optimal path. Consider completing it or generating a new approach entirely.\n\n%s", currentMS.Description, age.Minutes(), sysOverride)
		} else if stalls >= MaxStalls {
			// Phase 1/2: Break infinite loops on impossible milestones
			p.logger.Warn("Max stalls reached. Forcing milestone drop to prevent thrashing.", slog.String("milestone", currentMS.ID))
			contextOverride = fmt.Sprintf("CRITICAL: You have repeatedly failed to complete the milestone '%s'. It is currently impossible. Drop it entirely, generate a NEW milestone, and try a different approach.\n\n%s", currentMS.Description, sysOverride)
			currentMS = nil // Force the LLM to write a new one
		} else if stalls > 0 {
			contextOverride = fmt.Sprintf("You are currently trying to complete milestone '%s' but the last task failed. Evaluate the state, keep the milestone active, and generate the next tasks to recover and proceed.\n\n%s", currentMS.Description, sysOverride)
		}
	}

	tick := Tick{State: rawState}
	plan, err := p.brain.GeneratePlan(ctx, tick, sessionID, contextOverride, currentMS, 1)
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
		_ = p.memory.SaveMilestone(context.Background(), p.sessionID, nil)
		return
	}

	if plan.Milestone != nil && plan.Milestone.Description != "" {
		// Phase 2: Semantic Dedup - Keep the age/ID of identical descriptions
		if p.activeMilestone != nil && p.activeMilestone.Description == plan.Milestone.Description {
			plan.Milestone.ID = p.activeMilestone.ID
			plan.Milestone.StartedAt = p.activeMilestone.StartedAt
		} else {
			// It's genuinely a new milestone
			if plan.Milestone.StartedAt.IsZero() {
				plan.Milestone.StartedAt = time.Now()
			}
			if p.activeMilestone == nil || p.activeMilestone.ID != plan.Milestone.ID {
				p.logger.Info("Adopting new milestone", slog.String("id", plan.Milestone.ID), slog.String("desc", plan.Milestone.Description))
			}
		}

		p.activeMilestone = plan.Milestone
		p.stallCount = 0
		_ = p.memory.SaveMilestone(context.Background(), p.sessionID, p.activeMilestone)
	}
}
