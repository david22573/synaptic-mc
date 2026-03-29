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

// LLMPlanner implements Planner.
// It merges the physical state with the strategic directive.
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

// Enhanced System Rules with stricter naming conventions to stop TS rejections
// and an explicit command to heed validation feedback loops.
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
   - wooden_pickaxe: requires 3 oak_planks + 2 stick
   - stone_pickaxe: requires 3 cobblestone + 2 stick
6. If you lack the prerequisites for an item, your plan MUST include gathering or crafting those prerequisites first.
7. FEEDBACK LOOP: If the state contains 'feedback' showing a previous plan was rejected, YOU MUST NOT REPEAT THE EXACT SAME PLAN. Fix the validation errors (e.g., gather prerequisites first).

VALID TARGET TYPES: "block", "entity", "recipe", "location", "category", "none".
VALID ACTIONS: gather, craft, hunt, explore, build, smelt, mine, farm, mark_location, recall_location, idle, sleep, retreat, eat.

CRITICAL NAMING RULE: Target names MUST be exact Minecraft IDs (e.g., "pig", "cow", "cobblestone", "dirt"). DO NOT use generic or abstract terms like "animal", "shelter_hole", "passive_animals", or "food".

Response format (JSON only):
{
  "objective": "Sub-goal description",
  "tasks": [
    { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Need wood for planks" }
  ]
}`

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

	stateBytes, _ := json.Marshal(state)
	userContent := string(stateBytes)

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
