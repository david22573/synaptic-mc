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

// LLMPlanner implements Planner. It merges the physical state with the strategic directive.
type LLMPlanner struct {
	client    LLMClient
	evaluator *strategy.Evaluator
}

func NewLLMPlanner(client LLMClient, evaluator *strategy.Evaluator) *LLMPlanner {
	return &LLMPlanner{
		client:    client,
		evaluator: evaluator,
	}
}

const BaseSystemRules = `You are the tactical commander of an autonomous Minecraft agent.
CRITICAL GAME MECHANIC RULES:
1. Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
2. You CANNOT gather stone or coal without a wooden_pickaxe.
3. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM.

VALID TARGET TYPES: "block", "entity", "recipe", "location", "category", "none".
VALID ACTIONS: gather, craft, hunt, explore, build, smelt, mine, farm, mark_location, recall_location, idle, sleep, retreat, eat.

Response format (JSON only):
{
  "objective": "Sub-goal description",
  "tasks": [
    { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Closest target" }
  ]
}`

func (p *LLMPlanner) Generate(ctx context.Context, sessionID string, state domain.GameState) (*domain.Plan, error) {
	directive := p.evaluator.Evaluate(state)

	systemPrompt := fmt.Sprintf("%s\n\nPRIMARY STRATEGY: %s\nSECONDARY STRATEGY: %s\nAll tasks MUST align with these strategies.",
		BaseSystemRules,
		directive.PrimaryGoal,
		directive.SecondaryGoal,
	)

	// In a real system, you'd use a robust templating engine for state summarization.
	// For brevity, we pass the raw JSON, but strip massive arrays if needed.
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
