// internal/voyager/critic.go
package voyager

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// Critic interface — evaluates task outcomes.
type Critic interface {
	Evaluate(intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) (bool, *domain.Reflection)
}

// StateCritic evaluates task results. It now accepts an optional LLMClient;
// if provided, failed tasks get LLM-generated reflections instead of generic messages.
type StateCritic struct {
	llm LLMClient // optional — set via NewStateCriticWithLLM
}

func NewStateCritic() *StateCritic {
	return &StateCritic{}
}

// NewStateCriticWithLLM creates a critic backed by an LLM for richer failure analysis.
func NewStateCriticWithLLM(client LLMClient) *StateCritic {
	return &StateCritic{llm: client}
}

// Evaluate checks whether a task succeeded using state diffs, then optionally
// calls the LLM for a nuanced reflection on failure.
func (c *StateCritic) Evaluate(intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) (bool, *domain.Reflection) {
	// Fast-path: execution layer already reported failure with a cause.
	if !result.Success {
		refl := &domain.Reflection{
			Failure: fmt.Sprintf("Task '%s' failed", intent.Action),
			Cause:   result.Cause,
			Score:   0.0,
		}
		if failureCount >= 2 {
			refl.Fix = "Current approach is deadlocked. Try a completely different strategy or location."
		}
		// Enrich with LLM if available.
		if c.llm != nil {
			if enriched := c.llmReflection(context.Background(), intent, before, after, result, failureCount); enriched != nil {
				return false, enriched
			}
		}
		return false, refl
	}

	if after.Health <= 0 {
		return false, &domain.Reflection{
			Failure: "Bot died during execution",
			Cause:   "Lethal damage taken",
			Fix:     "Prioritize armor or avoid hostiles in this area",
			Score:   0.0,
		}
	}

	// Heuristic state-diff checks (fast, no LLM needed for successes).
	beforeInv := inventoryMap(before)
	afterInv := inventoryMap(after)
	target := strings.ToLower(strings.TrimSpace(intent.Target))

	successRefl := &domain.Reflection{Score: 1.0}

	switch intent.Action {
	case "mine", "gather", "farm":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount >= bCount+intent.Count {
			return true, successRefl
		}
		if aCount > bCount {
			return true, &domain.Reflection{Score: 0.7, Cause: "Partial success: gathered fewer items than requested"}
		}
		
		refl := &domain.Reflection{
			Failure: "Inventory unchanged",
			Cause:   fmt.Sprintf("State diff for '%s' failed", intent.Target),
			Fix:     "Ensure tools are correct and blocks are reachable",
			Score:   0.0,
		}
		if c.llm != nil {
			if enriched := c.llmReflection(context.Background(), intent, before, after, result, failureCount); enriched != nil {
				return false, enriched
			}
		}
		return false, refl

	case "craft":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount > bCount {
			return true, successRefl
		}
		return false, &domain.Reflection{
			Failure: "Craft failed",
			Cause:   "Inventory count did not increase",
			Fix:     "Verify ingredients and proximity to crafting table",
			Score:   0.0,
		}

	case "smelt":
		expectedOutput := getSmeltOutput(target)
		bCount := beforeInv[expectedOutput]
		aCount := afterInv[expectedOutput]
		if aCount > bCount {
			return true, successRefl
		}
		return false, &domain.Reflection{
			Failure: "Smelt failed",
			Cause:   "Output item not detected",
			Fix:     "Ensure fuel is in furnace and you are standing close enough to receive item",
			Score:   0.0,
		}

	case "hunt":
		if after.Health < before.Health && after.Health < 10 {
			return true, &domain.Reflection{Score: 0.5, Cause: "Hunt succeeded but with critical health loss"}
		}
		return true, successRefl

	case "store":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount < bCount || target == "all" || target == "dump" {
			return true, successRefl
		}
		return false, &domain.Reflection{
			Failure: "Store failed",
			Cause:   "Items still in inventory",
			Score:   0.0,
		}

	case "retrieve":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount > bCount {
			return true, successRefl
		}
		return false, &domain.Reflection{
			Failure: "Retrieve failed",
			Cause:   "No new items in inventory",
			Score:   0.0,
		}

	case "eat":
		if after.Food > before.Food || after.Health > before.Health {
			return true, successRefl
		}
		return false, &domain.Reflection{Failure: "Eat failed", Cause: "No change in food or health", Score: 0.0}

	case "build":
		beforeTotal := totalItems(beforeInv)
		afterTotal := totalItems(afterInv)
		if afterTotal < beforeTotal {
			return true, successRefl
		}
		return false, &domain.Reflection{Failure: "Build failed", Cause: "No blocks consumed from inventory", Score: 0.0}

	case "explore", "retreat":
		dx := after.Position.X - before.Position.X
		dy := after.Position.Y - before.Position.Y
		dz := after.Position.Z - before.Position.Z
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if dist > 3.0 {
			return true, successRefl
		}
		return false, &domain.Reflection{Failure: "Stagnant position", Cause: "Bot moved less than 3 blocks", Fix: "Check for obstacles or stuck pathfinding", Score: 0.0}

	case "use_skill":
		return true, successRefl
	}

	return true, successRefl
}

// llmReflection sends the full execution context to the LLM for a nuanced failure analysis.
func (c *StateCritic) llmReflection(ctx context.Context, intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) *domain.Reflection {
	if c.llm == nil {
		return nil
	}

	systemPrompt := `You are the Ruthless Critic for an autonomous Minecraft bot. 
Analyze why a task failed and return a JSON reflection object.
Format:
{
  "failure": "concise description of the failure",
  "cause": "root cause analysis (e.g., night travel no shield)",
  "fix": "better next strategy (e.g., sleep before travel)",
  "score": 0.0
}
Rules:
- Be brutally honest and precise.
- Identify the root cause, not just the symptom.
- Do NOT suggest actions outside the available set: gather, craft, mine, smelt, hunt, explore, eat, retreat, store, retrieve, build, farm, use_skill.
- Output strictly JSON.`

	userContent := fmt.Sprintf(`FAILED TASK:
Action: %s | Target: %s | Count: %d
Failure count: %d
Execution cause: %s

STATE BEFORE:
Health: %.0f/20, Food: %.0f/20, Position: (%.0f, %.0f, %.0f)
Inventory: %s

STATE AFTER:
Health: %.0f/20, Food: %.0f/20, Position: (%.0f, %.0f, %.0f)

Reflect on this failure.`,
		intent.Action, intent.Target, intent.Count,
		failureCount,
		result.Cause,
		before.Health, before.Food, before.Position.X, before.Position.Y, before.Position.Z,
		formatInventoryShort(before),
		after.Health, after.Food, after.Position.X, after.Position.Y, after.Position.Z,
	)

	ctx2, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	response, err := c.llm.GenerateText(ctx2, systemPrompt, userContent)
	if err != nil {
		return nil
	}

	var refl domain.Reflection
	if err := json.Unmarshal([]byte(domain.CleanJSON(response)), &refl); err != nil {
		return nil
	}
	return &refl
}

// GenerateRules is kept for interface compatibility but now delegates to the LLM.
// Falls back to essential hardcoded rules if the LLM is unavailable.
func (c *StateCritic) GenerateRules(ctx context.Context, sessionID string) string {
	if c.llm == nil {
		return staticSurvivalRules()
	}

	systemPrompt := `You are generating compact decision rules for an autonomous Minecraft bot.
Output a short bulleted list (max 8 bullets) of the most important survival and progression rules.
Focus on rules the bot frequently violates. Plain text only, no markdown headers.`

	ctx2, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	response, err := c.llm.GenerateText(ctx2, systemPrompt,
		"Generate the most critical rules for a Minecraft bot to survive and progress efficiently.")
	if err != nil {
		return staticSurvivalRules()
	}
	return strings.TrimSpace(response)
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func inventoryMap(state domain.GameState) map[string]int {
	m := make(map[string]int)
	for _, item := range state.Inventory {
		m[strings.ToLower(item.Name)] += item.Count
	}
	return m
}

func totalItems(inv map[string]int) int {
	total := 0
	for _, v := range inv {
		total += v
	}
	return total
}

func formatInventoryShort(state domain.GameState) string {
	inv := inventoryMap(state)
	var parts []string
	for name, count := range inv {
		parts = append(parts, fmt.Sprintf("%dx%s", count, name))
	}
	if len(parts) == 0 {
		return "empty"
	}
	if len(parts) > 8 {
		parts = parts[:8]
		parts = append(parts, "...")
	}
	return strings.Join(parts, ", ")
}

func staticSurvivalRules() string {
	return `CRITICAL SURVIVAL RULES:
- If health < 10, prioritize 'eat' or 'retreat' immediately
- Avoid 'hunt' when health < 12
- Cannot mine stone without wooden_pickaxe or better
- Crafting table required for pickaxes and swords
- Verify materials are in inventory before crafting
- 'store' requires accessible chest; 'retrieve' requires item in known chest
- Movement actions should result in >3 block displacement
- If an action fails twice in a row, choose a completely different approach`
}

func getSmeltOutput(input string) string {
	input = strings.ToLower(input)
	smeltMap := map[string]string{
		"cobblestone": "stone", "sand": "glass", "red_sand": "glass",
		"beef": "cooked_beef", "porkchop": "cooked_porkchop", "chicken": "cooked_chicken",
		"mutton": "cooked_mutton", "rabbit": "cooked_rabbit", "cod": "cooked_cod",
		"salmon": "cooked_salmon", "potato": "baked_potato", "kelp": "dried_kelp",
		"clay_ball": "brick", "cactus": "green_dye", "netherrack": "nether_brick",
		"stone": "smooth_stone",
	}
	if out, ok := smeltMap[input]; ok {
		return out
	}
	if strings.HasPrefix(input, "raw_") {
		return strings.TrimPrefix(input, "raw_") + "_ingot"
	}
	if strings.HasSuffix(input, "_ore") {
		return strings.TrimSuffix(input, "_ore") + "_ingot"
	}
	if strings.HasSuffix(input, "_log") || strings.HasSuffix(input, "_wood") {
		return "charcoal"
	}
	return "cooked_" + input
}
