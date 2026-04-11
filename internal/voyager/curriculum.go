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
	ProposeTask(ctx context.Context, state domain.GameState, memory []domain.TaskHistory, sessionID string) (*domain.ActionIntent, error)
}

type AutonomousCurriculum struct {
	client   LLMClient
	vector   VectorStore
	memStore memory.Store
}

func NewAutonomousCurriculum(client LLMClient, vector VectorStore, memStore memory.Store) *AutonomousCurriculum {
	return &AutonomousCurriculum{
		client:   client,
		vector:   vector,
		memStore: memStore,
	}
}

const SystemPrompt = `You are the Curriculum Agent for an autonomous Minecraft bot.
Your job is to evaluate the current Game State, review recent task history, and propose EXACTLY ONE optimal next intent to advance the bot's progression or ensure its survival.

CRITICAL RULES:
1.  SURVIVAL FIRST: If health < 10, your intent MUST be 'eat' or 'retreat'.
2.  PREREQUISITES: You cannot mine stone without a wooden_pickaxe. You cannot craft a pickaxe without sticks and planks.
3.  COUNT: The 'count' field determines how many of the target item the bot will attempt to gather/craft. Keep it reasonable (1-10).
4.  STORAGE: If inventory is full, use the 'store' action to put items in a chest.
5.  RETRIEVAL: If you need an item and it is in a known chest, use the 'retrieve' action.
6.  BUILDING: If you need physical protection through the night, use the 'build' action with target 'shelter' to construct a 3x3 bunker. You need at least 20 blocks (dirt, planks, or cobblestone).

AVAILABLE ACTIONS: "gather", "craft", "mine", "smelt", "hunt", "explore", "eat", "retreat", "store", "retrieve", "build", "farm"

OUTPUT FORMAT (Strict JSON):
{
	"id": "intent-123",
	"rationale": "Brief explanation of why this is the best next step based on state and history.",
	"action": "gather",
	"target": "oak_log",
	"count": 4
}`

func (c *AutonomousCurriculum) ProposeTask(ctx context.Context, state domain.GameState, memory []domain.TaskHistory, sessionID string) (*domain.ActionIntent, error) {
	if state.Health <= 0 {
		return nil, fmt.Errorf("bot is dead, waiting for respawn")
	}

	var historyStrs []string
	recentHistory := memory
	if len(memory) > 5 {
		recentHistory = memory[len(memory)-5:]
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

	if len(recentHistory) > 0 {
		lastTask := recentHistory[len(recentHistory)-1]
		if lastTask.Success && c.vector != nil {
			go func(task domain.TaskHistory, finalState domain.GameState) {
				saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				desc := fmt.Sprintf("Successfully executed '%s %d %s' when health was %.0f and time was %d", task.Intent.Action, task.Intent.Count, task.Intent.Target, finalState.Health, finalState.TimeOfDay)
				descEmb, err := c.client.CreateEmbedding(saveCtx, desc)
				if err == nil && len(descEmb) > 0 {
					intentJSON, _ := json.Marshal(task.Intent)
					_ = c.vector.SaveSkill(saveCtx, desc, string(intentJSON), descEmb)
				}
			}(lastTask, state)
		}
	}

	stateContext := domain.FormatStateForLLM(state)
	queryText := buildRetrievalQuery(state)
	queryEmb, err := c.client.CreateEmbedding(ctx, queryText)

	skillContext := ""
	if err == nil && c.vector != nil && len(queryEmb) > 0 {
		skills, _ := c.vector.RetrieveSkills(ctx, queryEmb, 3)
		if len(skills) > 0 {
			var skillStrs []string
			for _, s := range skills {
				var intent domain.ActionIntent
				if err := json.Unmarshal([]byte(s.Code), &intent); err == nil {
					skillStrs = append(skillStrs, fmt.Sprintf("- Situation: %s -> Intent: %s %d %s", s.Description, intent.Action, intent.Count, intent.Target))
				} else {
					skillStrs = append(skillStrs, fmt.Sprintf("- Situation: %s -> Code: %s", s.Description, s.Code))
				}
			}
			skillContext = "RELEVANT PAST SUCCESSES:\n" + strings.Join(skillStrs, "\n")
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

	userContent := fmt.Sprintf("CURRENT STATE:\n%s\n\n%sRECENT HISTORY (Learn from these):\n%s\n\n%s\n\nProvide the next JSON intent.",
		stateContext,
		memoryContext.String(),
		historyContext,
		skillContext)

	var intent domain.ActionIntent
	var parseErr error

	for attempt := 0; attempt < 3; attempt++ {
		rawResponse, genErr := c.client.Generate(ctx, SystemPrompt, userContent)
		if genErr != nil {
			return nil, fmt.Errorf("llm api failure: %w", genErr)
		}

		parseErr = json.Unmarshal([]byte(domain.CleanJSON(rawResponse)), &intent)
		if parseErr == nil && intent.Action != "" {
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

	// FIX: Align with survival.ts threshold (bot starts panicking about food around 8)
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
