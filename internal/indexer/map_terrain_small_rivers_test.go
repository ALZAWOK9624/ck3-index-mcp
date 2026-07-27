package indexer

import (
	"image"
	"image/color"
	"testing"
)

// slopeTerrain falls steadily toward the left edge with some relief, so water
// has both a direction to run and reasons to branch.
func slopeTerrain(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := 14 + float64(x)*0.55
			bump := 9 * gradientNoise(float64(x)/13, float64(y)/13, 4242)
			img.SetGray(x, y, color.Gray{Y: uint8(clampFloat(base+bump, 0, 255))})
		}
	}
	return img
}

// TestSmallRiversUseTheEnginePalette guards the failure that produces an image
// which looks right and loads wrong: CK3 reads rivers.png by palette index, so
// the palette order is part of the format, not a presentation detail.
func TestSmallRiversUseTheEnginePalette(t *testing.T) {
	height := slopeTerrain(160, 120)
	overlay, _, err := GenerateSmallRivers(height, height.Bounds(), DefaultSmallRivers())
	if err != nil {
		t.Fatal(err)
	}
	canonical := canonicalCK3RiverPalette()
	for _, index := range []int{0, 1, 2, 3, 7, 11, 255} {
		want := canonical[index]
		got, ok := overlay.Palette[index].(color.RGBA)
		if !ok {
			t.Fatalf("palette entry %d is %T, want color.RGBA", index, overlay.Palette[index])
		}
		if got.R != want[0] || got.G != want[1] || got.B != want[2] {
			t.Fatalf("palette index %d is #%02X%02X%02X, want #%02X%02X%02X",
				index, got.R, got.G, got.B, want[0], want[1], want[2])
		}
	}
}

// TestSmallRiversAreOrthogonallyConnected is the constraint CK3 actually
// enforces. Flow routing works on eight directions, so an unconverted diagonal
// step leaves a channel that is disconnected as far as the engine is concerned.
func TestSmallRiversAreOrthogonallyConnected(t *testing.T) {
	height := slopeTerrain(200, 150)
	overlay, stats, err := GenerateSmallRivers(height, height.Bounds(), DefaultSmallRivers())
	if err != nil {
		t.Fatal(err)
	}
	if stats.StreamPixels == 0 {
		t.Fatalf("no stream was painted: %+v", stats)
	}
	bounds := overlay.Bounds()
	isRiver := func(x, y int) bool {
		if x < bounds.Min.X || y < bounds.Min.Y || x >= bounds.Max.X || y >= bounds.Max.Y {
			return false
		}
		index := overlay.ColorIndexAt(x, y)
		return index >= riverBodyMin && index <= riverBodyMax ||
			index == riverSource || index == riverConfluence
	}
	isolated := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !isRiver(x, y) {
				continue
			}
			neighbours := 0
			for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				if isRiver(x+step[0], y+step[1]) {
					neighbours++
				}
			}
			// A stream cell must touch another orthogonally. Reaching the sea or
			// the region edge is the only way to have none, and those were
			// excluded when the channel was walked.
			if neighbours == 0 {
				isolated++
			}
		}
	}
	if isolated > 0 {
		t.Fatalf("%d stream cell(s) have no orthogonal neighbour, so CK3 sees a broken channel", isolated)
	}
}

// TestSmallRiversCutTheirOwnBed covers the point of doing both systems at once:
// a stream painted on ground that does not dip for it is a line drawn on a
// hillside.
func TestSmallRiversCutTheirOwnBed(t *testing.T) {
	height := slopeTerrain(180, 140)
	before := image.NewGray(height.Bounds())
	copy(before.Pix, height.Pix)

	overlay, stats, err := GenerateSmallRivers(height, height.Bounds(), DefaultSmallRivers())
	if err != nil {
		t.Fatal(err)
	}
	if stats.BedCells == 0 {
		t.Fatal("no bed was cut")
	}
	bounds := overlay.Bounds()
	lowered, raised := 0, 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			index := overlay.ColorIndexAt(x, y)
			isStream := index >= riverBodyMin && index <= riverBodyMax || index == riverSource || index == riverConfluence
			delta := int(height.GrayAt(x, y).Y) - int(before.GrayAt(x, y).Y)
			if !isStream && delta != 0 {
				t.Fatalf("non-stream pixel (%d,%d) changed by %d", x, y, delta)
			}
			if delta < 0 {
				lowered++
			}
			if delta > 0 {
				raised++
			}
		}
	}
	if raised > 0 {
		t.Fatalf("%d pixel(s) were raised; a stream bed only cuts down", raised)
	}
	if lowered == 0 {
		t.Fatal("no pixel was lowered under the painted streams")
	}
}

// TestSmallRiversStopWhereLargeRiversBegin keeps the two systems from
// describing the same water twice.
func TestSmallRiversStopWhereLargeRiversBegin(t *testing.T) {
	height := slopeTerrain(220, 160)
	// Drainage has to be re-derived from the terrain as it was *before*
	// generation: cutting the beds lowers the ground, which changes routing, so
	// measuring against the modified heightmap compares two different worlds.
	original := image.NewGray(height.Bounds())
	copy(original.Pix, height.Pix)

	settings := DefaultSmallRivers()
	settings.MinDrainage = 40
	settings.MaxDrainage = 300
	overlay, stats, err := GenerateSmallRivers(height, height.Bounds(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if stats.StreamPixels == 0 {
		t.Fatalf("no stream painted: %+v", stats)
	}
	w, h := original.Bounds().Dx(), original.Bounds().Dy()
	field := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			field[y*w+x] = float64(original.GrayAt(x, y).Y)
		}
	}
	filled, _ := fillDepressions(field, w, h, float64(settings.SeaLevel))
	drainage := accumulateDrainage(filled, w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			index := overlay.ColorIndexAt(x, y)
			if index < riverBodyMin || index > riverBodyMax {
				continue
			}
			// Allow the bridge cells inserted to orthogonalise diagonals, which
			// can sit beside a cell above the ceiling.
			if drainage[y*w+x] > settings.MaxDrainage*3 {
				t.Fatalf("stream painted at (%d,%d) with catchment %d, far above the %d ceiling",
					x, y, drainage[y*w+x], settings.MaxDrainage)
			}
		}
	}
}

func TestSmallRiversRejectBadSettings(t *testing.T) {
	height := slopeTerrain(60, 60)
	for _, tc := range []struct {
		name     string
		settings MapSmallRiverSettings
	}{
		{"zero min drainage", MapSmallRiverSettings{MinDrainage: 0, BedDepth: 1}},
		{"ceiling below floor", MapSmallRiverSettings{MinDrainage: 100, MaxDrainage: 50, BedDepth: 1}},
	} {
		if _, _, err := GenerateSmallRivers(height, height.Bounds(), tc.settings); err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
	if _, _, err := GenerateSmallRivers(nil, height.Bounds(), DefaultSmallRivers()); err == nil {
		t.Fatal("expected a nil heightmap to be refused")
	}
}
