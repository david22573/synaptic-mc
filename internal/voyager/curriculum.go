// internal/voyager/curriculum.go
package voyager

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
)

//go:embed prompts/curriculum.tmpl
var curriculumSystemPrompt string

//go:embed prompts/synthesis.tmpl
var codeSynthesisSystemPrompt string

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

func (c *AutonomousCurriculum) ProposeTask(ctx context.Context, state domain.GameState, mem []domain.TaskHistory, milestoneContext, sessionID string, horizon int) (*domain.ActionIntent, error) {
	if state.Health <= 0 {
		return nil, fmt.Errorf("bot is dead, waiting for respawn")
	}

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
		rawResponse, genErr := c.client.Generate(ctx, curriculumSystemPrompt, userContent)
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
		// LOG FAILURE FOR DEBUGGING
		fmt.Printf("[DEBUG] Curriculum LLM Parse Failure (Attempt %d):\nRaw Response: %s\nParse Error: %v\n", attempt, rawResponse, parseErr)
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

	rawCode, err := c.client.Generate(ctx, codeSynthesisSystemPrompt, userContent)
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
