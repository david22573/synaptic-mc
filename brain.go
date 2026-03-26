package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Priority int

const (
	PriReflex  Priority = 0
	PriRoutine Priority = 1
	PriLLM     Priority = 2
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
	Tasks             []Action       `json:"tasks"`
	MilestoneComplete FlexBool       `json:"milestone_complete"`
}

type Brain interface {
	GeneratePlan(ctx context.Context, t Tick, sessionID, systemOverride string, currentMilestone *MilestonePlan) (*LLMPlan, error)
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
			Timeout: 30 * time.Second, // Timeout to prevent hangs
		},
	}
}

const BaseSystemRules = `You are the tactical commander of an autonomous Minecraft agent.
Do NOT worry about eating, sleeping, or combat reflexes; the lower-level systems handle that automatically.

CRITICAL GAME MECHANIC RULES:
1. Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
2. You CANNOT gather stone or coal without a wooden_pickaxe.
3. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM.
4. In the FIRST 10 MINUTES or if you have ZERO logs, ALWAYS start with "gather" (target "wood" or specific log). NEVER start with explore.
5. ERROR RECOVERY: Only use "explore" if a task fails with PATHING_FAILED, PATHFINDER_TIMEOUT, or EXHAUSTED_CANDIDATES.
6. For "explore" ALWAYS use: {"type": "none", "name": "none"}
7. If FOOD is listed in inventory AND hunger < 5: ONLY valid action is "eat" with target type "category" and name = exact item name from inventory (e.g., "rotten_flesh"). Hunting is NOT eating. Do NOT plan anything else first.

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
		} else if strings.Contains(item.Name, "beef") || strings.Contains(item.Name, "porkchop") || strings.Contains(item.Name, "apple") || strings.Contains(item.Name, "bread") || strings.Contains(item.Name, "rotten_flesh") || strings.Contains(item.Name, "mutton") || strings.Contains(item.Name, "chicken") || strings.Contains(item.Name, "rabbit") {
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

	nearby := []string{}
	if state.NearbyWood {
		nearby = append(nearby, "wood")
	}
	if state.NearbyStone {
		nearby = append(nearby, "stone")
	}
	if state.NearbyCoal {
		nearby = append(nearby, "coal")
	}
	nearbyStr := "none"
	if len(nearby) > 0 {
		nearbyStr = strings.Join(nearby, ", ")
	}

	base := fmt.Sprintf("HEALTH: %.1f/20  HUNGER: %.1f/20  POS: %.0f,%.0f,%.0f  TIME: %s\nNEARBY: %s\nTHREATS: %s\nTOOLS: %s\nFOOD: %s\nMATERIALS: %s\nJUNK: %d blocks (ignored)",
		state.Health, state.Food, state.Position.X, state.Position.Y, state.Position.Z, timeOfDay,
		nearbyStr, threatStr, toolStr, foodStr, matStr, junkCount)

	if state.Food == 0 && len(rawFoodNames) > 0 {
		return fmt.Sprintf(
			"⚠️ CRITICAL: HUNGER=0. Food IS in inventory: %s. You MUST use action \"eat\", target type \"category\", target name \"%s\". DO NOT hunt. DO NOT gather.\n\n%s",
			strings.Join(food, ", "), rawFoodNames[0], base,
		)
	}

	return base
}

func (b *LLMBrain) GeneratePlan(ctx context.Context, t Tick, sessionID, systemOverride string, currentMilestone *MilestonePlan) (*LLMPlan, error) {
	var summary, history, locations string
	var wg sync.WaitGroup

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
		locations, _ = b.memory.GetKnownLocations(ctx)
	}()
	wg.Wait()

	milestoneSection := "No active milestone. You MUST generate a new one by populating the 'milestone' JSON object."
	if currentMilestone != nil {
		milestoneSection = fmt.Sprintf("ACTIVE MILESTONE: %s\nCOMPLETION HINT: %s\nIf this is complete, set milestone_complete to true. If you need a new milestone, provide it in the 'milestone' field. Otherwise, leave 'milestone' null.", currentMilestone.Description, currentMilestone.CompletionHint)
	}

	compactState := summarizeState(t.State)

	systemPrompt := fmt.Sprintf(`%s

Response format (JSON only):
{
  "milestone": { "id": "milestone-slug", "description": "...", "completion_hint": "..." },
  "milestone_complete": false,
  "objective": "Sub-goal description",
  "tasks": [ { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "..." } ]
}

%s
SUMMARY: %s
MEMORY: %s
%s
OVERRIDE: %s`, BaseSystemRules, milestoneSection, summary, history, locations, systemOverride)

	// Built-in retry loop for handling JSON schema breaks
	var plan *LLMPlan
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		plan, err = b.callLLMForPlan(ctx, systemPrompt, compactState, attempt)
		if err == nil {
			return plan, nil
		}
	}
	return nil, err
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
	for i := range plan.Tasks {
		plan.Tasks[i].Priority = PriLLM
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
