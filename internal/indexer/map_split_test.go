package indexer

import (
	"testing"
)

// rectRuns builds the geometry of a solid rectangle.
func rectRuns(x0, y0, x1, y1 int) []MapRun {
	runs := make([]MapRun, 0, y1-y0+1)
	for y := y0; y <= y1; y++ {
		runs = append(runs, MapRun{Y: int32(y), X0: int32(x0), X1: int32(x1)})
	}
	return runs
}

func partPixels(t *testing.T, part MapSplitPart) map[[2]int]bool {
	t.Helper()
	pixels := map[[2]int]bool{}
	for _, run := range part.Runs {
		for x := int(run.X0); x <= int(run.X1); x++ {
			pixels[[2]int{x, int(run.Y)}] = true
		}
	}
	return pixels
}

// assertConnected fails unless every pixel of the part is reachable from any
// other by 4-way steps. CK3 cannot represent a province made of detached
// islands, so a split that produces one is unusable regardless of how good the
// boundary looks.
func assertConnected(t *testing.T, part MapSplitPart) {
	t.Helper()
	pixels := partPixels(t, part)
	if len(pixels) == 0 {
		return
	}
	var start [2]int
	for pixel := range pixels {
		start = pixel
		break
	}
	seen := map[[2]int]bool{start: true}
	queue := [][2]int{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			next := [2]int{cur[0] + step[0], cur[1] + step[1]}
			if pixels[next] && !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	if len(seen) != len(pixels) {
		t.Fatalf("part %d is disconnected: %d of %d pixels reachable", part.Index, len(seen), len(pixels))
	}
}

func TestSplitProvincePartitionsEveryPixelExactlyOnce(t *testing.T) {
	runs := rectRuns(10, 10, 39, 29) // 30x20
	result, err := SplitProvinceGeometry(runs, MapSplitRequest{
		ProvinceID: 7,
		Seeds:      []MapSplitSeed{{X: 14, Y: 20}, {X: 35, Y: 20}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcePixel != 600 {
		t.Fatalf("source pixel count = %d, want 600", result.SourcePixel)
	}
	if result.Unreachable != 0 {
		t.Fatalf("a solid rectangle should be fully reachable, got %d unreachable", result.Unreachable)
	}

	seen := map[[2]int]int{}
	assigned := 0
	for _, part := range result.Parts {
		assertConnected(t, part)
		for pixel := range partPixels(t, part) {
			if prior, dup := seen[pixel]; dup {
				t.Fatalf("pixel %v claimed by both part %d and %d", pixel, prior, part.Index)
			}
			seen[pixel] = part.Index
			assigned++
		}
	}
	if assigned != result.SourcePixel {
		t.Fatalf("parts cover %d pixels, source has %d", assigned, result.SourcePixel)
	}
	// Two symmetric seeds in a rectangle should divide it roughly in half; a
	// wildly lopsided split would mean growth is not advancing evenly.
	for _, part := range result.Parts {
		if part.PixelCount < 200 || part.PixelCount > 400 {
			t.Fatalf("part %d has %d pixels, expected a roughly even division", part.Index, part.PixelCount)
		}
	}
}

// ridgeTerrain makes a vertical band expensive to cross. Width matters: the
// boundary shifts by roughly half the band's total crossing cost, so a
// one-pixel line can only nudge it a few pixels no matter how steep it is.
type ridgeTerrain struct{ x0, x1 int }

func (r ridgeTerrain) Resistance(x, _ int) float64 {
	if x >= r.x0 && x <= r.x1 {
		return 1
	}
	return 0
}

// TestSplitProvinceFollowsTerrainRidge is the behaviour that separates this
// from a plain Voronoi split: on flat ground the boundary sits at the midpoint
// between the seeds, but a ridge offset from that midpoint should capture it.
func TestSplitProvinceFollowsTerrainRidge(t *testing.T) {
	runs := rectRuns(0, 0, 59, 9)
	seeds := []MapSplitSeed{{X: 5, Y: 5}, {X: 55, Y: 5}}
	ridge := ridgeTerrain{x0: 38, x1: 42}

	flat, err := SplitProvinceGeometry(runs, MapSplitRequest{ProvinceID: 1, Seeds: seeds}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ridged, err := SplitProvinceGeometry(runs, MapSplitRequest{
		ProvinceID: 1, Seeds: seeds, TerrainWeight: 6,
	}, ridge)
	if err != nil {
		t.Fatal(err)
	}

	// Flat ground puts the frontier at the midpoint, well left of the ridge.
	if flat.Parts[0].MaxX > 32 {
		t.Fatalf("flat boundary at x=%d, expected it near the midpoint x=30", flat.Parts[0].MaxX)
	}
	// With the ridge, neither side wants to pay to cross it, so the frontier
	// should come to rest within the band rather than at the midpoint.
	if got := ridged.Parts[0].MaxX; got < ridge.x0-1 || got > ridge.x1+1 {
		t.Fatalf("boundary settled at x=%d, expected it to rest on the ridge band %d..%d", got, ridge.x0, ridge.x1)
	}
	if ridged.Parts[0].PixelCount <= flat.Parts[0].PixelCount {
		t.Fatalf("terrain did not deflect the boundary: flat left=%d ridged left=%d",
			flat.Parts[0].PixelCount, ridged.Parts[0].PixelCount)
	}
	for _, part := range ridged.Parts {
		assertConnected(t, part)
	}
}

func TestSplitProvinceIsDeterministic(t *testing.T) {
	runs := rectRuns(0, 0, 49, 39)
	request := MapSplitRequest{
		ProvinceID:    3,
		Seeds:         []MapSplitSeed{{X: 5, Y: 5}, {X: 44, Y: 34}, {X: 5, Y: 34}},
		TerrainWeight: 3,
	}
	first, err := SplitProvinceGeometry(runs, request, ridgeTerrain{x0: 24, x1: 26})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		again, err := SplitProvinceGeometry(runs, request, ridgeTerrain{x0: 24, x1: 26})
		if err != nil {
			t.Fatal(err)
		}
		for i := range first.Parts {
			if first.Parts[i].PixelCount != again.Parts[i].PixelCount {
				t.Fatalf("attempt %d: part %d pixel count drifted %d -> %d",
					attempt, i, first.Parts[i].PixelCount, again.Parts[i].PixelCount)
			}
			if len(first.Parts[i].Runs) != len(again.Parts[i].Runs) {
				t.Fatalf("attempt %d: part %d run count drifted", attempt, i)
			}
			for r := range first.Parts[i].Runs {
				if first.Parts[i].Runs[r] != again.Parts[i].Runs[r] {
					t.Fatalf("attempt %d: part %d run %d differs", attempt, i, r)
				}
			}
		}
	}
}

// TestSplitProvinceReportsDisconnectedSource covers a province made of two
// detached blobs with a seed in only one: the far blob cannot be reached, and
// silently dropping those pixels would corrupt provinces.png.
func TestSplitProvinceReportsDisconnectedSource(t *testing.T) {
	runs := append(rectRuns(0, 0, 9, 9), rectRuns(40, 0, 49, 9)...)
	result, err := SplitProvinceGeometry(runs, MapSplitRequest{
		ProvinceID: 9,
		Seeds:      []MapSplitSeed{{X: 2, Y: 2}, {X: 7, Y: 7}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unreachable != 100 {
		t.Fatalf("unreachable = %d, want the 100 pixels of the detached blob", result.Unreachable)
	}
	found := false
	for _, warning := range result.Warnings {
		if len(warning) > 0 && warning[0] == '1' {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning named the unreachable pixels: %v", result.Warnings)
	}
}

func TestSplitProvinceRejectsUnusableRequests(t *testing.T) {
	runs := rectRuns(0, 0, 9, 9)
	for _, tc := range []struct {
		name    string
		request MapSplitRequest
	}{
		{"one seed", MapSplitRequest{ProvinceID: 1, Seeds: []MapSplitSeed{{X: 1, Y: 1}}}},
		{"seed outside bounds", MapSplitRequest{ProvinceID: 1, Seeds: []MapSplitSeed{{X: 1, Y: 1}, {X: 99, Y: 99}}}},
		{"duplicate seed", MapSplitRequest{ProvinceID: 1, Seeds: []MapSplitSeed{{X: 4, Y: 4}, {X: 4, Y: 4}}}},
	} {
		if _, err := SplitProvinceGeometry(runs, tc.request, nil); err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
	// A seed inside the bounding box but outside the province itself must also
	// be refused: an L-shaped province has holes in its box.
	lShaped := append(rectRuns(0, 0, 9, 4), rectRuns(0, 5, 4, 9)...)
	if _, err := SplitProvinceGeometry(lShaped, MapSplitRequest{
		ProvinceID: 2, Seeds: []MapSplitSeed{{X: 1, Y: 1}, {X: 8, Y: 8}},
	}, nil); err == nil {
		t.Fatal("expected a seed in the bounding box but outside the province to be refused")
	}
}
