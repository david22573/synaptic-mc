package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/eventstore"
)

func main() {
	sessionID := flag.String("session", "", "Session ID to replay (required)")
	step := flag.Int("step", -1, "Event ID to reconstruct state up to (default: play all)")
	dbPath := flag.String("db", "data/events.db", "Path to SQLite event store")
	flag.Parse()

	if *sessionID == "" {
		fmt.Println("Error: --session is required")
		os.Exit(1)
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

	fmt.Printf("--- Replaying Session: %s ---\n", *sessionID)

	for _, ev := range events {
		if *step != -1 && ev.ID > int64(*step) {
			break
		}

		lastEventID = ev.ID

		// Reconstruct base GameState
		if ev.Type == domain.EventTypeStateTick || ev.Type == domain.EventTypeStateUpdated {
			var st domain.GameState
			if err := json.Unmarshal(ev.Payload, &st); err == nil {
				currentState = st
			}
		}

		// Enforce trace correlation visibility
		traceStr := ev.Trace.TraceID
		if ev.Trace.ActionID != "" {
			traceStr += " | Action: " + ev.Trace.ActionID
		}

		fmt.Printf("[ID: %d] [%s] %s\n", ev.ID, traceStr, ev.Type)
	}

	fmt.Printf("\n--- Reconstructed State at Step %d ---\n", lastEventID)
	b, _ := json.MarshalIndent(currentState, "", "  ")
	fmt.Println(string(b))
}
