package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

type FeatureFlags struct {
	AsyncLoop       bool `json:"async_loop" env:"FF_ASYNC_LOOP"`
	NonBlockPlanner bool `json:"non_block_planner" env:"FF_NON_BLOCK_PLANNER"`
	ActionQueue     bool `json:"action_queue" env:"FF_ACTION_QUEUE"`
	ClientSmooth    bool `json:"client_smooth" env:"FF_CLIENT_SMOOTH"`
}

func DefaultFlags() FeatureFlags {
	return FeatureFlags{
		AsyncLoop:       true, // Production: non-blocking loops
		NonBlockPlanner: true, // Production: background LLM planning
		ActionQueue:     true, // Production: priority queue + backpressure
		ClientSmooth:    true, // Production: 60 FPS interpolation
	}
}

// DynamicFlags wraps FeatureFlags with thread-safe access and file-watching for hot-reloads.
type DynamicFlags struct {
	mu    sync.RWMutex
	flags FeatureFlags

	filepath string
	lastMod  time.Time
	logger   *slog.Logger

	reloadCh chan struct{}
}

func NewDynamicFlags(path string, logger *slog.Logger) *DynamicFlags {
	df := &DynamicFlags{
		flags:    DefaultFlags(),
		filepath: path,
		logger:   logger.With(slog.String("component", "config_watcher")),
		reloadCh: make(chan struct{}),
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

// ReloadChannel returns a channel that gets closed whenever the config is successfully reloaded.
func (f *DynamicFlags) ReloadChannel() <-chan struct{} {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.reloadCh
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

	var newFlags FeatureFlags
	if err := json.Unmarshal(data, &newFlags); err != nil {
		return err
	}

	f.mu.Lock()
	f.flags = newFlags
	f.lastMod = info.ModTime()
	f.mu.Unlock()

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
					f.mu.Lock()
					// Close the old channel to broadcast the change, then make a new one for the next cycle
					close(f.reloadCh)
					f.reloadCh = make(chan struct{})
					f.mu.Unlock()
				} else {
					f.logger.Error("Failed to parse updated config file", slog.Any("error", err))
				}
			}
		}
	}
}
