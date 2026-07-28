package indexer

import (
	"image"
	"path/filepath"
	"testing"
)

// Erosion is the one pass that rewrites height without naming a target domain,
// which made it the one pass that could silently move a pixel across the water
// line. provinces.png decides what is sea; a sediment load dropped in a sea
// province leaves CK3 with land geometry inside a province it treats as ocean.
func TestErosionLeavesEveryWaterDomainPixelUnchanged(t *testing.T) {
	// The fixture's right third is a sea province and its lower-middle block is
	// a lake, so both water classes sit next to land that erodes hard.
	cfg, out := terrainEditWorkspace(t, 96, 96, 16)
	sourcePath := filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png")
	sourceImage, err := decodeMapImage(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	before := sourceImage.(*image.Gray16)

	mask, err := loadTerrainDomainMask(cfg, before.Bounds())
	if err != nil {
		t.Fatal(err)
	}

	result, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpCompose,
		Layers: []MapTerrainLayer{{
			ID: "ridge", Kind: TerrainRange, Domain: TerrainDomainLand,
			Geometry: MapTerrainGeometry{Type: TerrainGeometryPolyline, Coordinates: []MapTerrainPoint{
				{X: 8, Y: 20}, {X: 60, Y: 30},
			}},
			WidthPx: 16, Strength: 0.45, Roughness: 0.6, Detail: 1.1, Seed: 5,
		}},
		Erosion: &MapErosionSettings{
			Droplets: 40000, MaxSteps: 64, Inertia: 0.05, Capacity: 4,
			Erode: 0.3, Deposit: 0.3, Evaporate: 0.02, Radius: 2, Seed: 11,
		},
	}, out)
	if err != nil {
		t.Fatal(err)
	}

	written, err := decodeMapImage(outputPathForKind(t, result, "heightmap"))
	if err != nil {
		t.Fatal(err)
	}
	after, ok := written.(*image.Gray16)
	if !ok {
		t.Fatalf("heightmap decoded as %T, want Gray16", written)
	}

	waterSeen, landChanged := 0, 0
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			class := mask.classAt(x, y)
			isWater := class == TerrainDomainOcean || class == TerrainDomainLake
			b, a := before.Gray16At(x, y).Y, after.Gray16At(x, y).Y
			if isWater {
				waterSeen++
				if b != a {
					t.Fatalf("erosion moved %s pixel %d,%d from %d to %d", class, x, y, b, a)
				}
				continue
			}
			if b != a {
				landChanged++
			}
		}
	}
	if waterSeen == 0 {
		t.Fatal("fixture exposed no water-domain pixels, so the guarantee was not exercised")
	}
	// Without this the test would pass just as well if erosion did nothing.
	if landChanged == 0 {
		t.Fatal("erosion changed no land pixel, so the water result proves nothing")
	}
}

// A coast eroded under the water line leaves a land province rendering as
// submerged ground, which is the mirror of the sea-becomes-land failure above.
func TestErosionHonoursTheSeaLevelFloor(t *testing.T) {
	// Land in the fixture starts at 40..87 of 255; a floor above that proves the
	// clamp holds even when erosion is asked to cut far below it.
	floor := 60.0 / 255.0
	cfg, out := terrainEditWorkspace(t, 96, 96, 8)
	sourcePath := filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png")
	sourceImage, err := decodeMapImage(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	before := sourceImage.(*image.Gray)
	mask, err := loadTerrainDomainMask(cfg, before.Bounds())
	if err != nil {
		t.Fatal(err)
	}

	result, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpCompose,
		Layers: []MapTerrainLayer{{
			ID: "ridge", Kind: TerrainRange, Domain: TerrainDomainLand,
			Geometry: MapTerrainGeometry{Type: TerrainGeometryPolyline, Coordinates: []MapTerrainPoint{
				{X: 8, Y: 20}, {X: 60, Y: 30},
			}},
			WidthPx: 16, Strength: 0.5, Roughness: 0.6, Detail: 1.1, Seed: 5,
		}},
		Erosion: &MapErosionSettings{
			Droplets: 60000, MaxSteps: 64, Inertia: 0.05, Capacity: 6,
			Erode: 0.5, Deposit: 0.3, Evaporate: 0.02, Radius: 2, Seed: 11,
			SeaLevel: &floor,
		},
	}, out)
	if err != nil {
		t.Fatal(err)
	}
	written, err := decodeMapImage(outputPathForKind(t, result, "heightmap"))
	if err != nil {
		t.Fatal(err)
	}
	after := written.(*image.Gray)

	checked, loweredBelowStart := 0, 0
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			if mask.classAt(x, y) != TerrainDomainLand {
				continue
			}
			b, a := int(before.GrayAt(x, y).Y), int(after.GrayAt(x, y).Y)
			if b >= 60 {
				checked++
				if a < 60 {
					t.Fatalf("erosion cut land %d,%d from %d to %d, under the sea_level floor of 60", x, y, b, a)
				}
			} else if a < b {
				// Ground the author already drew below the floor may keep its
				// height, but the floor must not be used to dredge it further.
				loweredBelowStart++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no land pixel started above the floor, so the clamp was not exercised")
	}
	if loweredBelowStart > 0 {
		t.Fatalf("%d pixels below the floor were lowered further", loweredBelowStart)
	}
}
