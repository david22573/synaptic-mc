package pipeline

import (
	"context"
	"fmt"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type NormalizeStage struct{}

func NewNormalizeStage() *NormalizeStage {
	return &NormalizeStage{}
}

func (s *NormalizeStage) Name() string {
	return "Normalize"
}

func (s *NormalizeStage) Process(ctx context.Context, input PipelineState) (PipelineState, error) {
	output := input // By-value shallow copy to preserve prior fields

	if input.Plan == nil {
		output.Normalized = &domain.Plan{Tasks: []domain.Action{}}
		return output, nil
	}

	normalized := &domain.Plan{
		Objective: strings.TrimSpace(input.Plan.Objective),
		Tasks:     make([]domain.Action, 0, len(input.Plan.Tasks)),
	}

	for i, task := range input.Plan.Tasks {
		normTask := task

		normTask.Action = strings.ToLower(strings.TrimSpace(task.Action))
		normTask.Target.Name = strings.ToLower(strings.TrimSpace(task.Target.Name))
		normTask.Target.Type = strings.ToLower(strings.TrimSpace(task.Target.Type))

		if normTask.ID == "" {
			normTask.ID = fmt.Sprintf("cmd-%s-%d", input.Trace.ActionID, i)
		}

		normTask.Trace = input.Trace

		normalized.Tasks = append(normalized.Tasks, normTask)
	}

	output.Normalized = normalized
	return output, nil
}
