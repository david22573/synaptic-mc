package domain

import (
	"testing"
	"strings"
)

func TestVec3Distance(t *testing.T) {
	v1 := Vec3{X: 0, Y: 0, Z: 0}
	v2 := Vec3{X: 3, Y: 4, Z: 0}
	dist := v1.DistanceTo(v2)
	if dist != 5.0 {
		t.Errorf("Expected distance 5.0, got %f", dist)
	}
}

func TestFormatStateForLLM(t *testing.T) {
	state := GameState{
		Health: 20,
		Food:   20,
		Position: Vec3{X: 100, Y: 64, Z: 100},
		Inventory: []Item{
			{Name: "iron_pickaxe", Count: 1},
			{Name: "bread", Count: 5},
		},
		POIs: []POI{
			{Name: "village", Type: "village", Distance: 50},
		},
	}

	formatted := FormatStateForLLM(state)
	
	if !strings.Contains(formatted, "iron_pickaxe") {
		t.Error("Formatted state missing inventory item")
	}
	if !strings.Contains(formatted, "village") {
		t.Error("Formatted state missing POI")
	}
	if !strings.Contains(formatted, "\"health\": 20") {
		t.Errorf("Formatted state missing health or format mismatch. Got: %s", formatted)
	}
}

func TestMarkAreaRisky(t *testing.T) {
	state := GameState{}
	pos := Vec3{X: 10, Y: 10, Z: 10}
	state.MarkAreaRisky(pos, "lava", 0.9)

	if len(state.DangerZones) != 1 {
		t.Fatal("Expected 1 danger zone")
	}
	if state.DangerZones[0].Reason != "lava" {
		t.Errorf("Expected reason 'lava', got %s", state.DangerZones[0].Reason)
	}
}
