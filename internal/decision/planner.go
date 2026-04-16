// internal/decision/planner.go
package decision

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/humanization"
	"david22573/synaptic-mc/internal/learning"
	"david22573/synaptic-mc/internal/llm"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/observability"
	"david22573/synaptic-mc/internal/strategy"
)

//go:embed prompts/base_rules.tmpl
var baseSystemRules string

type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
	GenerateWithFormat(ctx context.Context, systemPrompt, userContent string, format *llm.ResponseFormat, useStrongModel bool) (string, error)
	GenerateText(ctx context.Context, systemPrompt, userContent string) (string, error)
	CompressState(sessionID string, state domain.GameState, events []domain.DomainEvent) string
	CreateEmbedding(ctx context.Context, input string) ([]float32, error)
}

type RuleExtractor interface {
	GenerateRules(ctx context.Context, sessionID string) string
}

var planResponseFormat = &llm.ResponseFormat{
	Type: "json_schema",
	JSONSchema: &llm.JSONSchema{
		Name:   "multi_candidate_plan",
		Strict: true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"strategic_goal": map[string]any{"type": "string"},
				"subgoals": map[string]any{
					"type": "array",
					"items": map[string]any{"type": "string"},
				},
				"objective": map[string]any{"type": "string"},
				"candidates": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":        map[string]any{"type": "string"},
								"action":    map[string]any{"type": "string"},
								"target":    map[string]any{
									"type": "object",
									"properties": map[string]any{
										"name": map[string]any{"type": "string"},
										"type": map[string]any{"type": "string"},
									},
									"required": []string{"name", "type"},
									"additionalProperties": false,
								},
								"count":     map[string]any{"type": "integer"},
								"priority":  map[string]any{"type": "integer"},
								"rationale": map[string]any{"type": "string"},
							},
							"required": []string{"action", "target", "rationale"},
							"additionalProperties": false,
						},
					},
				},
			},
			"required": []string{"strategic_goal", "subgoals", "objective", "candidates"},
			"additionalProperties": false,
		},
	},
}

type AdvancedPlanner struct {
	client     LLMClient
	evaluator  *strategy.Evaluator
	extractor  RuleExtractor
	memStore   memory.Store
	store      domain.EventStore
	worldModel *domain.WorldModel
	humanizer  *humanization.Engine
	logger     *slog.Logger
	flags      config.FeatureFlags
	skills     domain.SkillRetriever

	currentPlan atomic.Pointer[domain.Plan]
	latestState atomic.Pointer[domain.GameState]
	isStuck     atomic.Bool

	planCache *lru.Cache[string, cachedPlan]
	sf        singleflight.Group

	failures map[string]int
	failMu   sync.Mutex

	replanChan       chan struct{}
	onPlanReady      func()
	milestoneContext string

	cachedRules   string
	rulesCachedAt time.Time
	lastPlannedAt time.Time
	rulesMu       sync.Mutex
}

func (p *AdvancedPlanner) SetMilestoneContext(ctx string) {
	p.milestoneContext = ctx
}

type cachedPlan struct {
	plan      *domain.Plan
	createdAt time.Time
	cacheKey  string
}

const maxPlanCacheSize = 500

func NewAdvancedPlanner(
	client LLMClient,
	evaluator *strategy.Evaluator,
	extractor RuleExtractor,
	memStore memory.Store,
	store domain.EventStore,
	worldModel *domain.WorldModel,
	humanizer *humanization.Engine,
	logger *slog.Logger,
	flags config.FeatureFlags,
	skills domain.SkillRetriever,
) *AdvancedPlanner {
	cache, err := lru.New[string, cachedPlan](maxPlanCacheSize)
	if err != nil {
		// Should not happen with positive size
		panic(err)
	}

	return &AdvancedPlanner{
		client:     client,
		evaluator:  evaluator,
		extractor:  extractor,
		memStore:   memStore,
		store:      store,
		worldModel: worldModel,
		humanizer:  humanizer,
		logger:     logger.With(slog.String("component", "advanced_planner")),
		flags:      flags,
		skills:     skills,
		planCache:  cache,
		failures:   make(map[string]int),
		replanChan: make(chan struct{}, 1),
	}
}

type multiCandidateResponse struct {
	StrategicGoal string            `json:"strategic_goal"`
	Subgoals      []string          `json:"subgoals"`
	Objective     string            `json:"objective"`
	Candidates    [][]domain.Action `json:"candidates"`
}

func hashState(state domain.GameState) uint64 {
	h := fnv.New64a()

	// Semantic Bucketing for Cache Stability
	healthBucket := int(state.Health) / 4 // 0-4, 5-8, etc.
	foodBucket := int(state.Food) / 4
	threatBucket := len(state.Threats)
	if threatBucket > 3 {
		threatBucket = 3 // Cap threat bucket
	}
	
	// Significant position change (approx chunk level)
	chunkX := int(state.Position.X) / 16
	chunkZ := int(state.Position.Z) / 16

	str := fmt.Sprintf("h:%d|f:%d|t:%d|cx:%d|cz:%d", healthBucket, foodBucket, threatBucket, chunkX, chunkZ)
	h.Write([]byte(str))

	// Inventory stage (important items only for hashing)
	var items []string
	for _, item := range state.Inventory {
		if item.Count > 0 && (strings.Contains(item.Name, "log") || strings.Contains(item.Name, "pickaxe") || strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "food")) {
			items = append(items, item.Name)
		}
	}
	sort.Strings(items)
	for _, item := range items {
		h.Write([]byte(fmt.Sprintf("|%s", item)))
	}

	return h.Sum64()
}

func (p *AdvancedPlanner) stateChangedSignificantly(prev, curr domain.GameState) bool {
	if !prev.Initialized {
		return true
	}

	// Optimization: Lower polling rate to every 10 seconds unless massive health drop
	if time.Since(p.lastPlannedAt) < 10*time.Second && curr.Health > prev.Health-4 {
		return false
	}

	dx := prev.Position.X - curr.Position.X
	dy := prev.Position.Y - curr.Position.Y
	dz := prev.Position.Z - curr.Position.Z
	dist := math.Sqrt(dx*dx + dy*dy + dz*dz)

	return dist > 25.0 || prev.Health != curr.Health || prev.Food != curr.Food
}

func (p *AdvancedPlanner) Close() {
	// Let the GC handle replanChan to avoid panics from late concurrent writers.
}

func (p *AdvancedPlanner) SetOnPlanReady(cb func()) {
	p.onPlanReady = cb
}

func (p *AdvancedPlanner) SetFailures(failures map[string]int) {
	p.failMu.Lock()
	defer p.failMu.Unlock()
	p.failures = failures
}

func (p *AdvancedPlanner) RecordFailure(objective string) {
	p.failMu.Lock()
	p.failures[objective]++
	p.failMu.Unlock()
}

func (p *AdvancedPlanner) RecordSuccess(objective string) {
	p.failMu.Lock()
	delete(p.failures, objective)
	p.failMu.Unlock()
}

func (p *AdvancedPlanner) GetFailureCount(objective string) int {
	p.failMu.Lock()
	defer p.failMu.Unlock()
	return p.failures[objective]
}

func (p *AdvancedPlanner) ClearCurrentPlan() {
	p.currentPlan.Store(nil)
}

func (p *AdvancedPlanner) SlowReplanLoop(ctx context.Context, sessionID string) {
	var activeState *domain.GameState
	var lastPlannedState domain.GameState

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.replanChan:
			if s := p.latestState.Swap(nil); s != nil {
				activeState = s
			}

			if activeState == nil {
				continue
			}

			if !activeState.Initialized {
				continue
			}

			needsReplan := p.currentPlan.Load() == nil || p.stateChangedSignificantly(lastPlannedState, *activeState)

			if !needsReplan {
				continue
			}

			plan, err := p.generateLLMPlan(ctx, sessionID, *activeState)
			if err != nil {
				p.logger.Error("Background LLM planning failed", slog.Any("error", err))
				continue
			}

			p.currentPlan.Store(plan)
			lastPlannedState = *activeState
			p.logger.Info("Background replan complete",
				slog.String("strategic_goal", plan.StrategicGoal),
				slog.Any("subgoals", plan.Subgoals),
				slog.String("objective", plan.Objective))
			if p.onPlanReady != nil {
				go p.onPlanReady()
			}
		}
	}
}

func (p *AdvancedPlanner) TriggerReplan(state domain.GameState) {
	p.latestState.Store(&state)

	select {
	case p.replanChan <- struct{}{}:
	default:
	}
}

func (p *AdvancedPlanner) FastPlan(ctx context.Context, state domain.GameState) domain.Plan {
	current := p.currentPlan.Load()

	if current != nil {
		p.failMu.Lock()
		fails := p.failures[current.Objective]
		p.failMu.Unlock()

		if fails > 3 {
			p.logger.Warn("Plan stuck in failure loop, degrading behavior", slog.String("objective", current.Objective), slog.Int("failures", fails))
			p.currentPlan.Store(nil)
			return p.degradedPlan(state, fails)
		}
	} else {
		// Even if no LLM plan is active, check if the reactive fallback itself is failing
		p.failMu.Lock()
		fails := p.failures["Reactive Fallback Plan"]
		p.failMu.Unlock()

		if fails > 3 {
			p.logger.Warn("Reactive fallback stuck in failure loop, degrading behavior", slog.Int("failures", fails))
			return p.degradedPlan(state, fails)
		}
	}

	if current != nil && len(current.Tasks) > 0 {
		return *current
	}
	return p.reactivePlan(ctx, state)
}

func (p *AdvancedPlanner) degradedPlan(state domain.GameState, failureCount int) domain.Plan {
	return domain.Plan{
		Objective: "Degraded Recovery state",
		Tasks: []domain.Action{
			{
				ID:        fmt.Sprintf("recover-walk-%d", time.Now().UnixNano()),
				Action:    "random_walk",
				Target:    domain.Target{Name: "none", Type: "none"},
				Priority:  50,
				Rationale: fmt.Sprintf("System stuck in loop (%d consecutive failures): falling back to random walk to break deadlock", failureCount),
			},
		},
	}
}

func (p *AdvancedPlanner) reactivePlan(ctx context.Context, state domain.GameState) domain.Plan {
	var tasks []domain.Action
	hasFood := false
	hasImmediateThreat := false

	nearbyFoodEntity := ""
	nearbyFoodBlock := ""

	for _, poi := range state.POIs {
		if isFoodEntity(poi.Name) {
			nearbyFoodEntity = poi.Name
		} else if isFoodBlock(poi.Name) {
			nearbyFoodBlock = poi.Name
		}
	}

	for _, item := range state.Inventory {
		if item.Count > 0 && domain.IsFood(item.Name) {
			hasFood = true
			break
		}
	}

	for _, threat := range state.Threats {
		if threat.Distance <= domain.SurvivalMaxThreatDist {
			hasImmediateThreat = true
			break
		}
	}

	// A-5: Steer toward safe zones and known POIs
	currentZoneCost := 0.0
	if p.worldModel != nil {
		currentZoneCost = p.worldModel.GetZoneCost(domain.Location{X: state.Position.X, Y: state.Position.Y, Z: state.Position.Z})
	}

	knownNodes := []domain.WorldNode{}
	if p.memStore != nil {
		knownNodes, _ = p.memStore.GetNearbyNodes(ctx, state.Position, 5)
	}

	// Action Weights for priority adjustment
	getWeight := func(action string) float64 {
		if p.worldModel == nil {
			return 0
		}
		return p.worldModel.GetActionWeight(action)
	}

	switch {
	case hasImmediateThreat || currentZoneCost > 2.0:
		action := "retreat"
		weight := getWeight(action)
		priority := int(100 + (weight * 5))
		
		// If current zone is bad, but we have no immediate threat, "explore" might be better to find a better zone
		if !hasImmediateThreat && currentZoneCost > 2.0 {
			action = "explore"
		}

		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-heal-%d", time.Now().UnixNano()),
			Action:    action,
			Target:    domain.Target{Name: "none", Type: "none"},
			Priority:  priority,
			Rationale: fmt.Sprintf("Reactive fast-path fallback: Survival Critical (Zone Cost: %.1f)", currentZoneCost),
		})
	case (state.Health < domain.DecisionHealthSafe || state.Food < 14.0) && hasFood:
		action := "eat"
		weight := getWeight(action)
		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-eat-%d", time.Now().UnixNano()),
			Action:    action,
			Target:    domain.Target{Name: "best_food", Type: "item"},
			Priority:  int(95 + (weight * 5)),
			Rationale: "Reactive fast-path fallback: Recover health/food with available items",
		})
	case state.Health < domain.SurvivalCriticalHealth || state.Food < 12.0:
		// Prioritize actions that have been successful
		huntWeight := getWeight("hunt")
		gatherWeight := getWeight("gather")
		exploreWeight := getWeight("explore")

		if nearbyFoodEntity != "" && state.Health > 8.0 && huntWeight >= -1.0 {
			tasks = append(tasks, domain.Action{
				ID:        fmt.Sprintf("react-hunt-%d", time.Now().UnixNano()),
				Action:    "hunt",
				Target:    domain.Target{Name: nearbyFoodEntity, Type: "entity"},
				Priority:  int(90 + (huntWeight * 5)),
				Rationale: "Reactive fast-path: Hunt nearby food source",
			})
		} else if nearbyFoodBlock != "" && gatherWeight >= -1.0 {
			tasks = append(tasks, domain.Action{
				ID:        fmt.Sprintf("react-gather-%d", time.Now().UnixNano()),
				Action:    "gather",
				Target:    domain.Target{Name: nearbyFoodBlock, Type: "block"},
				Priority:  int(90 + (gatherWeight * 5)),
				Rationale: "Reactive fast-path: Gather nearby berry bush",
			})
		} else {
			tasks = append(tasks, domain.Action{
				ID:        fmt.Sprintf("react-food-%d", time.Now().UnixNano()),
				Action:    "explore",
				Target:    domain.Target{Name: "food_source", Type: "category"},
				Priority:  int(85 + (exploreWeight * 5)),
				Rationale: "Reactive fast-path fallback: Seek food or resources",
			})
		}
	}

	// Use known POIs if we are stuck or idle
	if len(tasks) == 0 {
		for _, node := range knownNodes {
			if node.Kind == "chest" && state.Food < 15.0 {
				tasks = append(tasks, domain.Action{
					ID:        fmt.Sprintf("react-poi-%d", time.Now().UnixNano()),
					Action:    "retrieve",
					Target:    domain.Target{Name: "food", Type: "item"},
					Priority:  40,
					Rationale: fmt.Sprintf("Stable state: retrieving food from known %s", node.Name),
				})
				break
			}
		}
	}

	if len(tasks) == 0 {
		exploreWeight := getWeight("explore")
		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-explore-%d", time.Now().UnixNano()),
			Action:    "explore",
			Target:    domain.Target{Name: "surroundings", Type: "category"},
			Priority:  int(5 + (exploreWeight * 2)),
			Rationale: "Reactive fast-path: Stable state, exploring surroundings while waiting for next plan",
		})
	}

	return domain.Plan{
		StrategicGoal: "Survival & Stabilization",
		Subgoals:      []string{"React to immediate threats", "Recover vital stats", "Maintain situational awareness"},
		Objective:     "Reactive Fallback Plan",
		Tasks:         tasks,
	}
}

func isFoodEntity(name string) bool {
	foods := []string{"pig", "cow", "sheep", "chicken", "rabbit"}
	for _, f := range foods {
		if strings.Contains(strings.ToLower(name), f) {
			return true
		}
	}
	return false
}

func isFoodBlock(name string) bool {
	foods := []string{"sweet_berry_bush"}
	for _, f := range foods {
		if strings.Contains(strings.ToLower(name), f) {
			return true
		}
	}
	return false
}

func (p *AdvancedPlanner) generateLLMPlan(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error) {
	start := time.Now()
	p.lastPlannedAt = start
	observability.Metrics.IncReplan()

	lastEventID, err := p.store.GetLastEventID(ctx, sessionID)
	if err != nil {
		p.logger.Warn("Failed to get last event ID, using 0", slog.Any("error", err))
		lastEventID = 0
	}

	stateHash := hashState(state)
	cacheKey := fmt.Sprintf("%s:%d:%d", sessionID, lastEventID/10, stateHash)
	sfKey := cacheKey

	if entry, ok := p.planCache.Get(cacheKey); ok {
		if time.Since(entry.createdAt) > 30*time.Second {
			p.planCache.Remove(cacheKey)
			goto cacheMiss
		}

		cachedPlan := entry.plan
		p.failMu.Lock()
		fails := p.failures[cachedPlan.Objective]
		p.failMu.Unlock()

		if fails == 0 {
			p.logger.Info("Using cached plan for stable state", slog.String("objective", cachedPlan.Objective))
			observability.Metrics.PlannerDuration.Observe(float64(time.Since(start).Milliseconds()))
			return p.clonePlanWithNewIDs(cachedPlan), nil
		}
	}

cacheMiss:

	val, err, _ := p.sf.Do(sfKey, func() (interface{}, error) {
		if entry, ok := p.planCache.Get(cacheKey); ok {
			return entry.plan, nil
		}

		directive := p.evaluator.Evaluate(state)
		learnedRules := ""

		if p.extractor != nil {
			p.rulesMu.Lock()
			if p.cachedRules == "" || time.Since(p.rulesCachedAt) > 5*time.Minute {
				p.cachedRules = p.extractor.GenerateRules(ctx, sessionID)
				p.rulesCachedAt = time.Now()
			}
			learnedRules = p.cachedRules
			p.rulesMu.Unlock()
		}

		knownWorld := "KNOWN WORLD: empty"
		longTermMem := "No active summary."
		tacticalFeedback := ""
		if p.memStore != nil {
			var err error
			knownWorld, err = p.memStore.GetKnownWorld(ctx, state.Position)
			if err != nil {
				p.logger.Warn("Failed to get known world", slog.Any("error", err))
			}
			longTermMem, err = p.memStore.GetSummary(ctx, sessionID)
			if err != nil {
				p.logger.Warn("Failed to get long-term memory", slog.Any("error", err))
			}
		}

		if p.worldModel != nil {
			tacticalFeedback = p.worldModel.GetTacticalFeedback()
		}

		availableSkillsDocs := "AVAILABLE SKILLS: none"
		if p.skills != nil {
			query := fmt.Sprintf("Goal: %s, Secondary: %s, Health: %.0f, Food: %.0f", directive.PrimaryGoal, directive.SecondaryGoal, state.Health, state.Food)
			retrieved, err := p.skills.RetrieveSkills(ctx, query, 5)
			if err == nil && len(retrieved) > 0 {
				var sb strings.Builder
				sb.WriteString("AVAILABLE SKILLS:\n")
				for _, sk := range retrieved {
					sb.WriteString(fmt.Sprintf("- ID: %s | Desc: %s\n", sk.Name, sk.Description))
				}
				availableSkillsDocs = sb.String()
			}
		}

		// PREFIX CACHING OPTIMIZATION:
		// Put static/long-lived data first so the provider can cache the top of the prompt.
		// baseSystemRules, milestoneContext, availableSkillsDocs are relatively static.
		systemPrompt := fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s\n\n%s\n\nLONG_TERM_MEMORY:\n%s\n\n%s\n\nPRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\nAll tasks MUST align with these strategies.",
			baseSystemRules,
			availableSkillsDocs,
			p.milestoneContext,
			learnedRules,
			tacticalFeedback,
			longTermMem,
			knownWorld,
			directive.PrimaryGoal,
			directive.SecondaryGoal,
		)

		userContent := domain.FormatStateForLLM(state)

		stats, statsLastID, err := learning.GetProjectedStats(ctx, p.store, sessionID, p.logger)
		if err != nil {
			p.logger.Warn("Failed to get projected stats, using empty", slog.Any("error", err))
			stats = make(map[string]*learning.ActionStats)
		}

		if statsLastID > 0 && statsLastID%100 == 0 {
			go func(sid string, lid int64, s map[string]*learning.ActionStats) {
				data, _ := json.Marshal(s)
				_ = p.store.SaveSnapshot(context.Background(), domain.SessionSnapshot{
					SessionID:   sid,
					LastEventID: lid,
					Data:        data,
				})
			}(sessionID, statsLastID, stats)
		}

		events, _ := p.store.GetRecentStream(ctx, sessionID, 100)
		historySummary := p.client.CompressState(sessionID, state, events)
		userContent = fmt.Sprintf("%s\n\nEXECUTION_HISTORY_SUMMARY:\n%s", userContent, historySummary)

		rawResponse, err := p.client.GenerateWithFormat(ctx, systemPrompt, userContent, planResponseFormat, true)
		if err != nil {
			return nil, fmt.Errorf("llm api failure: %w", err)
		}

		var parsed multiCandidateResponse
		if err := json.Unmarshal([]byte(domain.CleanJSON(rawResponse)), &parsed); err != nil {
			p.logger.Error("LLM hallucinated bad JSON, using hardcoded fallback", 
				slog.String("raw", rawResponse),
				slog.Any("error", err))
			
			// Return a safe emergency fallback plan instead of an error
			return &domain.Plan{
				ID:            fmt.Sprintf("hallucination-fb-%d", time.Now().UnixNano()),
				StrategicGoal: "Emergency Stabilization",
				Objective:     "LLM Parse Failure Recovery",
				Tasks: []domain.Action{{
					ID:        fmt.Sprintf("panic-retreat-%d", time.Now().UnixNano()),
					Action:    "retreat",
					Target:    domain.Target{Name: "safe_zone", Type: "none"},
					Priority:  100,
					Rationale: "LLM output was invalid JSON; forcing mechanical escape.",
				}},
			}, nil
		}

		if len(parsed.Candidates) == 0 {
			return nil, fmt.Errorf("planner returned zero candidates")
		}

		now := time.Now().UnixNano()
		for i := range parsed.Candidates {
			for j := range parsed.Candidates[i] {
				if parsed.Candidates[i][j].ID == "" {
					parsed.Candidates[i][j].ID = fmt.Sprintf("plan-%d-c%d-t%d", now, i, j)
				}
			}
		}

		p.failMu.Lock()
		failureCount := p.failures[parsed.Objective]
		p.failMu.Unlock()

		var scored []struct {
			tasks []domain.Action
			score float64
		}
		for _, candidate := range parsed.Candidates {
			score := p.scoreCandidate(ctx, candidate, state, stats, failureCount)
			scored = append(scored, struct {
				tasks []domain.Action
				score float64
			}{candidate, score})
		}

		sort.Slice(scored, func(i, j int) bool {
			return scored[i].score > scored[j].score
		})

		finalPlan := &domain.Plan{
			ID:            fmt.Sprintf("plan-%d", time.Now().UnixNano()),
			StrategicGoal: parsed.StrategicGoal,
			Subgoals:      parsed.Subgoals,
			Objective:     parsed.Objective,
			Tasks:         scored[0].tasks,
		}

		for i := 1; i < len(scored); i++ {
			finalPlan.Fallbacks = append(finalPlan.Fallbacks, scored[i].tasks)
		}

		p.planCache.Add(cacheKey, cachedPlan{
			plan:      finalPlan,
			createdAt: time.Now(),
			cacheKey:  cacheKey,
		})

		return finalPlan, nil
	})

	if err != nil {
		return nil, err
	}

	observability.Metrics.PlannerDuration.Observe(float64(time.Since(start).Milliseconds()))
	return p.clonePlanWithNewIDs(val.(*domain.Plan)), nil
}

func (p *AdvancedPlanner) WarmNextPlan(ctx context.Context, sessionID string, state domain.GameState) {
	go func() {
		_, _ = p.generateLLMPlan(ctx, sessionID, state)
	}()
}

func (p *AdvancedPlanner) clonePlanWithNewIDs(original *domain.Plan) *domain.Plan {
	clone := &domain.Plan{
		ID:            fmt.Sprintf("cached-plan-%d", time.Now().UnixNano()),
		StrategicGoal: original.StrategicGoal,
		Subgoals:      original.Subgoals,
		Objective:     original.Objective,
		Tasks:         make([]domain.Action, len(original.Tasks)),
	}

	now := time.Now().UnixNano()
	for i, t := range original.Tasks {
		clone.Tasks[i] = t
		clone.Tasks[i].ID = fmt.Sprintf("cached-plan-%d-t%d", now, i)
	}

	for fIdx, fallback := range original.Fallbacks {
		newFallback := make([]domain.Action, len(fallback))
		for i, t := range fallback {
			newFallback[i] = t
			newFallback[i].ID = fmt.Sprintf("cached-plan-fb-%d-f%d-t%d", now, fIdx, i)
		}
		clone.Fallbacks = append(clone.Fallbacks, newFallback)
	}

	return clone
}

func (p *AdvancedPlanner) scoreCandidate(ctx context.Context, tasks []domain.Action, state domain.GameState, stats map[string]*learning.ActionStats, failureCount int) float64 {
	if len(tasks) == 0 {
		return 0
	}

	var totalReward float64
	var totalUnlock float64
	var totalRisk float64
	var totalCost float64
	urgency := p.calculateUrgency(state)

	for _, t := range tasks {
		reward, unlock := domain.GetItemUtility(t.Target.Name)
		totalReward += reward
		totalUnlock += unlock

		actionRisk := 0.0
		if t.Action == "hunt" {
			actionRisk = 20.0
		} else if t.Action == "mine" {
			actionRisk = 10.0
		}
		
		if p.worldModel != nil {
			zoneCost := p.worldModel.GetZoneCost(domain.Location{X: state.Position.X, Y: state.Position.Y, Z: state.Position.Z})
			actionRisk += zoneCost * 5.0
		}
		totalRisk += actionRisk

		totalCost += p.estimateDistanceCost(ctx, t, state)
		
		routeScore := p.scoreRoute(ctx, state.Position, t, state)
		totalReward += routeScore * 0.1 
	}

	penalty := math.Min(float64(failureCount)*25.0, 80.0)
	score := totalReward + totalUnlock - totalRisk - totalCost + urgency - penalty

	if score < 0 {
		score = 0
	}

	return score
}

func (p *AdvancedPlanner) scoreRoute(ctx context.Context, from domain.Vec3, action domain.Action, state domain.GameState) float64 {
	targetPos := from 
	if p.memStore != nil {
		nodes, _ := p.memStore.GetNearbyNodes(ctx, from, 5)
		for _, n := range nodes {
			if strings.Contains(strings.ToLower(n.Name), strings.ToLower(action.Target.Name)) {
				targetPos = n.Pos
				break
			}
		}
	}

	dist := from.DistanceTo(targetPos)
	cost := 0.0
	if p.worldModel != nil {
		cost = p.worldModel.GetZoneCost(domain.Location{X: targetPos.X, Y: targetPos.Y, Z: targetPos.Z})
	}
	
	return 100.0 - (dist * 0.1) - (cost * 20.0)
}

func (p *AdvancedPlanner) calculateUrgency(state domain.GameState) float64 {
	urgency := 0.0
	if state.Health < 10 {
		urgency += (10 - state.Health) * 10.0
	}
	if state.Food < 10 {
		urgency += (10 - state.Food) * 5.0
	}
	if len(state.Threats) > 0 {
		urgency += 20.0
	}
	return urgency
}

func (p *AdvancedPlanner) estimateDistanceCost(ctx context.Context, action domain.Action, state domain.GameState) float64 {
	if p.memStore != nil && action.Target.Name != "" {
		nodes, _ := p.memStore.GetNearbyNodes(ctx, state.Position, 10)
		for _, n := range nodes {
			if strings.Contains(strings.ToLower(n.Name), strings.ToLower(action.Target.Name)) {
				dx, dy, dz := n.Pos.X-state.Position.X, n.Pos.Y-state.Position.Y, n.Pos.Z-state.Position.Z
				dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
				return dist * 0.1 
			}
		}
	}
	return 5.0
}
