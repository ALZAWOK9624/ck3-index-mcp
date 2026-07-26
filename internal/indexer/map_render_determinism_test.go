package indexer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

// renderPixelDigest hashes the decoded pixels rather than the PNG bytes so the
// digest is independent of the deflate level.
func renderPixelDigest(t *testing.T, data []byte) string {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := decoded.Bounds()
	sum := sha256.New()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := decoded.At(x, y).RGBA()
			sum.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bb >> 8), byte(a >> 8)})
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// TestMapRenderIsDeterministic pins two guarantees that used to be broken.
// Layer passes walked provinces in Go map order, and because several source
// provinces can land on one output pixel the image differed between runs; and
// resolution scaled line widths inside the caller's own layer slice, so each
// repeat of the same spec drew thinner borders than the last.
func TestMapRenderIsDeterministic(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()

	fontPath := filepath.Join(t.TempDir(), "determinism.ttf")
	if err := os.WriteFile(fontPath, goregular.TTF, 0644); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		spec MapRenderSpec
	}{
		{"political_atlas", MapRenderSpec{Recipe: "political_atlas", Target: "all", Width: 480, FontPath: fontPath}},
		{"thematic_atlas", MapRenderSpec{Recipe: "thematic_atlas", Target: "all", Theme: "culture", Width: 480, FontPath: fontPath}},
		{"wide_borders_and_markers", MapRenderSpec{
			Target: "all", Width: 480, FontPath: fontPath,
			Layers: []MapRenderLayer{
				{Type: "borders", Level: "county", Color: "#292620d0", LineWidth: 3},
				{Type: "borders", Source: "outer", Color: "#101719", LineWidth: 5},
				{Type: "markers", Source: "capitals", LineWidth: 4},
			},
		}},
		{"explicit_metric", MapRenderSpec{
			Target: "all", Width: 480, FontPath: fontPath,
			Layers: []MapRenderLayer{
				{Type: "fill", Level: "county", Palette: "political", LineWidth: 4, Metric: &MapMetricSpec{
					Target: "all", Level: "county", Kind: "category", Field: "entity_id",
					Aggregate: "majority", Provenance: "indexed"}},
				{Type: "borders", Level: "county", Color: "#292620d0", LineWidth: 3},
			},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, err := db.LLMMapRender(ctx, test.spec, LLMOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := renderPixelDigest(t, first.PNG)
			for attempt := 1; attempt <= 4; attempt++ {
				repeat, err := db.LLMMapRender(ctx, test.spec, LLMOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if got := renderPixelDigest(t, repeat.PNG); got != want {
					t.Fatalf("attempt %d rendered different pixels: %s != %s", attempt, got[:16], want[:16])
				}
				if repeat.Bytes != first.Bytes {
					t.Fatalf("attempt %d encoded %d bytes, want %d", attempt, repeat.Bytes, first.Bytes)
				}
			}
		})
	}
}

// TestMapRenderDoesNotMutateCallerSpec makes the aliasing contract explicit:
// the caller's layers must come back exactly as they went in.
func TestMapRenderDoesNotMutateCallerSpec(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()

	metric := MapMetricSpec{Target: "all", Level: "county", Kind: "category",
		Field: "entity_id", Aggregate: "majority", Provenance: "indexed"}
	layers := []MapRenderLayer{
		{Type: "fill", Level: "county", Palette: "political", Metric: &metric},
		{Type: "borders", Level: "county", Color: "#292620d0", LineWidth: 6},
	}
	before := append([]MapRenderLayer(nil), layers...)
	beforeMetric := metric

	if _, err := db.LLMMapRender(context.Background(), MapRenderSpec{
		Target: "all", Width: 480, Layers: layers,
	}, LLMOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := range layers {
		if layers[i].LineWidth != before[i].LineWidth {
			t.Errorf("layer %d line_width mutated: %d -> %d", i, before[i].LineWidth, layers[i].LineWidth)
		}
		if layers[i].Palette != before[i].Palette {
			t.Errorf("layer %d palette mutated: %q -> %q", i, before[i].Palette, layers[i].Palette)
		}
	}
	if fmt.Sprintf("%+v", metric) != fmt.Sprintf("%+v", beforeMetric) {
		t.Errorf("caller metric mutated:\n  before=%+v\n   after=%+v", beforeMetric, metric)
	}
}

// TestMapRenderCanvasStaysOpaque keeps the encoder on the 24-bit path. Text
// drawn through the bitmap fallback used to copy its own alpha into the canvas,
// which both punched translucent holes through the map and pushed every plate
// onto the larger 32-bit RGBA encoding.
func TestMapRenderCanvasStaysOpaque(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()

	// No font configured: labels and the badge fall back to the bitmap glyphs.
	t.Setenv("CK3_INDEX_MAP_FONT", "")
	result, err := db.LLMMapRender(context.Background(), MapRenderSpec{
		Recipe: "political_atlas", Target: "all", Width: 480,
	}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(result.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.(*image.NRGBA); ok {
		b := decoded.Bounds()
		count := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if _, _, _, a := decoded.At(x, y).RGBA(); a != 0xffff {
					count++
				}
			}
		}
		t.Fatalf("plate encoded as 32-bit RGBA with %d non-opaque pixels; map output must stay opaque", count)
	}
}

// TestMapRenderPNGCompressionModes checks the encode-latency knob: every mode
// must produce the same pixels, differing only in encoded size.
func TestMapRenderPNGCompressionModes(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	spec := MapRenderSpec{Recipe: "political_atlas", Target: "all", Width: 480}

	t.Setenv("CK3_INDEX_MAP_PNG_COMPRESSION", "default")
	reference, err := db.LLMMapRender(ctx, spec, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reference.PNGCompression != "default" {
		t.Fatalf("png_compression = %q, want default", reference.PNGCompression)
	}
	want := renderPixelDigest(t, reference.PNG)

	for _, mode := range []string{"fast", "best", "none", "auto"} {
		t.Setenv("CK3_INDEX_MAP_PNG_COMPRESSION", mode)
		result, err := db.LLMMapRender(ctx, spec, LLMOptions{})
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if got := renderPixelDigest(t, result.PNG); got != want {
			t.Fatalf("mode %s changed pixels: %s != %s", mode, got[:16], want[:16])
		}
		// This plate is far below the auto threshold, so auto must stay lossless-small.
		wantMode := mode
		if mode == "auto" {
			wantMode = "default"
		}
		if result.PNGCompression != wantMode {
			t.Fatalf("mode %s reported png_compression %q, want %q", mode, result.PNGCompression, wantMode)
		}
	}
}
