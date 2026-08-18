package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// registerCoatOfArmsTexture indexes one texture resource. The bytes never
// matter here: these tests are about which source a name resolves to.
func registerCoatOfArmsTexture(t *testing.T, db *DB, ctx context.Context, root, rel, source string, rank int) {
	t.Helper()
	abs := filepath.Join(root, source, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("dds"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO resources(resource_path,kind,file_id,source_name,source_rank,path) VALUES(?,'resource',0,?,?,?)`,
		rel, source, rank, abs); err != nil {
		t.Fatal(err)
	}
}

func publicCoatOfArmsOptions() LLMOptions {
	return LLMOptions{Mode: "public", PrivateSources: map[string]bool{"project": true, "game": false}}
}

// writeCoatOfArms indexes one definition into a layer of its own file.
func writeCoatOfArms(t *testing.T, db *DB, ctx context.Context, root, source string, rank int, id, body string) {
	t.Helper()
	rel := "common/coat_of_arms/coat_of_arms/" + id + ".txt"
	fileID := writeCoatOfArmsFile(t, db, ctx, root, source, rank, rel, body)
	registerCoatOfArmsObject(t, db, ctx, fileID, source, rank, id,
		filepath.Join(root, source, filepath.FromSlash(rel)))
}

// A public definition can reach private content sideways: through the textures
// and named colours it names. The definition passes the boundary check and the
// renderer then reads the private layer's winning texture and encodes it into
// the PNG, where no structured redaction downstream can recognise it.
func TestCoatOfArmsPublicModeDoesNotDrawPrivateTextures(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_vanilla",
		"dyn_vanilla = {\n\tpattern = \"pattern_solid.dds\"\n\tcolor1 = white\n}\n")
	writeCoatOfArmsFile(t, db, ctx, root, "game", 2, "common/named_colors/default_colors.txt",
		"colors = {\n\twhite = hsv { 0.08 0.02 0.8 }\n}\n")
	// The project layer overrides the pattern the public definition names.
	registerCoatOfArmsTexture(t, db, ctx, root, "gfx/coat_of_arms/patterns/pattern_solid.dds", "project", 1)
	registerCoatOfArmsTexture(t, db, ctx, root, "gfx/coat_of_arms/patterns/pattern_solid.dds", "game", 2)

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_vanilla"}, publicCoatOfArmsOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Textures) != 1 {
		t.Fatalf("reported %d texture references, want the pattern alone", len(result.Textures))
	}
	if result.Textures[0].Source != "game" {
		t.Fatalf("public visibility resolved the pattern to %q, want the highest-priority public source", result.Textures[0].Source)
	}
	if len(result.RedactedTextures) != 1 || result.RedactedTextures[0] != "pattern_solid.dds" {
		t.Fatalf("the withheld private winner was not reported: %+v", result.RedactedTextures)
	}
	if !result.PublicAssetsOnly {
		t.Fatal("a public answer did not say its assets were resolved from public sources alone")
	}

	// Private visibility still resolves the winner the game loads.
	private, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_vanilla"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if private.Textures[0].Source != "project" {
		t.Fatalf("private visibility resolved the pattern to %q, want the winning project layer", private.Textures[0].Source)
	}
	if len(private.RedactedTextures) != 0 || private.PublicAssetsOnly {
		t.Fatalf("private visibility reported a public-only restriction: %+v", private)
	}
}

// The same leak through the palette: a colour name a private layer defines
// resolves to a hex value and then to pixels.
func TestCoatOfArmsPublicModeDoesNotResolvePrivateNamedColors(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_vanilla",
		"dyn_vanilla = {\n\tpattern = \"pattern_solid.dds\"\n\tcolor1 = gh_house_blue\n}\n")
	writeCoatOfArmsFile(t, db, ctx, root, "project", 1, "common/named_colors/gh_colors.txt",
		"colors = {\n\tgh_house_blue = rgb { 20 40 120 }\n}\n")

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "render", ID: "dyn_vanilla", Size: 32}, publicCoatOfArmsOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Definition == nil {
		t.Fatalf("a public definition was withheld outright: %+v", result)
	}
	if got := result.Definition.Colors[0].Hex; got != "" {
		t.Fatalf("public visibility resolved a private named colour to %q", got)
	}
	if len(result.Guidance) == 0 {
		t.Fatal("a public render withheld colours without saying so")
	}

	private, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_vanilla"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if private.Definition.Colors[0].Hex != "#142878" {
		t.Fatalf("private visibility resolved color1 to %+v, want the project layer's value", private.Definition.Colors[0])
	}
}

// operation=assets takes no id, so nothing about it looks like reading a
// definition -- which is exactly why enumerating private artwork through it is
// worth a test of its own.
func TestCoatOfArmsAssetsRespectPublicVisibility(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	registerCoatOfArmsTexture(t, db, ctx, root, "gfx/coat_of_arms/colored_emblems/ce_secret.dds", "project", 1)
	registerCoatOfArmsTexture(t, db, ctx, root, "gfx/coat_of_arms/patterns/pattern_solid.dds", "game", 2)

	public, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "assets"}, publicCoatOfArmsOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range public.Assets {
		if asset.Name == "ce_secret.dds" || asset.Source == "project" {
			t.Fatalf("public visibility listed a private-source asset: %+v", asset)
		}
	}
	if public.AssetTotal != 1 {
		t.Fatalf("public listing reported %d assets, want the single public one: %+v", public.AssetTotal, public.Assets)
	}

	private, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "assets"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if private.AssetTotal != 2 {
		t.Fatalf("private listing reported %d assets, want both: %+v", private.AssetTotal, private.Assets)
	}
}

// A child inherits its parent's design and changes part of it. Read without the
// parent it has no pattern at all, so it renders as a flat field and reports
// itself as the finished answer.
func TestCoatOfArmsResolvesParentInheritance(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	writeCoatOfArmsFile(t, db, ctx, root, "game", 2, "common/named_colors/default_colors.txt",
		"colors = {\n\twhite = hsv { 0.08 0.02 0.8 }\n\tblack = hsv { 0.1 0.25 0.10 }\n}\n")
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_base",
		"dyn_base = {\n\tpattern = \"pattern_solid.dds\"\n\tcolor1 = white\n\tcolor2 = black\n"+
			"\tcolored_emblem = { texture = \"ce_lion.dds\" }\n}\n")
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_child",
		"dyn_child = {\n\tparent = dyn_base\n\tcolor1 = black\n"+
			"\tcolored_emblem = { texture = \"ce_star.dds\" }\n}\n")

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_child"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	definition := result.Definition
	if definition == nil {
		t.Fatalf("inspect returned no definition: %+v", result)
	}
	if definition.Pattern != "pattern_solid.dds" {
		t.Fatalf("the child resolved to pattern %q, want the parent's", definition.Pattern)
	}
	// color1 is the child's own, color2 comes from the parent.
	if definition.Colors[0].Name != "black" || definition.Colors[1].Name != "black" {
		t.Fatalf("resolved colours are %+v/%+v, want the child's color1 and the parent's color2",
			definition.Colors[0], definition.Colors[1])
	}
	if definition.Colors[0].Hex == "" || definition.Colors[1].Hex == "" {
		t.Fatalf("resolved colours did not resolve against the palette: %+v", definition.Colors)
	}
	if len(definition.Emblems) != 2 ||
		definition.Emblems[0].Texture != "ce_lion.dds" || definition.Emblems[1].Texture != "ce_star.dds" {
		t.Fatalf("emblems resolved to %+v, want the parent's under the child's", definition.Emblems)
	}
	if len(definition.Inherited) != 1 || definition.Inherited[0] != "dyn_base" {
		t.Fatalf("inherited chain is %v, want dyn_base", definition.Inherited)
	}
	if len(definition.Warnings) != 0 {
		t.Fatalf("a fully resolved child warned: %v", definition.Warnings)
	}
	// The pattern and both emblems must appear as texture references, which is
	// what tells a caller whether the resolved design can actually draw.
	if len(result.Textures) != 3 {
		t.Fatalf("reported %d texture references, want the parent's pattern and both emblems", len(result.Textures))
	}
}

func TestCoatOfArmsResolvesMultiLevelInheritance(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_grand",
		"dyn_grand = {\n\tpattern = \"pattern_grand.dds\"\n\tcolor3 = rgb { 1 2 3 }\n"+
			"\tcolored_emblem = { texture = \"ce_grand.dds\" }\n}\n")
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_mid",
		"dyn_mid = {\n\tparent = dyn_grand\n\tcolor2 = rgb { 4 5 6 }\n"+
			"\tcolored_emblem = { texture = \"ce_mid.dds\" }\n}\n")
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_leaf",
		"dyn_leaf = {\n\tparent = dyn_mid\n\tcolor1 = rgb { 7 8 9 }\n}\n")

	result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_leaf"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	definition := result.Definition
	if definition.Pattern != "pattern_grand.dds" {
		t.Fatalf("pattern resolved to %q, want the grandparent's", definition.Pattern)
	}
	if definition.Colors[0].Hex != "#070809" || definition.Colors[1].Hex != "#040506" || definition.Colors[2].Hex != "#010203" {
		t.Fatalf("colours resolved to %+v, want one from each level", definition.Colors)
	}
	if len(definition.Emblems) != 2 ||
		definition.Emblems[0].Texture != "ce_grand.dds" || definition.Emblems[1].Texture != "ce_mid.dds" {
		t.Fatalf("emblems resolved to %+v, want the grandparent's under the parent's", definition.Emblems)
	}
	want := []string{"dyn_mid", "dyn_grand"}
	if len(definition.Inherited) != len(want) {
		t.Fatalf("inherited chain is %v, want %v", definition.Inherited, want)
	}
	for i, name := range want {
		if definition.Inherited[i] != name {
			t.Fatalf("inherited chain is %v, want %v", definition.Inherited, want)
		}
	}
}

// A parent no active source defines, and a chain that comes back to where it
// started, both have to end the walk with an answer rather than a hang or a
// silent partial design presented as complete.
func TestCoatOfArmsReportsBrokenParentChains(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_orphan",
		"dyn_orphan = {\n\tparent = dyn_absent\n\tcolor1 = rgb { 1 1 1 }\n}\n")
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_a",
		"dyn_a = {\n\tparent = dyn_b\n\tcolor1 = rgb { 1 1 1 }\n}\n")
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_b",
		"dyn_b = {\n\tparent = dyn_a\n\tcolor2 = rgb { 2 2 2 }\n}\n")

	orphan, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_orphan"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !warningsMention(orphan.Definition.Warnings, "dyn_absent") {
		t.Fatalf("a missing parent was not reported: %v", orphan.Definition.Warnings)
	}

	cycle, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_a"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !warningsMention(cycle.Definition.Warnings, "circular") {
		t.Fatalf("a circular parent chain was not reported: %v", cycle.Definition.Warnings)
	}
	// The one level that did resolve still applies: B supplied color2.
	if cycle.Definition.Colors[1].Hex != "#020202" {
		t.Fatalf("the resolvable level of a circular chain was dropped: %+v", cycle.Definition.Colors)
	}
	// Rendering a broken chain still answers rather than failing.
	if _, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "render", ID: "dyn_a", Size: 16}, LLMOptions{}); err != nil {
		t.Fatalf("rendering a circular chain failed: %v", err)
	}
}

func warningsMention(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return true
		}
	}
	return false
}

// A resolved definition is mostly its parent's script. Handing a public caller
// the fields a private parent supplied would put that source's content in the
// answer under the child's name, and rendering it would put it in the pixels.
func TestCoatOfArmsWithholdsPrivateParentInPublicMode(t *testing.T) {
	db, ctx, root := newCoatOfArmsFixture(t)
	writeCoatOfArms(t, db, ctx, root, "project", 1, "dyn_private_base",
		"dyn_private_base = {\n\tpattern = \"pattern_secret.dds\"\n\tcolor1 = rgb { 9 9 9 }\n}\n")
	writeCoatOfArms(t, db, ctx, root, "game", 2, "dyn_public_child",
		"dyn_public_child = {\n\tparent = dyn_private_base\n\tcolor2 = rgb { 1 1 1 }\n}\n")

	public := publicCoatOfArmsOptions()
	for _, operation := range []string{"inspect", "render"} {
		result, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: operation, ID: "dyn_public_child", Size: 16}, public)
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		if result.Definition != nil {
			t.Fatalf("%s returned a definition resolved against a private parent: %+v", operation, result.Definition)
		}
		if len(result.PNG) != 0 {
			t.Fatalf("%s rendered %d PNG bytes from a private parent", operation, len(result.PNG))
		}
		if !result.Redacted {
			t.Fatalf("%s withheld the resolved definition without saying so", operation)
		}
	}

	// Private visibility resolves it as usual.
	private, err := db.LLMCoatOfArms(ctx, CoatOfArmsSpec{Operation: "inspect", ID: "dyn_public_child"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if private.Definition == nil || private.Definition.Pattern != "pattern_secret.dds" {
		t.Fatalf("private visibility did not resolve the parent: %+v", private.Definition)
	}
}
