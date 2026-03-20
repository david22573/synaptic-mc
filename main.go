// main.go
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"

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

	telemetry := NewTelemetry(logger)
	go telemetry.StartReporting(context.Background())

	brain := NewLLMBrain(apiURL, modelName, apiKey, memory, telemetry)

	// Phase 4: Wire UI Observability Hub
	uiHub := NewUIHub(logger)

	// Route 1: JS Bot WebSocket
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("WebSocket upgrade failed", slog.Any("error", err))
			return
		}
		logger.Info("Bot connected", slog.String("remote_addr", r.RemoteAddr))

		engine := NewEngine(brain, conn, memory, telemetry, uiHub, logger)
		engine.Run(context.Background())

		logger.Info("Bot disconnected", slog.String("remote_addr", r.RemoteAddr))
	})

	// Route 2: UI Dashboard WebSocket Feed
	http.HandleFunc("/ui/ws", uiHub.HandleConnections)

	// Route 3: Simple static HTML dashboard (Optional, serves from current dir if exists)
	http.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("./public"))))

	logger.Info("CraftD Control Plane listening", slog.String("url", "ws://localhost"+Port+"/ws"))
	logger.Info("Observability Dashboard Feed live", slog.String("url", "ws://localhost"+Port+"/ui/ws"))

	if err := http.ListenAndServe(Port, nil); err != nil {
		logger.Error("Server crashed", slog.Any("error", err))
		os.Exit(1)
	}
}
