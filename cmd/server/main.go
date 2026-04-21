// cmd/server/main.go
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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/anthdm/hollywood/actor"
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
	HTTPAddr       string
	EventStoreDB   string
	MemoryDB       string
	VectorDB       string
	UIPath         string
	LLMURL         string
	LLMKey         string
	LLMStrongModel string
	LLMCheapModel  string
	LLMEmbedModel  string
	SessionID      string
	DataDir        string
	BotScript      string
	HesitationMs   int
	NoiseLevel     float64
	ConfigPath     string
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
		APIURL:      cfg.LLMURL,
		APIKey:      cfg.LLMKey,
		StrongModel: cfg.LLMStrongModel,
		CheapModel:  cfg.LLMCheapModel,
		EmbedModel:  cfg.LLMEmbedModel,
		MaxRetries:  3,
	})

	uiHub := observability.NewHub(logger)

	dynFlags := config.NewDynamicFlags(cfg.ConfigPath, logger)
	initialFlags := dynFlags.Get()

	humanizer := humanization.NewEngine(humanization.MapToHumanizationConfig(initialFlags.Humanization))

	actorEngine, _ := actor.NewEngine(actor.NewEngineConfig())
	eventBus := domain.NewActorEventBus(actorEngine)

	stateSvc := state.NewService(eventBus, memoryStore, logger)
	ctrlManager := execution.NewControllerManagerActor(actorEngine, logger)
	execEngine := execution.NewTaskExecutionEngine(ctrlManager, logger, initialFlags.Execution)
	// Hot-reload loop
	go func() {
		sub := dynFlags.Subscribe()
		for range sub {
			newFlags := dynFlags.Get()
			humanizer.UpdateConfig(humanization.MapToHumanizationConfig(newFlags.Humanization))
			execEngine.UpdateConfig(newFlags.Execution)
			logger.Info("Components updated with new feature flags")
		}
	}()

	taskManager := execution.NewTaskManager(execEngine, ctrlManager, nil, logger)

	execSupervisor := execution.NewExecutionSupervisor(actorEngine, logger, execEngine)
	taskManager.SetDangerProvider(execSupervisor)

	execService := execution.NewControlService(eventBus, execEngine, taskManager, ctrlManager, execSupervisor, humanizer, stateSvc, logger)
	uiHub.SetOrchestrator(execService)

	evaluator := strategy.NewEvaluatorWithLLM(llmClient)
	worldModel := domain.NewWorldModel()

	// Initialize the new SkillManager
	skillManager := voyager.NewSkillManager(vectorStore, llmClient)

	// Inject skillManager into AdvancedPlanner
	plannerObj := decision.NewAdvancedPlanner(llmClient, evaluator, nil, memoryStore, eventStore, worldModel, humanizer, logger, dynFlags.Get(), skillManager)

	var latestStateUpdate atomic.Pointer[map[string]interface{}]

	eventBus.Subscribe(domain.EventTypeStateTick, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		var stateUpdate struct {
			POIs []struct {
				Name string      `json:"name"`
				Type string      `json:"type"`
				Pos  domain.Vec3 `json:"position"`
			} `json:"pois"`
		}
		if err := json.Unmarshal(ev.Payload, &stateUpdate); err != nil {
			logger.Warn("Failed to unmarshal state update POIs", slog.Any("error", err))
			return
		}
		for _, poi := range stateUpdate.POIs {
			_ = memoryStore.MarkWorldNode(ctx, domain.WorldNode{Name: poi.Name, Kind: poi.Type, Pos: poi.Pos})
		}
	}))

	eventBus.Subscribe(domain.EventBotRespawn, domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		eventBus.Publish(ctx, domain.DomainEvent{
			SessionID: ev.SessionID,
			Type:      domain.EventTypePlanInvalidated,
			Payload:   []byte(`{}`),
			CreatedAt: time.Now(),
		})
	}))

	eventWorkerCh := make(chan domain.DomainEvent, 1024)
	criticalWorkerCh := make(chan domain.DomainEvent, 512)
	globalSink := domain.FuncHandler(func(ctx context.Context, ev domain.DomainEvent) {
		if ev.Type == "" {
			return
		}

		isCritical := ev.Type == domain.EventTypeTaskStart ||
			ev.Type == domain.EventTypeTaskEnd ||
			ev.Type == domain.EventTypePlanCreated ||
			ev.Type == domain.EventTypePlanInvalidated ||
			ev.Type == domain.EventTypePlanCompleted ||
			ev.Type == domain.EventTypePlanFailed

		if isCritical {
			select {
			case criticalWorkerCh <- ev:
			case <-time.After(10 * time.Millisecond):
				observability.Metrics.DroppedEvents.Inc()
				slog.Warn("CRITICAL event worker channel full, dropping event",
					slog.String("type", string(ev.Type)),
					slog.String("session", ev.SessionID))
			}
		} else {
			select {
			case eventWorkerCh <- ev:
			default:
				observability.Metrics.DroppedEvents.Inc()
				slog.Warn("Event worker channel full, dropping event",
					slog.String("type", string(ev.Type)),
					slog.String("session", ev.SessionID))
			}
		}
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
	g.Go(func() error {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				observability.Metrics.AddSurvivalTime(1)
			}
		}
	})

	decisionSvc := decision.NewService(actorEngine, plannerObj, eventBus, memoryStore, cfg.SessionID, logger)
	if decisionSvc == nil {
		logger.Error("Failed to initialize decision service")
		os.Exit(1)
	}
	plannerObj.SetOnPlanReady(decisionSvc.RequestEvaluation)
	taskManager.OnDrain = decisionSvc.RequestEvaluation

	processEvent := func(ctx context.Context, ev domain.DomainEvent, eventStore domain.EventStore, logger *slog.Logger, latestStateUpdate *atomic.Pointer[map[string]interface{}], uiHub *observability.Hub) {
		if err := eventStore.Append(ctx, ev.SessionID, ev.Trace, ev.Type, ev.Payload); err != nil {
			logger.Error("Failed to store event", slog.Any("error", err), slog.String("type", string(ev.Type)))
		}

		var payloadObj interface{}
		if err := json.Unmarshal(ev.Payload, &payloadObj); err != nil {
			logger.Error("Failed to unmarshal event payload for UI broadcast",
				slog.Any("error", err),
				slog.String("event_type", string(ev.Type)),
				slog.String("session_id", ev.SessionID))
			return
		}

		broadcastEv := map[string]interface{}{
			"type":      string(ev.Type),
			"payload":   payloadObj,
			"timestamp": ev.CreatedAt.Format(time.Kitchen),
		}

		isState := ev.Type == domain.EventTypeStateUpdated || ev.Type == domain.EventTypeStateTick

		if isState {
			evCopy := broadcastEv
			latestStateUpdate.Store(&evCopy)
		} else {
			uiHub.Broadcast("EVENT_STREAM", broadcastEv)
		}
	}

	const numWorkers = 4
	for i := 0; i < numWorkers; i++ {
		g.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case ev, ok := <-criticalWorkerCh:
					if !ok {
						return nil
					}
					processEvent(ctx, ev, eventStore, logger, &latestStateUpdate, uiHub)
				case ev, ok := <-eventWorkerCh:
					if !ok {
						return nil
					}
					processEvent(ctx, ev, eventStore, logger, &latestStateUpdate, uiHub)
				}
			}
		})
	}

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

	// GET /api/skills/{name} — returns the stored executable skill by name.
	// Used by the TypeScript use_skill handler to fetch compiled JS.
	mux.HandleFunc("/api/skills/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract skill name from path: /api/skills/{name}
		name := strings.TrimPrefix(r.URL.Path, "/api/skills/")
		name = strings.TrimSpace(name)
		if name == "" {
			http.Error(w, "skill name required", http.StatusBadRequest)
			return
		}

		skill, err := vectorStore.RetrieveNamedSkill(r.Context(), name)
		if err != nil {
			http.Error(w, fmt.Sprintf("skill not found: %v", err), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(skill)
	})

	mux.HandleFunc("/ui/ws", uiHub.HandleConnections)
	mux.HandleFunc("/bot/ws", handleBotConnection(ctx, eventBus, ctrlManager, runner, logger, cfg.SessionID))
	mux.Handle("/metrics", promhttp.Handler())

	uiPath := cfg.UIPath
	if !filepath.IsAbs(uiPath) {
		uiPath = filepath.Join(".", uiPath)
	}

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
		plannerObj.SlowReplanLoop(ctx, cfg.SessionID)
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

func serveSPA(dir string) http.HandlerFunc {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	fs := http.FileServer(http.Dir(absDir))

	return func(w http.ResponseWriter, r *http.Request) {
		cleanPath := filepath.Clean(r.URL.Path)
		cleanPath = strings.TrimLeft(cleanPath, `/\`)

		path := filepath.Join(absDir, cleanPath)

		rel, err := filepath.Rel(absDir, path)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			http.ServeFile(w, r, filepath.Join(absDir, "index.html"))
			return
		}

		info, err := os.Stat(path)
		if os.IsNotExist(err) || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(absDir, "index.html"))
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
	defaultEmbed := "nomic-embed-text"
	if strings.Contains(*llmURL, "openrouter.ai") {
		defaultEmbed = "openai/text-embedding-3-small"
	}
	llmStrongModel := flag.String("llm-strong-model", getEnvOrDefault("LLM_STRONG_MODEL", "nvidia/nemotron-3-super-120b-a12b:free"), "LLM strong generation model")
	llmCheapModel := flag.String("llm-cheap-model", getEnvOrDefault("LLM_CHEAP_MODEL", "google/gemini-2.5-flash-lite"), "LLM cheap classification model")
	llmEmbedModel := flag.String("llm-embed-model", getEnvOrDefault("LLM_EMBED_MODEL", defaultEmbed), "LLM embedding model name")

	sessionID := flag.String("session", getEnvOrDefault("SESSION_ID", "minecraft-agent-01"), "Session ID")
	dataDir := flag.String("data-dir", getEnvOrDefault("DATA_DIR", "data"), "Data directory path")
	botScript := flag.String("bot-script", getEnvOrDefault("BOT_SCRIPT_PATH", "./js/index.ts"), "Path to the compiled TS bot index.js")
	hesitationMs := flag.Int("hesitation-ms", getEnvInt("HESITATION_MS", 180), "Base hesitation delay in milliseconds")
	noiseLevel := flag.Float64("noise-level", getEnvFloat("NOISE_LEVEL", 0.03), "Humanization noise level (0.0-1.0)")
	configPath := flag.String("config", getEnvOrDefault("CONFIG_PATH", "config.json"), "Path to hot-reloadable feature flags JSON")

	flag.Parse()

	return Config{
		HTTPAddr:       *httpAddr,
		EventStoreDB:   *eventsDB,
		MemoryDB:       *memoryDB,
		VectorDB:       *vectorDB,
		UIPath:         *uiPath,
		LLMURL:         *llmURL,
		LLMKey:         *llmKey,
		LLMStrongModel: *llmStrongModel,
		LLMCheapModel:  *llmCheapModel,
		LLMEmbedModel:  *llmEmbedModel,
		SessionID:      *sessionID,
		DataDir:        *dataDir,
		BotScript:      *botScript,
		HesitationMs:   *hesitationMs,
		NoiseLevel:     *noiseLevel,
		ConfigPath:     *configPath,
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

func handleBotConnection(appCtx context.Context, evBus domain.EventBus, cm *execution.ControllerManager, runner *supervisor.NodeRunner, logger *slog.Logger, sessionID string) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		CheckOrigin:     func(r *http.Request) bool { return true },
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("Bot WS upgrade failed", slog.Any("error", err))
			return
		}

		logger.Info("Bot connected", slog.String("remote", conn.RemoteAddr().String()))
		runner.Ping()

		onMessage := func(msgType string, payload []byte) {
			runner.Ping()
			normalizedType := domain.NormalizeEventType(msgType)
			cleanPayload := payload
			if len(cleanPayload) == 0 {
				cleanPayload = []byte(`{}`)
			}

			ev := domain.DomainEvent{
				SessionID: sessionID,
				Type:      normalizedType,
				Payload:   cleanPayload,
				CreatedAt: time.Now(),
			}

			evBus.Publish(appCtx, ev)
		}
		cm.RegisterConnection(conn, sessionID, onMessage)
	}
}
