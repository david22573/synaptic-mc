package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type SystemMetrics struct {
	DecisionJitter   prometheus.Histogram
	ActionGapMs      prometheus.Gauge
	TaskInterruption prometheus.Counter
	UIJitter         prometheus.Histogram
	UISyncDrift      prometheus.Gauge

	// Phase 5 Improvement: structured-metrics
	PlanOutcome        *prometheus.CounterVec
	TaskCompletionTime prometheus.Histogram
	LLMLatency         prometheus.Histogram
	RetryCountTotal    prometheus.Counter
}

var Metrics = &SystemMetrics{
	DecisionJitter: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_decision_jitter_ms",
		Help:    "Variance from target tick rate",
		Buckets: []float64{1, 5, 10, 25, 50, 100},
	}),
	ActionGapMs: promauto.NewGauge(prometheus.GaugeOpts{
		Name: "agent_action_gap_ms",
		Help: "Time between action dispatches",
	}),
	TaskInterruption: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_task_interruptions_total",
		Help: "How often tasks are aborted early due to replanning or panic",
	}),
	UIJitter: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ui_state_jitter_ms",
		Help:    "Time between UI state updates",
		Buckets: []float64{20, 50, 100, 250, 500},
	}),
	UISyncDrift: promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ui_position_drift_blocks",
		Help: "Distance between UI interpolated and server position",
	}),

	// Phase 5: Production Observability Metrics
	PlanOutcome: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_plan_outcome_total",
		Help: "Total number of plans evaluated by their outcome status (completed, failed, invalidated)",
	}, []string{"status"}),

	TaskCompletionTime: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_task_completion_time_ms",
		Help:    "Time taken to complete an individual task from dispatch to completion",
		Buckets: []float64{100, 500, 1000, 5000, 10000, 30000, 60000},
	}),

	LLMLatency: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_llm_latency_ms",
		Help:    "Latency of LLM API generation calls",
		Buckets: []float64{250, 500, 1000, 2500, 5000, 10000, 20000},
	}),

	RetryCountTotal: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_dispatch_retry_total",
		Help: "Total number of task dispatch retries due to controller errors",
	}),
}
