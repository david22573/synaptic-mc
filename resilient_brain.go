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

// ResilientBrain wraps a primary Brain with validation, retry logic, and a fallback Brain.
type ResilientBrain struct {
	primary   Brain
	fallback  Brain
	validator *PlanValidator
	logger    *slog.Logger
	telemetry *Telemetry
}

func NewResilientBrain(primary Brain, fallback Brain, logger *slog.Logger, tel *Telemetry) *ResilientBrain {
	return &ResilientBrain{
		primary:   primary,
		fallback:  fallback,
		validator: NewPlanValidator(),
		logger:    logger.With(slog.String("component", "ResilientBrain")),
		telemetry: tel,
	}
}

func (r *ResilientBrain) GenerateMilestone(ctx context.Context, t Tick, sessionID string) (*MilestonePlan, error) {
	var lastErr error
	backoff := InitialBackoff

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		milestone, err := r.primary.GenerateMilestone(ctx, t, sessionID)
		if err == nil {
			if valErr := r.validator.ValidateMilestone(milestone); valErr != nil {
				r.telemetry.RecordValidationFailure()
				err = fmt.Errorf("validation failed: %w", valErr)
			} else {
				return milestone, nil
			}
		}

		// If the engine intentionally aborted planning (e.g., evasion reflex), drop immediately.
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			r.logger.Debug("Primary brain context canceled (intentional preemption)")
			return nil, context.Canceled
		}

		lastErr = err
		r.logger.Warn("Milestone generation failed",
			slog.Int("attempt", attempt),
			slog.Any("error", err),
		)

		if attempt < MaxRetries {
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.Canceled) {
					return nil, context.Canceled
				}
				r.logger.Warn("Context timeout in primary brain, engaging fallback milestone")
				return r.fallback.GenerateMilestone(context.Background(), t, sessionID)
			case <-time.After(backoff):
				backoff *= 2 // Exponential backoff
			}
		}
	}

	r.logger.Error("Primary brain exhausted retries. Engaging fallback planner.", slog.Any("final_error", lastErr))
	return r.fallback.GenerateMilestone(context.Background(), t, sessionID)
}

func (r *ResilientBrain) EvaluatePlan(ctx context.Context, t Tick, sessionID, systemOverride string, milestone *MilestonePlan) (*LLMPlan, error) {
	var lastErr error
	backoff := InitialBackoff

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		currentOverride := systemOverride
		if lastErr != nil && attempt > 1 {
			correction := fmt.Sprintf("CRITICAL ERROR IN PREVIOUS OUTPUT: %v. Fix the JSON schema and semantic errors.", lastErr)
			if currentOverride != "" {
				currentOverride = currentOverride + "\n" + correction
			} else {
				currentOverride = correction
			}
		}

		plan, err := r.primary.EvaluatePlan(ctx, t, sessionID, currentOverride, milestone)
		if err == nil {
			if valErr := r.validator.ValidateTactics(plan); valErr != nil {
				r.telemetry.RecordValidationFailure()
				err = fmt.Errorf("validation failed: %w", valErr)
			} else {
				return plan, nil
			}
		}

		// If the engine intentionally aborted planning, drop immediately.
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			r.logger.Debug("Primary brain context canceled (intentional preemption)")
			return nil, context.Canceled
		}

		lastErr = err
		r.logger.Warn("Tactical planning failed",
			slog.Int("attempt", attempt),
			slog.Any("error", err),
		)

		if attempt < MaxRetries {
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.Canceled) {
					return nil, context.Canceled
				}
				r.logger.Warn("Context timeout in primary brain, engaging fallback tactics")
				return r.fallback.EvaluatePlan(context.Background(), t, sessionID, systemOverride, milestone)
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}

	r.logger.Error("Primary brain exhausted retries for tactics. Engaging fallback.", slog.Any("final_error", lastErr))
	return r.fallback.EvaluatePlan(context.Background(), t, sessionID, systemOverride, milestone)
}
