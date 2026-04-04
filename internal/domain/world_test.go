package domain

import (
	"testing"
)

func TestWorldModel_SpatialLearning(t *testing.T) {
	wm := NewWorldModel()
	loc := Location{X: 100, Y: 64, Z: 100}

	// Initially risk should be 0
	if wm.GetZoneCost(loc) != 0 {
		t.Errorf("Expected 0 initial cost, got %f", wm.GetZoneCost(loc))
	}

	// Penalize the zone
	wm.PenalizeZone(loc, 0.8)

	// Risk should be higher now
	cost := wm.GetZoneCost(loc)
	if cost < 0.5 {
		t.Errorf("Expected higher cost after penalty, got %f", cost)
	}

	// Different location should still be safe
	otherLoc := Location{X: 200, Y: 64, Z: 200}
	if wm.GetZoneCost(otherLoc) != 0 {
		t.Errorf("Expected 0 cost for distant location, got %f", wm.GetZoneCost(otherLoc))
	}
}

func TestWorldModel_ActionLearning(t *testing.T) {
	wm := NewWorldModel()
	
	// Record success
	wm.RecordSuccess("mine", Target{Name: "diamond_ore"})
	
	// Record failure
	wm.PenalizeAction("mine", 0.5)
	
	if len(wm.ActionWeights) == 0 {
		t.Error("ActionWeights map not populated")
	}
}
