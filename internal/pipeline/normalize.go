// internal/pipeline/normalize.go
package pipeline

import (
	"context"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

// NormalizeStage sanitizes the raw LLM plan, ensuring structural invariants.
type NormalizeStage struct{}

func NewNormalizeStage() *NormalizeStage {
	return &NormalizeStage{}
}

func (s *NormalizeStage) Process(ctx context.Context, state *PipelineState) error {
	if state.Plan == nil {
		state.Normalized = &domain.Plan{Tasks: []domain.Action{}}
		return nil
	}

	normalized := &domain.Plan{
		Objective: strings.TrimSpace(state.Plan.Objective),
		Tasks:     make([]domain.Action, 0, len(state.Plan.Tasks)),
	}

	for _, task := range state.Plan.Tasks {
		normTask := task
		// Sanitize structural strings
		normTask.Action = strings.ToLower(strings.TrimSpace(task.Action))
		normTask.Target.Name = strings.ToLower(strings.TrimSpace(task.Target.Name))
		normTask.Target.Type = strings.ToLower(strings.TrimSpace(task.Target.Type))

		// Imbue the execution trace context at the boundary
		normTask.Trace = state.Trace

		normalized.Tasks = append(normalized.Tasks, normTask)
	}

	state.Normalized = normalized
	return nil
}
