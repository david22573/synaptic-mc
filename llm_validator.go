package main

import (
	"fmt"
	"strings"
)

// PlanValidator enforces strict schema and semantic rules on LLM outputs.
type PlanValidator struct{}

func NewPlanValidator() *PlanValidator {
	return &PlanValidator{}
}

func (v *PlanValidator) ValidateTactics(plan *LLMPlan) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}

	if plan.Objective == "" {
		return fmt.Errorf("plan missing objective")
	}

	if len(plan.Tasks) == 0 && !plan.MilestoneComplete {
		return fmt.Errorf("plan has no tasks and milestone is not marked complete")
	}

	if len(plan.Tasks) > 3 {
		return fmt.Errorf("plan exceeds maximum task limit of 3 (got %d)", len(plan.Tasks))
	}

	for i, task := range plan.Tasks {
		if !IsValidAction(task.Action) {
			return fmt.Errorf("task %d has invalid action: '%s'", i, task.Action)
		}
		if !IsValidTargetType(task.Target.Type) {
			return fmt.Errorf("task %d has invalid target type: '%s'", i, task.Target.Type)
		}
		if task.Target.Type != string(TargetNone) && strings.TrimSpace(task.Target.Name) == "" {
			return fmt.Errorf("task %d requires a target name for type '%s'", i, task.Target.Type)
		}
		if task.Rationale == "" {
			return fmt.Errorf("task %d missing rationale", i)
		}
	}

	return nil
}

func (v *PlanValidator) ValidateMilestone(m *MilestonePlan) error {
	if m == nil {
		return fmt.Errorf("milestone is nil")
	}
	if m.ID == "" {
		return fmt.Errorf("milestone missing ID")
	}
	if m.Description == "" {
		return fmt.Errorf("milestone missing description")
	}
	if m.CompletionHint == "" {
		return fmt.Errorf("milestone missing completion hint")
	}
	return nil
}
