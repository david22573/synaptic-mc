package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Telemetry tracks critical loop metrics without blocking execution
type Telemetry struct {
	logger *slog.Logger
	mu     sync.Mutex

	llmInvocations int
	llmErrors      int
	totalLatency   time.Duration

	tasksCompleted int
	tasksFailed    int
	replans        int
	panics         int
	activeSessions int32
}

func (t *Telemetry) RecordSessionStart() {
	atomic.AddInt32(&t.activeSessions, 1)
}

func (t *Telemetry) RecordSessionEnd() {
	atomic.AddInt32(&t.activeSessions, -1)
}

func (t *Telemetry) ActiveSessions() int32 {
	return atomic.LoadInt32(&t.activeSessions)
}

func (t *Telemetry) AvgLatency() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.llmInvocations == 0 {
		return 0
	}
	return t.totalLatency / time.Duration(t.llmInvocations)
}

func NewTelemetry(logger *slog.Logger) *Telemetry {
	return &Telemetry{logger: logger}
}

func (t *Telemetry) RecordLLMCall(duration time.Duration, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.llmInvocations++
	t.totalLatency += duration
	if err != nil {
		t.llmErrors++
	}
}

func (t *Telemetry) RecordTaskStatus(status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch status {
	case "COMPLETED":
		t.tasksCompleted++
	case "FAILED", "ABORTED":
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

// StartReporting flushes metrics to the structured log every 60 seconds
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
				slog.Int("tasks_completed", t.tasksCompleted),
				slog.Int("tasks_failed", t.tasksFailed),
				slog.Int("replans", t.replans),
				slog.Int("panics", t.panics),
			)

			// Reset counters for the next window
			t.llmInvocations, t.llmErrors, t.totalLatency = 0, 0, 0
			t.tasksCompleted, t.tasksFailed, t.replans, t.panics = 0, 0, 0, 0
			t.mu.Unlock()
		}
	}
}
