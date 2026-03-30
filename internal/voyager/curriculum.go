package voyager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
	CreateEmbedding(ctx context.Context, input string) ([]float32, error)
}

type Curriculum interface {
	ProposeTask(ctx context.Context, state domain.GameState, memory []domain.TaskHistory) (*domain.ActionIntent, error)
}

type AutonomousCurriculum struct {
	client LLMClient
	vector VectorStore
}

func NewAutonomousCurriculum(client LLMClient, vector VectorStore) *AutonomousCurriculum {
	return &AutonomousCurriculum{
		client: client,
		vector: vector,
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
	"rationale": "Brief explanation of why this is the best next step based on state and history.",
	"action": "gather",
	"target": "oak_log",
	"count": 4
}`

func (c *AutonomousCurriculum) ProposeTask(ctx context.Context, state domain.GameState, memory []domain.TaskHistory) (*domain.ActionIntent, error) {
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

	// 1. Asynchronously save previous successful skill to vector memory
	if len(recentHistory) > 0 {
		lastTask := recentHistory[len(recentHistory)-1]
		if lastTask.Success && c.vector != nil {
			// Detach from request context so save completes even if evaluation context cancels
			go func(task domain.TaskHistory, finalState domain.GameState) {
				saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				desc := fmt.Sprintf("Successfully executed '%s %d %s' when health was %.0f and time was %d", task.Intent.Action, task.Intent.Count, task.Intent.Target, finalState.Health, finalState.TimeOfDay)
				descEmb, err := c.client.CreateEmbedding(saveCtx, desc)
				if err == nil && len(descEmb) > 0 {
					_ = c.vector.SaveSkill(saveCtx, desc, task.Intent, descEmb)
				}
			}(lastTask, state)
		}
	}

	// 2. Fetch Relevant Skills via Semantic Search
	stateContext := formatStateForLLM(state)
	queryText := fmt.Sprintf("Current Goal: Progression. Health: %.0f. Inventory focus: Tools and Food.", state.Health)
	queryEmb, err := c.client.CreateEmbedding(ctx, queryText)

	skillContext := ""
	if err == nil && c.vector != nil && len(queryEmb) > 0 {
		skills, _ := c.vector.RetrieveSkills(ctx, queryEmb, 3)
		if len(skills) > 0 {
			var skillStrs []string
			for _, s := range skills {
				skillStrs = append(skillStrs, fmt.Sprintf("- Situation: %s -> Intent: %s %d %s", s.Description, s.Intent.Action, s.Intent.Count, s.Intent.Target))
			}
			skillContext = "RELEVANT PAST SUCCESSES:\n" + strings.Join(skillStrs, "\n")
		}
	}

	userContent := fmt.Sprintf("CURRENT STATE:\n%s\n\nRECENT HISTORY (Learn from these):\n%s\n\n%s\n\nProvide the next JSON intent.", stateContext, historyContext, skillContext)

	// 3. LLM Generation with Schema Retry Loop
	var intent domain.ActionIntent
	var parseErr error

	for attempt := 0; attempt < 3; attempt++ {
		rawResponse, genErr := c.client.Generate(ctx, SystemPrompt, userContent)
		if genErr != nil {
			return nil, fmt.Errorf("llm api failure: %w", genErr)
		}

		parseErr = json.Unmarshal([]byte(cleanJSON(rawResponse)), &intent)
		if parseErr == nil && intent.Action != "" {
			break
		}

		// Append error to user content for self-correction
		userContent += fmt.Sprintf("\n\nSYSTEM: Your last response failed to parse as JSON or missed the required 'action' field. Error: %v. You must return strictly valid JSON.", parseErr)
	}

	if parseErr != nil || intent.Action == "" {
		return nil, fmt.Errorf("failed to generate valid intent after 3 attempts: %w", parseErr)
	}

	return &intent, nil
}

func formatStateForLLM(state domain.GameState) string {
	var inv []string
	for _, item := range state.Inventory {
		if item.Count > 0 {
			inv = append(inv, fmt.Sprintf("%s:%d", item.Name, item.Count))
		}
	}

	var pois []string
	for i, p := range state.POIs {
		if i >= 5 {
			break
		}
		pois = append(pois, fmt.Sprintf("%s (%.0fm)", p.Name, p.Distance))
	}

	var chests []string
	for pos, items := range state.KnownChests {
		var cInv []string
		for _, item := range items {
			if item.Count > 0 {
				cInv = append(cInv, fmt.Sprintf("%s:%d", item.Name, item.Count))
			}
		}
		if len(cInv) > 0 {
			chests = append(chests, fmt.Sprintf("Chest at [%s]: %s", pos, strings.Join(cInv, ", ")))
		} else {
			chests = append(chests, fmt.Sprintf("Chest at [%s]: EMPTY", pos))
		}
	}

	knownChestsContext := "None discovered."
	if len(chests) > 0 {
		knownChestsContext = strings.Join(chests, "\n")
	}

	return fmt.Sprintf("Health: %.0f/20\nFood: %.0f/20\nInventory: %s\nVisible POIs: %s\nKnown Chests:\n%s",
		state.Health, state.Food, strings.Join(inv, ", "), strings.Join(pois, ", "), knownChestsContext)
}

func cleanJSON(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	return strings.TrimSpace(cleaned)
}
