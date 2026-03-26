package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	MaxRetries     = 3
	InitialBackoff = 1 * time.Second
)

type ResilientBrain struct {
	primary   Brain
	fallback  Brain
	validator *PlanValidator
	simulator *InternalSimulator
	logger    *slog.Logger
	telemetry *Telemetry
}

func NewResilientBrain(primary Brain, fallback Brain, logger *slog.Logger, tel *Telemetry) *ResilientBrain {
	return &ResilientBrain{
		primary:   primary,
		fallback:  fallback,
		validator: NewPlanValidator(),
		simulator: NewInternalSimulator(logger),
		logger:    logger.With(slog.String("component", "ResilientBrain")),
		telemetry: tel,
	}
}

func (r *ResilientBrain) GeneratePlan(ctx context.Context, t Tick, sessionID, systemOverride string, milestone *MilestonePlan, attempt int) (*LLMPlan, error) {
	var lastErr error
	backoff := InitialBackoff

	if r.telemetry.HasExceededCostLimit() {
		r.logger.Warn("Cost limit exceeded, routing directly to fallback planner")
		return r.fallback.GeneratePlan(ctx, t, sessionID, systemOverride, milestone, 1)
	}

	for try := 1; try <= MaxRetries; try++ {
		currentOverride := systemOverride
		if lastErr != nil && try > 1 {
			correction := fmt.Sprintf("CRITICAL ERROR IN PREVIOUS OUTPUT: %v. Fix the JSON schema and semantic errors.", lastErr)
			if currentOverride != "" {
				currentOverride = currentOverride + "\n" + correction
			} else {
				currentOverride = correction
			}
		}

		plan, err := r.primary.GeneratePlan(ctx, t, sessionID, currentOverride, milestone, try)
		if err == nil {

			// Phase 7: Run internal simulation to collapse candidates into the optimal tasks
			if len(plan.CandidatePlans) > 0 {
				plan.Tasks = r.simulator.RankCandidates(plan.CandidatePlans, t.State)
			}

			// Ensure priorities are stamped
			for i := range plan.Tasks {
				plan.Tasks[i].Priority = PriLLM
			}

			// Final sanity check validation
			if valErr := r.validator.ValidatePlan(plan, t.State); valErr != nil {
				r.telemetry.RecordValidationFailure()
				err = fmt.Errorf("validation failed on chosen variant: %w", valErr)
			} else {
				r.logger.Debug("Simulator selected optimal variant", slog.Int("steps", len(plan.Tasks)))
				return plan, nil
			}
		}

		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			r.logger.Debug("Primary brain context canceled (intentional preemption)")
			return nil, context.Canceled
		}

		lastErr = err
		r.logger.Warn("Planning failed",
			slog.Int("attempt", try),
			slog.Any("error", err),
		)

		if try < MaxRetries {
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.Canceled) {
					return nil, context.Canceled
				}
				r.logger.Warn("Context timeout in primary brain, engaging fallback planner")
				return r.fallback.GeneratePlan(context.Background(), t, sessionID, systemOverride, milestone, 1)
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}

	r.logger.Error("Primary brain exhausted retries. Engaging fallback.", slog.Any("final_error", lastErr))
	return r.fallback.GeneratePlan(context.Background(), t, sessionID, systemOverride, milestone, 1)
}
