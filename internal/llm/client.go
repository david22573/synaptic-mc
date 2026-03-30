package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
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

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal generation payload: %w", err)
	}
	return c.doRequest(ctx, c.config.APIURL, jsonPayload)
}

func (c *Client) CreateEmbedding(ctx context.Context, input string) ([]float32, error) {
	embedURL := strings.Replace(c.config.APIURL, "chat/completions", "embeddings", 1)

	payload := map[string]any{
		"model": c.config.Model,
		"input": input,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding payload: %w", err)
	}

	var lastErr error
	baseDelay := 500 * time.Millisecond

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
		default:
		}

		if attempt > 0 {
			jitter := time.Duration(rand.Int63n(int64(baseDelay) / 2))
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
			case <-time.After(baseDelay + jitter):
			}
			baseDelay *= 2
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, embedURL, bytes.NewBuffer(jsonPayload))
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
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}

		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			lastErr = fmt.Errorf("failed to decode response: %w", err)
			continue
		}

		if len(result.Data) == 0 {
			lastErr = fmt.Errorf("empty embedding array returned")
			continue
		}

		return result.Data[0].Embedding, nil
	}

	return nil, fmt.Errorf("max retries exceeded. last error: %w", lastErr)
}

func (c *Client) doRequest(ctx context.Context, url string, jsonPayload []byte) (string, error) {
	var lastErr error
	baseDelay := 500 * time.Millisecond

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("request cancelled: %w", ctx.Err())
		default:
		}

		if attempt > 0 {
			jitter := time.Duration(rand.Int63n(int64(baseDelay) / 2))
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("request cancelled: %w", ctx.Err())
			case <-time.After(baseDelay + jitter):
			}
			baseDelay *= 2
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonPayload))
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
