package humanization

import "david22573/synaptic-mc/internal/domain"

// Context provides the situational awareness required for behavioral modifiers.
type Context struct {
	State   domain.GameState
	IsStuck bool
}

// BuildContext constructs a new behavioral context from the orchestrator's state.
func BuildContext(state domain.GameState, isStuck bool) Context {
	return Context{
		State:   state,
		IsStuck: isStuck,
	}
}
