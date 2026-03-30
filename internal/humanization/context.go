package humanization

import "david22573/synaptic-mc/internal/domain"

type Context struct {
	CurrentTask   *domain.Action
	NearbyThreats int
	Health        float64
	IsStuck       bool
}

func BuildContext(state domain.GameState, isStuck bool) Context {
	return Context{
		CurrentTask:   state.CurrentTask,
		NearbyThreats: len(state.Threats),
		Health:        state.Health,
		IsStuck:       isStuck,
	}
}
