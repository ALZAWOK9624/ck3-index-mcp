package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// newCoatOfArmsFixture builds a miniature workspace on disk and indexes it by
// hand, so the tests exercise the real queries against the real schema rather
// than a mock of them.
func newCoatOfArmsFixture(t *testing.T) (*DB, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, layer := range []struct {
		name string
		rank int
		role SourceRole
	}{
		{"project", 1, SourceRoleProject},
		{"game", 2, SourceRoleGame},
	} {
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO source_layers(name,rank,role,private) VALUES(?,?,?,0)`,
			layer.name, layer.rank, string(layer.role)); err != nil {
			t.Fatal(err)
		}
	}
	return db, ctx, root
}

func writeCoatOfArmsFile(t *testing.T, db *DB, ctx context.Context, root, source string, rank int, rel, body string) int64 {
	t.Helper()
	abs := filepath.Join(root, source, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,overridden)
		VALUES(?,?,?,?,'script',0,0,?,0)`, source, rank, abs, rel, rel)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func registerCoatOfArmsObject(t *testing.T, db *DB, ctx context.Context, fileID int64, source string, rank int, name, path string) {
	t.Helper()
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col,end_line,end_col)
		VALUES(?,?,'',?,0,?,?,?,1,1,1,1)`, coatOfArmsObjectType, name, fileID, source, rank, path); err != nil {
		t.Fatal(err)
	}
}

const coatOfArmsFixtureBody = `dyn_test_house = {
	pattern = "pattern_solid.dds"
	color1 = gh_house_blue
	color2 = white
	color3 = rgb { 200 30 40 }
	colored_emblem = {
		texture = "ce_lion.dds"
		instance = { position = { 0.5 0.5 } scale = { 0.8 0.8 } }
	}
}
`

func seedCoatOfArmsWorkspace(t *testing.T, db *DB, ctx context.Context, root string) {
	t.Helper()
	// Two named_colors files in different layers. CK3 merges the colors blocks
	// across files, so the lower-priority layer still supplies names the
	// higher-priority one never mentions.
	writeCoatOfArmsFile(t, db, ctx, root, "game", 2, "common/named_colors/default_colors.txt",
		"colors = {\n\twhite = hsv { 0.08 0.02 0.8 }\n\tblack = hsv { 0.1 0.25 0.10 }\n}\n")
	writeCoatOfArmsFile(t, db, ctx, root, "project", 1, "common/named_colors/gh_colors.txt",
		"colors = {\n\tgh_house_blue = rgb { 20 40 120 }\n}\n")

	fileID := writeCoatOfArmsFile(t, db, ctx, root, "project", 1,
		"common/coat_of_arms/coat_of_arms/90_gh.txt", coatOfArmsFixtureBody)
	abs := filepath.Join(root, "project", filepath.FromSlash("common/coat_of_arms/coat_of_arms/90_gh.txt"))
	registerCoatOfArmsObject(t, db, ctx, fileID, "project", 1, "dyn_test_house", abs)
}

func TestCoatOfArmsInspectResolvesColorsAcrossLayers(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_test_house"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Definition == nil {
		t.Fatalf("inspect returned no definition: %+v", result)
	}
	// gh_house_blue comes from the project layer, white from the game layer.
	if got := result.Definition.Colors[0].Hex; got != "#142878" {
		t.Fatalf("color1 resolved to %q, want the project layer's gh_house_blue", got)
	}
	if result.Definition.Colors[1].Name != "white" || result.Definition.Colors[1].Hex == "" {
		t.Fatalf("color2 did not resolve against the lower-priority layer: %+v", result.Definition.Colors[1])
	}
	if len(result.Definition.Warnings) != 0 {
		t.Fatalf("a fully resolvable definition warned: %v", result.Definition.Warnings)
	}
	if len(result.Textures) != 2 {
		t.Fatalf("reported %d texture references, want the pattern and the emblem", len(result.Textures))
	}
	// No resources are indexed in this fixture, so both must read as unresolved
	// rather than silently appearing to exist.
	for _, texture := range result.Textures {
		if texture.Resolved {
			t.Fatalf("texture %q reported as resolved with no indexed resources", texture.Name)
		}
	}
}

// An id no active file defines must come back as a plain miss with guidance,
// not an error: "not found" is an answer, and the caller often wants to know
// whether the definition was overridden rather than absent.
func TestCoatOfArmsInspectReportsUnknownIDWithoutFailing(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_absent"}, LLMOptions{})
	if err != nil {
		t.Fatalf("an unknown id failed instead of reporting a miss: %v", err)
	}
	if result.Definition != nil {
		t.Fatal("an unknown id returned a definition")
	}
	if len(result.Guidance) == 0 {
		t.Fatal("an unknown id came back with no guidance")
	}
}

// A definition inside an overridden file is not what the game loads, so it must
// not be served as the active one.
func TestCoatOfArmsSkipsOverriddenDefinitions(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	fileID := writeCoatOfArmsFile(t, db, ctx, root, "game", 2,
		"common/coat_of_arms/coat_of_arms/90_gh.txt", coatOfArmsFixtureBody)
	if _, err := db.sql.ExecContext(ctx, `UPDATE files SET overridden=1 WHERE id=?`, fileID); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "game", filepath.FromSlash("common/coat_of_arms/coat_of_arms/90_gh.txt"))
	registerCoatOfArmsObject(t, db, ctx, fileID, "game", 2, "dyn_test_house", abs)

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_test_house"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Definition != nil {
		t.Fatal("a definition inside an overridden file was served as active")
	}
}

func TestCoatOfArmsRenderProducesAPNGAndReportsMissingTextures(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "render", ID: "dyn_test_house", Size: 64}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PNG) == 0 {
		t.Fatal("render returned no PNG bytes")
	}
	if string(result.PNG[1:4]) != "PNG" {
		t.Fatalf("render returned %d bytes that are not a PNG", len(result.PNG))
	}
	if result.Render == nil || result.Render.Size != 64 {
		t.Fatalf("render metadata is %+v, want size 64", result.Render)
	}
	// The image itself must not travel inside the structured payload; the PNG
	// field is the only carrier.
	if result.Render.Image != nil {
		t.Fatal("the decoded image was left on the structured result")
	}
	if len(result.Render.MissingTextures) != 2 {
		t.Fatalf("missing textures are %v, want both unindexed names", result.Render.MissingTextures)
	}
}

func TestCoatOfArmsRenderRejectsOversizeRequests(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)

	if _, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{
		Operation: "render", ID: "dyn_test_house", Size: CoatOfArmsMaxRenderSize + 1,
	}, LLMOptions{}); err == nil {
		t.Fatal("a render larger than the advertised maximum was accepted")
	}
}

func TestCoatOfArmsAssetsListIndexedTexturesByKind(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)
	for _, asset := range []struct {
		rel    string
		source string
		rank   int
	}{
		{"gfx/coat_of_arms/patterns/pattern_solid.dds", "game", 2},
		{"gfx/coat_of_arms/colored_emblems/ce_lion.dds", "project", 1},
		{"gfx/coat_of_arms/colored_emblems/ce_lion.dds", "game", 2},
		{"gfx/coat_of_arms/textured_emblems/te_banner.dds", "game", 2},
		{"gfx/coat_of_arms/colored_emblems/50_designer.txt", "game", 2},
	} {
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO resources(resource_path,kind,file_id,source_name,source_rank,path) VALUES(?,'resource',0,?,?,?)`,
			asset.rel, asset.source, asset.rank, filepath.Join(root, asset.source, filepath.FromSlash(asset.rel))); err != nil {
			t.Fatal(err)
		}
	}

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "assets"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The .txt is not a texture, and the duplicated emblem name is listed once.
	if result.AssetTotal != 3 {
		t.Fatalf("listed %d assets, want 3: %+v", result.AssetTotal, result.Assets)
	}
	kinds := map[string]string{}
	for _, asset := range result.Assets {
		kinds[asset.Name] = asset.Kind
	}
	if kinds["pattern_solid.dds"] != "pattern" || kinds["ce_lion.dds"] != "colored_emblem" || kinds["te_banner.dds"] != "textured_emblem" {
		t.Fatalf("asset kinds resolved as %+v", kinds)
	}
	// Lower rank wins, so the project's emblem is the one the game would load.
	for _, asset := range result.Assets {
		if asset.Name == "ce_lion.dds" && asset.Source != "project" {
			t.Fatalf("the shadowed emblem was listed from %q, want the winning project layer", asset.Source)
		}
	}

	filtered, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "assets", Filter: "pattern"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.AssetTotal != 1 || filtered.Assets[0].Name != "pattern_solid.dds" {
		t.Fatalf("filtering by kind returned %+v", filtered.Assets)
	}
}

func TestCoatOfArmsRejectsUnknownOperation(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)
	if _, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "paint"}, LLMOptions{}); err == nil {
		t.Fatal("an unknown operation was accepted")
	}
	if _, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "render"}, LLMOptions{}); err == nil {
		t.Fatal("render without an id was accepted")
	}
}

// Everything under common/coat_of_arms/ indexes as the same object type,
// including options/atlases.txt. Without a folder filter, "atlas" resolves and
// renders as an empty black field that reads like a real answer.
func TestCoatOfArmsIgnoresNonHeraldrySiblingFolders(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)

	fileID := writeCoatOfArmsFile(t, db, ctx, root, "game", 2,
		"common/coat_of_arms/options/atlases.txt", "atlas = {\n\tname = \"coa_atlas\"\n}\n")
	abs := filepath.Join(root, "game", filepath.FromSlash("common/coat_of_arms/options/atlases.txt"))
	registerCoatOfArmsObject(t, db, ctx, fileID, "game", 2, "atlas", abs)

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "atlas"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Definition != nil {
		t.Fatalf("an atlas declaration was served as a coat of arms: %+v", result.Definition)
	}
}

// A definition is script content from its source layer, and a render is that
// content made legible. Public visibility must withhold both when the layer is
// private, or the render becomes a way around the evidence boundary.
func TestCoatOfArmsWithholdsPrivateSourcesInPublicMode(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)
	public := LLMOptions{Mode: "public", PrivateSources: map[string]bool{"project": true, "game": false}}

	for _, operation := range []string{"inspect", "render"} {
		result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: operation, ID: "dyn_test_house", Size: 32}, public)
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		if result.Definition != nil {
			t.Fatalf("%s returned a private-source definition in public mode", operation)
		}
		if len(result.PNG) != 0 {
			t.Fatalf("%s returned %d PNG bytes for a private-source definition", operation, len(result.PNG))
		}
		if !result.Redacted {
			t.Fatalf("%s withheld the definition without saying so", operation)
		}
	}

	// The same call in private mode still answers.
	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_test_house"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Definition == nil || result.Redacted {
		t.Fatal("private mode withheld a project definition")
	}
}

// A public-source definition is not affected by the boundary.
func TestCoatOfArmsServesPublicSourcesInPublicMode(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	seedCoatOfArmsWorkspace(t, db, ctx, root)
	fileID := writeCoatOfArmsFile(t, db, ctx, root, "game", 2,
		"common/coat_of_arms/coat_of_arms/00_vanilla.txt",
		"dyn_vanilla = {\n\tpattern = \"pattern_solid.dds\"\n\tcolor1 = white\n\tcolor2 = black\n\tcolor3 = black\n}\n")
	abs := filepath.Join(root, "game", filepath.FromSlash("common/coat_of_arms/coat_of_arms/00_vanilla.txt"))
	registerCoatOfArmsObject(t, db, ctx, fileID, "game", 2, "dyn_vanilla", abs)

	public := LLMOptions{Mode: "public", PrivateSources: map[string]bool{"project": true, "game": false}}
	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_vanilla"}, public)
	if err != nil {
		t.Fatal(err)
	}
	if result.Definition == nil || result.Redacted {
		t.Fatalf("a public-source definition was withheld: %+v", result)
	}
}
