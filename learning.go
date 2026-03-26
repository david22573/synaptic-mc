package main

import (
	"fmt"
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
}

func NewLearningSystem() *LearningSystem {
	return &LearningSystem{
		failures: make(map[string]int),
		causes:   make(map[string]string),
	}
}

func (l *LearningSystem) RecordOutcome(action, target, status, cause string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.outcomes = append(l.outcomes, Outcome{Action: action, Target: target, Status: status, Cause: cause})

	// Keep a rolling window so it can eventually "forget" old failures if the environment changes
	if len(l.outcomes) > 200 {
		l.outcomes = l.outcomes[1:]
	}

	key := action + ":" + target

	if status == "FAILED" || status == "ABORTED" {
		l.failures[key]++
		l.causes[key] = cause
	} else if status == "COMPLETED" {
		// Wipe the slate clean if it finally succeeds
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
			newRules = append(newRules, fmt.Sprintf("- AVOID: Action '%s' on target '%s'. It repeatedly fails with: %s.", act, tgt, cause))
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
	return "LEARNED ENVIRONMENTAL CONSTRAINTS:\n" + strings.Join(l.rules, "\n")
}

// PenalizePOIs dynamically drops the priority of objects that the bot knows it can't reach
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
