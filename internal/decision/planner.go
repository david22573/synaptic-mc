package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/strategy"
)

type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
}

type RuleExtractor interface {
	GenerateRules(ctx context.Context, sessionID string) string
}

type LLMPlanner struct {
	client    LLMClient
	evaluator *strategy.Evaluator
	extractor RuleExtractor
}

func NewLLMPlanner(client LLMClient, evaluator *strategy.Evaluator, extractor RuleExtractor) *LLMPlanner {
	return &LLMPlanner{
		client:    client,
		evaluator: evaluator,
		extractor: extractor,
	}
}

const BaseSystemRules = `You are the tactical commander of an autonomous Minecraft agent.
CRITICAL GAME MECHANIC RULES:
1. Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
2. You CANNOT gather stone or coal without a wooden_pickaxe.
3. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM.
4. SURVIVAL: You CANNOT 'eat' if your inventory has no food.
   You CANNOT 'hunt' if health is under 12.
5. CRAFTING RECIPES:
   - oak_planks: requires 1 oak_log (yields 4)
   - stick: requires 2 oak_planks (yields 4)
   - crafting_table: requires 4 oak_planks
   - wooden_pickaxe: requires 3 oak_planks + 2 stick + MUST HAVE crafting_table in inventory
   - stone_pickaxe: requires 3 cobblestone + 2 stick + MUST HAVE crafting_table in inventory
6. If you lack the prerequisites for an item, your plan MUST include gathering or crafting those prerequisites first.
7. FEEDBACK LOOP: If the state contains 'feedback' showing a previous plan was rejected, YOU MUST NOT REPEAT THE EXACT SAME PLAN.
   Fix the validation errors (e.g., gather prerequisites first).
8. To use a crafting table for recipes like pickaxes, you MUST have a crafting_table item physically in your inventory so the bot can place it.
   Craft a new one if you lack one.

VALID TARGET TYPES: "block", "entity", "recipe", "location", "category", "none".
VALID ACTIONS: gather, craft, hunt, explore, build, smelt, mine, farm, mark_location, recall_location, idle, sleep, retreat, eat.
CRITICAL NAMING RULE: Target names MUST be exact Minecraft IDs (e.g., "pig", "cow", "cobblestone", "dirt").
DO NOT use generic or abstract terms like "animal", "shelter_hole", "passive_animals", or "food".
Response format (JSON only):
{
  "objective": "Sub-goal description",
  "tasks": [
    { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Need wood for planks" }
  ]
}`

// 3.1 FIX: Compact state formatting to reduce token noise
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
		Health       float64         `json:"health"`
		Food         float64         `json:"food"`
		TimeOfDay    int             `json:"time_of_day"`
		HasBedNearby bool            `json:"has_bed_nearby"`
		Inventory    map[string]int  `json:"inventory"`
		Threats      []compactThreat `json:"threats"`
		POIs         []compactPOI    `json:"pois"`
		HasPickaxe   bool            `json:"has_pickaxe"`
		HasWeapon    bool            `json:"has_weapon"`
		Feedback     []string        `json:"feedback,omitempty"`
	}{
		Health:       state.Health,
		Food:         state.Food,
		TimeOfDay:    state.TimeOfDay,
		HasBedNearby: state.HasBedNearby,
		Inventory:    make(map[string]int),
		Threats:      make([]compactThreat, 0),
		POIs:         make([]compactPOI, 0),
		Feedback:     state.Feedback, // Keep feedback intact to break loops
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

func (p *LLMPlanner) Generate(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error) {
	directive := p.evaluator.Evaluate(state)
	learnedRules := ""

	if p.extractor != nil {
		learnedRules = p.extractor.GenerateRules(ctx, sessionID)
	}

	systemPrompt := fmt.Sprintf("%s\n\n%s\n\nPRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\nAll tasks MUST align with these strategies.",
		BaseSystemRules,
		learnedRules,
		directive.PrimaryGoal,
		directive.SecondaryGoal,
	)

	// 3.1 FIX: Use the compact state format instead of dumping the raw state
	userContent := formatStateForLLM(state)

	rawResponse, err := p.client.Generate(ctx, systemPrompt, userContent)
	if err != nil {
		return nil, fmt.Errorf("llm api failure: %w", err)
	}

	var plan domain.Plan
	if err := json.Unmarshal([]byte(cleanJSON(rawResponse)), &plan); err != nil {
		return nil, fmt.Errorf("llm schema violation: %w", err)
	}

	return &plan, nil
}

func cleanJSON(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	return strings.TrimSpace(cleaned)
}
