package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/config"
	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/learning"
	"david22573/synaptic-mc/internal/memory"
	"david22573/synaptic-mc/internal/strategy"
)

type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
}

type RuleExtractor interface {
	GenerateRules(ctx context.Context, sessionID string) string
}

type AdvancedPlanner struct {
	client    LLMClient
	evaluator *strategy.Evaluator
	extractor RuleExtractor
	memStore  memory.Store
	store     domain.EventStore
	logger    *slog.Logger
	flags     config.FeatureFlags

	currentPlan atomic.Pointer[domain.Plan]
	replanCh    chan domain.GameState

	planCache map[uint64]*domain.Plan
	cacheMu   sync.RWMutex
}

func NewAdvancedPlanner(
	client LLMClient,
	evaluator *strategy.Evaluator,
	extractor RuleExtractor,
	memStore memory.Store,
	store domain.EventStore,
	logger *slog.Logger,
	flags config.FeatureFlags,
) *AdvancedPlanner {
	return &AdvancedPlanner{
		client:    client,
		evaluator: evaluator,
		extractor: extractor,
		memStore:  memStore,
		store:     store,
		logger:    logger.With(slog.String("component", "advanced_planner")),
		flags:     flags,
		replanCh:  make(chan domain.GameState, 1),
		planCache: make(map[uint64]*domain.Plan),
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
VALID TARGET TYPES: "block", "entity", "recipe", "location", "category", "none".
    VALID ACTIONS: gather, craft, hunt, explore, build, smelt, mine, farm, mark_location, recall_location, idle, sleep, retreat, eat.
OUTPUT REQUIREMENT (ADVANCED PLANNING):
    You must generate ONE high-level objective, and exactly 2 to 3 DIFFERENT candidate task sequences to achieve that objective.
Make candidate 1 the most direct route, and candidate 2/3 alternative or safer routes.
Response format (JSON only):
    {
    "objective": "Sub-goal description",
    "candidates": [
    [
    { "id": "task-1", "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Directly gather wood" }
    ],
    [
    { "id": "task-2", "action": "explore", "target": { "type": "location", "name": "forest" }, "rationale": "Find a safer forest first" },
    { "id": "task-3", "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Gather wood safely" }
    ]
    ]
    }`

type multiCandidateResponse struct {
	Objective  string            `json:"objective"`
	Candidates [][]domain.Action `json:"candidates"`
}

func hashState(state domain.GameState) uint64 {
	h := fnv.New64a()
	// Discard minor floating point variances by casting to int for the hash
	str := fmt.Sprintf("%d|%d|%d|%d|%d|%d|%d",
		state.TimeOfDay,
		int(state.Health),
		int(state.Food),
		int(state.Position.X),
		int(state.Position.Y),
		int(state.Position.Z),
		len(state.Threats),
	)
	h.Write([]byte(str))

	for _, item := range state.Inventory {
		if item.Count > 0 {
			h.Write([]byte(fmt.Sprintf("|%s:%d", item.Name, item.Count)))
		}
	}
	return h.Sum64()
}

func (p *AdvancedPlanner) stateChangedSignificantly(prev, curr domain.GameState) bool {
	if prev.Health == 0 {
		return true // Initial state
	}

	dx := prev.Position.X - curr.Position.X
	dy := prev.Position.Y - curr.Position.Y
	dz := prev.Position.Z - curr.Position.Z
	dist := math.Sqrt(dx*dx + dy*dy + dz*dz)

	return dist > 2.0 || prev.Health != curr.Health || prev.Food != curr.Food
}

func (p *AdvancedPlanner) Close() {
	close(p.replanCh)
}

func (p *AdvancedPlanner) SlowReplanLoop(ctx context.Context, sessionID string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var latestState domain.GameState
	var lastPlannedState domain.GameState

	for {
		select {
		case <-ctx.Done():
			return
		case state, ok := <-p.replanCh:
			if !ok {
				return
			}
			latestState = state
		case <-ticker.C:
			if latestState.Health == 0 {
				continue
			}

			// Churn Limiter: Skip LLM call if state hasn't meaningfully changed
			if !p.stateChangedSignificantly(lastPlannedState, latestState) {
				continue
			}

			plan, err := p.generateLLMPlan(ctx, sessionID, latestState)
			if err != nil {
				p.logger.Error("Background LLM planning failed", slog.Any("error", err))
				continue
			}

			p.currentPlan.Store(plan)
			lastPlannedState = latestState
			p.logger.Info("Background replan complete", slog.String("objective", plan.Objective))
		}
	}
}

func (p *AdvancedPlanner) TriggerReplan(state domain.GameState) {
	select {
	case p.replanCh <- state:
	default:
		// Channel full, drop to avoid blocking
	}
}

func (p *AdvancedPlanner) FastPlan(state domain.GameState) domain.Plan {
	if plan := p.currentPlan.Swap(nil); plan != nil {
		if len(plan.Tasks) > 0 {
			return *plan
		}
	}
	return p.reactivePlan(state)
}

func (p *AdvancedPlanner) reactivePlan(state domain.GameState) domain.Plan {
	var tasks []domain.Action

	if state.Health < 10 {
		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-heal-%d", time.Now().UnixNano()),
			Action:    "retreat",
			Target:    domain.Target{Name: "none", Type: "none"},
			Priority:  100,
			Rationale: "Reactive fast-path fallback: Health critical",
		})
	} else {
		tasks = append(tasks, domain.Action{
			ID:        fmt.Sprintf("react-idle-%d", time.Now().UnixNano()),
			Action:    "idle",
			Target:    domain.Target{Name: "none", Type: "none"},
			Priority:  0,
			Rationale: "Reactive fast-path fallback: Awaiting LLM instruction",
		})
	}

	return domain.Plan{
		Objective: "Reactive Fallback Plan",
		Tasks:     tasks,
	}
}

func (p *AdvancedPlanner) generateLLMPlan(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error) {
	// Phase 2 Improvement: plan-cache-by-state-hash
	stateHash := hashState(state)

	p.cacheMu.RLock()
	if cachedPlan, ok := p.planCache[stateHash]; ok {
		p.cacheMu.RUnlock()
		p.logger.Info("Using cached plan for stable state", slog.String("objective", cachedPlan.Objective))
		return p.clonePlanWithNewIDs(cachedPlan), nil
	}
	p.cacheMu.RUnlock()

	directive := p.evaluator.Evaluate(state)
	learnedRules := ""

	if p.extractor != nil {
		learnedRules = p.extractor.GenerateRules(ctx, sessionID)
	}

	knownWorld := "KNOWN WORLD: empty"
	longTermMem := "No active summary."
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

	systemPrompt := fmt.Sprintf("%s\n\n%s\n\nLONG_TERM_MEMORY:\n%s\n\n%s\n\nPRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\nAll tasks MUST align with these strategies.",
		BaseSystemRules,
		learnedRules,
		longTermMem,
		knownWorld,
		directive.PrimaryGoal,
		directive.SecondaryGoal,
	)

	userContent := domain.FormatStateForLLM(state)

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

	for i := range parsed.Candidates {
		for j := range parsed.Candidates[i] {
			if parsed.Candidates[i][j].ID == "" {
				parsed.Candidates[i][j].ID = fmt.Sprintf("plan-%d-%d", time.Now().UnixNano(), j)
			}
		}
	}

	events, _ := p.store.GetRecentStream(ctx, sessionID, 500)
	stats := learning.CalculateActionStats(nil, events)

	bestIdx := 0
	highestScore := math.Inf(-1)

	for i, candidate := range parsed.Candidates {
		score := p.scoreCandidate(candidate, state, stats)
		if score > highestScore {
			highestScore = score
			bestIdx = i
		}
	}

	finalPlan := &domain.Plan{
		Objective: parsed.Objective,
		Tasks:     parsed.Candidates[bestIdx],
	}

	// Save to cache
	p.cacheMu.Lock()
	p.planCache[stateHash] = finalPlan
	p.cacheMu.Unlock()

	return finalPlan, nil
}

// clonePlanWithNewIDs prevents the IdempotentController from dropping
// actions if the exact same plan is pulled from the cache later.
func (p *AdvancedPlanner) clonePlanWithNewIDs(original *domain.Plan) *domain.Plan {
	clone := &domain.Plan{
		Objective: original.Objective,
		Tasks:     make([]domain.Action, len(original.Tasks)),
	}

	now := time.Now().UnixNano()
	for i, t := range original.Tasks {
		clone.Tasks[i] = t
		clone.Tasks[i].ID = fmt.Sprintf("cached-plan-%d-%d", now, i)
	}

	return clone
}

func (p *AdvancedPlanner) scoreCandidate(tasks []domain.Action, state domain.GameState, stats map[string]*learning.ActionStats) float64 {
	score := 100.0
	weights := p.flags.Weights

	for _, t := range tasks {
		if t.Action == "hunt" {
			score -= weights.RiskPenalty
		}
		if t.Action == "mine" {
			score -= (weights.RiskPenalty / 2.0)
		}

		if stat, ok := stats[t.Action]; ok && stat.Attempts > 0 {
			probability := stat.SuccessRate
			score += (probability * weights.SuccessWeight)

			// Penalty for low probability tasks scales with success weight
			score -= ((1.0 - probability) * (weights.SuccessWeight + 10.0))
		} else {
			score += weights.ExplorationBonus
		}
	}

	return score
}
