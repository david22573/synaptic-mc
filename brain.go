package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
1. Progression MUST be: logs -> planks -> sticks -> crafting_table -> wooden_pickaxe.
2. You CANNOT gather stone or coal without a wooden_pickaxe.
3. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM.
4. In the FIRST 10 MINUTES or if you have ZERO logs, ALWAYS start with "gather" (target "wood" or specific log). NEVER start with explore.
5. ERROR RECOVERY: Only use "explore" if a task fails with PATHING_FAILED, PATHFINDER_TIMEOUT, or EXHAUSTED_CANDIDATES.
6. For "explore" ALWAYS use: {"type": "none", "name": "none"}

VALID TARGET TYPES (you MUST use one of these):
- "block"     (oak_log, stone, etc.)
- "entity"    (zombie, cow, etc.)
- "recipe"    (stick, wooden_pickaxe, crafting_table)
- "location"  (only for mark/recall)
- "category"  (food, wood)
- "none"      (only for explore/idle/retreat/sleep)

Valid macro actions: gather, craft, hunt, explore, build, smelt, mark_location, recall_location, idle, sleep, retreat, eat.`

func (b *LLMBrain) GenerateMilestone(ctx context.Context, t Tick, sessionID string) (*MilestonePlan, error) {
	summary, _ := b.memory.GetSummary(ctx, sessionID)
	systemPrompt := fmt.Sprintf(`%s

Generate a new high-level Milestone.
Response format (JSON only):
{
  "id": "milestone-<short-slug>",
  "description": "Clear goal",
  "completion_hint": "Inventory/world state trigger"
}

ACTIVE CONTEXT: %s`, BaseSystemRules, summary)
	return b.callLLMForMilestone(ctx, systemPrompt, string(t.State))
}

func (b *LLMBrain) EvaluatePlan(ctx context.Context, t Tick, sessionID, systemOverride string, milestone *MilestonePlan) (*LLMPlan, error) {
	summary, _ := b.memory.GetSummary(ctx, sessionID)
	history, _ := b.memory.GetRecentContext(ctx, sessionID, 6)
	milestoneSection := "Focus on basic survival."
	if milestone != nil {
		milestoneSection = fmt.Sprintf("MILESTONE: %s\nCOMPLETION: %s", milestone.Description, milestone.CompletionHint)
	}

	systemPrompt := fmt.Sprintf(`%s

Generate 1-3 sequential tasks for the MILESTONE.
Response format (JSON only):
{
  "objective": "Sub-goal description",
  "milestone_complete": false,
  "tasks": [ { "action": "gather", "target": { "type": "block", "name": "oak_log" }, "rationale": "..." } ]
}

%s
SUMMARY: %s
MEMORY: %s
OVERRIDE: %s`, BaseSystemRules, milestoneSection, summary, history, systemOverride)
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
		return nil, err
	}
	return &milestone, nil
}

func (b *LLMBrain) callLLMForPlan(ctx context.Context, systemPrompt, userContent string) (*LLMPlan, error) {
	content, latency, inTok, outTok, err := b.doLLMRequest(ctx, systemPrompt, userContent)
	b.telemetry.RecordLLMCall(b.model, latency, inTok, outTok, err)
	if err != nil {
		return nil, err
	}
	var plan LLMPlan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return nil, err
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
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)

	start := time.Now()
	resp, err := b.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return "", latency, 0, 0, err
	}
	defer resp.Body.Close()

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
		return "", latency, 0, 0, err
	}

	if len(result.Choices) == 0 {
		return "", latency, 0, 0, fmt.Errorf("no choices returned")
	}

	return result.Choices[0].Message.Content, latency, result.Usage.PromptTokens, result.Usage.CompletionTokens, nil
}
