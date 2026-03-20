// brain.go
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

type Tick struct {
	State json.RawMessage
}

type Target struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type Action struct {
	ID        string `json:"id,omitempty"`
	Action    string `json:"action"`
	Target    Target `json:"target"`
	Rationale string `json:"rationale"`
}

type LLMPlan struct {
	Objective string   `json:"objective"`
	Tasks     []Action `json:"tasks"`
}

type Brain interface {
	EvaluatePlan(ctx context.Context, t Tick, sessionID string) (*LLMPlan, error)
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
		apiKey:    apiKey, // Cached internally to avoid os.Getenv syscalls in the hot path
		memory:    mem,
		telemetry: tel,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (b *LLMBrain) EvaluatePlan(ctx context.Context, t Tick, sessionID string) (*LLMPlan, error) {
	summary, err := b.memory.GetSummary(ctx, sessionID)
	if err != nil {
		summary = "No active summary."
	}

	history, err := b.memory.GetRecentContext(ctx, sessionID, 6)
	if err != nil {
		history = "No recent memory available."
	}

	// Refactored system prompt to enforce macro-level strategic thinking.
	// We explicitly point out the inventory payload to ensure the LLM considers prerequisites.
	systemPrompt := fmt.Sprintf(`You are the strategic commander of an autonomous Minecraft survival agent. 
You operate at a MACRO level. Do not micromanage movement.
You MUST output a valid JSON object. Keep plans STRICTLY SHORT-HORIZON: 1 to 3 tasks MAXIMUM.
The "tasks" field MUST be an array of objects.

Valid macro actions:
- "gather": Collect resources (wood, stone). The bot will automatically path, mine, and pick up drops.
- "craft": Create items. The bot will automatically handle tables and component assembly.
- "hunt": Track and kill entities, then collect their drops.
- "explore": Move to new map chunks.
- "build": Place blocks to create structures.
- "retreat": Move to safety.
- "idle": Wait and observe.

Target types: "block", "entity", "recipe", "direction", "none".

Example format:
{
  "objective": "Acquire basic tools",
  "tasks": [
    { 
      "action": "gather", 
      "target": { "type": "block", "name": "oak_log" }, 
      "rationale": "Need wood to craft a pickaxe" 
    },
    { 
      "action": "craft", 
      "target": { "type": "recipe", "name": "wooden_pickaxe" }, 
      "rationale": "Required to mine stone" 
    }
  ]
}

--- ACTIVE CONTEXT (SUMMARY) ---
%s

--- RECENT MEMORY ---
%s
---------------------

Analyze the state payload (health, position, threats, AND INVENTORY). 
If your inventory lacks raw materials for an objective, generate tasks to gather them first.
Generate a sequential list of 1-3 tasks.`, summary, history)

	payload := map[string]interface{}{
		"model": b.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": string(t.State)},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.1,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("HTTP-Referer", "http://localhost:8080")
	req.Header.Set("X-Title", "CraftD Bot Controller")

	startTime := time.Now()
	resp, err := b.client.Do(req)

	latency := time.Since(startTime)
	b.telemetry.RecordLLMCall(latency, err)

	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned")
	}

	content := result.Choices[0].Message.Content
	log.Printf("[+] AI Latency: %v | Tasks Generated", time.Since(startTime))

	var plan LLMPlan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	if len(plan.Tasks) > 3 {
		plan.Tasks = plan.Tasks[:3]
	}

	return &plan, nil
}
