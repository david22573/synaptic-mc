package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

const (
	Port         = ":8080"
	DefaultAPI   = "https://openrouter.ai/api/v1/chat/completions"
	DefaultModel = "mistralai/mistral-small-2603"
	DatabasePath = "./bot_memory.db"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("[!] No .env file found. Relying on system environment or defaults.")
	}

	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") == "true" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		logger.Error("Fatal: OPENROUTER_API_KEY environment variable is not set")
		os.Exit(1)
	}

	apiURL := os.Getenv("LLM_API_URL")
	if apiURL == "" {
		apiURL = DefaultAPI
	}
	modelName := os.Getenv("LLM_MODEL")
	if modelName == "" {
		modelName = DefaultModel
	}

	memory, err := NewSQLiteMemory(DatabasePath)
	if err != nil {
		logger.Error("Fatal: could not initialize database", slog.Any("error", err))
		os.Exit(1)
	}
	defer memory.Close()
	logger.Info("Database initialized", slog.String("path", DatabasePath))

	eventStore, err := NewSQLiteEventStore("./events.db")
	if err != nil {
		logger.Error("Fatal: could not initialize event store", slog.Any("error", err))
		os.Exit(1)
	}
	defer eventStore.Close()
	logger.Info("Event Store initialized", slog.String("path", "./events.db"))

	telemetry := NewTelemetry(logger, 5.00)
	go telemetry.StartReporting(context.Background())

	rawBrain := NewLLMBrain(apiURL, modelName, apiKey, memory, telemetry)
	fallbackBrain := NewFallbackBrain()
	brain := NewResilientBrain(rawBrain, fallbackBrain, logger, telemetry)

	uiHub := NewUIHub(logger)
	go uiHub.Run()

	config, err := LoadConfig("./config.json")
	if err != nil {
		logger.Error("Failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config)
	})

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("WebSocket upgrade failed", slog.Any("error", err))
			return
		}
		logger.Info("Bot connected", slog.String("remote_addr", r.RemoteAddr))
		telemetry.RecordSessionStart()

		sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())

		planner := NewLLMPlanner(brain, uiHub, memory, telemetry, logger, sessionID)
		routine := NewDefaultRoutineManager()
		exec := NewWSExecutor(conn)

		engine := NewEngine(planner, routine, exec, memory, telemetry, uiHub, logger, sessionID, eventStore)
		engine.Run(context.Background(), conn)

		logger.Info("Bot disconnected", slog.String("remote_addr", r.RemoteAddr))
		telemetry.RecordSessionEnd()
	})

	http.HandleFunc("/ui/ws", uiHub.HandleConnections)
	http.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("./public"))))

	logger.Info("CraftD Control Plane listening", slog.String("url", "ws://localhost"+Port+"/ws"))
	logger.Info("Observability Dashboard Feed live", slog.String("url", "ws://localhost"+Port+"/ui/ws"))

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		health := map[string]interface{}{
			"status":          "healthy",
			"active_sessions": telemetry.ActiveSessions(),
			"llm_avg_latency": telemetry.AvgLatency().String(),
			"timestamp":       time.Now().UTC(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(health)
	})

	if err := http.ListenAndServe(Port, nil); err != nil {
		logger.Error("Server crashed", slog.Any("error", err))
		os.Exit(1)
	}
}
