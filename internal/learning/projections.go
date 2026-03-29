package learning

import (
	"encoding/json"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type ActionStats struct {
	SuccessRate   float64
	FailureCauses map[string]int
	AvgDuration   time.Duration
	Attempts      int
}

// CalculateActionStats projects a stream of events into a statistical model of bot performance.
func CalculateActionStats(events []domain.DomainEvent) map[string]*ActionStats {
	type taskRun struct {
		start  time.Time
		action string
	}

	inFlight := make(map[string]taskRun)
	stats := make(map[string]*ActionStats)

	for _, ev := range events {
		switch ev.Type {
		case domain.EventTypeTaskStart:
			var payload struct {
				CommandID string `json:"command_id"`
				Action    string `json:"action"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				inFlight[payload.CommandID] = taskRun{
					start:  ev.CreatedAt,
					action: payload.Action,
				}

				baseAction := extractBaseAction(payload.Action)
				if _, ok := stats[baseAction]; !ok {
					stats[baseAction] = &ActionStats{FailureCauses: make(map[string]int)}
				}
			}

		case domain.EventTypeTaskEnd:
			var payload struct {
				CommandID string `json:"command_id"`
				Status    string `json:"status"`
				Cause     string `json:"cause"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				if run, ok := inFlight[payload.CommandID]; ok {
					baseAction := extractBaseAction(run.action)
					stat := stats[baseAction]

					stat.Attempts++
					duration := ev.CreatedAt.Sub(run.start)

					// Rolling average duration
					currentTotal := float64(stat.AvgDuration) * float64(stat.Attempts-1)
					stat.AvgDuration = time.Duration((currentTotal + float64(duration)) / float64(stat.Attempts))

					if payload.Status == "COMPLETED" {
						successes := (stat.SuccessRate * float64(stat.Attempts-1)) + 1.0
						stat.SuccessRate = successes / float64(stat.Attempts)
					} else {
						successes := stat.SuccessRate * float64(stat.Attempts-1)
						stat.SuccessRate = successes / float64(stat.Attempts)

						cause := payload.Cause
						if cause == "" {
							cause = "UNKNOWN"
						}
						stat.FailureCauses[cause]++
					}

					delete(inFlight, payload.CommandID)
				}
			}
		}
	}

	return stats
}

func extractBaseAction(actionStr string) string {
	parts := strings.SplitN(strings.TrimSpace(actionStr), " ", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}
