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

// TestGUICLIAcceptsDocumentedFlagsForEveryOperation pins the reported defect:
// the gui usage line offers --format, --limit and the rest to every operation,
// but only preview parsed them. summary and type read "--limit" as a
// positional path and failed with `GUI path "--limit" must be under gui/`,
// while file dropped the flag in silence — the worse of the two, because the
// caller got a truncated tree and no indication why.
//
// Parsing happens before the config is opened, so a missing config is the
// proof that an argument got past the parser rather than into a path slot.
func TestGUICLIAcceptsDocumentedFlagsForEveryOperation(t *testing.T) {
	for _, testcase := range []struct {
		name string
		args []string
	}{
		{name: "summary", args: []string{"gui", "summary", "--limit", "4"}},
		{name: "summary with prefix", args: []string{"gui", "summary", "gui/frontend", "--limit", "4"}},
		{name: "file", args: []string{"gui", "file", "gui/frontend/a.gui", "--limit", "4"}},
		{name: "type", args: []string{"gui", "type", "some_type", "--limit", "4"}},
		{name: "template", args: []string{"gui", "template", "some_template", "gui/frontend", "--limit", "4"}},
		{name: "preview keeps working", args: []string{"gui", "preview", "some_type", "--limit", "4"}},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			err := run(context.Background(), append([]string{"--config", "definitely-missing.toml"}, testcase.args...))
			if err == nil {
				t.Fatal("run gui error = nil, want the missing-config error")
			}
			if strings.Contains(err.Error(), "--limit") {
				t.Fatalf("run gui error = %v, want --limit consumed as a flag", err)
			}
			if !strings.Contains(err.Error(), "definitely-missing.toml") {
				t.Fatalf("run gui error = %v, want the parser to hand off to config loading", err)
			}
		})
	}
}

// TestGUICLIRejectsMalformedArguments covers the other half of the same
// defect: arguments the parser cannot honour must be named, not absorbed into
// a path or ignored.
func TestGUICLIRejectsMalformedArguments(t *testing.T) {
	for _, testcase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "non-integer limit", args: []string{"gui", "summary", "--limit", "four"}, want: "GUI --limit requires an integer value"},
		{name: "unknown flag", args: []string{"gui", "summary", "--depth", "2"}, want: `unknown GUI flag "--depth"`},
		{name: "output without preview", args: []string{"gui", "summary", "--out", "shot.png"}, want: "only valid for operation=preview"},
		{name: "extra summary positional", args: []string{"gui", "summary", "gui/a", "gui/b"}, want: `unexpected GUI summary argument "gui/b"`},
		{name: "extra file positional", args: []string{"gui", "file", "gui/a.gui", "gui/b.gui"}, want: `unexpected GUI file argument "gui/b.gui"`},
		{name: "extra type positional", args: []string{"gui", "type", "sym", "gui/a", "gui/b"}, want: `unexpected GUI type argument "gui/b"`},
		{name: "extra preview positional", args: []string{"gui", "preview", "sym", "gui/a", "gui/b"}, want: `unexpected GUI preview argument "gui/b"`},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			err := run(context.Background(), append([]string{"--config", "definitely-missing.toml"}, testcase.args...))
			if err == nil || !strings.Contains(err.Error(), testcase.want) {
				t.Fatalf("run gui error = %v, want %q", err, testcase.want)
			}
		})
	}
}

func TestRunHealthRequireReadyFailsForQueryableButUnpublishedDatabase(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "health.sqlite")
	db, err := indexer.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "ck3-index.toml")
	config := fmt.Sprintf("database = %q\n[[source]]\nname = \"project\"\npath = %q\nrank = 1\nrole = \"project\"\n",
		filepath.ToSlash(database), filepath.ToSlash(project))
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--config", configPath, "health"}); err != nil {
		t.Fatalf("report-only health failed for readable database: %v", err)
	}
	if err := run(context.Background(), []string{"--config", configPath, "health", "--require-ready"}); err == nil || !strings.Contains(err.Error(), "readiness gate failed") {
		t.Fatalf("strict health error = %v, want readiness failure", err)
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
