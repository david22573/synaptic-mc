package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

type Config struct {
	APIURL     string
	APIKey     string
	Model      string
	MaxRetries int
}

type Client struct {
	config Config
	http   *http.Client
}

func NewClient(cfg Config) *Client {
	return &Client{
		config: cfg,
		http: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (c *Client) Generate(ctx context.Context, systemPrompt, userContent string) (string, error) {
	payload := map[string]any{
		"model": c.config.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.2,
	}

	jsonPayload, _ := json.Marshal(payload)

	var lastErr error
	baseDelay := 500 * time.Millisecond

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		// FIX: Check context cancellation before each attempt
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("request cancelled: %w", ctx.Err())
		default:
		}

		if attempt > 0 {
			jitter := time.Duration(rand.Int63n(int64(baseDelay) / 2))
			select {
			case <-time.After(baseDelay + jitter):
				baseDelay *= 2
			case <-ctx.Done():
				return "", fmt.Errorf("request cancelled during backoff: %w", ctx.Err())
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.APIURL, bytes.NewBuffer(jsonPayload))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if c.config.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
			continue
		}

		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}

		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			lastErr = fmt.Errorf("failed to decode response: %w", err)
			continue
		}

		if len(result.Choices) == 0 {
			lastErr = fmt.Errorf("empty choices array returned")
			continue
		}

		return result.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("max retries exceeded. last error: %w", lastErr)
}
