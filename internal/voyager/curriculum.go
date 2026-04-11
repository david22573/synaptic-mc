// internal/voyager/curriculum.go
package voyager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
)

type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
	CreateEmbedding(ctx context.Context, input string) ([]float32, error)
}

type Curriculum interface {
	ProposeTask(ctx context.Context, state domain.GameState, memory []domain.TaskHistory, milestoneContext, sessionID string, horizon int) (*domain.ActionIntent, error)
}

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

const SystemPrompt = `You are the Curriculum Agent for an autonomous Minecraft bot.
Your job is to evaluate the current Game State, review recent task history and progression status, and propose an optimal plan to advance the bot's progression or ensure its survival.

CRITICAL RULES:
1.  SURVIVAL FIRST: If health < 10, your plan MUST be 'eat' or 'retreat'.
2.  PREREQUISITES: You cannot mine stone without a wooden_pickaxe. You cannot craft a pickaxe without sticks and planks.
3.  COUNT: The 'count' field determines how many of the target item the bot will attempt to gather/craft. Keep it reasonable (1-10).
4.  STORAGE: If inventory is full, use the 'store' action to put items in a chest.
5.  RETRIEVAL: If you need an item and it is in a known chest, use the 'retrieve' action.
6.  BUILDING: If you need physical protection through the night, use the 'build' action with target 'shelter'.
7.  SKILLS: If an 'AVAILABLE SKILL' is perfect, use action: "use_skill" and specify the skill name in "target".

AVAILABLE ACTIONS: "gather", "craft", "mine", "smelt", "hunt", "explore", "eat", "retreat", "store", "retrieve", "build", "farm", "use_skill"

OUTPUT FORMAT (Strict JSON):
{
	"id": "intent-123",
	"rationale": "Brief explanation of why this is the best next step.",
	"action": "acquire_wooden_pickaxe",
	"target": "wooden_pickaxe"
}`

func (c *AutonomousCurriculum) ProposeTask(ctx context.Context, state domain.GameState, memory []domain.TaskHistory, milestoneContext, sessionID string, horizon int) (*domain.ActionIntent, error) {
	if state.Health <= 0 {
		return nil, fmt.Errorf("bot is dead, waiting for respawn")
	}

	systemPrompt := fmt.Sprintf(SystemPrompt, horizon)

	var historyStrs []string
	recentHistory := memory
	if len(memory) > 20 {
		recentHistory = memory[len(memory)-20:]
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
				// Unmarshal into the new ExecutableSkill structure
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
					userContent += fmt.Sprintf("\n\nSYSTEM: Skill '%s' not found. Choose from the AVAILABLE SKILLS list.", intent.Target)
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
		state.Health,
		state.Food,
		strings.Join(priorities, " and "))
}
