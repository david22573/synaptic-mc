package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Phase 2 Improvement: scoring-system-first-class
// Move magic numbers into an explicit configuration struct.
type ScoreWeights struct {
	RiskPenalty      float64 `json:"risk_penalty"`
	SuccessWeight    float64 `json:"success_weight"`
	ExplorationBonus float64 `json:"exploration_bonus"`
}

type HumanizationConfig struct {
	AttentionDecay       float64 `json:"attention_decay"`
	HesitationBaseMs     int     `json:"hesitation_base_ms"`
	NoiseLevel           float64 `json:"noise_level"`
	BaseDriftRate        float64 `json:"base_drift_rate"`
	MaxDriftDelayMs      int     `json:"max_drift_delay_ms"`
	DistractionThreshold float64 `json:"distraction_threshold"`
	TaskSpacingMs        int     `json:"task_spacing_ms"`
}

type ExecutionConfig struct {
	CleanupTickerMs           int `json:"cleanup_ticker_ms"`
	DispatchTimeoutMs         int `json:"dispatch_timeout_ms"`
	MaintenanceTickerMs       int `json:"maintenance_ticker_ms"`
	DeathLoopThresholdMs      int `json:"death_loop_threshold_ms"`
	ExecutionWatchdogTimeoutMs int `json:"execution_watchdog_timeout_ms"`
	DeathCountResetMs         int `json:"death_count_reset_ms"`
}

type FeatureFlags struct {
	AsyncLoop         bool               `json:"async_loop" env:"FF_ASYNC_LOOP"`
	NonBlockPlanner   bool               `json:"non_block_planner" env:"FF_NON_BLOCK_PLANNER"`
	ActionQueue       bool               `json:"action_queue" env:"FF_ACTION_QUEUE"`
	ClientSmooth      bool               `json:"client_smooth" env:"FF_CLIENT_SMOOTH"`
	CurriculumHorizon int                `json:"curriculum_horizon" env:"FF_CURRICULUM_HORIZON"`
	Weights           ScoreWeights       `json:"score_weights"`
	Humanization      HumanizationConfig `json:"humanization"`
	Execution         ExecutionConfig    `json:"execution"`
}

func DefaultFlags() FeatureFlags {
	return FeatureFlags{
		AsyncLoop:         true, // Production: non-blocking loops
		NonBlockPlanner:   true, // Production: background LLM planning
		ActionQueue:       true, // Production: priority queue + backpressure
		ClientSmooth:      true, // Production: 60 FPS interpolation
		CurriculumHorizon: 6,    // Phase 8: Composable multi-step programs
		Weights: ScoreWeights{
			RiskPenalty:      20.0,
			SuccessWeight:    30.0,
			ExplorationBonus: 5.0,
		},
		Humanization: HumanizationConfig{
			AttentionDecay:       0.05,
			HesitationBaseMs:     200,
			NoiseLevel:           0.1,
			BaseDriftRate:        0.02,
			MaxDriftDelayMs:      2000,
			DistractionThreshold: 1.5,
			TaskSpacingMs:        100,
		},
		Execution: ExecutionConfig{
			CleanupTickerMs:            30000,
			DispatchTimeoutMs:          15000,
			MaintenanceTickerMs:        500,
			DeathLoopThresholdMs:       30000,
			ExecutionWatchdogTimeoutMs: 60000,
			DeathCountResetMs:          300000,
		},
	}
}

func (f FeatureFlags) MapToHumanizationConfig() HumanizationConfig {
	return f.Humanization
}

func (f FeatureFlags) MapToExecutionConfig() ExecutionConfig {
	return f.Execution
}

// DynamicFlags wraps FeatureFlags with thread-safe access and file-watching for hot-reloads.
type DynamicFlags struct {
	mu    sync.RWMutex
	flags FeatureFlags

	filepath string
	lastMod  time.Time
	logger   *slog.Logger

	subs []chan struct{}
}

func NewDynamicFlags(path string, logger *slog.Logger) *DynamicFlags {
	df := &DynamicFlags{
		flags:    DefaultFlags(),
		filepath: path,
		logger:   logger.With(slog.String("component", "config_watcher")),
		subs:     make([]chan struct{}, 0),
	}

	if path != "" {
		_ = df.LoadFromFile() // Attempt initial load, ignore error if file doesn't exist yet
	}

	return df
}

// Get returns a thread-safe copy of the current feature flags.
func (f *DynamicFlags) Get() FeatureFlags {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.flags
}

// Subscribe returns a new channel that receives a signal whenever the config is reloaded.
func (f *DynamicFlags) Subscribe() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{}, 1)
	f.subs = append(f.subs, ch)
	return ch
}

func (f *DynamicFlags) notify() {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, ch := range f.subs {
		select {
		case ch <- struct{}{}:
		default:
			// Buffer full, signal already pending
		}
	}
}

// LoadFromFile reads the JSON config and updates the flags safely.
func (f *DynamicFlags) LoadFromFile() error {
	info, err := os.Stat(f.filepath)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(f.filepath)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Start with a copy of current flags to support partial updates
	newFlags := f.flags
	if err := json.Unmarshal(data, &newFlags); err != nil {
		return err
	}

	f.flags = newFlags
	f.lastMod = info.ModTime()

	f.logger.Info("Feature flags hot-reloaded from file", slog.String("file", f.filepath))
	return nil
}

func (f *DynamicFlags) hasConfigChanged() bool {
	info, err := os.Stat(f.filepath)
	if err != nil {
		return false
	}

	f.mu.RLock()
	changed := info.ModTime().After(f.lastMod)
	f.mu.RUnlock()

	return changed
}

// Watch starts a background routine that polls the config file for modifications.
func (f *DynamicFlags) Watch(ctx context.Context) {
	if f.filepath == "" {
		f.logger.Warn("No config file path provided, skipping watcher")
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			f.logger.Info("Stopping config watcher")
			return
		case <-ticker.C:
			if f.hasConfigChanged() {
				if err := f.LoadFromFile(); err == nil {
					f.notify()
				} else {
					f.logger.Error("Failed to parse updated config file", slog.Any("error", err))
				}
			}
		}
	}
}
