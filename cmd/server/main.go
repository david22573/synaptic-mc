// cmd/server/main.go
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/eventstore"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/llm"
	"david22573/synaptic-mc/internal/observability"
	"david22573/synaptic-mc/internal/orchestrator"
	"david22573/synaptic-mc/internal/strategy"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	_ = godotenv.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Storage & API Clients
	store, err := eventstore.NewSQLiteEventStore("./events.db")
	if err != nil {
		logger.Error("Failed to init event store", slog.Any("error", err))
		os.Exit(1)
	}
	defer store.Close()

	llmClient := llm.NewClient(llm.Config{
		APIURL:     "https://openrouter.ai/api/v1/chat/completions",
		APIKey:     os.Getenv("OPENROUTER_API_KEY"),
		Model:      "deepseek/deepseek-v3.2",
		MaxRetries: 3,
	})

	// 2. Build the Pure Decision Pipeline (Phase 1)
	evaluator := strategy.NewEvaluator()
	planner := decision.NewLLMPlanner(llmClient, evaluator)
	policy := decision.NewHardGuardrails()

	decisionEngine := decision.NewPipeline(planner, policy)

	// 3. Network & Orchestration Binding
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("WebSocket upgrade failed", slog.Any("error", err))
			return
		}

		sessionID := "sess-" + time.Now().Format("20060102150405")
		logger.Info("Agent Connected", slog.String("session", sessionID))

		// 4. Execution layer with Idempotency
		baseController := execution.NewWSController(conn)
		safeController := execution.NewIdempotentController(baseController, 100)

		uiHub := observability.NewHub(logger)
		go uiHub.Run(ctx)

		mux := http.NewServeMux()
		mux.HandleFunc("/ui/ws", uiHub.HandleConnections) // Mount the UI websocket

		// Serve static UI files if needed
		mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("./public"))))

		// 5. Run Orchestrator
		orch := orchestrator.New(sessionID, store, decisionEngine, safeController, uiHub, logger)

		// Create a session-specific context
		sessCtx, sessCancel := context.WithCancel(ctx)
		defer sessCancel()

		// Start the Orchestrator lifecycle in the background
		go func() {
			if err := orch.Run(sessCtx); err != nil && err != context.Canceled {
				logger.Error("Orchestrator failed", slog.Any("error", err))
			}
		}()

		// WebSocket Read Loop: Ingests state and events into the Orchestrator
		for {
			var msg struct {
				Type    string              `json:"type"`
				Payload json.RawMessage     `json:"payload"`
				Trace   domain.TraceContext `json:"trace"`
			}

			if err := conn.ReadJSON(&msg); err != nil {
				logger.Warn("Bot disconnected", slog.Any("error", err))
				break // Exits loop, fires defer sessCancel() shutting down Orchestrator
			}

			switch msg.Type {
			case "state":
				var state domain.GameState
				if err := json.Unmarshal(msg.Payload, &state); err == nil {
					orch.IngestState(sessCtx, state)
				}
			case "event":
				orch.IngestEvent(sessCtx, domain.DomainEvent{
					SessionID: sessionID,
					Trace:     msg.Trace,
					Type:      domain.EventType(msg.Type),
					Payload:   msg.Payload,
					CreatedAt: time.Now(),
				})
			}
		}
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		logger.Info("Listening on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Server crashed", slog.Any("error", err))
		}
	}()

	<-ctx.Done()
	logger.Info("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}
