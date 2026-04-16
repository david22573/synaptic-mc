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
	
	// Record success: 0 * 0.95 + 1.0 = 1.0
	wm.RecordSuccess("mine", nil)
	if wm.ActionWeights["mine"] != 1.0 {
		t.Errorf("Expected weight 1.0, got %f", wm.ActionWeights["mine"])
	}

	// Repeated success should clamp at 5.0
	for i := 0; i < 20; i++ {
		wm.RecordSuccess("mine", nil)
	}
	if wm.ActionWeights["mine"] != 5.0 {
		t.Errorf("Expected weight 5.0 (clamped), got %f", wm.ActionWeights["mine"])
	}

	// Record failure: 5.0 * 0.95 - 2.0 = 4.75 - 2.0 = 2.75
	wm.PenalizeAction("mine", 2.0)
	if wm.ActionWeights["mine"] != 2.75 {
		t.Errorf("Expected weight 2.75, got %f", wm.ActionWeights["mine"])
	}

	// Repeated penalty should clamp at -5.0
	for i := 0; i < 20; i++ {
		wm.PenalizeAction("mine", 2.0)
	}
	if wm.ActionWeights["mine"] != -5.0 {
		t.Errorf("Expected weight -5.0 (clamped), got %f", wm.ActionWeights["mine"])
	}
}
