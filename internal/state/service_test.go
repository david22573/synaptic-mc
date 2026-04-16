package state

import (
	"testing"

	"david22573/synaptic-mc/internal/domain"
)

func TestMergeDangerZonesPrefersIncomingAndPreservesExisting(t *testing.T) {
	existing := []domain.DangerZone{
		{
			Center: domain.Vec3{X: 10, Y: 64, Z: 10},
			Radius: 8,
			Reason: "lava",
			Risk:   0.7,
		},
	}
	incoming := []domain.DangerZone{
		{
			Center: domain.Vec3{X: 11, Y: 64, Z: 11},
			Radius: 6,
			Reason: "lava",
			Risk:   0.95,
		},
		{
			Center: domain.Vec3{X: 40, Y: 64, Z: 40},
			Radius: 4,
			Reason: "cliff",
			Risk:   0.6,
		},
	}

	merged := mergeDangerZones(existing, incoming)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged danger zones, got %d", len(merged))
	}

	if merged[0].Reason != "lava" || merged[0].Risk != 0.95 {
		t.Fatalf("expected incoming lava zone to win, got %+v", merged[0])
	}
}

func TestMergeVisitedChunksDedupes(t *testing.T) {
	existing := []domain.ChunkCoord{{X: 1, Z: 2}, {X: 3, Z: 4}}
	incoming := []domain.ChunkCoord{{X: 3, Z: 4}, {X: 5, Z: 6}}

	merged := mergeVisitedChunks(existing, incoming)
	if len(merged) != 3 {
		t.Fatalf("expected 3 unique chunks, got %d", len(merged))
	}
}

func TestMergeTerrainRoughnessIncomingOverrides(t *testing.T) {
	existing := map[string]float64{"0,0": 0.2, "1,1": 0.4}
	incoming := map[string]float64{"1,1": 0.9, "2,2": 0.3}

	merged := mergeTerrainRoughness(existing, incoming)
	if merged["1,1"] != 0.9 {
		t.Fatalf("expected incoming roughness override, got %v", merged["1,1"])
	}
	if merged["0,0"] != 0.2 || merged["2,2"] != 0.3 {
		t.Fatalf("unexpected merged terrain roughness: %+v", merged)
	}
}
