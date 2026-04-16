// cmd/replay/main.go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/eventstore"
)

func main() {
	sessionID := flag.String("session", "", "Session ID to replay (required)")
	step := flag.Int("step", -1, "Event ID to reconstruct state up to (default: play all)")
	atTime := flag.String("at", "", "Timestamp to reconstruct state up to (e.g., 2026-04-02T21:03:15Z)")
	dbPath := flag.String("db", "data/events.db", "Path to SQLite event store")
	flag.Parse()

	if *sessionID == "" {
		fmt.Println("Error: --session is required")
		os.Exit(1)
	}

	var targetTime time.Time
	if *atTime != "" {
		parsed, err := time.Parse(time.RFC3339, *atTime)
		if err != nil {
			fmt.Printf("Error: invalid timestamp format for --at. Use RFC3339 (e.g. 2026-04-02T21:03:15Z)\n")
			os.Exit(1)
		}
		targetTime = parsed
	}

	// Silence standard logging so we only see the clean output dump
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	store, err := eventstore.NewSQLiteStore(*dbPath, logger)
	if err != nil {
		fmt.Printf("Failed to open event store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	events, err := store.GetStream(context.Background(), *sessionID)
	if err != nil {
		fmt.Printf("Failed to fetch event stream: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("No events found for session:", *sessionID)
		os.Exit(0)
	}

	var currentState domain.GameState
	var lastEventID int64
	var lastTimestamp time.Time

	fmt.Printf("--- Replaying Session: %s ---\n", *sessionID)

	for _, ev := range events {
		if *step != -1 && ev.ID > int64(*step) {
			break
		}
		if !targetTime.IsZero() && ev.CreatedAt.After(targetTime) {
			break
		}

		lastEventID = ev.ID
		lastTimestamp = ev.CreatedAt

		// Reconstruct base GameState
		if ev.Type == domain.EventTypeStateTick || ev.Type == domain.EventTypeStateUpdated {
			var st domain.GameState
			if err := json.Unmarshal(ev.Payload, &st); err != nil {
				fmt.Printf("[%s] Error unmarshaling state: %v\n", ev.CreatedAt.Format(time.RFC3339), err)
			} else {
				currentState = st
			}
		}

		// Clean, formatted thought process output
		switch ev.Type {
		case domain.EventTypePlanCreated:
			var plan domain.Plan
			if err := json.Unmarshal(ev.Payload, &plan); err != nil {
				fmt.Printf("[%s] Error unmarshaling plan: %v\n", ev.CreatedAt.Format(time.RFC3339), err)
			} else {
				fmt.Printf("[%s] 🧠 PLAN CREATED: %s (Tasks: %d)\n", ev.CreatedAt.Format(time.RFC3339), plan.Objective, len(plan.Tasks))
			}
		case domain.EventTypeTaskStart:
			fmt.Printf("[%s] 🚀 TASK START: %s\n", ev.CreatedAt.Format(time.RFC3339), ev.Trace.ActionID)
		case domain.EventTypeTaskEnd:
			var payload domain.TaskEndPayload
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				fmt.Printf("[%s] Error unmarshaling TaskEnd payload: %v\n", ev.CreatedAt.Format(time.RFC3339), err)
			} else {
				status := payload.Status
				if status == "FAILED" {
					fmt.Printf("[%s] ❌ TASK FAILED: %s (Cause: %s)\n", ev.CreatedAt.Format(time.RFC3339), payload.CommandID, payload.Cause)
					// (Concept) In a real replay we'd fetch reflection from task_history if not in payload
				} else {
					fmt.Printf("[%s] ✅ TASK COMPLETED: %s (Duration: %dms)\n", ev.CreatedAt.Format(time.RFC3339), payload.CommandID, payload.DurationMs)
				}
			}
		case domain.EventBotDeath:
			fmt.Printf("[%s] 💀 BOT DIED\n", ev.CreatedAt.Format(time.RFC3339))
		case domain.EventTypePlanInvalidated:
			fmt.Printf("[%s] ⚠️ PLAN INVALIDATED\n", ev.CreatedAt.Format(time.RFC3339))
		default:
			// Filter out high-frequency noise like state ticks to keep the replay human-readable
			if ev.Type != domain.EventTypeStateTick {
				traceStr := ev.Trace.TraceID
				if ev.Trace.ActionID != "" {
					traceStr += " | Action: " + ev.Trace.ActionID
				}
				fmt.Printf("[%s] 🔄 %s [Trace: %s]\n", ev.CreatedAt.Format(time.RFC3339), ev.Type, traceStr)
			}
		}
	}

	fmt.Printf("\n--- Reconstructed State at Step %d (%s) ---\n", lastEventID, lastTimestamp.Format(time.RFC3339))
	b, _ := json.MarshalIndent(currentState, "", "  ")
	fmt.Println(string(b))
}
