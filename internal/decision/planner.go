package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

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
}

func NewAdvancedPlanner(
	client LLMClient,
	evaluator *strategy.Evaluator,
	extractor RuleExtractor,
	memStore memory.Store,
	store domain.EventStore,
) *AdvancedPlanner {
	return &AdvancedPlanner{
		client:    client,
		evaluator: evaluator,
		extractor: extractor,
		memStore:  memStore,
		store:     store,
	}
}

const BaseSystemRules = `You are the tactical commander of an autonomous Minecraft agent.
CRITICAL GAME MECHANIC RULES:
1. Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
2. You CANNOT gather stone or coal without a wooden_pickaxe.
3. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM per candidate.
4. SURVIVAL: You CANNOT 'eat' if your inventory has no food.
   You CANNOT 'hunt' if health is under 12.
5. CRAFTING RECIPES:
   - oak_planks: requires 1 oak_log (yields 4)
   - stick: requires 2 oak_planks (yields 4)
   - crafting_table: requires 4 oak_planks
   - wooden_pickaxe: requires 3 oak_planks + 2 stick + MUST HAVE crafting_table in inventory
   - stone_pickaxe: requires 3 cobblestone + 2 stick + MUST HAVE crafting_table in inventory
6. If you lack prerequisites for an item, your tasks MUST include gathering/crafting those first.

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
      { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Directly gather wood" }
    ],
    [
      { "action": "explore", "target": { "type": "location", "name": "forest" }, "rationale": "Find a safer forest first" },
      { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Gather wood safely" }
    ]
  ]
}`

func formatStateForLLM(state domain.GameState) string {
	type compactPOI struct {
		Type      string  `json:"type"`
		Name      string  `json:"name"`
		Distance  float64 `json:"distance"`
		Direction string  `json:"direction"`
	}

	type compactThreat struct {
		Name string `json:"name"`
	}

	compact := struct {
		Health       float64           `json:"health"`
		Food         float64           `json:"food"`
		TimeOfDay    int               `json:"time_of_day"`
		HasBedNearby bool              `json:"has_bed_nearby"`
		Inventory    map[string]int    `json:"inventory"`
		Threats      []compactThreat   `json:"threats"`
		POIs         []compactPOI      `json:"pois"`
		HasPickaxe   bool              `json:"has_pickaxe"`
		HasWeapon    bool              `json:"has_weapon"`
		Feedback     []domain.Feedback `json:"feedback,omitempty"`
	}{
		Health:       state.Health,
		Food:         state.Food,
		TimeOfDay:    state.TimeOfDay,
		HasBedNearby: state.HasBedNearby,
		Inventory:    make(map[string]int),
		Threats:      make([]compactThreat, 0),
		POIs:         make([]compactPOI, 0),
		Feedback:     state.Feedback,
	}

	for _, item := range state.Inventory {
		if item.Count > 0 {
			compact.Inventory[item.Name] += item.Count
			if strings.Contains(item.Name, "pickaxe") {
				compact.HasPickaxe = true
			}
			if strings.Contains(item.Name, "sword") || (strings.Contains(item.Name, "axe") && !strings.Contains(item.Name, "pickaxe")) {
				compact.HasWeapon = true
			}
		}
	}

	for i, t := range state.Threats {
		if i >= 3 {
			break
		}
		compact.Threats = append(compact.Threats, compactThreat{Name: t.Name})
	}

	for i, p := range state.POIs {
		if i >= 5 {
			break
		}
		compact.POIs = append(compact.POIs, compactPOI{
			Type:      p.Type,
			Name:      p.Name,
			Distance:  p.Distance,
			Direction: p.Direction,
		})
	}

	b, _ := json.Marshal(compact)
	return string(b)
}

type multiCandidateResponse struct {
	Objective  string            `json:"objective"`
	Candidates [][]domain.Action `json:"candidates"`
}

func (p *AdvancedPlanner) Generate(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error) {
	directive := p.evaluator.Evaluate(state)
	learnedRules := ""

	if p.extractor != nil {
		learnedRules = p.extractor.GenerateRules(ctx, sessionID)
	}

	knownWorld := "KNOWN WORLD: empty"
	longTermMem := "No active summary."
	if p.memStore != nil {
		knownWorld, _ = p.memStore.GetKnownWorld(ctx, state.Position)
		longTermMem, _ = p.memStore.GetSummary(ctx, sessionID)
	}

	systemPrompt := fmt.Sprintf("%s\n\n%s\n\nLONG_TERM_MEMORY:\n%s\n\n%s\n\nPRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\nAll tasks MUST align with these strategies.",
		BaseSystemRules,
		learnedRules,
		longTermMem,
		knownWorld,
		directive.PrimaryGoal,
		directive.SecondaryGoal,
	)

	userContent := formatStateForLLM(state)

	rawResponse, err := p.client.Generate(ctx, systemPrompt, userContent)
	if err != nil {
		return nil, fmt.Errorf("llm api failure: %w", err)
	}

	var parsed multiCandidateResponse
	if err := json.Unmarshal([]byte(cleanJSON(rawResponse)), &parsed); err != nil {
		return nil, fmt.Errorf("llm schema violation: %w", err)
	}

	if len(parsed.Candidates) == 0 {
		return nil, fmt.Errorf("planner returned zero candidates")
	}

	// Calculate historical action stats for scoring
	events, _ := p.store.GetRecentStream(ctx, sessionID, 500)
	stats := learning.CalculateActionStats(events)

	bestIdx := 0
	highestScore := math.Inf(-1)

	for i, candidate := range parsed.Candidates {
		score := p.scoreCandidate(candidate, state, stats)
		if score > highestScore {
			highestScore = score
			bestIdx = i
		}
	}

	bestTasks := parsed.Candidates[bestIdx]

	return &domain.Plan{
		Objective: parsed.Objective,
		Tasks:     bestTasks,
	}, nil
}

func (p *AdvancedPlanner) scoreCandidate(tasks []domain.Action, state domain.GameState, stats map[string]*learning.ActionStats) float64 {
	score := 100.0

	for _, t := range tasks {
		// 1. Cost Penalty (longer plans are riskier)
		score -= 5.0

		// 2. Immediate Risk Penalty
		if t.Action == "hunt" {
			score -= 20.0
		}
		if t.Action == "mine" {
			score -= 10.0 // potential lava/mobs
		}

		// 3. Historical Probability Modifier
		if stat, ok := stats[t.Action]; ok && stat.Attempts > 0 {
			// Boost score if historically highly successful, heavily penalize if mostly failing
			probability := stat.SuccessRate
			score += (probability * 30.0)
			score -= ((1.0 - probability) * 40.0)
		} else {
			// Neutral bump for untried actions to encourage exploration
			score += 5.0
		}
	}

	return score
}

func cleanJSON(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	return strings.TrimSpace(cleaned)
}
