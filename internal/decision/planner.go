// internal/decision/planner.go
package decision

import (
	"context"
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

	"golang.org/x/sync/singleflight"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/humanization"
	"david22573/synaptic-mc/internal/learning"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/strategy"
)

type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
	CompressState(state domain.GameState, events []domain.DomainEvent) string
	CreateEmbedding(ctx context.Context, input string) ([]float32, error)
}

type RuleExtractor interface {
	GenerateRules(ctx context.Context, sessionID string) string
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

	planCache map[string]cachedPlan
	cacheMu   sync.RWMutex
	sf        singleflight.Group

	failures map[string]int
	failMu   sync.Mutex

	replanChan       chan struct{}
	onPlanReady      func()
	milestoneContext string
}

func (p *AdvancedPlanner) SetMilestoneContext(ctx string) {
	p.milestoneContext = ctx
}

type cachedPlan struct {
	plan      *domain.Plan
	createdAt time.Time
	lastUsed  time.Time
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
		planCache:  make(map[string]cachedPlan),
		failures:   make(map[string]int),
		replanChan: make(chan struct{}, 1),
	}
}

const BaseSystemRules = `You are the tactical commander of an autonomous Minecraft agent.
CRITICAL GAME MECHANIC RULES:
1.  Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
2.  You CANNOT gather stone or coal without a wooden_pickaxe.
3.  Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM per candidate.
4.  SURVIVAL: You CANNOT 'eat' if your inventory has no food.
    You CANNOT 'hunt' if health is under 12.
5.  CRAFTING RECIPES:
      - oak_planks: requires 1 oak_log (yields 4)
      - stick: requires 2 oak_planks (yields 4)
      - crafting_table: requires 4 oak_planks
      - wooden_pickaxe: requires 3 oak_planks + 2 stick + MUST HAVE crafting_table in inventory
      - stone_pickaxe: requires 3 cobblestone + 2 stick + MUST HAVE crafting_table in inventory
6.  If you lack prerequisites for an item, your tasks MUST include gathering/crafting those first.
7.  SKILL UTILIZATION: If a relevant skill exists in AVAILABLE SKILLS, you MUST use it instead of raw actions. 
    Set the action field to "use_skill" and the target.name field to the exact Skill ID.

VALID TARGET TYPES: "block", "entity", "recipe", "location", "category", "none", "skill".
VALID ACTIONS: gather, craft, hunt, explore, build, smelt, mine, farm, mark_location, recall_location, idle, sleep, retreat, eat, use_skill.
OUTPUT REQUIREMENT (ADVANCED PLANNING):
    You must generate ONE high-level objective, and exactly 2 to 3 DIFFERENT candidate task sequences to achieve that objective.
    Make candidate 1 the most direct route, and candidate 2/3 alternative or safer routes.
Response format (JSON only):
    {
    "objective": "Sub-goal description",
    "candidates": [
    [
    { "id": "task-1", "action": "use_skill", "target": { "type": "skill", "name": "safe_woodcut_v1" }, "rationale": "Leverage known skill for gathering" }
    ],
    [
    { "id": "task-2", "action": "explore", "target": { "type": "location", "name": "forest" }, "rationale": "Find a safer forest first" },
    { "id": "task-3", "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Gather wood manually if skill fails" }
    ]
    ]
    }`

type multiCandidateResponse struct {
	Objective  string            `json:"objective"`
	Candidates [][]domain.Action `json:"candidates"`
}

func hashState(state domain.GameState) uint64 {
	h := fnv.New64a()

	healthBucket := int(state.Health) / 4
	foodBucket := int(state.Food) / 4
	threatLevel := len(state.Threats)
	chunkX := int(state.Position.X) >> 4
	chunkZ := int(state.Position.Z) >> 4

	str := fmt.Sprintf("h:%d|f:%d|t:%d|cx:%d|cz:%d", healthBucket, foodBucket, threatLevel, chunkX, chunkZ)
	h.Write([]byte(str))

	var items []string
	for _, item := range state.Inventory {
		if item.Count > 0 {
			items = append(items, fmt.Sprintf("%s:%d", item.Name, item.Count))
		}
	}
	sort.Strings(items)
	for _, item := range items {
		h.Write([]byte(fmt.Sprintf("|%s", item)))
	}

	return h.Sum64()
}

func (p *AdvancedPlanner) stateChangedSignificantly(prev, curr domain.GameState) bool {
	if prev.Health == 0 {
		return true
	}

	dx := prev.Position.X - curr.Position.X
	dy := prev.Position.Y - curr.Position.Y
	dz := prev.Position.Z - curr.Position.Z
	dist := math.Sqrt(dx*dx + dy*dy + dz*dz)

	return dist > 2.0 || prev.Health != curr.Health || prev.Food != curr.Food
}

func (p *AdvancedPlanner) Close() {
	// Let the GC handle replanChan to avoid panics from late concurrent writers.
}

func (p *AdvancedPlanner) SetOnPlanReady(cb func()) {
	p.onPlanReady = cb
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
			p.logger.Info("Background replan complete", slog.String("objective", plan.Objective))
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

func (p *AdvancedPlanner) FastPlan(state domain.GameState) domain.Plan {
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
	}

	if current != nil && len(current.Tasks) > 0 {
		return *current
	}
	return p.reactivePlan(state)
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

func (p *AdvancedPlanner) reactivePlan(state domain.GameState) domain.Plan {
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

	switch {
	case hasImmediateThreat:
		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-heal-%d", time.Now().UnixNano()),
			Action:    "retreat",
			Target:    domain.Target{Name: "none", Type: "none"},
			Priority:  100,
			Rationale: "Reactive fast-path fallback: Survival Critical",
		})
	case state.Health < domain.DecisionHealthSafe && hasFood:
		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-eat-%d", time.Now().UnixNano()),
			Action:    "eat",
			Target:    domain.Target{Name: "best_food", Type: "item"},
			Priority:  95,
			Rationale: "Reactive fast-path fallback: Recover health with available food",
		})
	case state.Health < domain.SurvivalCriticalHealth || state.Food < 12.0:
		if nearbyFoodEntity != "" && state.Health > 8.0 {
			tasks = append(tasks, domain.Action{
				ID:        fmt.Sprintf("react-hunt-%d", time.Now().UnixNano()),
				Action:    "hunt",
				Target:    domain.Target{Name: nearbyFoodEntity, Type: "entity"},
				Priority:  90,
				Rationale: "Reactive fast-path: Hunt nearby food source",
			})
		} else if nearbyFoodBlock != "" {
			tasks = append(tasks, domain.Action{
				ID:        fmt.Sprintf("react-gather-%d", time.Now().UnixNano()),
				Action:    "gather",
				Target:    domain.Target{Name: nearbyFoodBlock, Type: "block"},
				Priority:  90,
				Rationale: "Reactive fast-path: Gather nearby berry bush",
			})
		} else {
			tasks = append(tasks, domain.Action{
				ID:        fmt.Sprintf("react-food-%d", time.Now().UnixNano()),
				Action:    "explore",
				Target:    domain.Target{Name: "food_source", Type: "category"},
				Priority:  85,
				Rationale: "Reactive fast-path fallback: Seek food or resources",
			})
		}
	}

	if len(tasks) == 0 {
		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-explore-%d", time.Now().UnixNano()),
			Action:    "explore",
			Target:    domain.Target{Name: "surroundings", Type: "category"},
			Priority:  5,
			Rationale: "Reactive fast-path: Stable state, exploring surroundings while waiting for next plan",
		})
	}

	return domain.Plan{
		Objective: "Reactive Fallback Plan",
		Tasks:     tasks,
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

func (p *AdvancedPlanner) evictCache() {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()

	if len(p.planCache) < maxPlanCacheSize {
		return
	}

	type entry struct {
		key  string
		used time.Time
	}
	entries := make([]entry, 0, len(p.planCache))
	for k, v := range p.planCache {
		entries = append(entries, entry{k, v.lastUsed})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].used.Before(entries[j].used)
	})

	target := maxPlanCacheSize / 5
	for i := 0; i < target && i < len(entries); i++ {
		delete(p.planCache, entries[i].key)
	}
}

func (p *AdvancedPlanner) generateLLMPlan(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error) {
	lastEventID, err := p.store.GetLastEventID(ctx, sessionID)
	if err != nil {
		p.logger.Warn("Failed to get last event ID, using 0", slog.Any("error", err))
		lastEventID = 0
	}

	stateHash := hashState(state)
	cacheKey := fmt.Sprintf("%s:%d:%d", sessionID, lastEventID, stateHash)
	sfKey := cacheKey

	p.cacheMu.RLock()
	if entry, ok := p.planCache[cacheKey]; ok {
		if time.Since(entry.createdAt) > 10*time.Second {
			p.cacheMu.RUnlock()
			p.cacheMu.Lock()
			delete(p.planCache, cacheKey)
			p.cacheMu.Unlock()
			goto cacheMiss
		}

		cachedPlan := entry.plan
		p.failMu.Lock()
		fails := p.failures[cachedPlan.Objective]
		p.failMu.Unlock()

		if fails == 0 {
			p.cacheMu.RUnlock()
			p.cacheMu.Lock()
			if entry, ok := p.planCache[cacheKey]; ok {
				entry.lastUsed = time.Now()
				p.planCache[cacheKey] = entry
			}
			p.cacheMu.Unlock()

			p.logger.Info("Using cached plan for stable state", slog.String("objective", cachedPlan.Objective))
			return p.clonePlanWithNewIDs(cachedPlan), nil
		}
	}
	p.cacheMu.RUnlock()

cacheMiss:

	val, err, _ := p.sf.Do(sfKey, func() (interface{}, error) {
		p.cacheMu.RLock()
		if entry, ok := p.planCache[cacheKey]; ok {
			p.cacheMu.RUnlock()
			return entry.plan, nil
		}
		p.cacheMu.RUnlock()

		directive := p.evaluator.Evaluate(state)
		learnedRules := ""

		if p.extractor != nil {
			learnedRules = p.extractor.GenerateRules(ctx, sessionID)
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

		systemPrompt := fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s\n\nLONG_TERM_MEMORY:\n%s\n\n%s\n\n%s\n\nPRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\nAll tasks MUST align with these strategies.",
			BaseSystemRules,
			p.milestoneContext,
			tacticalFeedback,
			learnedRules,
			longTermMem,
			knownWorld,
			availableSkillsDocs,
			directive.PrimaryGoal,
			directive.SecondaryGoal,
		)

		userContent := domain.FormatStateForLLM(state)

		events, _ := p.store.GetRecentStream(ctx, sessionID, 100)
		historySummary := p.client.CompressState(state, events)
		userContent = fmt.Sprintf("%s\n\nEXECUTION_HISTORY_SUMMARY:\n%s", userContent, historySummary)

		rawResponse, err := p.client.Generate(ctx, systemPrompt, userContent)
		if err != nil {
			return nil, fmt.Errorf("llm api failure: %w", err)
		}

		var parsed multiCandidateResponse
		if err := json.Unmarshal([]byte(domain.CleanJSON(rawResponse)), &parsed); err != nil {
			return nil, fmt.Errorf("llm schema violation: %w", err)
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

		events, _ = p.store.GetRecentStream(ctx, sessionID, 500)
		stats := learning.CalculateActionStats(nil, events, p.logger)

		p.failMu.Lock()
		failureCount := p.failures[parsed.Objective]
		p.failMu.Unlock()

		var scored []struct {
			tasks []domain.Action
			score float64
		}
		for _, candidate := range parsed.Candidates {
			score := p.scoreCandidate(candidate, state, stats, failureCount)
			scored = append(scored, struct {
				tasks []domain.Action
				score float64
			}{candidate, score})
		}

		sort.Slice(scored, func(i, j int) bool {
			return scored[i].score > scored[j].score
		})

		finalPlan := &domain.Plan{
			ID:        fmt.Sprintf("plan-%d", time.Now().UnixNano()),
			Objective: parsed.Objective,
			Tasks:     scored[0].tasks,
		}

		for i := 1; i < len(scored); i++ {
			finalPlan.Fallbacks = append(finalPlan.Fallbacks, scored[i].tasks)
		}

		p.evictCache()
		p.cacheMu.Lock()
		p.planCache[cacheKey] = cachedPlan{
			plan:      finalPlan,
			createdAt: time.Now(),
			lastUsed:  time.Now(),
			cacheKey:  cacheKey,
		}
		p.cacheMu.Unlock()

		return finalPlan, nil
	})

	if err != nil {
		return nil, err
	}

	return p.clonePlanWithNewIDs(val.(*domain.Plan)), nil
}

func (p *AdvancedPlanner) clonePlanWithNewIDs(original *domain.Plan) *domain.Plan {
	clone := &domain.Plan{
		ID:        fmt.Sprintf("cached-plan-%d", time.Now().UnixNano()),
		Objective: original.Objective,
		Tasks:     make([]domain.Action, len(original.Tasks)),
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

func (p *AdvancedPlanner) scoreCandidate(tasks []domain.Action, state domain.GameState, stats map[string]*learning.ActionStats, failureCount int) float64 {
	if len(tasks) == 0 {
		return 0
	}

	score := 100.0
	weights := p.flags.Weights

	score -= float64(failureCount) * 15.0

	explorationBonus := weights.ExplorationBonus
	if failureCount >= 2 {
		explorationBonus *= 2.5
	}

	hasExplored := false
	var totalRisk float64

	for _, t := range tasks {
		if t.Action == "hunt" {
			totalRisk += weights.RiskPenalty
		}
		if t.Action == "mine" {
			totalRisk += (weights.RiskPenalty / 2.0)
		}

		if stat, ok := stats[t.Action]; ok && stat.Attempts > 0 {
			probability := stat.SuccessRate
			score += (probability * weights.SuccessWeight)
			score -= ((1.0 - probability) * (weights.SuccessWeight + 10.0))
		} else if !hasExplored {
			score += explorationBonus
			hasExplored = true
		}
	}

	score -= (totalRisk / float64(len(tasks)))

	return score
}
