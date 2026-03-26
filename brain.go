package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Priority int

const (
	PriReflex  Priority = 0
	PriRoutine Priority = 1
	PriLLM     Priority = 2
	PriIdle    Priority = 3
)

type Tick struct {
	State json.RawMessage
}

type Target struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type Action struct {
	ID        string       `json:"id,omitempty"`
	Source    string       `json:"source,omitempty"`
	Trace     TraceContext `json:"-"`
	Action    string       `json:"action"`
	Target    Target       `json:"target"`
	Rationale string       `json:"rationale"`
	Priority  Priority     `json:"priority"`
}

type MilestonePlan struct {
	ID             string `json:"id"`
	Description    string `json:"description"`
	CompletionHint string `json:"completion_hint"`
}

type FlexBool bool

func (f *FlexBool) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		*f = FlexBool(b)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = FlexBool(s == "true" || s == "True" || s == "1")
		return nil
	}
	return nil
}

type LLMPlan struct {
	Milestone         *MilestonePlan `json:"milestone"`
	Objective         string         `json:"objective"`
	CandidatePlans    [][]Action     `json:"candidate_plans"`
	Tasks             []Action       `json:"-"` // Simulator populates this
	MilestoneComplete FlexBool       `json:"milestone_complete"`
}

type Brain interface {
	GeneratePlan(ctx context.Context, t Tick, sessionID, systemOverride string, currentMilestone *MilestonePlan, attempt int) (*LLMPlan, error)
}

type LLMBrain struct {
	apiURL    string
	model     string
	apiKey    string
	client    *http.Client
	memory    MemoryBank
	telemetry *Telemetry
}

func NewLLMBrain(apiURL, model, apiKey string, mem MemoryBank, tel *Telemetry) *LLMBrain {
	return &LLMBrain{
		apiURL:    apiURL,
		model:     model,
		apiKey:    apiKey,
		memory:    mem,
		telemetry: tel,
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

const BaseSystemRules = `You are the tactical commander of an autonomous Minecraft agent.
Do NOT worry about eating, sleeping, or combat reflexes; the lower-level systems handle that automatically.
CRITICAL GAME MECHANIC RULES:
1. Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
2. You CANNOT gather stone or coal without a wooden_pickaxe.
3. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM per variant.
4. In the FIRST 10 MINUTES or if you have ZERO logs, ALWAYS start with "gather" (target "wood" or specific log).
NEVER start with explore.
5. ERROR RECOVERY: Only use "explore" if a task fails with PATH_FAILED, STUCK, or NO_BLOCKS.
6. For "explore" ALWAYS use: {"type": "none", "name": "none"}
7. If FOOD is listed in inventory AND hunger < 5: ONLY valid action is "eat" with target type "category" and name = exact item name from inventory.
SPATIAL AWARENESS RULES:
1. Targets in 'VISIBLE POIs' can be interacted with immediately.
2. Targets in 'KNOWN WORLD' are out of sight. You MUST use 'recall_location' to navigate to them BEFORE you can interact with them.
3. Example: [ {"action": "recall_location", "target": {"type": "location", "name": "crafting_table"}}, {"action": "interact", "target": {"type": "recipe", "name": "crafting_table"}} ]

VALID TARGET TYPES (you MUST use one of these):
- "block"     (oak_log, stone, etc.)
- "entity"    (zombie, cow, etc.)
- "recipe"    (stick, wooden_pickaxe, crafting_table)
- "location"  (only for mark/recall)
- "category"  (food, wood)
- "none"      (only for explore/idle/retreat/sleep)

Valid macro actions: gather, craft, hunt, explore, build, smelt, mine, farm, mark_location, recall_location, idle, sleep, retreat, eat.`

func summarizeState(raw json.RawMessage) string {
	var state GameState
	if err := json.Unmarshal(raw, &state); err != nil {
		return string(raw)
	}

	var junkCount int
	var tools, food, mats, rawFoodNames []string

	junkBlocks := map[string]bool{"dirt": true, "stone": true, "granite": true, "andesite": true, "diorite": true, "cobbled_deepslate": true}

	for _, item := range state.Inventory {
		if junkBlocks[item.Name] {
			junkCount += item.Count
			continue
		}
		if strings.Contains(item.Name, "pickaxe") || strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") || strings.Contains(item.Name, "shovel") {
			tools = append(tools, fmt.Sprintf("1x %s", item.Name))
		} else if strings.Contains(item.Name, "beef") || strings.Contains(item.Name, "porkchop") || strings.Contains(item.Name, "apple") || strings.Contains(item.Name, "bread") || strings.Contains(item.Name, "rotten_flesh") || strings.Contains(item.Name, "mutton") || strings.Contains(item.Name, "chicken") || strings.Contains(item.Name, "rabbit") || strings.Contains(item.Name, "cod") || strings.Contains(item.Name, "salmon") || strings.HasPrefix(item.Name, "cooked_") || strings.Contains(item.Name, "melon_slice") || strings.Contains(item.Name, "sweet_berries") || strings.Contains(item.Name, "cookie") || strings.Contains(item.Name, "pumpkin_pie") {
			food = append(food, fmt.Sprintf("%dx %s", item.Count, item.Name))
			rawFoodNames = append(rawFoodNames, item.Name)
		} else {
			mats = append(mats, fmt.Sprintf("%dx %s", item.Count, item.Name))
		}
	}

	toolStr := "none"
	if len(tools) > 0 {
		toolStr = strings.Join(tools, ", ")
	}
	foodStr := "none"
	if len(food) > 0 {
		foodStr = strings.Join(food, ", ")
	}
	matStr := "none"
	if len(mats) > 0 {
		matStr = strings.Join(mats, ", ")
	}

	threatStr := "none"
	if len(state.Threats) > 0 {
		var tn []string
		for _, t := range state.Threats {
			tn = append(tn, t.Name)
		}
		threatStr = strings.Join(tn, ", ")
	}

	timeOfDay := "day"
	if state.TimeOfDay > 12541 && state.TimeOfDay < 23000 {
		timeOfDay = "night"
	}

	poiStr := "none"
	if len(state.POIs) > 0 {
		sort.Slice(state.POIs, func(i, j int) bool {
			return state.POIs[i].Score > state.POIs[j].Score
		})

		var pStrs []string
		limit := 5
		if len(state.POIs) < limit {
			limit = len(state.POIs)
		}

		for i := 0; i < limit; i++ {
			p := state.POIs[i]
			pStrs = append(pStrs, fmt.Sprintf("%s (%.1fm, score:%d, pos:%s)", p.Name, p.Distance, p.Score, p.Direction))
		}
		poiStr = strings.Join(pStrs, ", ")
	}

	base := fmt.Sprintf("HEALTH: %.1f/20  HUNGER: %.1f/20  POS: %.0f,%.0f,%.0f  TIME: %s\nVISIBLE POIs: %s\nTHREATS: %s\nTOOLS: %s\nFOOD: %s\nMATERIALS: %s\nJUNK: %d blocks (ignored)",
		state.Health, state.Food, state.Position.X, state.Position.Y, state.Position.Z, timeOfDay,
		poiStr, threatStr, toolStr, foodStr, matStr, junkCount)

	if state.Food == 0 && len(rawFoodNames) > 0 {
		return fmt.Sprintf(
			"⚠️ CRITICAL: HUNGER=0. Food IS in inventory: %s. You MUST use action \"eat\", target type \"category\", target name \"%s\". DO NOT hunt. DO NOT gather.\n\n%s",
			strings.Join(food, ", "), rawFoodNames[0], base,
		)
	}

	return base
}

func (b *LLMBrain) GeneratePlan(ctx context.Context, t Tick, sessionID, systemOverride string, currentMilestone *MilestonePlan, attempt int) (*LLMPlan, error) {
	var summary, history, worldModel string
	var wg sync.WaitGroup

	var state GameState
	_ = json.Unmarshal(t.State, &state)

	wg.Add(3)
	go func() {
		defer wg.Done()
		summary, _ = b.memory.GetSummary(ctx, sessionID)
	}()
	go func() {
		defer wg.Done()
		history, _ = b.memory.GetRecentContext(ctx, sessionID, 6)
	}()
	go func() {
		defer wg.Done()
		worldModel, _ = b.memory.GetKnownWorld(ctx, state.Position.X, state.Position.Y, state.Position.Z)
	}()
	wg.Wait()

	milestoneSection := "No active milestone. You MUST generate a new one by populating the 'milestone' JSON object."
	if currentMilestone != nil {
		milestoneSection = fmt.Sprintf(`ACTIVE MILESTONE: %s
COMPLETION HINT: %s

CRITICAL MILESTONE RULES:
1. If this milestone is NOT complete, you MUST return the EXACT same milestone object in your response and set 'milestone_complete' to false.
2. Generate tasks that continue progress toward this active milestone.
3. If the milestone IS complete, set 'milestone_complete' to true.`, currentMilestone.Description, currentMilestone.CompletionHint)
	}

	compactState := summarizeState(t.State)

	systemPrompt := fmt.Sprintf(`%s

Response format (JSON only):
{
  "milestone": { "id": "milestone-slug", "description": "...", "completion_hint": "..." },
  "milestone_complete": false,
  "objective": "Sub-goal description",
  "candidate_plans": [
    [ { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "Variant 1: Closest target" } ],
    [ { "action": "explore", "target": { "type": "none", "name": "none" }, "rationale": "Variant 2: Find better resources" } ],
    [ { "action": "craft", "target": { "type": "recipe", "name": "stick" }, "rationale": "Variant 3: Pre-craft needed items" } ]
  ]
}

CRITICAL: You MUST generate 2 to 3 distinct tactical approaches in the 'candidate_plans' array.
The internal simulation engine will evaluate them against physics and logic to pick the optimal path.
%s
SUMMARY: %s
MEMORY: %s
%s
OVERRIDE: %s`, BaseSystemRules, milestoneSection, summary, history, worldModel, systemOverride)

	return b.callLLMForPlan(ctx, systemPrompt, compactState, attempt)
}

func (b *LLMBrain) callLLMForPlan(ctx context.Context, systemPrompt, userContent string, attempt int) (*LLMPlan, error) {
	temps := map[int]float64{1: 0.1, 2: 0.25, 3: 0.40}
	temp, exists := temps[attempt]
	if !exists {
		temp = 0.50
	}

	payload := map[string]interface{}{
		"model": b.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     temp,
	}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)

	start := time.Now()
	resp, err := b.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		b.telemetry.RecordLLMCall(b.model, latency, 0, 0, err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
		b.telemetry.RecordLLMCall(b.model, latency, 0, 0, err)
		return nil, err
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.telemetry.RecordLLMCall(b.model, latency, 0, 0, err)
		return nil, err
	}

	if len(result.Choices) == 0 {
		err := fmt.Errorf("no choices returned")
		b.telemetry.RecordLLMCall(b.model, latency, 0, 0, err)
		return nil, err
	}

	b.telemetry.RecordLLMCall(b.model, latency, result.Usage.PromptTokens, result.Usage.CompletionTokens, nil)

	var plan LLMPlan
	if err := json.Unmarshal([]byte(cleanJSON(result.Choices[0].Message.Content)), &plan); err != nil {
		return nil, err
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
