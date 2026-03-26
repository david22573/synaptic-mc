package main

import (
	"errors"
	"fmt"
)

type PlanValidator struct{}

func NewPlanValidator() *PlanValidator {
	return &PlanValidator{}
}

func (v *PlanValidator) ValidatePlan(plan *LLMPlan) error {
	if plan == nil {
		return errors.New("plan is nil")
	}

	// Truncate rather than reject if the LLM spits out too many tasks
	if len(plan.Tasks) > 3 {
		plan.Tasks = plan.Tasks[:3]
	}

	for i, task := range plan.Tasks {
		if task.Action == "" {
			return fmt.Errorf("task %d is missing an action", i)
		}
		if task.Target.Type == "" || task.Target.Name == "" {
			return fmt.Errorf("task %d '%s' is missing target type or name", i, task.Action)
		}
		if task.Rationale == "" {
			return fmt.Errorf("task %d '%s' is missing a rationale", i, task.Action)
		}

		// Enforce the strict explore rule
		if task.Action == string(ActionExplore) && task.Target.Name != "none" {
			return fmt.Errorf("explore action must have target name 'none', got '%s'", task.Target.Name)
		}
	}

	return nil
}
