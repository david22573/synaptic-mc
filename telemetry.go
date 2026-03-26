package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ModelPricing represents cost per 1M tokens (Input / Output)
var PricingMap = map[string]struct{ Input, Output float64 }{
	"mistralai/mistral-small-2603": {Input: 1.00, Output: 3.00},
	"gpt-4-turbo":                  {Input: 10.00, Output: 30.00},
	"deepseek/deepseek-v3.2":       {Input: 0.14, Output: 0.28}, // Added Deepseek
}

type Telemetry struct {
	logger *slog.Logger
	mu     sync.Mutex

	llmInvocations int
	llmErrors      int
	totalLatency   time.Duration

	inputTokens  int
	outputTokens int
	totalCost    float64

	tasksCompleted int
	tasksFailed    int
	replans        int
	panics         int
	activeSessions int32

	// Phase 3: Orchestration metrics
	dispatchFailures   int
	stalePlansDropped  int
	milestonesGen      int
	validationFailures int

	costLimit float64
}

func NewTelemetry(logger *slog.Logger, costLimit float64) *Telemetry {
	return &Telemetry{
		logger:    logger.With(slog.String("component", "telemetry")),
		costLimit: costLimit,
	}
}

func (t *Telemetry) RecordSessionStart()   { atomic.AddInt32(&t.activeSessions, 1) }
func (t *Telemetry) RecordSessionEnd()     { atomic.AddInt32(&t.activeSessions, -1) }
func (t *Telemetry) ActiveSessions() int32 { return atomic.LoadInt32(&t.activeSessions) }

func (t *Telemetry) RecordLLMCall(model string, duration time.Duration, inTokens, outTokens int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.llmInvocations++
	t.totalLatency += duration

	if err != nil {
		t.llmErrors++
		return
	}

	t.inputTokens += inTokens
	t.outputTokens += outTokens

	prices, ok := PricingMap[model]
	if !ok {
		t.logger.Warn("Model pricing not found, using fallback", slog.String("model", model))
		prices = PricingMap["mistralai/mistral-small-2603"] // default fallback
	}

	callCost := (float64(inTokens) / 1000000.0 * prices.Input) + (float64(outTokens) / 1000000.0 * prices.Output)
	t.totalCost += callCost

	if t.totalCost > t.costLimit {
		t.logger.Warn("GLOBAL COST LIMIT EXCEEDED",
			slog.Float64("current_cost", t.totalCost),
			slog.Float64("limit", t.costLimit),
		)
	}
}

func (t *Telemetry) HasExceededCostLimit() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totalCost >= t.costLimit
}

func (t *Telemetry) RecordTaskStatus(status TaskStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch status {
	case StatusCompleted:
		t.tasksCompleted++
	case StatusFailed, StatusAborted:
		t.tasksFailed++
	}
}

func (t *Telemetry) RecordReplan() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.replans++
}

func (t *Telemetry) RecordPanic() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.panics++
}

func (t *Telemetry) RecordDispatchFailure() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dispatchFailures++
}

func (t *Telemetry) RecordStalePlan() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stalePlansDropped++
}

func (t *Telemetry) RecordMilestoneGenerated() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.milestonesGen++
}

func (t *Telemetry) RecordValidationFailure() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.validationFailures++
}

func (t *Telemetry) StartReporting(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.mu.Lock()
			avgLatency := time.Duration(0)
			if t.llmInvocations > 0 {
				avgLatency = t.totalLatency / time.Duration(t.llmInvocations)
			}

			t.logger.Info("Telemetry Report",
				slog.Int("llm_calls", t.llmInvocations),
				slog.Int("llm_errors", t.llmErrors),
				slog.String("llm_avg_latency", avgLatency.String()),
				slog.Float64("total_cost_usd", t.totalCost),
				slog.Int("tasks_completed", t.tasksCompleted),
				slog.Int("tasks_failed", t.tasksFailed),
				slog.Int("replans", t.replans),
				slog.Int("panics", t.panics),
				slog.Int("dispatch_failures", t.dispatchFailures),
				slog.Int("stale_plans_dropped", t.stalePlansDropped),
				slog.Int("milestones_gen", t.milestonesGen),
				slog.Int("validation_failures", t.validationFailures),
			)

			t.llmInvocations, t.llmErrors, t.totalLatency = 0, 0, 0
			t.tasksCompleted, t.tasksFailed, t.replans, t.panics = 0, 0, 0, 0
			t.dispatchFailures, t.stalePlansDropped, t.milestonesGen, t.validationFailures = 0, 0, 0, 0
			t.mu.Unlock()
		}
	}
}

func (t *Telemetry) AvgLatency() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.llmInvocations == 0 {
		return 0
	}
	return t.totalLatency / time.Duration(t.llmInvocations)
}
