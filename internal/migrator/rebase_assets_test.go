package migrator

import (
	"strings"
	"testing"
)

func TestBuildRebaseRasterCandidatePNGZeroConflict(t *testing.T) {
	base := testRebaseRaster(2, 1,
		0x00, 0x00, 0x00, 0xff,
		0x00, 0x00, 0x00, 0xff,
	)
	project := cloneTestRebaseRaster(base)
	setTestRebasePixel(&project, 0, 0, 0xff, 0x00, 0x00, 0xff)
	target := cloneTestRebaseRaster(base)
	setTestRebasePixel(&target, 1, 0, 0x00, 0x00, 0xff, 0xff)

	baseData := mustEncodeTestRebasePNG(t, base)
	projectData := mustEncodeTestRebasePNG(t, project)
	targetData := mustEncodeTestRebasePNG(t, target)
	candidate, conflicts, err := buildRebaseRasterCandidate(
		"gfx/map/test.png", baseData, projectData, targetData,
		&SnapshotFile{SHA256: "base-sha"}, &SnapshotFile{SHA256: "project-sha"}, &SnapshotFile{SHA256: "target-sha"},
	)
	if err != nil {
		t.Fatalf("build PNG candidate: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no PNG conflicts, got %#v", conflicts)
	}
	merged, err := decodeRebasePNG(candidate)
	if err != nil {
		t.Fatalf("decode candidate PNG: %v", err)
	}
	assertTestRebasePixel(t, merged, 0, 0, 0xff, 0x00, 0x00, 0xff)
	assertTestRebasePixel(t, merged, 1, 0, 0x00, 0x00, 0xff, 0xff)
}

func TestBuildRebaseRasterCandidatePNGConflictCarriesHashes(t *testing.T) {
	base := testRebaseRaster(1, 1, 0x00, 0x00, 0x00, 0xff)
	project := cloneTestRebaseRaster(base)
	setTestRebasePixel(&project, 0, 0, 0xff, 0x00, 0x00, 0xff)
	target := cloneTestRebaseRaster(base)
	setTestRebasePixel(&target, 0, 0, 0x00, 0x00, 0xff, 0xff)
	baseData := mustEncodeTestRebasePNG(t, base)
	projectData := mustEncodeTestRebasePNG(t, project)
	targetData := mustEncodeTestRebasePNG(t, target)
	baseFile := &SnapshotFile{SHA256: "base-sha"}
	projectFile := &SnapshotFile{SHA256: "project-sha"}
	targetFile := &SnapshotFile{SHA256: "target-sha"}

	candidate, conflicts, err := buildRebaseRasterCandidate("gfx/map/test.png", baseData, projectData, targetData, baseFile, projectFile, targetFile)
	if err != nil {
		t.Fatalf("build conflicting PNG candidate: %v", err)
	}
	if candidate != nil {
		t.Fatal("a conflicting raster must not publish a candidate")
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected one PNG conflict, got %#v", conflicts)
	}
	conflict := conflicts[0]
	if conflict.Code != "pixel_merge_conflict" {
		t.Fatalf("conflict code = %q, want pixel_merge_conflict", conflict.Code)
	}
	if !strings.Contains(conflict.Message, "project_sha256=project-sha") || !strings.Contains(conflict.Message, "target_sha256=target-sha") {
		t.Fatalf("conflict must retain project/target hash metadata: %q", conflict.Message)
	}

	changedProjectFile := &SnapshotFile{SHA256: "project-sha-2"}
	_, changedConflicts, err := buildRebaseRasterCandidate("gfx/map/test.png", baseData, projectData, targetData, baseFile, changedProjectFile, targetFile)
	if err != nil || len(changedConflicts) != 1 {
		t.Fatalf("build changed-metadata conflict: conflicts=%#v err=%v", changedConflicts, err)
	}
	if conflict.ID == changedConflicts[0].ID {
		t.Fatal("conflict ID did not incorporate project hash metadata")
	}
}

func TestBuildRebaseRasterCandidateTGAZeroConflict(t *testing.T) {
	base := testRebaseRaster(3, 2,
		0x00, 0x00, 0x00, 0xff, 0x11, 0x11, 0x11, 0xff, 0x22, 0x22, 0x22, 0xff,
		0x33, 0x33, 0x33, 0xff, 0x44, 0x44, 0x44, 0xff, 0x55, 0x55, 0x55, 0xff,
	)
	project := cloneTestRebaseRaster(base)
	setTestRebasePixel(&project, 0, 0, 0xff, 0x80, 0x00, 0x7f)
	target := cloneTestRebaseRaster(base)
	setTestRebasePixel(&target, 2, 1, 0x00, 0x80, 0xff, 0xc0)

	baseData := mustEncodeTestRebaseTGA(t, base, rebaseTGAFormat{depth: 32, rle: false, topOrigin: true})
	projectData := mustEncodeTestRebaseTGA(t, project, rebaseTGAFormat{depth: 32, rle: true, topOrigin: false, rightOrigin: true, id: []byte("project")})
	targetData := mustEncodeTestRebaseTGA(t, target, rebaseTGAFormat{depth: 32, rle: false, topOrigin: true})
	candidate, conflicts, err := buildRebaseRasterCandidate(
		"gfx/map/test.tga", baseData, projectData, targetData,
		&SnapshotFile{SHA256: "base-sha"}, &SnapshotFile{SHA256: "project-sha"}, &SnapshotFile{SHA256: "target-sha"},
	)
	if err != nil {
		t.Fatalf("build TGA candidate: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no TGA conflicts, got %#v", conflicts)
	}
	if len(candidate) < 18 || candidate[2] != 10 {
		t.Fatalf("candidate should retain the project's RLE TGA representation, header=%v", candidate[:min(len(candidate), 18)])
	}
	merged, err := decodeRebaseTGA(candidate)
	if err != nil {
		t.Fatalf("decode candidate TGA: %v", err)
	}
	assertTestRebasePixel(t, merged, 0, 0, 0xff, 0x80, 0x00, 0x7f)
	assertTestRebasePixel(t, merged, 2, 1, 0x00, 0x80, 0xff, 0xc0)
}

func TestBuildRebaseMapCoordinateCandidateProducesTransparentDelta(t *testing.T) {
	base := testRebaseRaster(2, 1,
		0x00, 0x00, 0x00, 0xff,
		0x00, 0x00, 0x00, 0xff,
	)
	project := cloneTestRebaseRaster(base)
	setTestRebasePixel(&project, 0, 0, 0xff, 0x00, 0x00, 0xff)
	target := cloneTestRebaseRaster(base)
	setTestRebasePixel(&target, 1, 0, 0x00, 0x00, 0xff, 0xff)

	candidate, deltaData, delta, conflicts, err := buildRebaseMapCoordinateCandidate(
		"map_data/provinces.png",
		mustEncodeTestRebasePNG(t, base),
		mustEncodeTestRebasePNG(t, project),
		mustEncodeTestRebasePNG(t, target),
		&SnapshotFile{SHA256: "base"}, &SnapshotFile{SHA256: "project"}, &SnapshotFile{SHA256: "target"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 || delta.ChangedPixels != 1 || delta.OriginX != 0 || delta.OriginY != 0 {
		t.Fatalf("coordinate delta metadata=%+v conflicts=%+v", delta, conflicts)
	}
	patch, err := decodeRebasePNG(deltaData)
	if err != nil {
		t.Fatal(err)
	}
	assertTestRebasePixel(t, patch, 0, 0, 0xff, 0x00, 0x00, 0xff)
	assertTestRebasePixel(t, patch, 1, 0, 0x00, 0x00, 0x00, 0x00)
	merged, err := decodeRebasePNG(candidate)
	if err != nil {
		t.Fatal(err)
	}
	assertTestRebasePixel(t, merged, 0, 0, 0xff, 0x00, 0x00, 0xff)
	assertTestRebasePixel(t, merged, 1, 0, 0x00, 0x00, 0xff, 0xff)
}

func TestBuildRebaseMapCoordinateCandidateBlocksDimensionMismatch(t *testing.T) {
	base := testRebaseRaster(1, 1, 0x00, 0x00, 0x00, 0xff)
	project := cloneTestRebaseRaster(base)
	target := testRebaseRaster(2, 1,
		0x00, 0x00, 0x00, 0xff,
		0x00, 0x00, 0x00, 0xff,
	)
	_, _, _, conflicts, err := buildRebaseMapCoordinateCandidate(
		"map_data/provinces.png",
		mustEncodeTestRebasePNG(t, base),
		mustEncodeTestRebasePNG(t, project),
		mustEncodeTestRebasePNG(t, target),
		&SnapshotFile{SHA256: "base"}, &SnapshotFile{SHA256: "project"}, &SnapshotFile{SHA256: "target"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].Code != "map_coordinate_dimension_mismatch" {
		t.Fatalf("dimension mismatch conflicts = %+v", conflicts)
	}
}

func TestRebaseTGA24RoundTrip(t *testing.T) {
	raster := testRebaseRaster(2, 2,
		0x10, 0x20, 0x30, 0xff, 0x40, 0x50, 0x60, 0xff,
		0x70, 0x80, 0x90, 0xff, 0xa0, 0xb0, 0xc0, 0xff,
	)
	encoded := mustEncodeTestRebaseTGA(t, raster, rebaseTGAFormat{depth: 24, rle: true, topOrigin: false, rightOrigin: true})
	decoded, err := decodeRebaseTGA(encoded)
	if err != nil {
		t.Fatalf("decode 24-bit RLE TGA: %v", err)
	}
	if decoded.tga.depth != 24 || !decoded.tga.rle || decoded.tga.topOrigin || !decoded.tga.rightOrigin {
		t.Fatalf("decoded 24-bit TGA format = %#v", decoded.tga)
	}
	if string(decoded.pixels) != string(raster.pixels) {
		t.Fatalf("24-bit TGA round trip changed pixels: got %v want %v", decoded.pixels, raster.pixels)
	}
}

func testRebaseRaster(width, height int, pixels ...byte) rebaseRaster {
	return rebaseRaster{width: width, height: height, pixels: append([]byte(nil), pixels...)}
}

func cloneTestRebaseRaster(source rebaseRaster) rebaseRaster {
	clone := source
	clone.pixels = append([]byte(nil), source.pixels...)
	return clone
}

func setTestRebasePixel(raster *rebaseRaster, x, y int, red, green, blue, alpha byte) {
	offset := (y*raster.width + x) * 4
	raster.pixels[offset+0] = red
	raster.pixels[offset+1] = green
	raster.pixels[offset+2] = blue
	raster.pixels[offset+3] = alpha
}

func assertTestRebasePixel(t *testing.T, raster rebaseRaster, x, y int, red, green, blue, alpha byte) {
	t.Helper()
	offset := (y*raster.width + x) * 4
	got := raster.pixels[offset : offset+4]
	want := []byte{red, green, blue, alpha}
	if string(got) != string(want) {
		t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
	}
}

func mustEncodeTestRebasePNG(t *testing.T, raster rebaseRaster) []byte {
	t.Helper()
	encoded, err := encodeRebasePNG(raster)
	if err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return encoded
}

func mustEncodeTestRebaseTGA(t *testing.T, raster rebaseRaster, format rebaseTGAFormat) []byte {
	t.Helper()
	encoded, err := encodeRebaseTGA(raster, format)
	if err != nil {
		t.Fatalf("encode test TGA: %v", err)
	}
	return encoded
}
