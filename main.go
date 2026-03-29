package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

const (
	Port         = ":8080"
	DefaultAPI   = "https://openrouter.ai/api/v1/chat/completions"
	DefaultModel = "deepseek/deepseek-v3.2"
	DatabasePath = "./bot_memory.db"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		allowedOrigin := os.Getenv("ALLOWED_ORIGIN")
		if allowedOrigin == "" || allowedOrigin == "*" {
			return true
		}
		origin := r.Header.Get("Origin")
		return origin == allowedOrigin
	},
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expectedToken := os.Getenv("API_TOKEN")
		if expectedToken == "" {
			// If no token is configured, allow all
			next.ServeHTTP(w, r)
			return
		}

		token := r.Header.Get("Authorization")
		if token == "" {
			token = r.URL.Query().Get("token") // Fallback for WebSockets
		}

		token = strings.TrimPrefix(token, "Bearer ")

		if token != expectedToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	}
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

	// Context with interrupt signal handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	memory, err := NewSQLiteMemory(DatabasePath, logger)
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
	go telemetry.StartReporting(ctx)

	rawBrain := NewLLMBrain(apiURL, modelName, apiKey, memory, telemetry)
	fallbackBrain := NewFallbackBrain()
	brain := NewResilientBrain(rawBrain, fallbackBrain, logger, telemetry)

	uiHub := NewUIHub(logger)
	go uiHub.Run(ctx)

	config, err := LoadConfig("./config.json")
	if err != nil {
		logger.Error("Failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	mux := http.NewServeMux()

	// Protected Endpoints
	mux.HandleFunc("/api/config", authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config)
	}))

	mux.HandleFunc("/ui/ws", authMiddleware(uiHub.HandleConnections))

	// Bot WS Connection
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("WebSocket upgrade failed", slog.Any("error", err))
			return
		}
		logger.Info("Bot connected", slog.String("remote_addr", r.RemoteAddr))
		telemetry.RecordSessionStart()

		sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())

		learningSystem := NewLearningSystem(logger)
		learningSystem.LoadEpisodicMemory(ctx, eventStore) // Load trauma

		planner := NewTacticalPlanner(brain, memory, sessionID, logger)
		routine := NewDefaultRoutineManager()
		exec := NewWSExecutor(conn)

		engine := NewEngine(planner, routine, exec, memory, telemetry, uiHub, learningSystem, logger, sessionID, eventStore)
		engine.Run(ctx, conn)

		logger.Info("Bot disconnected", slog.String("remote_addr", r.RemoteAddr))
		telemetry.RecordSessionEnd()
	})

	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("./public"))))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		health := map[string]interface{}{
			"status":          "healthy",
			"active_sessions": telemetry.ActiveSessions(),
			"llm_avg_latency": telemetry.AvgLatency().String(),
			"timestamp":       time.Now().UTC(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(health)
	})

	server := &http.Server{
		Addr:    Port,
		Handler: mux,
	}

	// Spin up server in goroutine
	go func() {
		logger.Info("CraftD Control Plane listening", slog.String("url", "ws://localhost"+Port+"/ws"))
		logger.Info("Observability Dashboard Feed live", slog.String("url", "ws://localhost"+Port+"/ui/ws"))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Server crashed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	logger.Info("Graceful shutdown initiated...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Server shutdown error", slog.Any("error", err))
	}
	logger.Info("Server exited.")
}
