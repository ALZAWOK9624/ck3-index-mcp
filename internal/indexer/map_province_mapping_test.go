package indexer

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestMapProvinceMappingDetectsRenumberSplitAndMerge(t *testing.T) {
	tests := []struct {
		name          string
		source        [][]int
		target        [][]int
		wantKind      string
		wantGroups    int
		wantUnmappedS int
		wantUnmappedT int
	}{
		{
			name:     "renumbered",
			source:   [][]int{{1, 1, 2, 2}, {1, 1, 2, 2}},
			target:   [][]int{{10, 10, 20, 20}, {10, 10, 20, 20}},
			wantKind: "renumbered", wantGroups: 2,
		},
		{
			name:     "split",
			source:   [][]int{{1, 1, 1, 1}, {1, 1, 1, 1}},
			target:   [][]int{{10, 10, 11, 11}, {10, 10, 11, 11}},
			wantKind: "split", wantGroups: 1,
		},
		{
			name:     "merge",
			source:   [][]int{{1, 1, 2, 2}, {1, 1, 2, 2}},
			target:   [][]int{{10, 10, 10, 10}, {10, 10, 10, 10}},
			wantKind: "merge", wantGroups: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := mappingFixtureConfig(t, test.source, test.target, "", "")
			result, err := MapProvinceMapping(context.Background(), cfg, MapProvinceMappingSpec{Source: "old", Target: "new"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Groups) != test.wantGroups {
				t.Fatalf("groups=%+v, want %d", result.Groups, test.wantGroups)
			}
			for _, group := range result.Groups {
				if group.Kind != test.wantKind {
					t.Fatalf("group=%+v, want kind %s", group, test.wantKind)
				}
			}
			if result.Summary.UnmappedSource != test.wantUnmappedS || result.Summary.UnmappedTarget != test.wantUnmappedT {
				t.Fatalf("summary=%+v", result.Summary)
			}
			if result.ComparedPixels != 8 {
				t.Fatalf("compared_pixels=%d, want 8", result.ComparedPixels)
			}
		})
	}
}

func TestMapProvinceMappingUsesControlPointWarp(t *testing.T) {
	cfg := mappingFixtureConfig(t,
		[][]int{{1, 2}, {1, 2}},
		[][]int{{10, 10}, {20, 20}},
		"", "",
	)
	result, err := MapProvinceMapping(context.Background(), cfg, MapProvinceMappingSpec{
		Source: "old", Target: "new",
		ControlPoints: []MapControlPoint{
			{SourceX: 0, SourceY: 0, TargetX: 0, TargetY: 0},
			{SourceX: 1, SourceY: 0, TargetX: 0, TargetY: 1},
			{SourceX: 1, SourceY: 1, TargetX: 1, TargetY: 1},
			{SourceX: 0, SourceY: 1, TargetX: 1, TargetY: 0},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 || result.Summary.Renumbered != 2 || result.ComparedPixels != 4 {
		t.Fatalf("unexpected warped result: %+v", result)
	}
}

func TestMapProvinceMappingNormalizesDifferentRasterSizes(t *testing.T) {
	cfg := mappingFixtureConfig(t,
		[][]int{{1, 2}, {1, 2}},
		[][]int{{10, 10, 20, 20}, {10, 10, 20, 20}, {10, 10, 20, 20}, {10, 10, 20, 20}},
		"", "",
	)
	result, err := MapProvinceMapping(context.Background(), cfg, MapProvinceMappingSpec{Source: "old", Target: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Renumbered != 2 || len(result.Sources) != 2 {
		t.Fatalf("unexpected scaled result: %+v", result)
	}
	for _, source := range result.Sources {
		if len(source.Candidates) != 1 || source.Candidates[0].SourceShare != 1 || source.Candidates[0].TargetShare != 1 {
			t.Fatalf("scaled shares were not normalized: %+v", source)
		}
	}
}

func TestMapProvinceMappingSeparatesWaterAndLand(t *testing.T) {
	cfg := mappingFixtureConfig(t,
		[][]int{{1, 1, 2, 2}, {1, 1, 2, 2}},
		[][]int{{10, 10, 20, 20}, {10, 10, 20, 20}},
		"sea_zones = { 1 }", "sea_zones = { }",
	)
	result, err := MapProvinceMapping(context.Background(), cfg, MapProvinceMappingSpec{Source: "old", Target: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TypeMismatchPixels != 4 || result.Summary.UnmappedSource != 1 || result.Summary.UnmappedTarget != 1 {
		t.Fatalf("unexpected water result: %+v", result)
	}
	if len(result.Sources) != 2 || result.Sources[0].Classification != "unmapped" || result.Sources[1].Classification != "renumbered" {
		t.Fatalf("source rows=%+v", result.Sources)
	}

	allowed, err := MapProvinceMapping(context.Background(), cfg, MapProvinceMappingSpec{Source: "old", Target: "new", AllowCrossWater: true})
	if err != nil {
		t.Fatal(err)
	}
	if allowed.TypeMismatchPixels != 0 || allowed.Summary.Renumbered != 2 {
		t.Fatalf("cross-water result=%+v", allowed)
	}
}

func TestMapProvinceMappingRejectsBadControlPoints(t *testing.T) {
	cfg := mappingFixtureConfig(t, [][]int{{1, 1}, {1, 1}}, [][]int{{2, 2}, {2, 2}}, "", "")
	_, err := MapProvinceMapping(context.Background(), cfg, MapProvinceMappingSpec{
		Source: "old", Target: "new",
		ControlPoints: []MapControlPoint{
			{SourceX: 0, SourceY: 0},
			{SourceX: 0, SourceY: 0},
			{SourceX: 1, SourceY: 1},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate control point error")
	}
}

func mappingFixtureConfig(t *testing.T, sourceLabels, targetLabels [][]int, sourceDefault, targetDefault string) Config {
	t.Helper()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "old")
	targetRoot := filepath.Join(root, "new")
	writeMappingRaster(t, sourceRoot, sourceLabels, sourceDefault)
	writeMappingRaster(t, targetRoot, targetLabels, targetDefault)
	return Config{Sources: []Source{{Name: "old", Path: sourceRoot, Rank: 2}, {Name: "new", Path: targetRoot, Rank: 3}}}
}

func writeMappingRaster(t *testing.T, root string, labels [][]int, defaultMap string) {
	t.Helper()
	mapDir := filepath.Join(root, "map_data")
	if err := os.MkdirAll(mapDir, 0755); err != nil {
		t.Fatal(err)
	}
	palette := map[int]color.RGBA{}
	for _, row := range labels {
		for _, id := range row {
			palette[id] = color.RGBA{R: uint8((id*53)%251 + 1), G: uint8((id*97)%251 + 1), B: uint8((id*193)%251 + 1), A: 255}
		}
	}
	definition := ""
	ids := make([]int, 0, len(palette))
	for id := range palette {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		value := palette[id]
		definition += fmt.Sprintf("%d;%d;%d;%d;province_%d;x\n", id, value.R, value.G, value.B, id)
	}
	if err := os.WriteFile(filepath.Join(mapDir, "definition.csv"), []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	if defaultMap != "" {
		if err := os.WriteFile(filepath.Join(mapDir, "default.map"), []byte(defaultMap), 0644); err != nil {
			t.Fatal(err)
		}
	}
	height, width := len(labels), len(labels[0])
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y, row := range labels {
		if len(row) != width {
			t.Fatal("ragged label fixture")
		}
		for x, id := range row {
			img.SetRGBA(x, y, palette[id])
		}
	}
	f, err := os.Create(filepath.Join(mapDir, "provinces.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
