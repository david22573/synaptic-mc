package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	APIURL     string
	APIKey     string
	Model      string
	EmbedModel string
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
			// Base transport timeout; context timeouts will override this per-call
			Timeout: 45 * time.Second,
		},
	}
}

func (c *Client) Generate(ctx context.Context, systemPrompt, userContent string) (string, error) {
	// 1. Enforce strict timeout for the entire generation process
	// Phase 2 Improvement: llm-timeout-and-circuit-breaker
	genCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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
	return c.doRequest(genCtx, c.config.APIURL, jsonPayload)
}

func (c *Client) CreateEmbedding(ctx context.Context, input string) ([]float32, error) {
	// Robustly convert the chat completion endpoint into an embeddings endpoint
	parsedURL, err := url.Parse(c.config.APIURL)
	if err != nil {
		return nil, fmt.Errorf("invalid APIURL configured: %w", err)
	}

	// Handles variations like /v1/chat/completions -> /v1/embeddings
	pathParts := strings.Split(parsedURL.Path, "/")
	if len(pathParts) > 0 {
		pathParts = pathParts[:len(pathParts)-2] // strip off "chat/completions" or similar
	}
	pathParts = append(pathParts, "embeddings")
	parsedURL.Path = strings.Join(pathParts, "/")

	embedURL := parsedURL.String()

	// Embeddings should be fast; enforce a tighter timeout
	embedCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	payload := map[string]any{
		"model": c.config.EmbedModel,
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
		case <-embedCtx.Done():
			return nil, fmt.Errorf("request cancelled or timed out: %w", embedCtx.Err())
		default:
		}

		if attempt > 0 {
			jitter := time.Duration(rand.Int63n(int64(baseDelay) / 2))
			select {
			case <-embedCtx.Done():
				return nil, fmt.Errorf("request cancelled or timed out during backoff: %w", embedCtx.Err())
			case <-time.After(baseDelay + jitter):
			}
			baseDelay *= 2
		}

		req, err := http.NewRequestWithContext(embedCtx, http.MethodPost, embedURL, bytes.NewBuffer(jsonPayload))
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
			// Network errors are transient, proceed to retry
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		// Phase 2 Improvement: Circuit Breaker / Strict Retry Policy
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))

			// 4xx errors (Auth, Bad Request) are terminal. Do NOT retry.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return nil, lastErr
			}
			// 5xx errors (Gateway timeout, server overload) are transient. Proceed to retry.
			continue
		}

		var result struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}

		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			// Schema validation failures are terminal. Do NOT retry.
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}

		if len(result.Data) == 0 {
			// Bad API response payload is terminal. Do NOT retry.
			return nil, fmt.Errorf("empty embedding array returned")
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
			return "", fmt.Errorf("request cancelled or timed out: %w", ctx.Err())
		default:
		}

		if attempt > 0 {
			jitter := time.Duration(rand.Int63n(int64(baseDelay) / 2))
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("request cancelled or timed out during backoff: %w", ctx.Err())
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
			// Network errors are transient, proceed to retry
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		// Phase 2 Improvement: Circuit Breaker / Strict Retry Policy
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))

			// 4xx errors (Auth, Bad Request) are terminal. Do NOT retry.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return "", lastErr
			}
			// 5xx errors (Gateway timeout, server overload) are transient. Proceed to retry.
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
			// Schema validation failures are terminal. Do NOT retry.
			return "", fmt.Errorf("failed to decode response: %w", err)
		}

		if len(result.Choices) == 0 {
			// Bad API response payload is terminal. Do NOT retry.
			return "", fmt.Errorf("empty choices array returned")
		}

		return result.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("max retries exceeded. last error: %w", lastErr)
}
