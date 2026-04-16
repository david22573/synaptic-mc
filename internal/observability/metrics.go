// internal/observability/metrics.go
package observability

import (
	"sync/atomic"

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

	// Week 6: Core Metrics
	StuckEvents    prometheus.Counter
	ReflexTriggers prometheus.Counter
	FailureLoops   prometheus.Counter
	DroppedEvents  prometheus.Counter

	// Hard Performance Budgeting Metrics
	PlannerDuration    prometheus.Histogram
	TaskExecDuration   prometheus.Histogram
	StateAgeMs         prometheus.Histogram
	ReplanCount        prometheus.Counter
	TaskInterruptCount prometheus.Counter
	EventQueueLag      prometheus.Histogram

	// Scientific Dashboard Metrics (Counters)
	DeathsTotal            prometheus.Counter
	TasksCompletedTotal    prometheus.Counter
	ResourcesGatheredTotal prometheus.Counter
	PathFailuresTotal      prometheus.Counter
	SkillReuseTotal        prometheus.Counter
	SurvivalTimeSeconds    prometheus.Counter

	// Internal Atomic tracking for JSON dashboard (high-ROI simplicity)
	deaths     atomic.Uint64
	tasks      atomic.Uint64
	resources  atomic.Uint64
	paths      atomic.Uint64
	skills     atomic.Uint64
	survival   atomic.Uint64
	replans    atomic.Uint64
	interrupts atomic.Uint64
	stuck      atomic.Uint64

	// Offline Trainer Metrics
	OfflineTrainingRuns   prometheus.Counter
	SkillsExtracted       prometheus.Counter
	DeathReviewsCompleted prometheus.Counter
}

func (s *SystemMetrics) IncDeath() {
	s.DeathsTotal.Inc()
	s.deaths.Add(1)
}

func (s *SystemMetrics) IncTask() {
	s.TasksCompletedTotal.Inc()
	s.tasks.Add(1)
}

func (s *SystemMetrics) AddResource(n uint64) {
	s.ResourcesGatheredTotal.Add(float64(n))
	s.resources.Add(n)
}

func (s *SystemMetrics) IncPathFailure() {
	s.PathFailuresTotal.Inc()
	s.paths.Add(1)
}

func (s *SystemMetrics) IncSkillReuse() {
	s.SkillReuseTotal.Inc()
	s.skills.Add(1)
}

func (s *SystemMetrics) AddSurvivalTime(seconds uint64) {
	s.SurvivalTimeSeconds.Add(float64(seconds))
	s.survival.Add(seconds)
}

func (s *SystemMetrics) IncReplan() {
	s.ReplanCount.Inc()
	s.replans.Add(1)
}

func (s *SystemMetrics) IncInterrupt() {
	s.TaskInterruptCount.Inc()
	s.interrupts.Add(1)
}

func (s *SystemMetrics) IncStuck() {
	s.StuckEvents.Inc()
	s.stuck.Add(1)
}

func (s *SystemMetrics) GetStats() map[string]any {
	return map[string]any{
		"deaths":             s.deaths.Load(),
		"tasks_completed":    s.tasks.Load(),
		"resources_gathered": s.resources.Load(),
		"path_failures":      s.paths.Load(),
		"skill_reuse":        s.skills.Load(),
		"survival_time":      s.survival.Load(),
		"replan_count":       s.replans.Load(),
		"interrupt_count":    s.interrupts.Load(),
		"stuck_events":       s.stuck.Load(),
	}
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

	// Week 6 Metrics
	StuckEvents: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_stuck_events_total",
		Help: "Number of times the movement watchdog detected the bot as physically stuck",
	}),
	ReflexTriggers: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_reflex_triggers_total",
		Help: "Number of times the survival policy overrode the planner",
	}),
	FailureLoops: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_failure_loops_total",
		Help: "Number of times a task hit the infinite failure loop threshold",
	}),
	DroppedEvents: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_dropped_events_total",
		Help: "Number of events dropped due to worker saturation",
	}),

	// Performance Budgeting
	PlannerDuration: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_planner_duration_ms",
		Help:    "Time taken to generate a full LLM plan",
		Buckets: []float64{100, 500, 1000, 2500, 5000, 10000},
	}),
	TaskExecDuration: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_task_exec_duration_ms",
		Help:    "Actual time taken to execute a task in TS",
		Buckets: []float64{500, 1000, 5000, 10000, 30000, 60000},
	}),
	StateAgeMs: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_state_age_ms",
		Help:    "Latency between state tick and decision processing",
		Buckets: []float64{10, 50, 100, 250, 500},
	}),
	ReplanCount: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_replan_total",
		Help: "Total number of background replans triggered",
	}),
	TaskInterruptCount: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_task_interrupt_total",
		Help: "Number of times a running task was aborted by the planner",
	}),
	EventQueueLag: promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_event_queue_lag_ms",
		Help:    "Latency between event creation and worker processing",
		Buckets: []float64{1, 5, 10, 25, 50, 100},
	}),

	DeathsTotal: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_deaths_total",
		Help: "Total number of bot deaths",
	}),
	TasksCompletedTotal: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_tasks_completed_total",
		Help: "Total number of successfully completed tasks",
	}),
	ResourcesGatheredTotal: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_resources_gathered_total",
		Help: "Total quantity of items/blocks collected",
	}),
	PathFailuresTotal: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_path_failures_total",
		Help: "Total number of pathfinding or blocked-path failures",
	}),
	SkillReuseTotal: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_skill_reuse_total",
		Help: "Total number of times a synthesized skill was executed",
	}),
	SurvivalTimeSeconds: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_survival_time_seconds_total",
		Help: "Total cumulative time spent alive",
	}),
	OfflineTrainingRuns: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_offline_training_runs_total",
		Help: "Total number of offline training sessions completed",
	}),
	SkillsExtracted: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_skills_extracted_total",
		Help: "Number of permanent skills synthesized from success mining",
	}),
	DeathReviewsCompleted: promauto.NewCounter(prometheus.CounterOpts{
		Name: "agent_death_reviews_total",
		Help: "Number of post-mortem death analyses completed",
	}),
}
