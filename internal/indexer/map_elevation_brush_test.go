package indexer

import (
	"image"
	"math"
	"testing"
)

// flatHeightmap builds ground at a uniform level, so any variation in the
// result comes from the brush rather than the source.
func flatHeightmap(w, h int, level uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = level
	}
	return img
}

func TestElevationBrushLeavesNoSeamAtTheEdge(t *testing.T) {
	const level = 37 // this workspace's median playable-land elevation
	source := flatHeightmap(200, 200, level)
	out, change, err := ApplyElevationBrushes(source, []MapElevationBrush{
		{CenterX: 100, CenterY: 100, Radius: 60, Amplitude: 60, Roughness: 0.8, Seed: 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	if change.ChangedPixels == 0 {
		t.Fatal("the brush changed nothing")
	}
	// Outside the radius the ground must be untouched, and just inside it the
	// rise must still be small: a visible step there is the seam that makes
	// generated terrain look pasted on.
	for _, tc := range []struct {
		name    string
		x, y    int
		wantMax uint8
	}{
		{"outside", 100 + 61, 100, level},
		{"far outside", 10, 10, level},
		{"just inside", 100 + 58, 100, level + 3},
	} {
		got := out.GrayAt(tc.x, tc.y).Y
		if got > tc.wantMax {
			t.Fatalf("%s at (%d,%d) = %d, want <= %d", tc.name, tc.x, tc.y, got, tc.wantMax)
		}
	}
	if out.GrayAt(100, 100).Y <= level+20 {
		t.Fatalf("the centre barely rose: %d", out.GrayAt(100, 100).Y)
	}
	// The source must be left alone; callers rely on being able to compare.
	if source.GrayAt(100, 100).Y != level {
		t.Fatal("the brush modified the source image")
	}
}

// TestElevationBrushIsDeterministic matters because a generated mountain has to
// be reproducible: a reviewer who reruns the same request must get the same
// terrain, or the output cannot be verified at all.
func TestElevationBrushIsDeterministic(t *testing.T) {
	brush := MapElevationBrush{CenterX: 60, CenterY: 60, Radius: 40, Amplitude: 55, Roughness: 1, Seed: 7}
	first, _, err := ApplyElevationBrushes(flatHeightmap(128, 128, 30), []MapElevationBrush{brush})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		again, _, err := ApplyElevationBrushes(flatHeightmap(128, 128, 30), []MapElevationBrush{brush})
		if err != nil {
			t.Fatal(err)
		}
		for i := range first.Pix {
			if first.Pix[i] != again.Pix[i] {
				t.Fatalf("attempt %d: pixel %d differs (%d vs %d)", attempt, i, first.Pix[i], again.Pix[i])
			}
		}
	}
	// A different seed must produce different terrain, otherwise the seed is
	// not actually selecting anything.
	other, _, err := ApplyElevationBrushes(flatHeightmap(128, 128, 30),
		[]MapElevationBrush{{CenterX: 60, CenterY: 60, Radius: 40, Amplitude: 55, Roughness: 1, Seed: 8}})
	if err != nil {
		t.Fatal(err)
	}
	same := true
	for i := range first.Pix {
		if first.Pix[i] != other.Pix[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("two different seeds produced identical terrain")
	}
}

// TestElevationBrushRoughnessProducesRidges checks the property that separates
// a mountain range from a mound: the surface must vary, not rise monotonically.
func TestElevationBrushRoughnessProducesRidges(t *testing.T) {
	smooth, _, err := ApplyElevationBrushes(flatHeightmap(160, 160, 30),
		[]MapElevationBrush{{CenterX: 80, CenterY: 80, Radius: 70, Amplitude: 70, Roughness: 0, Seed: 3}})
	if err != nil {
		t.Fatal(err)
	}
	rough, _, err := ApplyElevationBrushes(flatHeightmap(160, 160, 30),
		[]MapElevationBrush{{CenterX: 80, CenterY: 80, Radius: 70, Amplitude: 70, Roughness: 1, Seed: 3}})
	if err != nil {
		t.Fatal(err)
	}
	// Count sign changes along a line through the centre. A dome crosses from
	// rising to falling once; ridged relief crosses many times.
	reversals := func(img *image.Gray) int {
		count, previous := 0, 0
		for x := 12; x < 148; x++ {
			delta := int(img.GrayAt(x, 80).Y) - int(img.GrayAt(x-1, 80).Y)
			if delta == 0 {
				continue
			}
			sign := 1
			if delta < 0 {
				sign = -1
			}
			if previous != 0 && sign != previous {
				count++
			}
			previous = sign
		}
		return count
	}
	smoothReversals, roughReversals := reversals(smooth), reversals(rough)
	if smoothReversals > 2 {
		t.Fatalf("a dome should rise and fall once, got %d reversals", smoothReversals)
	}
	if roughReversals < 6 {
		t.Fatalf("ridged relief produced only %d reversals; it is not ridged", roughReversals)
	}
}

func TestElevationBrushCarvesAndClamps(t *testing.T) {
	// A negative amplitude must dig, and hitting the floor must be reported
	// rather than silently producing a flat crater.
	out, change, err := ApplyElevationBrushes(flatHeightmap(80, 80, 20),
		[]MapElevationBrush{{CenterX: 40, CenterY: 40, Radius: 25, Amplitude: -60, Roughness: 0, Seed: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if out.GrayAt(40, 40).Y != 0 {
		t.Fatalf("centre = %d, expected it carved to the floor", out.GrayAt(40, 40).Y)
	}
	if change.LoweredPixels == 0 || change.RaisedPixels != 0 {
		t.Fatalf("expected only lowering, got raised=%d lowered=%d", change.RaisedPixels, change.LoweredPixels)
	}
	if change.ClampedPixels == 0 {
		t.Fatal("clamping at the floor was not reported")
	}
	if len(change.Warnings) == 0 {
		t.Fatal("clamping produced no warning")
	}
}

func TestElevationBrushReportsPeakAndRejectsBadInput(t *testing.T) {
	_, change, err := ApplyElevationBrushes(flatHeightmap(60, 60, 40),
		[]MapElevationBrush{{CenterX: 30, CenterY: 30, Radius: 20, Amplitude: 50, Roughness: 0.5, Seed: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if change.PeakBefore != 40 {
		t.Fatalf("peak before = %d, want 40", change.PeakBefore)
	}
	if change.PeakAfter <= change.PeakBefore {
		t.Fatalf("peak did not rise: %d -> %d", change.PeakBefore, change.PeakAfter)
	}
	if math.Abs(change.MeanDelta) < 1 {
		t.Fatalf("mean delta %.2f is implausibly small for a 50-level brush", change.MeanDelta)
	}

	for _, tc := range []struct {
		name   string
		brush  MapElevationBrush
		source *image.Gray
	}{
		{"zero radius", MapElevationBrush{CenterX: 10, CenterY: 10, Radius: 0, Amplitude: 10}, flatHeightmap(40, 40, 10)},
		{"NaN amplitude", MapElevationBrush{CenterX: 10, CenterY: 10, Radius: 5, Amplitude: math.NaN()}, flatHeightmap(40, 40, 10)},
		// NaN roughness must be refused too: the shared clamp01 is Max/Min based
		// and lets NaN through, which would write arbitrary bytes into the map.
		{"NaN roughness", MapElevationBrush{CenterX: 10, CenterY: 10, Radius: 5, Amplitude: 10, Roughness: math.NaN()}, flatHeightmap(40, 40, 10)},
		{"Inf roughness", MapElevationBrush{CenterX: 10, CenterY: 10, Radius: 5, Amplitude: 10, Roughness: math.Inf(1)}, flatHeightmap(40, 40, 10)},
	} {
		if _, _, err := ApplyElevationBrushes(tc.source, []MapElevationBrush{tc.brush}); err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
	if _, _, err := ApplyElevationBrushes(nil, []MapElevationBrush{{Radius: 1}}); err == nil {
		t.Fatal("expected a nil source to be refused")
	}
	if _, _, err := ApplyElevationBrushes(flatHeightmap(10, 10, 1), nil); err == nil {
		t.Fatal("expected an empty brush list to be refused")
	}
}

// A brush entirely off the map is a mistake worth naming rather than a silent
// no-op, since the caller almost certainly meant a coordinate on the map.
func TestElevationBrushReportsOffMapPlacement(t *testing.T) {
	_, change, err := ApplyElevationBrushes(flatHeightmap(50, 50, 20),
		[]MapElevationBrush{{CenterX: 900, CenterY: 900, Radius: 10, Amplitude: 40}})
	if err != nil {
		t.Fatal(err)
	}
	if change.ChangedPixels != 0 {
		t.Fatalf("an off-map brush changed %d pixels", change.ChangedPixels)
	}
	if len(change.Warnings) == 0 {
		t.Fatal("an off-map brush produced no warning")
	}
}
