// cmd/train/main.go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/eventstore"
	"david22573/synaptic-mc/internal/llm"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/observability"
	"david22573/synaptic-mc/internal/voyager"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		slog.Debug("No .env file found")
	}

	dbDir := flag.String("data-dir", "data", "Data directory containing DB files")
	sessionID := flag.String("session", "", "Optional session ID to focus training on")
	llmURL := flag.String("llm-url", getEnvOrDefault("LLM_URL", "http://localhost:11434/v1/chat/completions"), "LLM API URL")
	llmKey := flag.String("llm-key", getEnvOrDefault("LLM_API_KEY", ""), "LLM API key")
	llmModel := flag.String("llm-model", getEnvOrDefault("LLM_MODEL", "llama3.2"), "LLM generation model name")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	eventStorePath := filepath.Join(*dbDir, "events.db")
	memoryStorePath := filepath.Join(*dbDir, "memory.db")
	vectorStorePath := filepath.Join(*dbDir, "skills.db")

	eStore, err := eventstore.NewSQLiteStore(eventStorePath, logger)
	if err != nil {
		logger.Error("Failed to open event store", slog.Any("error", err))
		os.Exit(1)
	}
	defer eStore.Close()

	mStore, err := memory.NewSQLiteStore(memoryStorePath)
	if err != nil {
		logger.Error("Failed to open memory store", slog.Any("error", err))
		os.Exit(1)
	}
	defer mStore.Close()

	vStore, err := voyager.NewSQLiteVectorStore(vectorStorePath)
	if err != nil {
		logger.Error("Failed to open vector store", slog.Any("error", err))
		os.Exit(1)
	}
	defer vStore.Close()

	llmClient := llm.NewClient(llm.Config{
		APIURL:      *llmURL,
		APIKey:      *llmKey,
		StrongModel: *llmModel,
		CheapModel:  *llmModel,
	})

	critic := voyager.NewStateCriticWithLLM(llmClient)
	curriculum := voyager.NewAutonomousCurriculum(llmClient, vStore, mStore, nil)
	skillManager := voyager.NewSkillManager(vStore, llmClient)

	logger.Info("Starting Offline Training Session")
	observability.Metrics.OfflineTrainingRuns.Inc()

	// 1. Get sessions to process
	sessions := []string{*sessionID}
	if *sessionID == "" {
		// Mock: in a real impl we'd query all session IDs from the event store
		sessions = []string{"minecraft-agent-01"} 
	}

	for _, sid := range sessions {
		logger.Info("Training on session", slog.String("session_id", sid))
		
		events, err := eStore.GetStream(ctx, sid)
		if err != nil {
			logger.Warn("Failed to get event stream", slog.String("session_id", sid), slog.Any("error", err))
			continue
		}

		// 2. Post-Mortem Death Review
		processDeaths(ctx, sid, events, critic, mStore, logger)

		// 3. Success Mining & Skill Extraction
		processSuccesses(ctx, sid, events, curriculum, skillManager, logger)
	}

	logger.Info("Offline Training Session Complete")
}

func processDeaths(ctx context.Context, sid string, events []domain.DomainEvent, critic *voyager.StateCritic, mStore memory.Store, logger *slog.Logger) {
	for i, ev := range events {
		if ev.Type == domain.EventBotDeath {
			logger.Info("POST-MORTEM: Bot death detected", slog.String("session_id", sid), slog.Int64("event_id", ev.ID))
			
			// Extract context: last 10 relevant events
			start := i - 10
			if start < 0 { start = 0 }
			_ = events[start:i] // contextEvents - preserved for future post-mortem logic

			// Reconstruct last known state
			var lastState domain.GameState
			for j := i - 1; j >= 0; j-- {
				if events[j].Type == domain.EventTypeStateUpdated || events[j].Type == domain.EventTypeStateTick {
					_ = json.Unmarshal(events[j].Payload, &lastState)
					break
				}
			}

			// Generate post-mortem rule
			_, refl := critic.Evaluate(domain.ActionIntent{Action: "survival"}, lastState, lastState, domain.ExecutionResult{Success: false, Cause: "DEATH"}, 0)
			
			if refl != nil {
				summary := fmt.Sprintf("DEATH POST-MORTEM: %s. Cause: %s. Fix: %s", refl.Failure, refl.Cause, refl.Fix)
				logger.Info("Extracted learning from death", slog.String("summary", summary))
				_ = mStore.SetSummary(ctx, sid, fmt.Sprintf("death_learning_%d", ev.ID), summary)
				observability.Metrics.DeathReviewsCompleted.Inc()
			}
		}
	}
}

func processSuccesses(ctx context.Context, sid string, events []domain.DomainEvent, curriculum *voyager.AutonomousCurriculum, skillManager *voyager.SkillManager, logger *slog.Logger) {
	for i, ev := range events {
		if ev.Type == domain.EventTypeTaskEnd {
			var payload domain.TaskEndPayload
			if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.Success {
				// Only mine for "complex" or "long" successful tasks that aren't already skills
				if payload.DurationMs > 5000 && payload.Action != "use_skill" && payload.Action != "idle" {
					logger.Info("SUCCESS MINING: Potential skill detected", slog.String("action", payload.Action), slog.String("target", payload.Target))

					// Find before/after states
					var before, after domain.GameState
					
					// Re-find after state
					for j := i; j < len(events); j++ {
						if events[j].Type == domain.EventTypeStateUpdated {
							_ = json.Unmarshal(events[j].Payload, &after)
							break
						}
					}
					// Re-find before state (based on TaskStart event ID)
					for j := i - 1; j >= 0; j-- {
						if events[j].Type == domain.EventTypeTaskStart {
							// Found start, find state before it
							for k := j - 1; k >= 0; k-- {
								if events[k].Type == domain.EventTypeStateUpdated {
									_ = json.Unmarshal(events[k].Payload, &before)
									break
								}
							}
							break
						}
					}

					if before.Initialized && after.Initialized {
						intent := domain.ActionIntent{
							Action: payload.Action,
							Target: payload.Target,
							Count: 1, // heuristic
							Rationale: "Offline success mining",
						}
						
						res, err := curriculum.SynthesizeCode(ctx, intent, before, after)
						if err == nil && res.JSCode != "" {
							skillName := fmt.Sprintf("mined_%s_%s", payload.Action, payload.Target)
							skill := voyager.ExecutableSkill{
								Name: skillName,
								Description: fmt.Sprintf("Mined skill for %s %s", payload.Action, payload.Target),
								JSCode: res.JSCode,
								Version: 1,
								RequiredItems: res.RequiredItems,
							}
							if err := skillManager.SaveSkill(ctx, skill); err == nil {
								logger.Info("Extracted new permanent skill", slog.String("name", skillName))
								observability.Metrics.SkillsExtracted.Inc()
							}
						}
					}
				}
			}
		}
	}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
