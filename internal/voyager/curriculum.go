// internal/voyager/curriculum.go
package voyager

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
)

// LLMClient is the minimal interface this package needs from the LLM layer.
type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
	CreateEmbedding(ctx context.Context, input string) ([]float32, error)
}

// Curriculum proposes the next task given the current game state.
type Curriculum interface {
	ProposeTask(ctx context.Context, state domain.GameState, memory []domain.TaskHistory, milestoneContext, sessionID string, horizon int) (*domain.ActionIntent, error)
}

// CodeSynthesizer can be implemented by a Curriculum to generate executable JS programs.
// Service checks for this interface via type assertion — it is not required by Curriculum.
type CodeSynthesizer interface {
	SynthesizeCode(ctx context.Context, intent domain.ActionIntent, before, after domain.GameState) (string, error)
}

// AutonomousCurriculum is the live implementation.
type AutonomousCurriculum struct {
	client     LLMClient
	vector     VectorStore
	memStore   memory.Store
	worldModel *domain.WorldModel
}

func NewAutonomousCurriculum(client LLMClient, vector VectorStore, memStore memory.Store, worldModel *domain.WorldModel) *AutonomousCurriculum {
	return &AutonomousCurriculum{
		client:     client,
		vector:     vector,
		memStore:   memStore,
		worldModel: worldModel,
	}
}

// ────────────────────────────────────────────────────────────────────────────
// ProposeTask — unchanged contract, same as before.
// ────────────────────────────────────────────────────────────────────────────

const SystemPrompt = `You are the Curriculum Agent for an autonomous Minecraft bot.
Your job is to evaluate the current Game State, review recent task history and progression status, and propose an optimal plan to advance the bot's progression or ensure its survival.

CRITICAL RULES:
1.  SURVIVAL FIRST: If health < 10, your plan MUST be 'eat' or 'retreat'.
2.  PREREQUISITES: You cannot mine stone without a wooden_pickaxe. You cannot craft a pickaxe without sticks and planks.
3.  COUNT: The 'count' field determines how many of the target item the bot will attempt to gather/craft. Keep it reasonable (1-10).
4.  STORAGE: If inventory is full, use the 'store' action to put items in a chest.
5.  RETRIEVAL: If you need an item and it is in a known chest, use the 'retrieve' action.
6.  BUILDING: If you need physical protection through the night, use the 'build' action with target 'shelter'.
7.  SKILLS: If an 'AVAILABLE SKILL' is perfect for this task, use action: "use_skill" and set "target" to the skill name.

AVAILABLE ACTIONS: "gather", "craft", "mine", "smelt", "hunt", "explore", "eat", "retreat", "store", "retrieve", "build", "farm", "use_skill"

OUTPUT FORMAT (Strict JSON, no markdown):
{
	"id": "intent-123",
	"rationale": "Brief explanation of why this is the best next step.",
	"action": "gather",
	"target": "oak_log",
	"count": 5
}`

func (c *AutonomousCurriculum) ProposeTask(ctx context.Context, state domain.GameState, mem []domain.TaskHistory, milestoneContext, sessionID string, horizon int) (*domain.ActionIntent, error) {
	if state.Health <= 0 {
		return nil, fmt.Errorf("bot is dead, waiting for respawn")
	}

	systemPrompt := SystemPrompt

	var historyStrs []string
	recentHistory := mem
	if len(mem) > 20 {
		recentHistory = mem[len(mem)-20:]
	}
	for _, m := range recentHistory {
		status := "FAILED"
		if m.Success {
			status = "SUCCESS"
		}
		historyStrs = append(historyStrs, fmt.Sprintf("[%s] Intent: %s %d %s | Critic: %s", status, m.Intent.Action, m.Intent.Count, m.Intent.Target, m.Critique))
	}
	historyContext := "No recent history."
	if len(historyStrs) > 0 {
		historyContext = strings.Join(historyStrs, "\n")
	}

	stateContext := domain.FormatStateForLLM(state)
	queryText := buildRetrievalQuery(state)
	queryEmb, err := c.client.CreateEmbedding(ctx, queryText)

	skillContext := ""
	if err == nil && c.vector != nil && len(queryEmb) > 0 {
		skills, _ := c.vector.RetrieveSkills(ctx, queryEmb, 5)
		if len(skills) > 0 {
			var skillStrs []string
			for _, s := range skills {
				var skill domain.ExecutableSkill
				if err := json.Unmarshal([]byte(s.Code), &skill); err == nil && skill.Name != "" {
					skillStrs = append(skillStrs, fmt.Sprintf("- Skill: %s (%s)", skill.Name, skill.Description))
				}
			}
			if len(skillStrs) > 0 {
				skillContext = "AVAILABLE SKILLS:\n" + strings.Join(skillStrs, "\n")
			}
		}
	}

	var memoryContext strings.Builder
	if c.memStore != nil {
		summary, err := c.memStore.GetSummary(ctx, sessionID)
		if err == nil && summary != "No active summary." {
			memoryContext.WriteString("SESSION SUMMARY:\n")
			memoryContext.WriteString(summary)
			memoryContext.WriteString("\n\n")
		}
		knownWorld, err := c.memStore.GetKnownWorld(ctx, state.Position)
		if err == nil && knownWorld != "KNOWN WORLD: empty" {
			memoryContext.WriteString(knownWorld)
			memoryContext.WriteString("\n\n")
		}
	}

	tacticalFeedback := ""
	if c.worldModel != nil {
		tacticalFeedback = c.worldModel.GetTacticalFeedback()
		if tacticalFeedback != "" {
			tacticalFeedback += "\n\n"
		}
	}

	userContent := fmt.Sprintf("CURRENT STATE:\n%s\n\n%s%s%sRECENT HISTORY (Learn from these):\n%s\n\n%s\n\nProvide the next JSON intent.",
		stateContext,
		memoryContext.String(),
		milestoneContext,
		tacticalFeedback,
		historyContext,
		skillContext)

	var intent domain.ActionIntent
	var parseErr error

	for attempt := 0; attempt < 3; attempt++ {
		rawResponse, genErr := c.client.Generate(ctx, systemPrompt, userContent)
		if genErr != nil {
			return nil, fmt.Errorf("llm api failure: %w", genErr)
		}

		parseErr = json.Unmarshal([]byte(domain.CleanJSON(rawResponse)), &intent)
		if parseErr == nil && intent.Action != "" {
			if intent.Action == "use_skill" && intent.Target != "" && c.vector != nil {
				skill, err := c.vector.RetrieveNamedSkill(ctx, intent.Target)
				if err == nil && skill != nil {
					intent.SkillName = skill.Name
				} else {
					parseErr = fmt.Errorf("skill '%s' not found", intent.Target)
					userContent += fmt.Sprintf("\n\nSYSTEM: Skill '%s' not found. Choose from the AVAILABLE SKILLS list or use a standard action.", intent.Target)
					continue
				}
			}
			break
		}
		userContent += fmt.Sprintf("\n\nSYSTEM: Your last response failed to parse as JSON or missed the required 'action' field. Error: %v. You must return strictly valid JSON.", parseErr)
	}

	if parseErr != nil || intent.Action == "" {
		return nil, fmt.Errorf("failed to generate valid intent after 3 attempts: %w", parseErr)
	}
	if intent.ID == "" {
		intent.ID = fmt.Sprintf("intent-%d", time.Now().UnixNano())
	}

	return &intent, nil
}

// ────────────────────────────────────────────────────────────────────────────
// SynthesizeCode — called after a task succeeds to grow the skill library.
// Implements CodeSynthesizer. Service calls this via type assertion, so the
// Curriculum interface itself doesn't change.
// ────────────────────────────────────────────────────────────────────────────

const codeSynthesisSystem = `You are a Minecraft bot programmer. Your output will be executed directly by a Mineflayer bot runtime.

RUNTIME CONTEXT:
- Your code runs inside an async function body.
- The following bindings are injected into scope:
  - bot      — the Mineflayer bot instance
  - goals    — mineflayer-pathfinder goals (GoalBlock, GoalNear, GoalNearXZ, GoalFollow, GoalLookAtBlock, GoalY, GoalXZ)
  - Vec3     — the prismarine-math Vec3 class
  - signal   — an AbortSignal; check signal.aborted periodically in long loops

RULES:
1. Do NOT use require() or import. All necessary APIs are already in scope via bot.* and the injected bindings.
2. Navigation: await bot.pathfinder.goto(goal) — use goals.GoalNear, goals.GoalBlock, etc.
3. Mining: const block = bot.findBlock({ matching: ..., maxDistance: 64 }); await bot.dig(block);
4. Inventory: bot.inventory.items() — returns Item[]; each item has .name, .count, .type
5. Collecting drops: await bot.collectBlock.collect(block) — preferred over manual dig+pickup
6. Throw errors with descriptive ALL_CAPS strings: throw new Error('NO_LOGS_FOUND')
7. Check signal.aborted in any loop: if (signal.aborted) throw new Error('aborted')
8. Keep code under 80 lines. Prefer simple, linear logic over complex error recovery.
9. Output ONLY the JavaScript code body — no function declaration wrapper, no markdown fences.`

// SynthesizeCode asks the LLM to write JavaScript that implements the given completed intent.
// before/after are passed so the LLM can see what actually changed and write accurate code.
func (c *AutonomousCurriculum) SynthesizeCode(ctx context.Context, intent domain.ActionIntent, before, after domain.GameState) (string, error) {
	invDiff := buildInventoryDiff(before, after)
	positionDelta := fmt.Sprintf("moved %.1f blocks (%.1f, %.1f, %.1f) → (%.1f, %.1f, %.1f)",
		distance(before.Position, after.Position),
		before.Position.X, before.Position.Y, before.Position.Z,
		after.Position.X, after.Position.Y, after.Position.Z,
	)

	userContent := fmt.Sprintf(`TASK THAT SUCCEEDED:
Action: %s
Target: %s
Count: %d
Rationale: %s

WHAT ACTUALLY HAPPENED (use this to write accurate code):
Inventory change: %s
Position: %s
Health before/after: %.0f / %.0f

Write JavaScript code that performs this task reliably for future runs.
The code should handle the case where the target might not be immediately visible.`,
		intent.Action,
		intent.Target,
		intent.Count,
		intent.Rationale,
		invDiff,
		positionDelta,
		before.Health, after.Health,
	)

	rawCode, err := c.client.Generate(ctx, codeSynthesisSystem, userContent)
	if err != nil {
		return "", fmt.Errorf("code synthesis llm failure: %w", err)
	}

	// Strip any markdown fences the LLM might have added despite instructions.
	code := domain.CleanJSON(rawCode) // reuse the JSON fence stripper
	code = strings.TrimPrefix(code, "```javascript")
	code = strings.TrimPrefix(code, "```js")
	code = strings.TrimPrefix(code, "```")
	code = strings.TrimSuffix(code, "```")
	code = strings.TrimSpace(code)

	return code, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func buildRetrievalQuery(state domain.GameState) string {
	var priorities []string
	if state.Health <= 10 {
		priorities = append(priorities, "survival, healing, retreating")
	}
	if state.Food <= 8 {
		priorities = append(priorities, "finding food, eating, hunting")
	}
	if state.TimeOfDay >= 13000 && state.TimeOfDay <= 23000 {
		priorities = append(priorities, "shelter, defense, avoiding mobs at night")
	}
	if len(priorities) == 0 {
		priorities = append(priorities, "progression, resource gathering, crafting tools")
	}
	return fmt.Sprintf("State - Health: %.0f/20, Food: %.0f/20. Priority focus: %s.",
		state.Health, state.Food, strings.Join(priorities, " and "))
}

func buildInventoryDiff(before, after domain.GameState) string {
	bInv := make(map[string]int)
	for _, item := range before.Inventory {
		bInv[item.Name] += item.Count
	}
	aInv := make(map[string]int)
	for _, item := range after.Inventory {
		aInv[item.Name] += item.Count
	}

	var gained, lost []string
	allItems := make(map[string]struct{})
	for k := range bInv {
		allItems[k] = struct{}{}
	}
	for k := range aInv {
		allItems[k] = struct{}{}
	}
	for item := range allItems {
		diff := aInv[item] - bInv[item]
		if diff > 0 {
			gained = append(gained, fmt.Sprintf("+%d %s", diff, item))
		} else if diff < 0 {
			lost = append(lost, fmt.Sprintf("%d %s", diff, item))
		}
	}

	var parts []string
	if len(gained) > 0 {
		parts = append(parts, "gained: "+strings.Join(gained, ", "))
	}
	if len(lost) > 0 {
		parts = append(parts, "lost: "+strings.Join(lost, ", "))
	}
	if len(parts) == 0 {
		return "no inventory change"
	}
	return strings.Join(parts, "; ")
}

func distance(a, b domain.Vec3) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	dz := a.Z - b.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
