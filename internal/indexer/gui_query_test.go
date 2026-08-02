package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

	gamePath := writeGUIQueryFixture(t, root, "game/gui/base.gui", `types Demo {
	type base_panel = container { block "content" { text_single = { text = "base" } } }
}`)
	projectPath := writeGUIQueryFixture(t, root, "project/gui/child.gui", `types Demo {
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
	publishVerifiedGUIFixture(t, ctx, db)

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

func TestQueryGUISummaryStreamsRawModels(t *testing.T) {
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
	path := writeGUIQueryFixture(t, root, "game/gui/summary.gui", `template SharedLabel {
	text_single = { align = right }
}
types Demo {
	type aligned_label = text_single {
		align = left|top
		visible = "[IsShown]"
	}
}
widget = {
	align = center
	checked = "[IsChecked]"
	alwaystransparent = "[IsMouseTransparent]"
	button_ignore = "[ShouldIgnoreParentButton]"
	selectedindex = "[CurrentSortIndex]"
	@progress_bar_spacing = @[progress_bar_size / 20]
	fontcolor = "[TrackColor]"
	oncolorchanged = "[OnColorChanged]"
	coat_of_arms = "[Title.GetTitleCoA.GetTexture('(int32)256','(int32)256')]"
	coat_of_arms_mask = "[GovernmentType.GetRealmMask]"
	coat_of_arms_offset = "[GovernmentType.GetRealmMaskOffset]"
	coat_of_arms_scale = "[GovernmentType.GetRealmMaskScale]"
	video = "[EventWindowBackgroundData.GetVideo]"
	loop = "[ShouldLoop]"
	delay = "[StaggerDelay]"
	maxcharacters = "[MaxNameLength]"
	intersectionmask_texture = "[DomicileBuildingAsset.GetTextureMask]"
	on_start = "[PdxGuiTriggerAllAnimations('show')]"
	trigger_when = "[ShowWidget]"
	raw_tooltip = "#X Direct inspector tooltip#!"
	tooltip_when_disabled = "[DisabledReason]"
	animated_progress_value = "[CurrentProgress]"
}
`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES('game',3,?,?, 'script',0,'test',0)`, path, "gui/summary.gui"); err != nil {
		t.Fatal(err)
	}
	publishVerifiedGUIFixture(t, ctx, db)
	result, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "summary", AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Files != 1 || result.CacheHit || result.Summary == nil {
		t.Fatalf("streaming GUI summary failed: %+v", result)
	}
	summary := result.Summary
	if summary.ResolutionComplete || summary.Types != 1 || summary.Templates != 1 || summary.Roots != 1 {
		t.Fatalf("streaming GUI summary metadata is wrong: %+v", summary)
	}
	usage := map[string]GUIPropertyUsage{}
	for _, value := range summary.PropertyUsage {
		usage[value.Name] = value
	}
	if align, ok := usage["align"]; !ok || align.Count != 3 || align.Support != "rendered" {
		t.Fatalf("raw align usage missing from summary: %+v", summary.PropertyUsage)
	}
	if visible, ok := usage["visible"]; !ok || visible.Expressions != 1 || visible.Support != "simulated" {
		t.Fatalf("raw runtime field missing from summary: %+v", summary.PropertyUsage)
	}
	if checked, ok := usage["checked"]; !ok || checked.Expressions != 1 || checked.Support != "simulated" {
		t.Fatalf("checked support missing from summary: %+v", summary.PropertyUsage)
	}
	if rawTooltip, ok := usage["raw_tooltip"]; !ok || rawTooltip.Count != 1 || rawTooltip.Support != "simulated" {
		t.Fatalf("raw tooltip support missing from summary: %+v", summary.PropertyUsage)
	}
	if disabledTooltip, ok := usage["tooltip_when_disabled"]; !ok || disabledTooltip.Expressions != 1 || disabledTooltip.Support != "simulated" {
		t.Fatalf("disabled tooltip support missing from summary: %+v", summary.PropertyUsage)
	}
	if animatedProgress, ok := usage["animated_progress_value"]; !ok || animatedProgress.Expressions != 1 || animatedProgress.Support != "simulated" {
		t.Fatalf("animated progress support missing from summary: %+v", summary.PropertyUsage)
	}
	if transparent, ok := usage["alwaystransparent"]; !ok || transparent.Expressions != 1 || transparent.Support != "preserved" {
		t.Fatalf("mouse routing support missing from summary: %+v", summary.PropertyUsage)
	}
	if buttonIgnore, ok := usage["button_ignore"]; !ok || buttonIgnore.Expressions != 1 || buttonIgnore.Support != "preserved" {
		t.Fatalf("button input routing support missing from summary: %+v", summary.PropertyUsage)
	}
	if selectedIndex, ok := usage["selectedindex"]; !ok || selectedIndex.Expressions != 1 || selectedIndex.Support != "preserved" {
		t.Fatalf("dropdown model-index support missing from summary: %+v", summary.PropertyUsage)
	}
	if fontColor, ok := usage["fontcolor"]; !ok || fontColor.Expressions != 1 || fontColor.Support != "simulated" {
		t.Fatalf("fontcolor support missing from summary: %+v", summary.PropertyUsage)
	}
	if coatOfArms, ok := usage["coat_of_arms"]; !ok || coatOfArms.Expressions != 1 || coatOfArms.Support != "simulated" {
		t.Fatalf("coat_of_arms sample support missing from summary: %+v", summary.PropertyUsage)
	}
	if video, ok := usage["video"]; !ok || video.Expressions != 1 || video.Support != "simulated" {
		t.Fatalf("video poster sample support missing from summary: %+v", summary.PropertyUsage)
	}
	for _, name := range []string{"coat_of_arms_mask", "coat_of_arms_offset", "coat_of_arms_scale"} {
		if value, ok := usage[name]; !ok || value.Expressions != 1 || value.Support != "simulated" {
			t.Fatalf("coat-of-arms composition support missing for %q: %+v", name, summary.PropertyUsage)
		}
	}
	if _, found := usage["@progress_bar_spacing"]; found {
		t.Fatalf("GUI macro declaration leaked into property summary: %+v", summary.PropertyUsage)
	}
	if colorChange, ok := usage["oncolorchanged"]; !ok || colorChange.Expressions != 1 || colorChange.Support != "preserved" {
		t.Fatalf("color-change callback support missing from summary: %+v", summary.PropertyUsage)
	}
	for _, name := range []string{"loop", "delay", "maxcharacters", "intersectionmask_texture"} {
		if value, ok := usage[name]; !ok || value.Expressions != 1 || value.Support != "preserved" {
			t.Fatalf("nonvisual runtime property %q support missing: %+v", name, summary.PropertyUsage)
		}
	}
	if onStart, ok := usage["on_start"]; !ok || onStart.Expressions != 1 || onStart.Support != "preserved" {
		t.Fatalf("lifecycle callback support missing from summary: %+v", summary.PropertyUsage)
	}
	if trigger, ok := usage["trigger_when"]; !ok || trigger.Expressions != 1 || trigger.Support != "simulated" {
		t.Fatalf("state trigger support missing from summary: %+v", summary.PropertyUsage)
	}
	if len(summary.RuntimeHotspots) != 0 {
		t.Fatalf("fully supported fixture should not report runtime hotspots: %+v", summary.RuntimeHotspots)
	}
	if len(result.Guidance) == 0 || !strings.Contains(strings.Join(result.Guidance, " "), "resolution_complete=false") {
		t.Fatalf("streaming summary did not explain its resolution boundary: %+v", result.Guidance)
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

	sharedPath := writeGUIQueryFixture(t, root, "game/gui/shared.gui", `types Shared {
	type shared_badge = widget {
		icon = { name = "layer" texture = "gfx/interface/shared.dds" size = { 32 32 } }
	}
}`)
	consumerPath := writeGUIQueryFixture(t, root, "game/gui/consumer.gui", `types Consumer {
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
	publishVerifiedGUIFixture(t, ctx, db)

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

	path := writeGUIQueryFixture(t, root, "project/gui/cache.gui", `types Demo { type old_panel = widget { size = { 10 10 } } }`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'old-hash',0)`, "project", 1, path, "gui/cache.gui"); err != nil {
		t.Fatal(err)
	}
	publishVerifiedGUIFixture(t, ctx, db)
	first, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "type", Symbol: "old_panel", AllowProject: true})
	if err != nil || !first.Found || first.CacheHit {
		t.Fatalf("initial GUI resolution failed: result=%+v err=%v", first, err)
	}

	if err := os.WriteFile(path, []byte(`types Demo { type new_panel = widget { size = { 20 20 } } }`), 0644); err != nil {
		t.Fatal(err)
	}
	warm, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "type", Symbol: "old_panel", AllowProject: true})
	var changed *SourceChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("warm GUI resolution read changed live bytes: result=%+v err=%v", warm, err)
	}
	publishVerifiedGUIFixture(t, ctx, db)
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
		{Code: "gui_expansion_limit", Severity: "warning", Source: "gui/dependency.gui", Span: SourceSpan{Line: 1, EndLine: 1}},
	}
	selected := selectGUIPreviewDiagnostics(values, "panel", nodes, 8)
	// Ordered by severity rank, not by the severity string: alphabetically
	// "info" sorts before "warning", which is the wrong priority.
	if len(selected) != 3 || selected[0].Code != "inside" || selected[1].Code != "gui_expansion_limit" || selected[2].Code != "named" {
		t.Fatalf("focused GUI diagnostics=%+v", selected)
	}
}

// A focused query must report the findings that matter, not the ones that
// happen to be scanned first. Truncating before sorting let a run of leading
// informational diagnostics evict every error from the response.
func TestSelectGUIDiagnosticsRanksBySeverityBeforeApplyingLimit(t *testing.T) {
	var values []GUIDiagnostic
	for index := 0; index < 8; index++ {
		values = append(values, GUIDiagnostic{Code: "note", Severity: "info", Symbol: "panel", Source: "gui/panel.gui"})
	}
	values = append(values,
		GUIDiagnostic{Code: "broken_reference", Severity: "error", Symbol: "panel", Source: "gui/panel.gui"},
		GUIDiagnostic{Code: "suspicious", Severity: "warning", Symbol: "panel", Source: "gui/panel.gui"},
	)
	selected := selectGUIDiagnostics(values, "panel", "", 2)
	if len(selected) != 2 || selected[0].Code != "broken_reference" || selected[1].Code != "suspicious" {
		t.Fatalf("severity-ranked GUI diagnostics=%+v", selected)
	}
	previewSelected := selectGUIPreviewDiagnostics(values, "panel", nil, 2)
	if len(previewSelected) != 2 || previewSelected[0].Code != "broken_reference" || previewSelected[1].Code != "suspicious" {
		t.Fatalf("severity-ranked GUI preview diagnostics=%+v", previewSelected)
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

	path := writeGUIQueryFixture(t, root, "project/gui/preview.gui", `types Demo {
	type preview_panel = widget {
		size = { 320 180 }
		icon = { size = { 32 32 } parentanchor = center widgetanchor = center texture = "gfx/interface/preview.dds" }
	}
	type unrelated_panel = widget { using = MissingUnrelatedTemplate }
}`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'test',0)`, "project", 1, path, "gui/preview.gui"); err != nil {
		t.Fatal(err)
	}
	resourcePath := writeGUIQueryFixture(t, root, "project/gfx/interface/preview.dds", "fixture")
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
	publishVerifiedGUIFixture(t, ctx, db)

	result, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "preview", Symbol: "preview_panel", AllowProject: true, Width: 800, Height: 450, Format: "both", HTMLMode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.ResolutionTruncated || result.Preview == nil || result.Preview.SymbolKind != "type" || len(result.Preview.PNG) == 0 {
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

	path := writeGUIQueryFixture(t, root, "project/gui/root.gui", `widget = {
	name = "named_root"
	size = { 400 200 }
	text_single = { name = "nested_label" text = "Root preview" }
}`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'test',0)`, "project", 1, path, "gui/root.gui"); err != nil {
		t.Fatal(err)
	}
	publishVerifiedGUIFixture(t, ctx, db)

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

func publishVerifiedGUIFixture(tb testing.TB, ctx context.Context, db *DB) {
	tb.Helper()
	rows, err := db.sql.QueryContext(ctx, `SELECT id,path FROM files WHERE kind IN ('script','resource')`)
	if err != nil {
		tb.Fatal(err)
	}
	type indexedPath struct {
		id   int64
		path string
	}
	var files []indexedPath
	for rows.Next() {
		var file indexedPath
		if err := rows.Scan(&file.id, &file.path); err != nil {
			rows.Close()
			tb.Fatal(err)
		}
		files = append(files, file)
	}
	if err := rows.Close(); err != nil {
		tb.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			tb.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if _, err := db.sql.ExecContext(ctx, `UPDATE files SET file_size=?,sha256=? WHERE id=?`, len(data), hex.EncodeToString(digest[:]), file.id); err != nil {
			tb.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES
		('scan_generation','1'),('scan_revision','gui-fixture'),('scan_status','ready')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		tb.Fatal(err)
	}
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
	path := filepath.Join(root, "gui", "panel.gui")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`types Demo { type benchmark_panel = widget { size = { 1280 720 } text_single = { text = "benchmark" } } }`), 0600); err != nil {
		b.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?, 'script',0,'benchmark-hash',0)`, "project", 1, path, "gui/panel.gui"); err != nil {
		b.Fatal(err)
	}
	publishVerifiedGUIFixture(b, ctx, db)
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
