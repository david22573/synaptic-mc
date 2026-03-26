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
	configMu       sync.Mutex
)

func LoadConfig(path string) (*Config, error) {
	configMu.Lock()
	defer configMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	configInstance = &cfg
	return configInstance, nil
}
