package pipeline

import (
	"context"
	"fmt"
	"math"
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
	output := input

	// Week 1: Normalize State Hashing (Data Prep)
	// We normalize the state itself before it reaches the decision layer
	// so the planner hashes stable buckets instead of micro-jitter.
	output.GameState.Health = math.Round(input.GameState.Health/2.0) * 2.0 // Bucket to nearest 2 (half heart)
	output.GameState.Food = math.Round(input.GameState.Food/2.0) * 2.0     // Bucket to nearest 2

	// Truncate exact coordinates (we only care about region/biome context for planning)
	output.GameState.Position.X = math.Round(input.GameState.Position.X/10.0) * 10.0
	output.GameState.Position.Y = math.Round(input.GameState.Position.Y/5.0) * 5.0
	output.GameState.Position.Z = math.Round(input.GameState.Position.Z/10.0) * 10.0

	// Cap POI distances to stable intervals
	for i := range output.GameState.POIs {
		output.GameState.POIs[i].Distance = math.Round(output.GameState.POIs[i].Distance/5.0) * 5.0
	}

	if input.Plan == nil {
		output.Normalized = &domain.Plan{Tasks: []domain.Action{}}
		return output, nil
	}

	normalized := &domain.Plan{
		Objective: strings.TrimSpace(input.Plan.Objective),
		Tasks:     make([]domain.Action, 0, len(input.Plan.Tasks)),
		Fallbacks: input.Plan.Fallbacks,
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
