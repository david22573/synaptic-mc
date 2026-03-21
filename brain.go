package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

type LLMPlan struct {
	Objective         string   `json:"objective"`
	Tasks             []Action `json:"tasks"`
	MilestoneComplete bool     `json:"milestone_complete"`
}

type Brain interface {
	GenerateMilestone(ctx context.Context, t Tick, sessionID string) (*MilestonePlan, error)
	EvaluatePlan(ctx context.Context, t Tick, sessionID, systemOverride string, milestone *MilestonePlan) (*LLMPlan, error)
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
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

const BaseSystemRules = `You are the tactical commander of an autonomous Minecraft agent.
Do NOT worry about eating, sleeping, or combat reflexes; the lower-level systems handle that automatically.
CRITICAL GAME MECHANIC RULES:
1. You CANNOT gather cobblestone, stone, or coal without a wooden_pickaxe.
Do not ask to gather stone if it's not in the inventory.
2. You CANNOT craft tools directly from logs.
Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
3. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM.
4. ERROR RECOVERY (EXPLORE): If a task fails with a cause like 'NO_BLOCK_FOUND', 'NO_..._NEARBY_MUST_EXPLORE', or 'EXHAUSTED_ALL_CANDIDATES', you MUST use the "explore" action next to find a new area.
5. ERROR RECOVERY (CRAFTING): If a task fails with 'MISSING_INGREDIENTS_OR_CRAFTING_TABLE' or 'UNKNOWN_ITEM', you MUST gather the raw materials AND ensure a crafting_table is placed.
DO NOT retry the exact same action immediately if it just failed.

Valid macro actions (Use strictly these):
- "gather": Collect resources (wood, stone).
- "craft": Create items. 
- "hunt": Track and kill entities.
- "explore": Move to new map chunks.
- "build": Place blocks to create structures.
- "smelt": Cook food or ores in a furnace.
- "mark_location": Save current coordinates (target.name is the label, e.g., "base").
- "recall_location": Retrieve coordinates from memory.
- "idle": Wait for routines to finish.
Target types: "block", "entity", "recipe", "location", "category", "none".`

func (b *LLMBrain) GenerateMilestone(ctx context.Context, t Tick, sessionID string) (*MilestonePlan, error) {
	summary, err := b.memory.GetSummary(ctx, sessionID)
	if err != nil {
		summary = "No active summary."
	}

	systemPrompt := fmt.Sprintf(`%s

YOUR ONLY JOB: Generate a new high-level Milestone.

Response format (JSON only):
{
  "id": "milestone-<short-slug>",
  "description": "A clear, one-sentence goal (e.g. 'Craft a full set of iron tools')",
  "completion_hint": "What inventory or world state would confirm this milestone is done"
}

--- ACTIVE CONTEXT (SUMMARY) ---
%s`, BaseSystemRules, summary)

	return b.callLLMForMilestone(ctx, systemPrompt, string(t.State))
}

func (b *LLMBrain) EvaluatePlan(ctx context.Context, t Tick, sessionID, systemOverride string, milestone *MilestonePlan) (*LLMPlan, error) {
	summary, err := b.memory.GetSummary(ctx, sessionID)
	if err != nil {
		summary = "No active summary."
	}

	history, err := b.memory.GetRecentContext(ctx, sessionID, 6)
	if err != nil {
		history = "No recent memory available."
	}

	milestoneSection := "No active milestone. Focus on basic survival."
	if milestone != nil {
		milestoneSection = fmt.Sprintf(
			"MILESTONE: %s\nCOMPLETION CRITERIA: %s",
			milestone.Description,
			milestone.CompletionHint,
		)
	}

	systemPrompt := fmt.Sprintf(`%s

YOUR ONLY JOB: Generate 1-3 sequential tasks that advance the ACTIVE MILESTONE below.
Do NOT switch goals. Do NOT plan beyond the milestone.
If you believe the milestone completion criteria have been met, set "milestone_complete": true.

Response format (JSON only):
{
  "objective": "One sentence describing the immediate sub-goal",
  "milestone_complete": false,
  "tasks": [
    {
      "action": "gather",
      "target": { "type": "block", "name": "oak_log" },
      "rationale": "Need logs to craft planks"
    }
  ]
}

--- ACTIVE MILESTONE ---
%s

--- ACTIVE CONTEXT (SUMMARY) ---
%s

--- RECENT MEMORY ---
%s

--- CRITICAL SYSTEM OVERRIDE ---
%s
---------------------

Analyze the state payload. Respect any SYSTEM OVERRIDE warnings.
Generate 1-3 tasks that progress the active milestone.`,
		BaseSystemRules, milestoneSection, summary, history, systemOverride)

	return b.callLLMForPlan(ctx, systemPrompt, string(t.State))
}

func (b *LLMBrain) callLLMForMilestone(ctx context.Context, systemPrompt, userContent string) (*MilestonePlan, error) {
	content, latency, inTok, outTok, err := b.doLLMRequest(ctx, systemPrompt, userContent)
	b.telemetry.RecordLLMCall(b.model, latency, inTok, outTok, err)
	if err != nil {
		return nil, err
	}

	var milestone MilestonePlan
	if err := json.Unmarshal([]byte(content), &milestone); err != nil {
		return nil, fmt.Errorf("failed to parse milestone JSON: %w", err)
	}
	if milestone.ID == "" || milestone.Description == "" {
		return nil, fmt.Errorf("milestone response missing required fields")
	}
	return &milestone, nil
}

func (b *LLMBrain) callLLMForPlan(ctx context.Context, systemPrompt, userContent string) (*LLMPlan, error) {
	content, latency, inTok, outTok, err := b.doLLMRequest(ctx, systemPrompt, userContent)
	b.telemetry.RecordLLMCall(b.model, latency, inTok, outTok, err)
	if err != nil {
		return nil, err
	}

	log.Printf("[+] AI Latency: %v | Tasks Generated", latency)

	var plan LLMPlan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w", err)
	}

	if len(plan.Tasks) > 3 {
		plan.Tasks = plan.Tasks[:3]
	}

	for i := range plan.Tasks {
		plan.Tasks[i].Priority = PriLLM
	}

	return &plan, nil
}

func (b *LLMBrain) doLLMRequest(ctx context.Context, systemPrompt, userContent string) (string, time.Duration, int, int, error) {
	payload := map[string]interface{}{
		"model": b.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.1,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("HTTP-Referer", "http://localhost:8080")
	req.Header.Set("X-Title", "CraftD Bot Controller")

	start := time.Now()
	resp, err := b.client.Do(req)
	latency := time.Since(start)

	if err != nil {
		return "", latency, 0, 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", latency, 0, 0, fmt.Errorf("API HTTP %d: %s", resp.StatusCode, string(body))
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
		return "", latency, 0, 0, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", latency, 0, 0, fmt.Errorf("no choices returned")
	}

	return result.Choices[0].Message.Content, latency, result.Usage.PromptTokens, result.Usage.CompletionTokens, nil
}
