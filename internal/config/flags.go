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

type FeatureFlags struct {
	AsyncLoop       bool         `json:"async_loop" env:"FF_ASYNC_LOOP"`
	NonBlockPlanner bool         `json:"non_block_planner" env:"FF_NON_BLOCK_PLANNER"`
	ActionQueue     bool         `json:"action_queue" env:"FF_ACTION_QUEUE"`
	ClientSmooth    bool         `json:"client_smooth" env:"FF_CLIENT_SMOOTH"`
	Weights         ScoreWeights `json:"score_weights"`
}

func DefaultFlags() FeatureFlags {
	return FeatureFlags{
		AsyncLoop:       true, // Production: non-blocking loops
		NonBlockPlanner: true, // Production: background LLM planning
		ActionQueue:     true, // Production: priority queue + backpressure
		ClientSmooth:    true, // Production: 60 FPS interpolation
		Weights: ScoreWeights{
			RiskPenalty:      20.0,
			SuccessWeight:    30.0,
			ExplorationBonus: 5.0,
		},
	}
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
