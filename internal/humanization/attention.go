package humanization

import (
	"fmt"
	"math/rand"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

// ProcessAttentionDrift checks if the bot should be distracted by its environment.
func ProcessAttentionDrift(ctx Context, state *State, now time.Time) []ScheduledAction {
	var actions []ScheduledAction

	attention := state.GetAttention()

	// If attention is low and we aren't fighting for our life or stuck...
	if attention < 0.5 && !ctx.IsStuck && ctx.State.Health >= 15 {

		// 15% chance to actually trigger a physical drift action per evaluation
		if rand.Float64() < 0.15 && len(ctx.State.POIs) > 0 {

			// Pick a random nearby Point of Interest
			poi := ctx.State.POIs[rand.Intn(len(ctx.State.POIs))]

			driftAction := domain.Action{
				ID:        fmt.Sprintf("drift-%d", time.Now().UnixNano()),
				Action:    "look",
				Target:    domain.Target{Type: "poi", Name: poi.Name},
				Priority:  -1, // Background priority, instantly preempted by real tasks
				Rationale: "Attention drifted to nearby " + poi.Name,
			}

			actions = append(actions, ScheduledAction{
				Action: driftAction,
				// Execute the drift almost immediately
				ExecuteAt: now.Add(50 * time.Millisecond),
			})
		}
	}

	return actions
}
