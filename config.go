package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type Config struct {
	WSURL         string         `json:"ws_url"`
	ViewerPort    int            `json:"viewer_port"`
	EnableViewer  bool           `json:"enable_viewer"`
	DebugChat     bool           `json:"debug_chat"`
	TaskTimeouts  map[string]int `json:"task_timeouts"`
	ThreatWeights map[string]int `json:"threat_weights"`
}

var (
	configInstance *Config
	configOnce     sync.Once
	configError    error
)

func LoadConfig(path string) (*Config, error) {
	configOnce.Do(func() {
		data, err := os.ReadFile(path)
		if err != nil {
			configError = fmt.Errorf("failed to read config: %w", err)
			return
		}

		configInstance = &Config{}
		if err := json.Unmarshal(data, configInstance); err != nil {
			configError = fmt.Errorf("failed to parse config: %w", err)
			configInstance = nil
		}
	})

	return configInstance, configError
}
