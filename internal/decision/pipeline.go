package decision

import (
	"context"
	"david22573/synaptic-mc/internal/domain"
	"fmt"
)

type Stage interface {
	Process(ctx context.Context, frame EvaluationFrame, plan *domain.Plan) (*domain.Plan, error)
}

type Pipeline struct {
	planner Planner // Special first stage
	stages  []Stage
}

func NewPipeline(planner Planner, stages ...Stage) *Pipeline {
	return &Pipeline{
		planner: planner,
		stages:  stages,
	}
}

func (p *Pipeline) Evaluate(ctx context.Context, frame EvaluationFrame) (*Outcome, error) {
	// 1. Generate Candidate (Planner)
	plan, err := p.planner.Generate(ctx, frame)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}

	// 2. Linear Transformation (The Pipeline)
	// Stages: Normalize -> Validate -> Simulate -> Policy
	for _, stage := range p.stages {
		plan, err = stage.Process(ctx, frame, plan)
		if err != nil {
			// If a stage fails, it returns a "Hard Rejection" error.
			return nil, err
		}
	}

	return &Outcome{
		Plan:     plan,
		Priority: 100, // Default priority
	}, nil
}
