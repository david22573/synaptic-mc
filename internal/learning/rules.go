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
			var payload domain.TaskEndPayload
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				p.logger.Warn("Failed to unmarshal TaskEnd event payload", slog.Any("error", err), slog.String("event_id", fmt.Sprintf("%d", ev.ID)))
				continue
			}
			key := fmt.Sprintf("%s:%s", payload.Action, payload.Target)
			switch payload.Status {
			case "FAILED", "ABORTED":
				failures[key]++
				if causes[key] == nil {
					causes[key] = make(map[string]int)
				}
				causes[key][payload.Cause]++
			case "COMPLETED", "PREEMPTED", "CANCELED":
				// These are not failures, so clear recent failure counts for this action
				delete(failures, key)
				delete(causes, key)
			}
		}
	}

	var rules []string
	for key, count := range failures {
		if count >= 2 {
			parts := strings.SplitN(key, ":", 2)
			action := parts[0]
			target := "unknown"
			if len(parts) > 1 && parts[1] != "" {
				target = parts[1]
			}

			// Find the most frequent cause
			maxFreq := 0
			primaryCause := ""
			for c, freq := range causes[key] {
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
			rule := fmt.Sprintf("- AVOID: Executing '%s'", action)
			if target != "unknown" {
				rule += fmt.Sprintf(" on '%s'", target)
			}
			rule += fmt.Sprintf(". %s", attribution)
			rules = append(rules, rule)
		}
	}

	if len(rules) == 0 {
		return ""
	}
	return "LEARNED CONSTRAINTS (DO NOT REPEAT THESE ACTIONS):\n" + strings.Join(rules, "\n")
}
