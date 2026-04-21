package execution

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// PrepOrchestrator handles proactive actions like food farming and pre-escape.
type PrepOrchestrator struct {
	logger             *slog.Logger
	engine             *TaskExecutionEngine
	supervisor         *ExecutionSupervisor
	lastEscapeDispatch time.Time
	escapeCooldown     time.Duration
}

func NewPrepOrchestrator(engine *TaskExecutionEngine, supervisor *ExecutionSupervisor, logger *slog.Logger) *PrepOrchestrator {
	return &PrepOrchestrator{
		engine:         engine,
		supervisor:     supervisor,
		logger:         logger.With(slog.String("component", "prep_orchestrator")),
		escapeCooldown: 5 * time.Second,
	}
}

// CheckProactiveFarming triggers a farming task if food is getting low but not yet critical.
func (o *PrepOrchestrator) CheckProactiveFarming(ctx context.Context, state domain.GameState) {
	if state.Food < 16 && state.Food >= 12 && o.engine.IsIdle() {
		o.logger.Info("Triggering proactive food farming")
		action := domain.Action{
			ID:        fmt.Sprintf("prep-farm-%d", time.Now().UnixNano()),
			Action:    "gather",
			Target:    domain.Target{Name: "food_source", Type: "category"},
			Priority:  40,
			Rationale: "Proactive farming based on declining food levels",
		}

		// Phase 4: Single Writer Action Bus
		o.supervisor.Request(ctx, action)
	}
}

// PreEscape initiates a retreat before danger becomes lethal.
func (o *PrepOrchestrator) PreEscape(ctx context.Context, state domain.GameState) {
	dangerRising := false
	threatCount := 0
	for _, t := range state.Threats {
		if t.Distance < 10 {
			threatCount++
		}
	}

	// If 2+ mobs are approaching and health isn't full, escape early
	if threatCount >= 2 && state.Health < 18 {
		dangerRising = true
	}

	if dangerRising {
		if time.Since(o.lastEscapeDispatch) < o.escapeCooldown {
			return
		}
		o.lastEscapeDispatch = time.Now()

		o.logger.Warn("Danger rising: initiating pre-escape")
		action := domain.Action{
			ID:        fmt.Sprintf("prep-escape-%d", time.Now().UnixNano()),
			Action:    "retreat",
			Target:    domain.Target{Name: "safe_zone"},
			Priority:  90,
			Rationale: "Predictive escape: threat density increasing",
		}

		// Phase 4: Single Writer Action Bus
		o.supervisor.Request(ctx, action)
	}
}
