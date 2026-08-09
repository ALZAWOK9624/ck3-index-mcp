package indexer

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditMapAssetsFindsProvinceAndRiverIntegrityFailures(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	mapData := filepath.Join(project, "map_data")
	if err := os.MkdirAll(mapData, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mapData, "definition.csv"), []byte("0;0;0;0;x;x;\n1;255;0;0;red;x;\n2;0;255;0;green;x;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	provinceImage := image.NewRGBA(image.Rect(0, 0, 2, 1))
	provinceImage.Set(0, 0, color.RGBA{R: 255, A: 255})
	provinceImage.Set(1, 0, color.RGBA{A: 255})
	writePNGForAuditTest(t, filepath.Join(mapData, "provinces.png"), provinceImage)

	palette := canonicalCK3RiverPalette()
	colors := make(color.Palette, len(palette))
	for index, rgb := range palette {
		colors[index] = color.RGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 255}
	}
	colors[15] = color.RGBA{R: 1, G: 2, B: 3, A: 255}
	riverImage := image.NewPaletted(image.Rect(0, 0, 4, 1), colors)
	copy(riverImage.Pix, []byte{0, 3, 3, 255})
	writePNGForAuditTest(t, filepath.Join(mapData, "rivers.png"), riverImage)

	cfg := Config{Sources: []Source{{Name: "project", Path: project, Rank: 1}}}
	result, err := AuditMapAssets(context.Background(), cfg, "summary", 8)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "error" {
		t.Fatalf("status = %q, want error: %+v", result.Status, result.Findings)
	}
	for _, code := range []string{"map_provinces_black_pixels", "map_provinces_undefined_colors", "map_definition_missing_pixels", "map_rivers_palette_order"} {
		if !mapAuditHasCode(result, code) {
			t.Fatalf("missing %s: %+v", code, result.Findings)
		}
	}
	if mapAuditHasCode(result, "map_rivers_topology") {
		t.Fatalf("valid source/body/body path produced topology warning: %+v", result.Findings)
	}
	if result.Provenance.Commit != mapAssetAuditUpstreamCommit || len(result.Provenance.ExcludedOverlap) == 0 {
		t.Fatalf("missing absorption provenance: %+v", result.Provenance)
	}
}

func TestAuditMapAssetsOperationAndUnavailableWorkspace(t *testing.T) {
	cfg := Config{Sources: []Source{{Name: "project", Path: t.TempDir(), Rank: 1}}}
	result, err := AuditMapAssets(context.Background(), cfg, "rivers", 4)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unavailable" || result.Operation != "rivers" {
		t.Fatalf("unexpected unavailable result: %+v", result)
	}
	if _, err := AuditMapAssets(context.Background(), cfg, "heuristics", 4); err == nil {
		t.Fatal("unknown audit operation was accepted")
	}
}

func TestAuditMapAssetsReportsDuplicateDefinitionIDAndRGB(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	mapData := filepath.Join(project, "map_data")
	if err := os.MkdirAll(mapData, 0755); err != nil {
		t.Fatal(err)
	}
	definition := "1;1;2;3;one;x\n1;4;5;6;duplicate-id;x\n2;1;2;3;duplicate-rgb;x\n"
	if err := os.WriteFile(filepath.Join(mapData, "definition.csv"), []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	provinceImage := image.NewRGBA(image.Rect(0, 0, 1, 1))
	provinceImage.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	writePNGForAuditTest(t, filepath.Join(mapData, "provinces.png"), provinceImage)

	result, err := AuditMapAssets(context.Background(), Config{Sources: []Source{{Name: "project", Path: project, Rank: 1}}}, "provinces", 8)
	if err != nil {
		t.Fatal(err)
	}
	idFinding := mapAuditFinding(result, "map_definition_duplicate_ids")
	colorFinding := mapAuditFinding(result, "map_definition_duplicate_colors")
	if idFinding == nil || idFinding.Count != 1 || len(idFinding.Samples) != 1 {
		t.Fatalf("duplicate ID finding=%+v", idFinding)
	}
	if colorFinding == nil || colorFinding.Count != 1 || len(colorFinding.Samples) != 1 {
		t.Fatalf("duplicate RGB finding=%+v", colorFinding)
	}
}

func writePNGForAuditTest(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
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

func mapAuditHasCode(result MapAssetAuditResult, code string) bool {
	return mapAuditFinding(result, code) != nil
}

func mapAuditFinding(result MapAssetAuditResult, code string) *MapAssetAuditFinding {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return &finding
		}
	}
	return nil
}
