// internal/voyager/critic.go
package voyager

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// Critic interface — unchanged.
type Critic interface {
	Evaluate(intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) (bool, string)
}

// StateCritic evaluates task results. It now accepts an optional LLMClient;
// if provided, failed tasks get LLM-generated critiques instead of generic messages.
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
// calls the LLM for a nuanced critique on failure.
func (c *StateCritic) Evaluate(intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) (bool, string) {
	// Fast-path: execution layer already reported failure with a cause.
	if !result.Success {
		baseCritique := fmt.Sprintf("Task '%s' failed. Cause: %s.", intent.Action, result.Cause)
		if failureCount >= 2 {
			baseCritique += fmt.Sprintf(" This is failure #%d — the current approach is deadlocked. Rethink the entire sequence.", failureCount)
		}
		// Enrich with LLM if available.
		if c.llm != nil {
			enriched := c.llmCritique(context.Background(), intent, before, after, result, failureCount)
			if enriched != "" {
				return false, enriched
			}
		}
		return false, baseCritique
	}

	if after.Health <= 0 {
		return false, "Bot died during execution. Re-evaluate threat assessment and survival priorities."
	}

	// Heuristic state-diff checks (fast, no LLM needed for successes).
	beforeInv := inventoryMap(before)
	afterInv := inventoryMap(after)
	target := strings.ToLower(strings.TrimSpace(intent.Target))

	switch intent.Action {
	case "mine", "gather", "farm":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount >= bCount+intent.Count {
			return true, fmt.Sprintf("Verified: gathered %d %s.", aCount-bCount, intent.Target)
		}
		if aCount > bCount {
			return true, fmt.Sprintf("Partial: gathered %d %s (wanted %d).", aCount-bCount, intent.Target, intent.Count)
		}
		critique := fmt.Sprintf("State diff failed: inventory for '%s' unchanged at %d. Item may have dropped without pickup.", intent.Target, bCount)
		if c.llm != nil {
			if enriched := c.llmCritique(context.Background(), intent, before, after, result, failureCount); enriched != "" {
				return false, enriched
			}
		}
		return false, critique

	case "craft":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount > bCount {
			return true, fmt.Sprintf("Verified craft: %s increased %d→%d.", intent.Target, bCount, aCount)
		}
		return false, fmt.Sprintf("Craft state diff failed: '%s' count did not increase. Check prerequisites and crafting table proximity.", intent.Target)

	case "smelt":
		expectedOutput := getSmeltOutput(target)
		bCount := beforeInv[expectedOutput]
		aCount := afterInv[expectedOutput]
		if aCount > bCount {
			return true, fmt.Sprintf("Verified smelt: output '%s' increased.", expectedOutput)
		}
		return false, fmt.Sprintf("Smelt output '%s' not verified. Check furnace, fuel, and proximity.", expectedOutput)

	case "hunt":
		if after.Health < before.Health && after.Health < 10 {
			return true, "Hunt completed with critical damage taken — high risk."
		}
		return true, "Hunt resolved — survival verified by health delta."

	case "store":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount < bCount || target == "all" || target == "dump" {
			return true, "Store verified: items removed from local inventory."
		}
		return false, fmt.Sprintf("Store failed: inventory count for '%s' unchanged.", intent.Target)

	case "retrieve":
		bCount := beforeInv[target]
		aCount := afterInv[target]
		if aCount > bCount {
			return true, fmt.Sprintf("Retrieve verified: '%s' increased.", intent.Target)
		}
		return false, fmt.Sprintf("Retrieve failed: no increase in '%s'.", intent.Target)

	case "eat":
		if after.Food > before.Food || after.Health > before.Health {
			return true, "Eat verified: food/health delta is positive."
		}
		return false, "Eat failed: no change in food or health."

	case "build":
		beforeTotal := totalItems(beforeInv)
		afterTotal := totalItems(afterInv)
		if afterTotal < beforeTotal {
			return true, fmt.Sprintf("Build verified: %d resources consumed.", beforeTotal-afterTotal)
		}
		return false, "Build failed: no resource consumption detected."

	case "explore", "retreat":
		dx := after.Position.X - before.Position.X
		dy := after.Position.Y - before.Position.Y
		dz := after.Position.Z - before.Position.Z
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if dist > 3.0 {
			return true, fmt.Sprintf("Movement verified: displaced %.1f blocks.", dist)
		}
		return false, "Stagnant position: bot moved less than 3 blocks. Pathing may be blocked."

	case "use_skill":
		// For synthesized skills, trust the execution result; no heuristic available.
		return true, fmt.Sprintf("Skill '%s' executed without runtime error.", intent.Target)
	}

	return true, fmt.Sprintf("%s executed — general state integrity maintained.", intent.Action)
}

// llmCritique sends the full execution context to the LLM for a nuanced failure analysis.
// Returns an empty string if the LLM call fails, so the caller can fall back to heuristic text.
func (c *StateCritic) llmCritique(ctx context.Context, intent domain.ActionIntent, before, after domain.GameState, result domain.ExecutionResult, failureCount int) string {
	if c.llm == nil {
		return ""
	}

	systemPrompt := `You are the Critic for an autonomous Minecraft bot. Analyze why a task failed and give precise, actionable feedback.
Rules:
- Be concise (2-4 sentences max).
- Identify the root cause, not just the symptom.
- If the failure repeats, suggest a completely different approach.
- Do NOT suggest actions outside the available set: gather, craft, mine, smelt, hunt, explore, eat, retreat, store, retrieve, build, farm, use_skill.
- Output plain text only — no JSON, no markdown.`

	userContent := fmt.Sprintf(`FAILED TASK:
Action: %s | Target: %s | Count: %d
Failure count: %d
Execution cause: %s

STATE BEFORE:
Health: %.0f/20, Food: %.0f/20, Position: (%.0f, %.0f, %.0f)
Inventory: %s

STATE AFTER:
Health: %.0f/20, Food: %.0f/20, Position: (%.0f, %.0f, %.0f)

Why did this fail, and what should the bot do differently?`,
		intent.Action, intent.Target, intent.Count,
		failureCount,
		result.Cause,
		before.Health, before.Food, before.Position.X, before.Position.Y, before.Position.Z,
		formatInventoryShort(before),
		after.Health, after.Food, after.Position.X, after.Position.Y, after.Position.Z,
	)

	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	response, err := c.llm.Generate(ctx2, systemPrompt, userContent)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(response)
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

	response, err := c.llm.Generate(ctx2, systemPrompt,
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
