package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"david22573/synaptic-mc/internal/domain"
)

type PolicyExtractor struct {
	store      domain.EventStore
	logger     *slog.Logger
	windowSize int
}

func NewPolicyExtractor(store domain.EventStore, logger *slog.Logger) *PolicyExtractor {
	return &PolicyExtractor{
		store:      store,
		logger:     logger.With(slog.String("component", "policy_extractor")),
		windowSize: 200,
	}
}

func (p *PolicyExtractor) GenerateRules(ctx context.Context, sessionID string) string {
	events, err := p.store.GetRecentStream(ctx, sessionID, p.windowSize)
	if err != nil {
		p.logger.Warn("Failed to extract rules from stream", slog.Any("error", err))
		return ""
	}

	failures := make(map[string]int)
	causes := make(map[string]map[string]int)

	for _, ev := range events {
		if ev.Type == domain.EventTypeTaskEnd {
			var payload struct {
				Status string `json:"status"`
				Action string `json:"action"`
				Cause  string `json:"cause"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				switch payload.Status {
				case "FAILED", "ABORTED":
					failures[payload.Action]++
					if causes[payload.Action] == nil {
						causes[payload.Action] = make(map[string]int)
					}
					causes[payload.Action][payload.Cause]++
				case "COMPLETED":
					delete(failures, payload.Action)
					delete(causes, payload.Action)
				}
			}
		}
	}

	var rules []string
	for action, count := range failures {
		if count >= 2 {
			// Find the most frequent cause
			maxFreq := 0
			primaryCause := ""
			for c, freq := range causes[action] {
				if freq > maxFreq {
					maxFreq = freq
					primaryCause = c
				}
			}

			attribution := "Unknown failure."

			switch {
			case strings.Contains(primaryCause, "PATH_FAILED"):
				attribution = "The target is physically inaccessible or blocked by terrain."
			case strings.Contains(primaryCause, "NO_ENTITY"):
				attribution = "The entity despawned, died, or moved out of range."
			case strings.Contains(primaryCause, "TIMEOUT"):
				attribution = "The action took too long, likely due to a stuck path or lag."
			case strings.Contains(primaryCause, "NO_TOOL"):
				attribution = "You lack the required tool (e.g., pickaxe). Craft it first."
			case strings.Contains(primaryCause, "NO_FURNACE"):
				attribution = "No furnace is available to smelt items. Craft and place one."
			case strings.Contains(primaryCause, "NO_MATURE_CROP"):
				attribution = "The crops are not fully grown yet. Wait or find another food source."
			case strings.Contains(primaryCause, "MISSING_INGREDIENTS"):
				attribution = "You are missing the required materials or crafting table. Gather them first."
			default:
				attribution = fmt.Sprintf("Reported cause: %s", primaryCause)
			}
			rules = append(rules, fmt.Sprintf("- AVOID: Executing '%s'. %s", action, attribution))
		}
	}

	if len(rules) == 0 {
		return ""
	}
	return "LEARNED CONSTRAINTS (DO NOT REPEAT THESE ACTIONS):\n" + strings.Join(rules, "\n")
}
