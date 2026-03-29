package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// MockMemory implements MemoryBank for deterministic testing
type MockMemory struct {
	summary map[string]string
	history string
	world   string
}

func (m *MockMemory) LogEvent(action, details string, meta EventMeta) {}
func (m *MockMemory) GetRecentContext(ctx context.Context, sessionID string, limit int) (string, error) {
	return m.history, nil
}
func (m *MockMemory) SetSummary(ctx context.Context, sessionID, key, value string) error {
	m.summary[key] = value
	return nil
}
func (m *MockMemory) GetSummary(ctx context.Context, sessionID string) (string, error) {
	var sb strings.Builder
	for k, v := range m.summary {
		sb.WriteString("- " + k + ": " + v + "\n")
	}
	return sb.String(), nil
}
func (m *MockMemory) GetSummaryValue(ctx context.Context, sessionID, key string) (string, error) {
	return m.summary[key], nil
}
func (m *MockMemory) SaveMilestone(ctx context.Context, sessionID string, ms *MilestonePlan) error {
	return nil
}
func (m *MockMemory) LoadMilestone(ctx context.Context, sessionID string) (*MilestonePlan, error) {
	return nil, nil
}
func (m *MockMemory) ConsolidateSession(ctx context.Context, sessionID string) error {
	return nil
}
func (m *MockMemory) MarkWorldNode(ctx context.Context, name, nodeType string, x, y, z float64) error {
	return nil
}
func (m *MockMemory) GetNode(ctx context.Context, name string) (*WorldNode, error) {
	return nil, nil
}
func (m *MockMemory) GetKnownWorld(ctx context.Context, botX, botY, botZ float64) (string, error) {
	return m.world, nil
}
func (m *MockMemory) Close() error { return nil }

type ReplayTestCase struct {
	Name           string
	State          GameState
	SystemOverride string
	MemoryHistory  string
	ExpectedAction string
	ExpectedTarget string
}

func TestReplayHarness(t *testing.T) {
	if err := godotenv.Load(); err != nil {
		log.Println("[!] No .env file found. Relying on system environment or defaults.")
	}
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping LLM replay tests; OPENROUTER_API_KEY not set")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	telemetry := NewTelemetry(logger, 5.00)

	tests := []ReplayTestCase{
		{
			Name: "Initial Wood Gathering",
			State: GameState{
				Health:    20.0,
				Food:      20.0,
				TimeOfDay: 1000,
				Position:  Vec3{X: 0, Y: 64, Z: 0},
				Inventory: []InventoryItem{}, // Empty inventory
				POIs: []POI{
					{Type: "resource", Name: "oak_log", Distance: 5.0, Score: 100, Direction: "center", Position: Vec3{X: 5, Y: 64, Z: 0}},
				},
			},
			ExpectedAction: "gather",
			ExpectedTarget: "oak_log",
		},
		{
			Name: "Progression: Crafting Planks",
			State: GameState{
				Health:    20.0,
				Food:      20.0,
				TimeOfDay: 2000,
				Position:  Vec3{X: 0, Y: 64, Z: 0},
				Inventory: []InventoryItem{
					{Name: "oak_log", Count: 3},
				},
			},
			ExpectedAction: "craft",
			ExpectedTarget: "oak_planks",
		},
		{
			Name: "Critical Hunger Override",
			State: GameState{
				Health:    8.0,
				Food:      0.0, // Starving
				TimeOfDay: 5000,
				Position:  Vec3{X: 0, Y: 64, Z: 0},
				Inventory: []InventoryItem{
					{Name: "cooked_beef", Count: 5},
				},
			},
			ExpectedAction: "eat",
			ExpectedTarget: "cooked_beef",
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			mem := &MockMemory{
				summary: make(map[string]string),
				history: tc.MemoryHistory,
				world:   "KNOWN WORLD: empty",
			}

			// Wrap the raw LLMBrain in the ResilientBrain
			rawBrain := NewLLMBrain(DefaultAPI, DefaultModel, apiKey, mem, telemetry)
			fallbackBrain := NewFallbackBrain()
			brain := NewResilientBrain(rawBrain, fallbackBrain, logger, telemetry)

			planner := NewTacticalPlanner(brain, mem, "test-session", logger)

			// Phase 4 Integration: Evaluate strategy exactly like the engine does
			strategyManager := NewStrategyManager()
			learningSystem := NewLearningSystem(logger)

			strat := strategyManager.Evaluate(tc.State)
			learnedRules := learningSystem.GetRules()

			// Format the exact override string the new prompt expects
			sysOverride := fmt.Sprintf("PRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\n%s\nAll milestones and tasks MUST align with these strategies.\n\n%s",
				strat.PrimaryGoal,
				strat.SecondaryGoal,
				learnedRules,
				tc.SystemOverride)

			rawState, _ := json.Marshal(tc.State)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			plan, err := planner.GeneratePlan(ctx, rawState, "test-session", sysOverride)
			if err != nil {
				t.Fatalf("Plan generation failed: %v", err)
			}

			if plan == nil || len(plan.Tasks) == 0 {
				t.Fatalf("LLM returned empty plan or zero tasks")
			}

			firstTask := plan.Tasks[0]

			if firstTask.Action != tc.ExpectedAction {
				t.Errorf("Expected action '%s', got '%s'\nRationale: %s", tc.ExpectedAction, firstTask.Action, firstTask.Rationale)
			}

			if tc.ExpectedTarget != "" && firstTask.Target.Name != tc.ExpectedTarget && firstTask.Target.Name != "wood" {
				t.Errorf("Expected target '%s', got '%s'", tc.ExpectedTarget, firstTask.Target.Name)
			}
		})
	}
}
