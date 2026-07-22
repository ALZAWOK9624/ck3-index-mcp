package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryGUIReusesIndexedFilesAndPrivacyBoundary(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := syncSourceLayers(ctx, db.sql, []Source{
		{Name: "game", Path: "game", Rank: 3, Role: SourceRoleGame, Private: false},
		{Name: "project", Path: "project", Rank: 1, Role: SourceRoleProject, Private: true},
	}); err != nil {
		t.Fatal(err)
	}

	gamePath := writeGUIQueryFixture(t, root, "game/base.gui", `types Demo {
	type base_panel = container { block "content" { text_single = { text = "base" } } }
}`)
	projectPath := writeGUIQueryFixture(t, root, "project/child.gui", `types Demo {
	type child_panel = base_panel { blockoverride "content" { icon = { texture = "gfx/interface/child.dds" } } }
}`)
	for _, row := range []struct {
		path, rel, source string
		rank              int
	}{
		{gamePath, "gui/base.gui", "game", 3},
		{projectPath, "gui/child.gui", "project", 1},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'test',0)`, row.source, row.rank, row.path, row.rel); err != nil {
			t.Fatal(err)
		}
	}

	private, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "type", Symbol: "child_panel", AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if private.Files != 2 || !private.Found || private.Type == nil || len(private.Type.Element.Children) != 1 {
		t.Fatalf("private resolved GUI query omitted indexed inheritance: %+v", private)
	}
	slot := private.Type.Element.Children[0]
	if slot.Slot != "content" || len(slot.Children) != 1 || slot.Children[0].Texture != "gfx/interface/child.dds" {
		t.Fatalf("resolved blockoverride missing: %+v", slot)
	}

	public, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "type", Symbol: "child_panel", AllowProject: false})
	if err != nil {
		t.Fatal(err)
	}
	if public.Files != 1 || public.Found || public.Type != nil {
		t.Fatalf("public GUI query leaked rank-1 project data: %+v", public)
	}

	file, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "file", Path: "gui/base.gui", AllowProject: true})
	if err != nil || !file.Found || file.File == nil || file.File.Path != "gui/base.gui" || file.File.Source != "game" {
		t.Fatalf("file model query failed: result=%+v err=%v", file, err)
	}
}

func TestQueryGUIPathPrefixScopesSymbolButResolvesCrossFileTypes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	sharedPath := writeGUIQueryFixture(t, root, "game/shared.gui", `types Shared {
	type shared_badge = widget {
		icon = { name = "layer" texture = "gfx/interface/shared.dds" size = { 32 32 } }
	}
}`)
	consumerPath := writeGUIQueryFixture(t, root, "game/consumer.gui", `types Consumer {
	type scoped_panel = widget {
		size = { 120 80 }
		shared_badge = { name = "badge" size = { 32 32 } }
	}
}`)
	for _, row := range []struct {
		path, rel string
	}{
		{sharedPath, "gui/shared.gui"},
		{consumerPath, "gui/consumer.gui"},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES('game',3,?,?, 'script',0,'test',0)`, row.path, row.rel); err != nil {
			t.Fatal(err)
		}
	}

	result, err := db.QueryGUI(ctx, GUIQueryOptions{
		Operation: "type", Symbol: "scoped_panel", PathPrefix: "gui/consumer.gui", AllowProject: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Type == nil || result.Files != 1 || result.ResolutionFiles != 2 {
		t.Fatalf("scoped cross-file resolution metadata unexpected: %+v", result)
	}
	if len(result.Type.Element.Children) != 1 {
		t.Fatalf("scoped panel child count=%d want 1", len(result.Type.Element.Children))
	}
	badge := result.Type.Element.Children[0]
	if badge.Kind != "widget" || badge.Name != "badge" || len(badge.Children) != 1 ||
		badge.Children[0].Name != "layer" || badge.Children[0].Texture != "gfx/interface/shared.dds" {
		t.Fatalf("cross-file custom child was not expanded: %+v", badge)
	}

	outOfScope, err := db.QueryGUI(ctx, GUIQueryOptions{
		Operation: "type", Symbol: "shared_badge", PathPrefix: "gui/consumer.gui", AllowProject: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outOfScope.Found || outOfScope.Type != nil {
		t.Fatalf("path_prefix leaked an out-of-scope symbol: %+v", outOfScope)
	}
}

func TestQueryGUIResolutionCacheUsesIndexedHashes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	path := writeGUIQueryFixture(t, root, "project/cache.gui", `types Demo { type old_panel = widget { size = { 10 10 } } }`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'old-hash',0)`, "project", 1, path, "gui/cache.gui"); err != nil {
		t.Fatal(err)
	}
	first, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "type", Symbol: "old_panel", AllowProject: true})
	if err != nil || !first.Found || first.CacheHit {
		t.Fatalf("initial GUI resolution failed: result=%+v err=%v", first, err)
	}

	if err := os.WriteFile(path, []byte(`types Demo { type new_panel = widget { size = { 20 20 } } }`), 0644); err != nil {
		t.Fatal(err)
	}
	warm, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "type", Symbol: "old_panel", AllowProject: true})
	if err != nil || !warm.Found || !warm.CacheHit {
		t.Fatalf("warm GUI resolution did not reuse indexed-hash cache: result=%+v err=%v", warm, err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE files SET sha256='new-hash' WHERE rel_path='gui/cache.gui'`); err != nil {
		t.Fatal(err)
	}
	invalidated, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "type", Symbol: "new_panel", AllowProject: true})
	if err != nil || !invalidated.Found || invalidated.CacheHit {
		t.Fatalf("changed indexed hash did not invalidate GUI resolution cache: result=%+v err=%v", invalidated, err)
	}
}

func TestQueryGUIRejectsPathsOutsideGUIRoot(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret.gui", "common/test.gui", "C:/secret.gui"} {
		if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "file", Path: path, AllowProject: true}); err == nil {
			t.Errorf("path %q was accepted", path)
		}
	}
}

func TestQueryGUIRejectsPreviewFormatOnOtherOperations(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "summary", Format: "html"}); err == nil {
		t.Fatal("summary silently accepted preview-only format")
	}
	if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "summary", HTMLMode: GUIHTMLModeInspector}); err == nil {
		t.Fatal("summary silently accepted preview-only HTML mode")
	}
	if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "summary", Language: GUIPreviewLanguageEnglish}); err == nil {
		t.Fatal("summary silently accepted preview-only language")
	}
	if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "summary", Samples: []GUIScenarioSample{{Property: "text", Expression: "[X]", Value: "x"}}}); err == nil {
		t.Fatal("summary silently accepted preview-only scenario samples")
	}
	if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "summary", ModelSamples: []GUIModelSampleCollection{{
		Target: "grid", Rows: []GUIModelSampleRow{{ID: "row", Samples: []GUIScenarioSample{{Property: "text", Expression: "[X]", Value: "x"}}}},
	}}}); err == nil {
		t.Fatal("summary silently accepted preview-only model samples")
	}
	if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "preview", Symbol: "missing", Format: "png", HTMLMode: GUIHTMLModeInspector}); err == nil {
		t.Fatal("PNG-only preview silently accepted an HTML mode")
	}
	if _, err := db.QueryGUI(context.Background(), GUIQueryOptions{Operation: "preview", Symbol: "missing", Language: "klingon"}); err == nil {
		t.Fatal("preview silently accepted an unknown language")
	}
}

func TestGUIQueryCompactsLargeResolvedTrees(t *testing.T) {
	root := GUIElement{Kind: "container"}
	for index := 0; index < 150; index++ {
		root.Children = append(root.Children, GUIElement{Kind: "icon", Name: "item"})
	}
	compacted, stats := compactGUIElementForQuery(root, 50, defaultGUIQueryMaxDepth)
	if stats.TotalNodes != 151 || stats.ReturnedNodes != 50 || !stats.Truncated {
		t.Fatalf("unexpected tree budget: %+v", stats)
	}
	if len(compacted.Children) != 49 {
		t.Fatalf("compacted child count=%d want 49", len(compacted.Children))
	}
}

func TestGUIQueryNodeLimitMatchesPublicMaximum(t *testing.T) {
	if got := guiQueryNodeLimit(300); got != 300 {
		t.Fatalf("GUI node limit silently changed 300 to %d", got)
	}
	if got := guiQueryNodeLimit(900); got != 500 {
		t.Fatalf("GUI node limit cap=%d want 500", got)
	}
}

func TestSelectGUIPreviewDiagnosticsKeepsOnlyContributingSpans(t *testing.T) {
	nodes := []GUIPreviewNode{{Source: "gui/panel.gui", Line: 200}, {Source: "gui/panel.gui", Line: 240}}
	values := []GUIDiagnostic{
		{Code: "irrelevant", Source: "gui/panel.gui", Span: SourceSpan{Line: 20, EndLine: 30}},
		{Code: "inside", Severity: "error", Source: "gui/panel.gui", Span: SourceSpan{Line: 210, EndLine: 220}},
		{Code: "named", Severity: "info", Symbol: "panel", Source: "gui/other.gui", Span: SourceSpan{Line: 1, EndLine: 1}},
	}
	selected := selectGUIPreviewDiagnostics(values, "panel", nodes, 8)
	if len(selected) != 2 || selected[0].Code != "inside" || selected[1].Code != "named" {
		t.Fatalf("focused GUI diagnostics=%+v", selected)
	}
}

func TestQueryGUIRendersPreviewFromIndexedResolvedType(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	path := writeGUIQueryFixture(t, root, "project/preview.gui", `types Demo {
	type preview_panel = widget {
		size = { 320 180 }
		icon = { size = { 32 32 } parentanchor = center widgetanchor = center texture = "gfx/interface/preview.dds" }
	}
	type unrelated_panel = widget { using = MissingUnrelatedTemplate }
}`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'test',0)`, "project", 1, path, "gui/preview.gui"); err != nil {
		t.Fatal(err)
	}
	resourcePath := writeGUIQueryFixture(t, root, "project/preview.dds", "fixture")
	inserted, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'resource',0,'test',0)`, "project", 1, resourcePath, "gfx/interface/preview.dds")
	if err != nil {
		t.Fatal(err)
	}
	resourceFileID, err := inserted.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO resources(resource_path,kind,file_id,source_name,source_rank,path) VALUES(?,?,?,?,?,?)`, "gfx/interface/preview.dds", "dds", resourceFileID, "project", 1, resourcePath); err != nil {
		t.Fatal(err)
	}

	result, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "preview", Symbol: "preview_panel", AllowProject: true, Width: 800, Height: 450, Format: "both", HTMLMode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Preview == nil || result.Preview.SymbolKind != "type" || len(result.Preview.PNG) == 0 {
		t.Fatalf("resolved GUI preview missing: %+v", result)
	}
	if result.Preview.Format != "both" || result.Preview.HTML == nil || result.Preview.HTML.Mode != GUIHTMLModeInspector || !strings.Contains(result.Preview.HTML.Document, `id="ck3-gui-inspector"`) {
		t.Fatalf("resolved GUI HTML preview missing: %+v", result.Preview.HTML)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("focused preview included unrelated same-file diagnostics: %+v", result.Diagnostics)
	}
	if len(result.Preview.Nodes) != 2 || result.Preview.Nodes[1].Bounds.X != 144 || result.Preview.Nodes[1].Bounds.Y != 74 {
		t.Fatalf("resolved preview layout unexpected: %+v", result.Preview.Nodes)
	}
	if result.Preview.Textures.Resolved != 1 || result.Preview.Nodes[1].TextureRef == nil || !result.Preview.Nodes[1].TextureRef.Resolved || result.Preview.Nodes[1].TextureRef.Source != "project" {
		t.Fatalf("preview did not reuse indexed texture binding: %+v", result.Preview)
	}

	public, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "preview", Symbol: "preview_panel", AllowProject: false})
	if err != nil {
		t.Fatal(err)
	}
	if public.Found || public.Preview != nil {
		t.Fatalf("public GUI preview leaked project file: %+v", public)
	}
}

func TestQueryGUIRendersExistingPreviewForNamedRootElement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	path := writeGUIQueryFixture(t, root, "project/root.gui", `widget = {
	name = "named_root"
	size = { 400 200 }
	text_single = { name = "nested_label" text = "Root preview" }
}`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'test',0)`, "project", 1, path, "gui/root.gui"); err != nil {
		t.Fatal(err)
	}

	result, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "preview", Symbol: "named_root", AllowProject: true, Width: 800, Height: 450})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Preview == nil || result.Preview.SymbolKind != "element" || len(result.Preview.PNG) == 0 {
		t.Fatalf("named root did not reuse GUI preview: %+v", result)
	}
	if len(result.Preview.Nodes) != 2 || result.Preview.Nodes[0].Name != "named_root" {
		t.Fatalf("named root preview tree unexpected: %+v", result.Preview.Nodes)
	}
}

func writeGUIQueryFixture(t *testing.T, root, rel string, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func BenchmarkQueryGUIWarmResolutionCache(b *testing.B) {
	ctx := context.Background()
	root := b.TempDir()
	db, err := Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(root, "panel.gui")
	if err := os.WriteFile(path, []byte(`types Demo { type benchmark_panel = widget { size = { 1280 720 } text_single = { text = "benchmark" } } }`), 0600); err != nil {
		b.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'benchmark-hash',0)`, "project", 1, path, "gui/panel.gui"); err != nil {
		b.Fatal(err)
	}
	options := GUIQueryOptions{Operation: "type", Symbol: "benchmark_panel", AllowProject: true}
	if result, err := db.QueryGUI(ctx, options); err != nil || !result.Found || result.CacheHit {
		b.Fatalf("failed to prime GUI cache: result=%+v err=%v", result, err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result, err := db.QueryGUI(ctx, options)
		if err != nil || !result.Found || !result.CacheHit {
			b.Fatalf("warm GUI query failed: result=%+v err=%v", result, err)
		}
	}
}
