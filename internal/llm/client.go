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
	APIURL     string
	APIKey     string
	Model      string
	EmbedModel string
	MaxRetries int
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
		config: cfg,
		http: &http.Client{
			Timeout: 60 * time.Second,
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

// CompressState implements lossy compression of the world state and event history.
// It prioritizes recent failure causes and critical variables over raw logs.
// This version is strictly token-capped and only includes variables for immediate decisions.
func (c *Client) CompressState(state domain.GameState, events []domain.DomainEvent) string {
	summary := StateSummary{
		Failures:  make([]string, 0),
		Variables: make(map[string]any),
	}

	const maxFailureChars = 64
	const maxKeyItems = 10

	// 1. Extract recent failures (last 3 unique reasons), truncated for token efficiency
	seenFailures := make(map[string]bool)
	for i := len(events) - 1; i >= 0 && len(summary.Failures) < 3; i-- {
		ev := events[i]
		if ev.Type == domain.EventTypeTaskEnd {
			var payload domain.TaskEndPayload
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if !payload.Success && payload.Cause != "" {
					cause := payload.Cause
					if len(cause) > maxFailureChars {
						cause = cause[:maxFailureChars-3] + "..."
					}
					if !seenFailures[cause] {
						summary.Failures = append(summary.Failures, cause)
						seenFailures[cause] = true
					}
				}
				if summary.LastAction == "" {
					action := payload.Action
					if len(action) > 32 {
						action = action[:29] + "..."
					}
					summary.LastAction = action
					summary.Outcome = payload.Status
				}
			}
		}
	}

	// 2. Extract critical state variables
	summary.Variables["health"] = int(state.Health)
	summary.Variables["food"] = int(state.Food)
	summary.Variables["pos"] = fmt.Sprintf("%.0f,%.0f,%.0f", state.Position.X, state.Position.Y, state.Position.Z)

	if len(state.Inventory) > 0 {
		inv := make([]string, 0, maxKeyItems)
		for _, item := range state.Inventory {
			if item.Count > 0 {
				inv = append(inv, fmt.Sprintf("%s:%d", item.Name, item.Count))
			}
			if len(inv) >= maxKeyItems {
				break
			}
		}
		summary.Variables["inv"] = inv
	}

	if len(state.Threats) > 0 {
		summary.Variables["threats"] = len(state.Threats)
	}

	// 3. Serialize to compact JSON
	b, _ := json.Marshal(summary)
	return string(b)
}

func (c *Client) Generate(ctx context.Context, systemPrompt, userContent string) (string, error) {
	if c.IsCircuitOpen() {
		return "", ErrCircuitOpen
	}

	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type request struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
	}

	reqBody := request{
		Model: c.config.Model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
	}

	data, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", c.config.APIURL, bytes.NewBuffer(data))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := c.http.Do(req)
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

func (c *Client) GenerateText(ctx context.Context, systemPrompt, userContent string) (string, error) {
	return c.Generate(ctx, systemPrompt, userContent)
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
