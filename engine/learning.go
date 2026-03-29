package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

type Outcome struct {
	Action string
	Target string
	Status string
	Cause  string
}

type LearningSystem struct {
	outcomes []Outcome
	failures map[string]int
	causes   map[string]string
	rules    []string
	mu       sync.Mutex
	logger   *slog.Logger
}

func NewLearningSystem(logger *slog.Logger) *LearningSystem {
	return &LearningSystem{
		failures: make(map[string]int),
		causes:   make(map[string]string),
		logger:   logger.With(slog.String("component", "LearningSystem")),
	}
}

// LoadEpisodicMemory scans past sessions for repeated traumatic failures to pre-load constraints
func (l *LearningSystem) LoadEpisodicMemory(ctx context.Context, store EventStore) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Phase 4: Episodic Memory. Fetch the last 1000 cross-session events to find persistent death/failure zones.
	events, err := store.GetStream(ctx, "") // Empty sessionID to get cross-session stream if supported, or implement a specific query
	if err != nil {
		l.logger.Warn("Failed to load episodic memory", slog.Any("error", err))
		return
	}

	for _, ev := range events {
		if ev.Type == "TASK_FAILED" || ev.Type == "TASK_ABORTED" || ev.Type == "DEATH" {
			// Basic heuristic: if it failed historically, pre-warm the failure map
			// In a full implementation, you'd parse the JSON payload for exact action/target
			l.logger.Debug("Loaded historical trauma", slog.String("type", ev.Type))
		}
	}
}

func (l *LearningSystem) RecordOutcome(action, target, status, cause string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.outcomes = append(l.outcomes, Outcome{Action: action, Target: target, Status: status, Cause: cause})

	if len(l.outcomes) > 200 {
		l.outcomes = l.outcomes[1:]
	}

	key := action + ":" + target

	if status == "FAILED" || status == "ABORTED" {
		l.failures[key]++
		l.causes[key] = cause
	} else if status == "COMPLETED" {
		l.failures[key] = 0
		delete(l.causes, key)
	}

	l.extractRules()
}

func (l *LearningSystem) extractRules() {
	var newRules []string
	for key, count := range l.failures {
		if count >= 2 {
			parts := strings.Split(key, ":")
			if len(parts) != 2 {
				continue
			}
			act, tgt := parts[0], parts[1]
			cause := l.causes[key]

			// Phase 4: Causal Attribution
			var attribution string
			switch {
			case strings.Contains(cause, "PATH_FAILED"):
				attribution = "The target is physically inaccessible or blocked by terrain."
			case strings.Contains(cause, "NO_ENTITY"):
				attribution = "The entity despawned, died, or moved out of range."
			case strings.Contains(cause, "TIMEOUT"):
				attribution = "The action took too long, likely due to a stuck path or lag."
			default:
				attribution = fmt.Sprintf("Root cause reported as: %s", cause)
			}

			newRules = append(newRules, fmt.Sprintf("- AVOID: Action '%s' on target '%s'. %s", act, tgt, attribution))
		}
	}
	l.rules = newRules
}

func (l *LearningSystem) GetRules() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.rules) == 0 {
		return ""
	}
	return "LEARNED ENVIRONMENTAL CONSTRAINTS & CAUSAL ATTRIBUTION:\n" + strings.Join(l.rules, "\n")
}

func (l *LearningSystem) PenalizePOIs(pois []POI) []POI {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i := range pois {
		for failKey, count := range l.failures {
			parts := strings.Split(failKey, ":")
			if len(parts) == 2 && parts[1] == pois[i].Name && count >= 2 {
				pois[i].Score /= 2
			}
		}
	}
	return pois
}

func (l *LearningSystem) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures = make(map[string]int)
	l.causes = make(map[string]string)
	l.rules = nil
}
