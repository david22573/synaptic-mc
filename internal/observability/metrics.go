package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type FluidityMetrics struct {
	DecisionJitter   prometheus.Histogram
	ActionGapMs      prometheus.Gauge
	TaskInterruption prometheus.Counter
	UIJitter         prometheus.Histogram
	UISyncDrift      prometheus.Gauge
}

var Metrics = &FluidityMetrics{
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
}
