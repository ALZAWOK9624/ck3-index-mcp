package indexer

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProvincePNG paints one solid province rectangle on a black canvas.
func writeProvincePNG(t *testing.T, path string, w, h int, rect image.Rectangle, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{A: 255})
		}
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func imageSplitFixture(t *testing.T) (string, MapSplitResult, MapSplitPlan) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "provinces.png")
	original := color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 255}
	writeProvincePNG(t, source, 64, 32, image.Rect(10, 8, 50, 24), original)

	runs := rectRuns(10, 8, 49, 23)
	result, err := SplitProvinceGeometry(runs, MapSplitRequest{
		ProvinceID: 100,
		Seeds:      []MapSplitSeed{{X: 14, Y: 16}, {X: 45, Y: 16}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanProvinceSplit(result, MapSplitContext{
		UsedProvinceIDs: map[int]bool{100: true},
		UsedColors:      map[uint32]bool{0x102030: true},
		SourceName:      "Oldshire",
		SourceCounty:    "c_oldshire",
		SourceColor:     0x102030,
	}, MapSplitEmit{Definition: true})
	if err != nil {
		t.Fatal(err)
	}
	return source, result, plan
}

func TestApplySplitRecoloursOnlyTheNewParts(t *testing.T) {
	source, result, plan := imageSplitFixture(t)
	output := filepath.Join(filepath.Dir(source), "out", "provinces.png")

	change, err := ApplyProvinceSplitToImage(source, output, result, plan)
	if err != nil {
		t.Fatal(err)
	}
	created := plan.NewProvinces[0]
	if change.RecoloredCount != created.PixelCount {
		t.Fatalf("recoloured %d pixels, plan expected %d", change.RecoloredCount, created.PixelCount)
	}

	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoded, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[[3]uint8]int{}
	bounds := decoded.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := decoded.At(x, y).RGBA()
			counts[[3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}]++
		}
	}
	newKey := [3]uint8{uint8(created.R), uint8(created.G), uint8(created.B)}
	if counts[newKey] != created.PixelCount {
		t.Fatalf("new colour covers %d pixels, expected %d", counts[newKey], created.PixelCount)
	}
	// The retained part must still carry the original colour, otherwise the
	// province's existing history and titles would point at vanished land.
	if counts[[3]uint8{0x10, 0x20, 0x30}] != plan.RetainedPixels {
		t.Fatalf("retained colour covers %d pixels, expected %d", counts[[3]uint8{0x10, 0x20, 0x30}], plan.RetainedPixels)
	}
	// Total painted area must be conserved: a split moves a boundary, it never
	// creates or destroys land.
	if counts[newKey]+counts[[3]uint8{0x10, 0x20, 0x30}] != result.SourcePixel {
		t.Fatalf("split changed the province's total area")
	}
}

// TestApplySplitRefusesStaleIndex is the safety property: geometry comes from
// the index, and if the image on disk has moved on, recolouring by those
// coordinates would overwrite whatever province occupies them now.
func TestApplySplitRefusesStaleIndex(t *testing.T) {
	source, result, plan := imageSplitFixture(t)
	// Repaint the province a different colour, as an edit outside the index would.
	writeProvincePNG(t, source, 64, 32, image.Rect(10, 8, 50, 24), color.RGBA{R: 0x99, G: 0x88, B: 0x77, A: 255})

	output := filepath.Join(filepath.Dir(source), "stale.png")
	_, err := ApplyProvinceSplitToImage(source, output, result, plan)
	if err == nil {
		t.Fatal("expected a stale index to be refused")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error does not explain the staleness: %v", err)
	}
	if _, statErr := os.Stat(output); statErr == nil {
		t.Fatal("a refused write still produced an output file")
	}
}

func TestApplySplitRefusesInPlaceAndBlockedPlans(t *testing.T) {
	source, result, plan := imageSplitFixture(t)
	if _, err := ApplyProvinceSplitToImage(source, source, result, plan); err == nil {
		t.Fatal("expected an in-place overwrite to be refused")
	}
	blocked := plan
	blocked.Blockers = []string{"unresolved"}
	if _, err := ApplyProvinceSplitToImage(source, filepath.Join(t.TempDir(), "o.png"), result, blocked); err == nil {
		t.Fatal("expected a blocked plan to be refused")
	}
}
