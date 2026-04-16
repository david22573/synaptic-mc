package state

import (
	"testing"

	"david22573/synaptic-mc/internal/domain"
)

func TestScenario_MemoryCorruptionReplay(t *testing.T) {
	initialState := domain.GameState{
		Health: 20,
		Food:   20,
		Position: domain.Vec3{X: 10, Y: 64, Z: 10},
		Initialized: true,
	}

	// 1. Simulate malformed JSON payload
	corruptEvent := domain.DomainEvent{
		Type:    domain.EventTypeStateTick,
		Payload: []byte(`{"health": "invalid_type", "food": {}}`),
	}

	// Reduce should handle the error and return the previous state
	nextState := Reduce(initialState, corruptEvent)
	if nextState.Health != 20 {
		t.Errorf("Expected state to be preserved on malformed JSON, got health: %v", nextState.Health)
	}

	// 2. Simulate binary/corrupted payload
	binaryCorrupt := domain.DomainEvent{
		Type:    domain.EventTypeStateTick,
		Payload: []byte{0xFF, 0xFE, 0xFD},
	}

	nextState2 := Reduce(nextState, binaryCorrupt)
	if nextState2.Position.X != 10 {
		t.Errorf("Expected state to be preserved on binary corruption, got pos.X: %v", nextState2.Position.X)
	}
}
