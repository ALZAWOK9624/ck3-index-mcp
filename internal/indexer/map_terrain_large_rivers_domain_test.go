package indexer

import (
	"image"
	"path/filepath"
	"testing"
)

// Carving a valley can only ever remove ground. The pass used to clamp the
// result up to sea level, which turned a lake bed at grey 11 into a bank at
// grey 18 -- a raised line following the channel, which is the opposite of a
// river. Water provinces are one class here: sea, lake and river provinces are
// all declared water by default.map and none of them may be redrawn.
func TestLargeRiversOnlyLowerLandAndNeverTouchWater(t *testing.T) {
	cfg, out := terrainEditWorkspace(t, 96, 96, 16)
	sourcePath := filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png")
	decoded, err := decodeMapImage(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	before := decoded.(*image.Gray16)
	mask, err := loadTerrainDomainMask(cfg, before.Bounds())
	if err != nil {
		t.Fatal(err)
	}

	result, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpLargeRivers,
		LargeRivers: &MapTerrainLargeRiverSettings{
			// A low threshold and a wide valley are what push the spread onto
			// neighbouring water; the defaults would hide the bug.
			MinDrainage: 20, Depth: 0.06, ValleyWidth: 6, SeaLevel: 20.0 / 255.0,
		},
	}, out)
	if err != nil {
		t.Fatal(err)
	}
	if result.LargeRivers == nil || result.LargeRivers.ChannelCells == 0 {
		t.Fatalf("no channel was carved, so the guarantees are untested: %+v", result.LargeRivers)
	}

	written, err := decodeMapImage(outputPathForKind(t, result, "heightmap"))
	if err != nil {
		t.Fatal(err)
	}
	after := written.(*image.Gray16)

	waterSeen, lowered := 0, 0
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			b, a := before.Gray16At(x, y).Y, after.Gray16At(x, y).Y
			class := mask.classAt(x, y)
			isWater := class == TerrainDomainOcean || class == TerrainDomainLake
			if isWater {
				waterSeen++
				if b != a {
					t.Fatalf("large_rivers changed %s pixel %d,%d from %d to %d", class, x, y, b, a)
				}
				continue
			}
			if a > b {
				t.Fatalf("large_rivers raised land %d,%d from %d to %d; carving may only lower", x, y, b, a)
			}
			if a < b {
				lowered++
			}
		}
	}
	if waterSeen == 0 {
		t.Fatal("fixture exposed no water province, so the domain guard was not exercised")
	}
	if lowered == 0 {
		t.Fatal("no land was lowered, so the no-raise result proves nothing")
	}
}
