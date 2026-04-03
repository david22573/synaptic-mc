package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"golang.org/x/sync/errgroup"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/eventstore"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/llm"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/observability"
	"david22573/synaptic-mc/internal/state"
	"david22573/synaptic-mc/internal/strategy"
	"david22573/synaptic-mc/internal/supervisor"
	"david22573/synaptic-mc/internal/voyager"
)

type Config struct {
	HTTPAddr      string
	EventStoreDB  string
	MemoryDB      string
	VectorDB      string
	UIPath        string
	LLMURL        string
	LLMKey        string
	LLMModel      string
	LLMEmbedModel string
	SessionID     string
	DataDir       string
	BotScript     string
	HesitationMs  int
	NoiseLevel    float64
	ConfigPath    string
}

func main() {
	if err := godotenv.Load(); err != nil {
		slog.Debug("No .env file found")
	}

	cfg := parseConfig()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		logger.Error("Failed to create data directory", slog.String("path", cfg.DataDir), slog.Any("error", err))
		os.Exit(1)
	}

	eventStorePath := filepath.Join(cfg.DataDir, cfg.EventStoreDB)
	memoryStorePath := filepath.Join(cfg.DataDir, cfg.MemoryDB)
	vectorStorePath := filepath.Join(cfg.DataDir, cfg.VectorDB)

	eventStore, err := eventstore.NewSQLiteStore(eventStorePath, logger)
	if err != nil {
		logger.Error("Failed to create event store", slog.String("path", eventStorePath), slog.Any("error", err))
		os.Exit(1)
	}
	defer eventStore.Close()

	memoryStore, err := memory.NewSQLiteStore(memoryStorePath)
	if err != nil {
		logger.Error("Failed to create memory store", slog.String("path", memoryStorePath), slog.Any("error", err))
		os.Exit(1)
	}
	defer memoryStore.Close()

	vectorStore, err := voyager.NewSQLiteVectorStore(vectorStorePath)
	if err != nil {
		logger.Error("Failed to create vector store", slog.String("path", vectorStorePath), slog.Any("error", err))
		os.Exit(1)
	}
	defer vectorStore.Close()

	llmClient := llm.NewClient(llm.Config{
		APIURL:     cfg.LLMURL,
		APIKey:     cfg.LLMKey,
		Model:      cfg.LLMModel,
		EmbedModel: cfg.LLMEmbedModel,
		MaxRetries: 3,
	})

	// === CORE ARCHITECTURE WIRING ===

	eventBus := domain.NewEventBus()
	uiHub := observability.NewHub(logger)

	// 1. State Service
	stateSvc := state.NewService(eventBus, logger)

	// 2. Execution Service
	ctrlManager := execution.NewControllerManager()
	execEngine := execution.NewTaskExecutionEngine(ctrlManager, logger)
	taskManager := execution.NewTaskManager(execEngine, ctrlManager, nil, logger)
	execService := execution.NewService(eventBus, execEngine, taskManager, ctrlManager, logger)

	// Wire UI Hub to Execution Service for direct control inputs
	uiHub.SetOrchestrator(execService)

	// 3. Decision Service
	evaluator := strategy.NewEvaluator()
	critic := voyager.NewStateCritic()
	curriculum := voyager.NewAutonomousCurriculum(llmClient, vectorStore, memoryStore)

	dynFlags := config.NewDynamicFlags(cfg.ConfigPath, logger)

	planner := decision.NewAdvancedPlanner(llmClient, evaluator, critic, memoryStore, eventStore, logger, dynFlags.Get())
	planManager := decision.NewPlanManager()
	_ = decision.NewService(cfg.SessionID, eventBus, planner, planManager, curriculum, critic, stateSvc, logger)

	// Global Event Sink for DB persistence and UI Broadcasts
	globalSink := domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		// Persist to SQLite
		_ = eventStore.Append(ctx, ev.SessionID, ev.Trace, ev.Type, ev.Payload)

		// Broadcast to UI
		var payloadObj interface{}
		_ = json.Unmarshal(ev.Payload, &payloadObj)
		broadcastEv := map[string]interface{}{
			"id":         ev.ID,
			"session_id": ev.SessionID,
			"type":       ev.Type,
			"trace":      ev.Trace,
			"payload":    payloadObj,
			"created_at": ev.CreatedAt,
		}
		uiHub.Broadcast("event_stream", broadcastEv)
	})

	// Explicitly map all events so the local bus routes them correctly
	eventTypes := []domain.EventType{
		domain.EventTypeStateTick,
		domain.EventTypeStateUpdated,
		domain.EventTypeTaskStart,
		domain.EventTypeTaskEnd,
		domain.EventTypePlanCreated,
		domain.EventTypePlanInvalidated,
		domain.EventTypePlanCompleted,
		domain.EventTypePlanFailed,
		domain.EventTypePanic,
		domain.EventTypePanicResolved,
		domain.EventBotDeath,
		domain.EventBotRespawn,
		domain.EventType("STATE_UPDATE"), // Catch manual UI updates
		domain.EventType("DISPATCH_TASK"),
	}

	for _, et := range eventTypes {
		eventBus.Subscribe(et, globalSink)
	}

	runner := supervisor.NewNodeRunner(cfg.BotScript, logger)

	g, ctx := errgroup.WithContext(context.Background())
	mux := http.NewServeMux()

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]any{
			"ws_url":        "ws://localhost:8080/ui/ws",
			"bot_ws_url":    "ws://localhost:8080/bot/ws",
			"viewer_port":   3000,
			"enable_viewer": true,
		})
	})

	mux.HandleFunc("/ui/ws", uiHub.HandleConnections)
	mux.HandleFunc("/bot/ws", handleBotConnection(ctx, eventBus, ctrlManager, runner, logger, cfg.SessionID))

	uiPath := cfg.UIPath
	if !filepath.IsAbs(uiPath) {
		uiPath = filepath.Join(".", uiPath)
	}
	mux.Handle("/", http.FileServer(http.Dir(uiPath)))

	server := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start Background Routines
	g.Go(func() error {
		logger.Info("Starting Dynamic Config Watcher")
		dynFlags.Watch(ctx)
		return nil
	})

	g.Go(func() error {
		logger.Info("Starting UI hub")
		uiHub.Run(ctx)
		return nil
	})

	g.Go(func() error {
		logger.Info("Starting TS supervisor")
		runner.Start(ctx)
		return nil
	})

	g.Go(func() error {
		logger.Info("Starting Planner slow loop")
		planner.SlowReplanLoop(ctx, cfg.SessionID)
		return nil
	})

	g.Go(func() error {
		logger.Info("Starting Task Execution Engine")
		execEngine.Start(ctx)
		return nil
	})

	g.Go(func() error {
		logger.Info("Starting Task Manager Queue")
		taskManager.Run(ctx)
		return nil
	})

	g.Go(func() error {
		logger.Info("Starting HTTP server", slog.String("addr", cfg.HTTPAddr))
		return server.ListenAndServe()
	})

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutdown signal received - graceful shutdown starting")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", slog.Any("error", err))
	}

	if err := g.Wait(); err != nil && err != context.Canceled {
		logger.Error("Service error during shutdown", slog.Any("error", err))
	}

	logger.Info("Synaptic MC shutdown complete")
}

func parseConfig() Config {
	httpAddr := flag.String("http", getEnvOrDefault("HTTP_ADDR", ":8080"), "HTTP server address")
	eventsDB := flag.String("events", getEnvOrDefault("EVENT_STORE_DB", "events.db"), "Event store database filename")
	memoryDB := flag.String("memory", getEnvOrDefault("MEMORY_DB", "memory.db"), "Memory store database filename")
	vectorDB := flag.String("vector", getEnvOrDefault("VECTOR_DB", "skills.db"), "Vector skill database filename")
	uiPath := flag.String("ui", getEnvOrDefault("UI_PATH", "ui/dist"), "UI static files path")
	llmURL := flag.String("llm-url", getEnvOrDefault("LLM_URL", "http://localhost:11434/v1/chat/completions"), "LLM API URL")
	llmKey := flag.String("llm-key", getEnvOrDefault("LLM_API_KEY", ""), "LLM API key")
	llmModel := flag.String("llm-model", getEnvOrDefault("LLM_MODEL", "llama3.2"), "LLM generation model name")
	llmEmbedModel := flag.String("llm-embed-model", getEnvOrDefault("LLM_EMBED_MODEL", "nomic-embed-text"), "LLM embedding model name")
	sessionID := flag.String("session", getEnvOrDefault("SESSION_ID", "minecraft-agent-01"), "Session ID")
	dataDir := flag.String("data-dir", getEnvOrDefault("DATA_DIR", "data"), "Data directory path")
	botScript := flag.String("bot-script", getEnvOrDefault("BOT_SCRIPT_PATH", "./js/index.ts"), "Path to the compiled TS bot index.js")
	hesitationMs := flag.Int("hesitation-ms", getEnvInt("HESITATION_MS", 180), "Base hesitation delay in milliseconds")
	noiseLevel := flag.Float64("noise-level", getEnvFloat("NOISE_LEVEL", 0.03), "Humanization noise level (0.0-1.0)")
	configPath := flag.String("config", getEnvOrDefault("CONFIG_PATH", "config.json"), "Path to hot-reloadable feature flags JSON")

	flag.Parse()

	return Config{
		HTTPAddr:      *httpAddr,
		EventStoreDB:  *eventsDB,
		MemoryDB:      *memoryDB,
		VectorDB:      *vectorDB,
		UIPath:        *uiPath,
		LLMURL:        *llmURL,
		LLMKey:        *llmKey,
		LLMModel:      *llmModel,
		LLMEmbedModel: *llmEmbedModel,
		SessionID:     *sessionID,
		DataDir:       *dataDir,
		BotScript:     *botScript,
		HesitationMs:  *hesitationMs,
		NoiseLevel:    *noiseLevel,
		ConfigPath:    *configPath,
	}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func handleBotConnection(appCtx context.Context, bus domain.EventBus, cm *execution.ControllerManager, runner *supervisor.NodeRunner, logger *slog.Logger, sessionID string) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			allowedOrigins := map[string]bool{
				"http://localhost:3000": true,
			}
			return allowedOrigins[r.Header.Get("Origin")]
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("Bot WS upgrade failed", slog.Any("error", err))
			return
		}

		controllerID := fmt.Sprintf("ws-%d", time.Now().UnixNano())
		logger.Info("Bot connected", slog.String("remote", conn.RemoteAddr().String()), slog.String("controller_id", controllerID))

		runner.Ping()

		onMessage := func(msgType string, payload []byte) {
			runner.Ping()

			bus.Publish(appCtx, domain.DomainEvent{
				SessionID: sessionID,
				Type:      domain.EventType(msgType),
				Payload:   payload,
				CreatedAt: time.Now(),
			})
		}

		onClose := func() {
			logger.Warn("Bot connection closed", slog.String("controller_id", controllerID))
		}

		botController := execution.NewWSController(conn, logger, onMessage, onClose)
		idempotentController := execution.NewIdempotentController(botController, 1000)

		cm.SetController(controllerID, idempotentController)
	}
}
