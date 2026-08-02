package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
	"ck3-index/internal/mcpserver"
)

func TestMapPhysicalContextCLIRequest(t *testing.T) {
	request := mapPhysicalContextCLIRequest{
		TargetType:           "province",
		Target:               "1911",
		Targets:              []string{"1911"},
		Operation:            "surface",
		IncludeAdjacentWater: true,
		Limit:                6,
	}
	spec := request.spec()
	if spec.TargetType != request.TargetType ||
		spec.Target != request.Target ||
		!reflect.DeepEqual(spec.Targets, request.Targets) ||
		spec.Operation != request.Operation ||
		spec.IncludeAdjacentWater != request.IncludeAdjacentWater {
		t.Fatalf("CLI request did not preserve physical-context fields: request=%+v spec=%+v", request, spec)
	}
	if limit, err := request.normalizedLimit(); err != nil || limit != 6 {
		t.Fatalf("normalized limit = %d, %v; want 6", limit, err)
	}
}

func TestMapPhysicalContextCLILimit(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		want    int
		wantErr bool
	}{
		{name: "default", limit: 0, want: 16},
		{name: "minimum", limit: 1, want: 1},
		{name: "maximum", limit: 20, want: 20},
		{name: "negative", limit: -1, wantErr: true},
		{name: "too large", limit: 21, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (mapPhysicalContextCLIRequest{Limit: tt.limit}).normalizedLimit()
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizedLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("normalizedLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunScanFilesRequiresAtLeastOnePathBeforeConfigAccess(t *testing.T) {
	err := run(context.Background(), []string{"--config", "definitely-missing.toml", "scan", "--files"})
	if err == nil || !strings.Contains(err.Error(), "scan --files requires at least one") {
		t.Fatalf("run scan --files error = %v, want explicit empty-path error", err)
	}
}

func TestMapTerrainEditCLIPreviewsThenPublishes(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	mapDir := filepath.Join(project, "map_data")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mapDir, "definition.csv"), []byte(
		"province;red;green;blue\n1;255;0;0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mapDir, "default.map"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	provinces := image.NewRGBA(image.Rect(0, 0, 16, 16))
	heightmap := image.NewGray16(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			provinces.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			heightmap.SetGray16(x, y, color.Gray16{Y: uint16(12000 + x*31 + y*47)})
		}
	}
	writeMainTestPNG(t, filepath.Join(mapDir, "provinces.png"), provinces)
	writeMainTestPNG(t, filepath.Join(mapDir, "heightmap.png"), heightmap)

	artifactRoot := filepath.Join(root, "artifacts")
	configPath := filepath.Join(root, "ck3-index.toml")
	config := fmt.Sprintf("database = \"cache/test.sqlite\"\nartifact_root = %q\n[[source]]\nname = \"project\"\npath = %q\nrank = 1\nrole = \"project\"\n",
		filepath.ToSlash(artifactRoot), filepath.ToSlash(project))
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "terrain.json")
	spec := `{
  "operation": "compose",
  "layers": [{
    "id": "cli-hills",
    "kind": "hills",
    "geometry": {"type": "point", "coordinates": [{"x": 8, "y": 8}]},
    "width_px": 8,
    "domain": "land",
    "strength": 0.08,
    "roughness": 0.5,
    "seed": 3
  }]
}`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(root, "preview.png")
	if err := run(context.Background(), []string{
		"--config", configPath, "map", "terrain-edit", specPath, "--preview-out", preview,
	}); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(preview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(file); err != nil {
		file.Close()
		t.Fatalf("CLI preview is not a PNG: %v", err)
	}
	file.Close()
	if _, err := os.Stat(filepath.Join(artifactRoot, "map-edits")); !os.IsNotExist(err) {
		t.Fatal("CLI preview created an artifact")
	}
	if err := run(context.Background(), []string{
		"--config", configPath, "map", "terrain-edit", specPath, "--preview-out", preview,
	}); err == nil {
		t.Fatal("CLI preview overwrote an existing file")
	}
	sourcePreview := filepath.Join(project, "preview.png")
	if err := run(context.Background(), []string{
		"--config", configPath, "map", "terrain-edit", specPath, "--preview-out", sourcePreview,
	}); err == nil {
		t.Fatal("CLI preview wrote inside a configured source")
	}
	if _, err := os.Stat(sourcePreview); !os.IsNotExist(err) {
		t.Fatalf("CLI preview left a source file behind: %v", err)
	}
	if err := run(context.Background(), []string{
		"--config", configPath, "map", "terrain-edit", specPath, "--confirm",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(artifactRoot, "map-edits"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("CLI confirmation did not publish exactly one artifact: entries=%d err=%v", len(entries), err)
	}

	manifestData, err := os.ReadFile(filepath.Join(artifactRoot, "map-edits", entries[0].Name(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cliManifest indexer.MapTerrainArtifactManifest
	if err := json.Unmarshal(manifestData, &cliManifest); err != nil {
		t.Fatal(err)
	}
	cliHash := ""
	for _, file := range cliManifest.Files {
		if file.Kind == "heightmap" {
			cliHash = file.SHA256
			break
		}
	}
	if cliHash == "" {
		t.Fatal("CLI artifact manifest has no heightmap hash")
	}

	cfg, err := indexer.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	dbPath, err := indexer.ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(spec), &arguments); err != nil {
		t.Fatal(err)
	}
	arguments["confirm"] = true
	argumentJSON, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"cli-hash-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"map_terrain_edit","arguments":%s}}`, argumentJSON),
	}, "\n") + "\n"
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		err := mcpserver.Serve(context.Background(), cfg, dbPath, inputReader, outputWriter)
		_ = outputWriter.CloseWithError(err)
		serveDone <- err
	}()
	if _, err := io.WriteString(inputWriter, requests); err != nil {
		t.Fatal(err)
	}
	// Keep stdin open until the artifact response arrives. EOF now correctly
	// means the client exited and cancels unfinished work.
	var mcpOutput bytes.Buffer
	mcpHash := ""
	decoder := json.NewDecoder(io.TeeReader(outputReader, &mcpOutput))
	for mcpHash == "" {
		var response map[string]any
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response["id"] != float64(2) {
			continue
		}
		result, _ := response["result"].(map[string]any)
		if result["isError"] == true {
			t.Fatalf("MCP terrain edit failed: %+v", result)
		}
		structured, _ := result["structuredContent"].(map[string]any)
		outputs, _ := structured["outputs"].([]any)
		for _, raw := range outputs {
			output, _ := raw.(map[string]any)
			if output["kind"] == "heightmap" {
				mcpHash, _ = output["sha256"].(string)
				break
			}
		}
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	if mcpHash == "" {
		t.Fatalf("MCP result has no heightmap hash: %s", mcpOutput.String())
	}
	if mcpHash != cliHash {
		t.Fatalf("MCP and CLI hashes differ for the same spec: mcp=%s cli=%s", mcpHash, cliHash)
	}
}

func writeMainTestPNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
