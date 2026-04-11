package learning

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type ActionStats struct {
	SuccessRate   float64        `json:"success_rate"`
	FailureCauses map[string]int `json:"failure_causes"`
	AvgDuration   time.Duration  `json:"avg_duration"`
	Attempts      int            `json:"attempts"`
}

// GetProjectedStats loads the latest snapshot, fetches delta events, and computes the current stats.
func GetProjectedStats(ctx context.Context, store domain.EventStore, sessionID string, logger *slog.Logger) (map[string]*ActionStats, int64, error) {
	snap, err := store.GetLatestSnapshot(ctx, sessionID)
	var lastID int64 = 0
	stats := make(map[string]*ActionStats)

	if err == nil && snap != nil {
		lastID = snap.LastEventID
		if err := json.Unmarshal(snap.Data, &stats); err != nil {
			if logger != nil {
				logger.Warn("Failed to unmarshal snapshot data", slog.Any("error", err), slog.String("session_id", sessionID))
			}
			// Non-fatal, we just start with empty stats if snapshot is corrupt
		}
	}

	events, err := store.GetStreamSince(ctx, sessionID, lastID)
	if err != nil {
		return stats, lastID, err
	}

	if len(events) > 0 {
		stats = CalculateActionStats(stats, events, logger)
		lastID = events[len(events)-1].ID
	}

	return stats, lastID, nil
}

// CalculateActionStats projects a stream of events into a statistical model of bot performance.
func CalculateActionStats(existing map[string]*ActionStats, events []domain.DomainEvent, logger *slog.Logger) map[string]*ActionStats {
	stats := make(map[string]*ActionStats)

	// Deep copy existing state to prevent mutating the cached read-model
	for k, v := range existing {
		causes := make(map[string]int)
		for ck, cv := range v.FailureCauses {
			causes[ck] = cv
		}
		stats[k] = &ActionStats{
			SuccessRate:   v.SuccessRate,
			FailureCauses: causes,
			AvgDuration:   v.AvgDuration,
			Attempts:      v.Attempts,
		}
	}

	type taskRun struct {
		start  time.Time
		action string
	}

	inFlight := make(map[string]taskRun)

	for _, ev := range events {
		switch ev.Type {
		case domain.EventTypeTaskStart:
			var payload struct {
				CommandID string `json:"command_id"`
				Action    string `json:"action"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				if logger != nil {
					logger.Warn("Failed to unmarshal TaskStart payload in projection", slog.Any("error", err), slog.Int64("event_id", ev.ID))
				}
				continue
			}
			inFlight[payload.CommandID] = taskRun{
				start:  ev.CreatedAt,
				action: payload.Action,
			}

			baseAction := extractBaseAction(payload.Action)
			if _, ok := stats[baseAction]; !ok {
				stats[baseAction] = &ActionStats{FailureCauses: make(map[string]int)}
			}

		case domain.EventTypeTaskEnd:
			var payload struct {
				CommandID string `json:"command_id"`
				Status    string `json:"status"`
				Cause     string `json:"cause"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				if logger != nil {
					logger.Warn("Failed to unmarshal TaskEnd payload in projection", slog.Any("error", err), slog.Int64("event_id", ev.ID))
				}
				continue
			}
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

	return stats
}

func extractBaseAction(actionStr string) string {
	parts := strings.SplitN(strings.TrimSpace(actionStr), " ", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}
