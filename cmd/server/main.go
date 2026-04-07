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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/decision"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/eventstore"
	"david22573/synaptic-mc/internal/execution"
	"david22573/synaptic-mc/internal/humanization"
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

	eventBus := domain.NewEventBus()
	uiHub := observability.NewHub(logger)

	humanCfg := humanization.Config{
		AttentionDecay: 0.08,
		HesitationBase: time.Duration(cfg.HesitationMs) * time.Millisecond,
		NoiseLevel:     cfg.NoiseLevel,
		BaseDriftRate:  0.08,
		MaxDriftDelay:  2 * time.Second,
	}
	humanizer := humanization.NewEngine(humanCfg)

	stateSvc := state.NewService(eventBus, logger)
	ctrlManager := execution.NewControllerManager()
	execEngine := execution.NewTaskExecutionEngine(ctrlManager, logger)
	taskManager := execution.NewTaskManager(execEngine, ctrlManager, nil, logger)
	execService := execution.NewControlService(eventBus, execEngine, taskManager, ctrlManager, humanizer, stateSvc, logger)

	uiHub.SetOrchestrator(execService)

	evaluator := strategy.NewEvaluator()
	critic := voyager.NewStateCritic()
	curriculum := voyager.NewAutonomousCurriculum(llmClient, vectorStore, memoryStore)

	dynFlags := config.NewDynamicFlags(cfg.ConfigPath, logger)

	planner := decision.NewAdvancedPlanner(llmClient, evaluator, critic, memoryStore, eventStore, logger, dynFlags.Get())
	planManager := decision.NewPlanManager()
	worldModel := domain.NewWorldModel()

	decisionSvc := decision.NewService(cfg.SessionID, eventBus, planner, planManager, curriculum, critic, stateSvc, worldModel, logger)
	if decisionSvc == nil {
		logger.Error("Failed to initialize decision service")
		os.Exit(1)
	}
	planner.SetOnPlanReady(decisionSvc.RequestEvaluation)
	taskManager.OnDrain = decisionSvc.RequestEvaluation

	// Phase 6: Throttled UI state updates to reduce OBS Browser Source CPU usage
	var latestStateUpdate atomic.Pointer[map[string]interface{}]

	eventBus.Subscribe(domain.EventTypeStateUpdated, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		var stateUpdate struct {
			POIs []struct {
				Name string      `json:"name"`
				Type string      `json:"type"`
				Pos  domain.Vec3 `json:"position"`
			} `json:"pois"`
		}
		if err := json.Unmarshal(ev.Payload, &stateUpdate); err == nil {
			for _, poi := range stateUpdate.POIs {
				_ = memoryStore.MarkWorldNode(ctx, poi.Name, poi.Type, poi.Pos)
			}
		}
	}))

	eventBus.Subscribe(domain.EventBotRespawn, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		eventBus.Publish(ctx, domain.DomainEvent{
			SessionID: ev.SessionID,
			Type:      domain.EventTypePlanInvalidated,
			CreatedAt: time.Now(),
		})
	}))

	globalSink := domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		if ev.Type == "" {
			return
		}

		// Phase 6: Non-blocking fan-out to prevent event bus bottleneck

		// 1. Persist to SQLite (Async)
		go func(e domain.DomainEvent) {
			_ = eventStore.Append(context.Background(), e.SessionID, e.Trace, e.Type, e.Payload)
		}(ev)

		// 2. UI Broadcasting (Async)
		go func(e domain.DomainEvent) {
			var payloadObj interface{}
			if err := json.Unmarshal(e.Payload, &payloadObj); err != nil {
				return
			}

			// BroadcastEv structure
			broadcastEv := map[string]interface{}{
				"type":      string(e.Type),
				"payload":   payloadObj,
				"timestamp": e.CreatedAt.Format(time.Kitchen),
			}

			// Identify if this is a high-frequency state update or a discrete event
			isState := e.Type == domain.EventTypeStateUpdated || e.Type == domain.EventTypeStateTick

			if isState {
				// Phase 6: Throttled UI state updates to reduce OBS Browser Source CPU usage
				latestStateUpdate.Store(&broadcastEv)
			} else {
				// Send to the log/sidebar stream
				uiHub.Broadcast("EVENT_STREAM", broadcastEv)
			}
		}(ev)
	})
	eventTypes := []domain.EventType{
		domain.EventTypeStateTick, domain.EventTypeStateUpdated,
		domain.EventTypeTaskStart, domain.EventTypeTaskEnd,
		domain.EventTypePlanCreated, domain.EventTypePlanInvalidated,
		domain.EventTypePlanCompleted, domain.EventTypePlanFailed,
		domain.EventBotDeath, domain.EventBotRespawn,
	}

	for _, et := range eventTypes {
		eventBus.Subscribe(et, globalSink)
	}

	runner := supervisor.NewNodeRunner(cfg.BotScript, logger)

	rootCtx, cancelRoot := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelRoot()

	g, ctx := errgroup.WithContext(rootCtx)

	// Phase 6: Throttled UI state broadcast (10 FPS)
	g.Go(func() error {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if state := latestStateUpdate.Swap(nil); state != nil {
					uiHub.Broadcast("STATE_UPDATE", *state)
				}
			}
		}
	})

	mux := http.NewServeMux()

	// FIX: Dynamically construct the WebSocket host based on the client's HTTP request
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		scheme := "ws://"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "wss://"
		}
		host := r.Host
		if host == "" {
			host = "localhost:8080"
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]any{
			"ws_url":        fmt.Sprintf("%s%s/ui/ws", scheme, host),
			"bot_ws_url":    fmt.Sprintf("%s%s/bot/ws", scheme, host),
			"viewer_port":   3000,
			"enable_viewer": true,
		})
	})

	mux.HandleFunc("/ui/ws", uiHub.HandleConnections)
	mux.HandleFunc("/bot/ws", handleBotConnection(ctx, eventBus, ctrlManager, runner, logger, cfg.SessionID))
	mux.Handle("/metrics", promhttp.Handler())

	uiPath := cfg.UIPath
	if !filepath.IsAbs(uiPath) {
		uiPath = filepath.Join(".", uiPath)
	}

	// FIX: Serve the SPA correctly, falling back to index.html for unknown routes
	mux.Handle("/", serveSPA(uiPath))

	server := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

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
		err := server.ListenAndServe()
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	})

	<-ctx.Done()
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

// serveSPA wraps the standard file server to redirect 404s to index.html for Svelte routing
func serveSPA(dir string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(dir))
	return func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean(r.URL.Path))
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	}
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
			return true
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
			// Phase 6: Non-blocking ingestion to prevent WebSocket thread from stalling
			go func(mt string, p []byte) {
				runner.Ping()
				normalizedType := domain.NormalizeEventType(mt)
				cleanPayload := p
				if len(cleanPayload) == 0 {
					cleanPayload = []byte(`{}`)
				}

				bus.Publish(appCtx, domain.DomainEvent{
					SessionID: sessionID,
					Type:      normalizedType,
					Payload:   cleanPayload,
					CreatedAt: time.Now(),
				})
			}(msgType, payload)
		}
		onClose := func() {
			logger.Warn("Bot connection closed", slog.String("controller_id", controllerID))
			cm.RemoveController(controllerID)
		}
		botController := execution.NewWSController(appCtx, conn, logger, onMessage, onClose)		idempotentController := execution.NewIdempotentController(botController, 1000)

		cm.SetController(controllerID, idempotentController)
	}
}
