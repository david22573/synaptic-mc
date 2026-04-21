// internal/llm/client.go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type Config struct {
	APIURL      string
	APIKey      string
	StrongModel string
	CheapModel  string
	EmbedModel  string
	MaxRetries  int
}

type StateSummary struct {
	LastAction string         `json:"last_action"`
	Outcome    string         `json:"outcome"`
	Failures   []string       `json:"recent_failures"`
	Variables  map[string]any `json:"vars"`
}

type Client struct {
	config Config
	http   *http.Client

	// lastState stores the last compressed state sent to the LLM to calculate deltas
	lastStateMu sync.Mutex
	lastStates  map[string]string // sessionID -> lastCompressedJSON

	// Circuit breaker state
	mu          sync.Mutex
	errorCount  int
	lastError   time.Time
	circuitOpen bool
	openUntil   time.Time
}

var ErrCircuitOpen = fmt.Errorf("llm circuit is open")

func NewClient(cfg Config) *Client {
	return &Client{
		config:     cfg,
		lastStates: make(map[string]string),
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (c *Client) IsCircuitOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.circuitOpen && time.Now().After(c.openUntil) {
		c.circuitOpen = false
		c.errorCount = 0
	}
	return c.circuitOpen
}

func (c *Client) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errorCount = 0
	c.circuitOpen = false
}

func (c *Client) recordError() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.Sub(c.lastError) > 30*time.Second {
		c.errorCount = 1
	} else {
		c.errorCount++
	}
	c.lastError = now

	if c.errorCount >= 3 {
		c.circuitOpen = true
		c.openUntil = now.Add(60 * time.Second)
	}
}

// CompressState implements mandatory delta compression and hard caps.
func (c *Client) CompressState(sessionID string, state domain.GameState, events []domain.DomainEvent) string {
	summary := StateSummary{
		Failures:  make([]string, 0),
		Variables: make(map[string]any),
	}

	const maxFailureCount = 3
	const maxInvItems = 8
	const maxThreats = 5
	const maxPOIs = 8

	// 1. Hard caps on collections
	seenFailures := make(map[string]bool)
	for i := len(events) - 1; i >= 0 && len(summary.Failures) < maxFailureCount; i-- {
		ev := events[i]
		if ev.Type == domain.EventTypeTaskEnd {
			var payload domain.TaskEndPayload
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if !payload.Success && payload.Cause != "" && !seenFailures[payload.Cause] {
					summary.Failures = append(summary.Failures, payload.Cause)
					seenFailures[payload.Cause] = true
				}
				if summary.LastAction == "" {
					summary.LastAction = payload.Action
					summary.Outcome = payload.Status
				}
			}
		}
	}

	summary.Variables["hp"] = int(state.Health)
	summary.Variables["food"] = int(state.Food)
	summary.Variables["pos"] = fmt.Sprintf("%.0f,%.0f", state.Position.X, state.Position.Z)

	inv := make(map[string]int)
	for i, item := range state.Inventory {
		if i >= maxInvItems {
			break
		}
		inv[item.Name] = item.Count
	}
	summary.Variables["inv"] = inv

	thr := make([]string, 0, maxThreats)
	for i, t := range state.Threats {
		if i >= maxThreats {
			break
		}
		thr = append(thr, fmt.Sprintf("%s@%.0fm", t.Name, t.Distance))
	}
	if len(thr) > 0 {
		summary.Variables["threats"] = thr
	}

	poi := make([]string, 0, maxPOIs)
	for i, p := range state.POIs {
		if i >= maxPOIs {
			break
		}
		poi = append(poi, p.Name)
	}
	if len(poi) > 0 {
		summary.Variables["pois"] = poi
	}

	currentJSON, _ := json.Marshal(summary)
	currentStr := string(currentJSON)

	c.lastStateMu.Lock()
	defer c.lastStateMu.Unlock()

	lastStr := c.lastStates[sessionID]
	c.lastStates[sessionID] = currentStr

	if lastStr == "" {
		return currentStr
	}

	// Calculate Delta (simple hash check for now to save tokens if "no_change")
	if currentStr == lastStr {
		return "no_change"
	}

	return currentStr
}

func (s *StateSummary) HealthValue() int {
	if v, ok := s.Variables["hp"].(int); ok {
		return v
	}
	if v, ok := s.Variables["hp"].(float64); ok {
		return int(v)
	}
	return 0
}

type ResponseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

type JSONSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

func (c *Client) GenerateWithFormat(ctx context.Context, systemPrompt, userContent string, format *ResponseFormat, useStrongModel bool) (string, error) {
	if c.IsCircuitOpen() {
		return "", ErrCircuitOpen
	}

	model := c.config.CheapModel
	if useStrongModel {
		model = c.config.StrongModel
	}

	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type request struct {
		Model          string          `json:"model"`
		Messages       []message       `json:"messages"`
		ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	}

	reqBody := request{
		Model: model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		ResponseFormat: format,
	}

	data, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", c.config.APIURL, bytes.NewBuffer(data))
	if err != nil {
		return "", err
	}

	// 120 second timeout for slow model providers
	client := &http.Client{Timeout: 120 * time.Second}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		c.recordError()
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.recordError()
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("llm api error (%d): %s", resp.StatusCode, string(body))
	}

	c.recordSuccess()

	type response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if len(res.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from llm")
	}

	return res.Choices[0].Message.Content, nil
}

func (c *Client) Generate(ctx context.Context, systemPrompt, userContent string) (string, error) {
	return c.GenerateWithFormat(ctx, systemPrompt, userContent, nil, true)
}

func (c *Client) GenerateText(ctx context.Context, systemPrompt, userContent string) (string, error) {
	return c.GenerateWithFormat(ctx, systemPrompt, userContent, nil, false)
}

func (c *Client) CreateEmbedding(ctx context.Context, input string) ([]float32, error) {
	if c.IsCircuitOpen() {
		return nil, ErrCircuitOpen
	}

	type request struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}

	reqBody := request{
		Model: c.config.EmbedModel,
		Input: input,
	}

	data, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", c.config.APIURL+"/embeddings", bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		c.recordError()
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.recordError()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm embedding api error (%d): %s", resp.StatusCode, string(body))
	}

	c.recordSuccess()

	type response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	if len(res.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned from llm")
	}

	return res.Data[0].Embedding, nil
}
