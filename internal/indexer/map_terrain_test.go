package indexer

import (
	"image"
	"math"
	"testing"
)

func flatTerrain(w, h int, level uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = level
	}
	return img
}

// roughness measures mean absolute difference between neighbours, which is how
// "eroded" or "detailed" a patch is in a single number.
func roughness(img *image.Gray, region image.Rectangle) float64 {
	total, count := 0.0, 0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X + 1; x < region.Max.X; x++ {
			total += math.Abs(float64(img.GrayAt(x, y).Y) - float64(img.GrayAt(x-1, y).Y))
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

// TestTerrainRangeFollowsItsPath is the property that separates a belt from a
// stamp: height must track the polyline, including its bend, rather than
// radiating from one centre.
func TestTerrainRangeFollowsItsPath(t *testing.T) {
	path := []MapTerrainPoint{{X: 40, Y: 40}, {X: 40, Y: 160}, {X: 160, Y: 160}}
	out, change, err := SynthesizeTerrain(flatTerrain(200, 200, 40), []MapTerrainFeature{
		{Kind: TerrainRange, Path: path, Width: 25, Amplitude: 80, Roughness: 0.6, Detail: 1, Seed: 5},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if change.ChangedPixels == 0 {
		t.Fatal("the feature changed nothing")
	}
	// Points on the path must be raised; points far from it must not be.
	for _, on := range [][2]int{{40, 60}, {40, 140}, {100, 160}, {150, 160}} {
		if got := out.GrayAt(on[0], on[1]).Y; got <= 55 {
			t.Fatalf("on-path pixel %v is %d, expected clearly raised", on, got)
		}
	}
	for _, off := range [][2]int{{140, 40}, {170, 60}, {10, 190}} {
		if got := out.GrayAt(off[0], off[1]).Y; got != 40 {
			t.Fatalf("off-path pixel %v is %d, expected untouched ground", off, got)
		}
	}
	// The corner is on the path, so it must be raised too -- a radial stamp at
	// either endpoint would leave it low.
	if got := out.GrayAt(40, 160).Y; got <= 55 {
		t.Fatalf("the path corner is %d, expected raised", got)
	}
}

func TestTerrainProfilesDifferByKind(t *testing.T) {
	centre := []MapTerrainPoint{{X: 100, Y: 100}}
	build := func(kind string) *image.Gray {
		out, _, err := SynthesizeTerrain(flatTerrain(200, 200, 40), []MapTerrainFeature{
			{Kind: kind, Path: centre, Width: 60, Amplitude: 80, Roughness: 0, Detail: 1, Seed: 3},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// A plateau is flat on top: its centre and a point well inside the rim
	// should be at the same height.
	plateau := build(TerrainPlateau)
	if a, b := plateau.GrayAt(100, 100).Y, plateau.GrayAt(120, 100).Y; a != b {
		t.Fatalf("plateau top is not flat: centre %d vs inner %d", a, b)
	}

	// A volcano dips at the summit: the rim must stand above the centre.
	volcano := build(TerrainVolcano)
	summit, rim := volcano.GrayAt(100, 100).Y, volcano.GrayAt(117, 100).Y
	if rim <= summit {
		t.Fatalf("volcano has no crater: summit %d, rim %d", summit, rim)
	}

	// A range concentrates on its spine, so it falls off faster than hills.
	rangeImg, hills := build(TerrainRange), build(TerrainHills)
	rangeFall := int(rangeImg.GrayAt(100, 100).Y) - int(rangeImg.GrayAt(130, 100).Y)
	hillsFall := int(hills.GrayAt(100, 100).Y) - int(hills.GrayAt(130, 100).Y)
	if rangeFall <= hillsFall {
		t.Fatalf("range should fall off faster than hills: %d vs %d", rangeFall, hillsFall)
	}

	// A canyon with a negative amplitude must cut below the surrounding ground.
	canyon, _, err := SynthesizeTerrain(flatTerrain(200, 200, 120), []MapTerrainFeature{
		{Kind: TerrainCanyon, Path: centre, Width: 40, Amplitude: -60, Roughness: 0, Detail: 1, Seed: 4},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if canyon.GrayAt(100, 100).Y >= 120 {
		t.Fatalf("canyon did not cut down: %d", canyon.GrayAt(100, 100).Y)
	}
}

// TestErosionActsWhereWaterRuns pins what erosion actually does, which is not
// "add roughness": water smooths as much as it cuts, so a slope can come out
// less noisy overall while gaining channels. What must hold is that the work
// happens on the slopes, where water gathers speed, and barely at all on flat
// ground away from any relief.
func TestErosionActsWhereWaterRuns(t *testing.T) {
	features := []MapTerrainFeature{
		{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 60, Y: 100}, {X: 200, Y: 120}}, Width: 50, Amplitude: 90, Roughness: 0.7, Detail: 1, Seed: 9},
	}
	plain, _, err := SynthesizeTerrain(flatTerrain(260, 220, 40), features, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultErosion(11, 40000)
	eroded, change, err := SynthesizeTerrain(flatTerrain(260, 220, 40), features, &settings)
	if err != nil {
		t.Fatal(err)
	}
	if change.ErodedDrops != 40000 {
		t.Fatalf("simulated %d droplets, want 40000", change.ErodedDrops)
	}

	meanChange := func(region image.Rectangle) float64 {
		total, count := 0.0, 0
		for y := region.Min.Y; y < region.Max.Y; y++ {
			for x := region.Min.X; x < region.Max.X; x++ {
				total += math.Abs(float64(eroded.GrayAt(x, y).Y) - float64(plain.GrayAt(x, y).Y))
				count++
			}
		}
		return total / float64(count)
	}
	onRange := meanChange(image.Rect(80, 85, 190, 135))
	offRange := meanChange(image.Rect(10, 190, 90, 215))
	if onRange < 1 {
		t.Fatalf("erosion barely touched the range itself: mean change %.3f", onRange)
	}
	if onRange <= offRange*3 {
		t.Fatalf("erosion was not concentrated on the slopes: on-range %.3f vs off-range %.3f", onRange, offRange)
	}
}

// TestErosionConnectsSeparateFeatures is the point of the erosion pass. Two
// landforms placed side by side are unrelated until something crosses the gap
// between them; water running off both does exactly that, so the ground in
// between must stop being untouched flat.
func TestErosionConnectsSeparateFeatures(t *testing.T) {
	features := []MapTerrainFeature{
		{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 70, Y: 60}, {X: 70, Y: 180}}, Width: 34, Amplitude: 95, Roughness: 0.7, Detail: 1, Seed: 31},
		{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 190, Y: 60}, {X: 190, Y: 180}}, Width: 34, Amplitude: 95, Roughness: 0.7, Detail: 1, Seed: 32},
	}
	gap := image.Rect(112, 70, 148, 170)

	plain, _, err := SynthesizeTerrain(flatTerrain(260, 240, 40), features, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Without erosion the corridor between the two ranges is untouched ground.
	if roughness(plain, gap) > 0.05 {
		t.Fatalf("the gap already varies before erosion: %.3f", roughness(plain, gap))
	}
	settings := DefaultErosion(13, 60000)
	eroded, _, err := SynthesizeTerrain(flatTerrain(260, 240, 40), features, &settings)
	if err != nil {
		t.Fatal(err)
	}
	if roughness(eroded, gap) <= 0.05 {
		t.Fatalf("erosion left the ground between the two ranges untouched: %.3f", roughness(eroded, gap))
	}
}

func TestTerrainSynthesisIsDeterministic(t *testing.T) {
	features := []MapTerrainFeature{
		{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 30, Y: 30}, {X: 120, Y: 90}}, Width: 30, Amplitude: 70, Roughness: 0.8, Detail: 1.2, Seed: 17},
	}
	settings := DefaultErosion(19, 8000)
	first, _, err := SynthesizeTerrain(flatTerrain(160, 140, 35), features, &settings)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		again, _, err := SynthesizeTerrain(flatTerrain(160, 140, 35), features, &settings)
		if err != nil {
			t.Fatal(err)
		}
		for i := range first.Pix {
			if first.Pix[i] != again.Pix[i] {
				t.Fatalf("attempt %d: pixel %d differs (%d vs %d)", attempt, i, first.Pix[i], again.Pix[i])
			}
		}
	}
}

// TestTerrainNoiseHasNoAxisAlignedGrid guards the artefact that made the first
// implementation unusable: value noise folded into ridges traced the lattice
// and produced a rectangular mesh of creases. Comparing variation along the
// axes with variation along the diagonals detects that bias.
func TestTerrainNoiseHasNoAxisAlignedGrid(t *testing.T) {
	noise := DefaultMountainNoise(101, 1.0/16)
	axis, diagonal := 0.0, 0.0
	const span = 220
	for y := 8; y < span; y++ {
		for x := 8; x < span; x++ {
			here := noise.Sample(float64(x), float64(y))
			axis += math.Abs(here - noise.Sample(float64(x-1), float64(y)))
			axis += math.Abs(here - noise.Sample(float64(x), float64(y-1)))
			diagonal += math.Abs(here-noise.Sample(float64(x-1), float64(y-1))) / math.Sqrt2
			diagonal += math.Abs(here-noise.Sample(float64(x-1), float64(y+1))) / math.Sqrt2
		}
	}
	ratio := axis / diagonal
	// Isotropic noise gives a ratio near 1 once the diagonals are corrected for
	// their longer step. A lattice-aligned field concentrates its change on the
	// axes and pushes this well above 1.
	if ratio < 0.75 || ratio > 1.3 {
		t.Fatalf("noise is anisotropic: axis/diagonal variation ratio %.3f", ratio)
	}
}

func TestTerrainRejectsBadFeatures(t *testing.T) {
	source := flatTerrain(60, 60, 30)
	for _, tc := range []struct {
		name    string
		feature MapTerrainFeature
	}{
		{"empty path", MapTerrainFeature{Kind: TerrainRange, Width: 10, Amplitude: 10}},
		{"zero width", MapTerrainFeature{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 10, Y: 10}}, Amplitude: 10}},
		{"unknown kind", MapTerrainFeature{Kind: "atlantis", Path: []MapTerrainPoint{{X: 10, Y: 10}}, Width: 5, Amplitude: 10}},
		{"NaN amplitude", MapTerrainFeature{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 10, Y: 10}}, Width: 5, Amplitude: math.NaN()}},
		{"NaN roughness", MapTerrainFeature{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 10, Y: 10}}, Width: 5, Amplitude: 10, Roughness: math.NaN()}},
	} {
		if _, _, err := SynthesizeTerrain(source, []MapTerrainFeature{tc.feature}, nil); err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
	if _, _, err := SynthesizeTerrain(nil, []MapTerrainFeature{{Kind: TerrainRange, Path: []MapTerrainPoint{{X: 1, Y: 1}}, Width: 2, Amplitude: 1}}, nil); err == nil {
		t.Fatal("expected a nil source to be refused")
	}
	if _, _, err := SynthesizeTerrain(source, nil, nil); err == nil {
		t.Fatal("expected an empty feature list to be refused")
	}
}
