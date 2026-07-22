package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryGUILocalizationBindsBilingualValuesWithoutPathLeak(t *testing.T) {
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

	guiPath := writeGUIQueryFixture(t, root, "project/localized.gui", `types Demo {
	type localized_panel = vbox {
		size = { 420 160 }
		text_single = { name = "heading" text = OPEN_BESTIARY }
		button = { name = "action" text = "Literal" tooltip = OPEN_BESTIARY }
	}
}`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(1,'project',1,?, 'gui/localized.gui','script',0,'gui',0)`, guiPath); err != nil {
		t.Fatal(err)
	}

	localizations := []struct {
		id, rank int
		source   string
		path     string
		value    string
	}{
		{2, 2, "godherja", filepath.Join(root, "localization/godherja/gui/example_l_english.yml"), `#T Open Bestiary#!\nLook up [monsters|E].`},
		{3, 4, "translation", filepath.Join(root, "localization/godherja/gui/example_l_simp_chinese.yml"), `#T 打开动物图鉴#!\n查看[monsters|E]。[SelectLocalization( GetPlayer.IsValid, '可用', '不可用' )]`},
	}
	for _, item := range localizations {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?,?,'localization',0,'loc',0)`, item.id, item.source, item.rank, item.path, filepath.ToSlash(item.path)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES('OPEN_BESTIARY','godherja',?,?,?,?,?,1,0)`, item.value, item.id, item.source, item.rank, item.path); err != nil {
			t.Fatal(err)
		}
		nested := "Monsters"
		if item.source == "translation" {
			nested = "怪物"
		}
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES('monsters','godherja',?,?,?,?,?,2,0)`, nested, item.id, item.source, item.rank, item.path); err != nil {
			t.Fatal(err)
		}
	}

	result, err := db.QueryGUI(ctx, GUIQueryOptions{
		Operation: "preview", Symbol: "localized_panel", AllowProject: true,
		Width: 800, Height: 450, Format: "both", HTMLMode: GUIHTMLModeInspector,
		Language: GUIPreviewLanguageBilingual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview == nil || result.Preview.Language != GUIPreviewLanguageBilingual {
		t.Fatalf("localized preview missing: %+v", result.Preview)
	}
	stats := result.Preview.Localization
	if stats.Bindings != 2 || stats.Resolved != 2 || stats.Bilingual != 2 || stats.Partial != 2 || stats.Missing != 0 {
		t.Fatalf("unexpected localization stats: %+v", stats)
	}
	heading := result.Preview.Nodes[1]
	if heading.TextLocalization == nil || heading.TextLocalization.English == nil || heading.TextLocalization.SimpChinese == nil {
		t.Fatalf("bilingual text binding missing: %+v", heading.TextLocalization)
	}
	if !strings.Contains(heading.TextLocalization.SelectedText, "打开动物图鉴") || !strings.Contains(heading.TextLocalization.SelectedText, "Open Bestiary") {
		t.Fatalf("bilingual selection missing either language: %q", heading.TextLocalization.SelectedText)
	}
	if strings.Contains(heading.TextLocalization.SimpChinese.DisplayText, "#T") || !strings.Contains(heading.TextLocalization.SimpChinese.DisplayText, "<runtime>") {
		t.Fatalf("formatting-only projection was not conservative: %q", heading.TextLocalization.SimpChinese.DisplayText)
	}
	if !strings.Contains(heading.TextLocalization.English.ResolvedValue, "Monsters") || strings.Contains(heading.TextLocalization.English.ResolvedValue, "[monsters") {
		t.Fatalf("nested indexed localization was not expanded: %+v", heading.TextLocalization.English)
	}
	document := result.Preview.HTML.Document
	if result.Preview.HTML.Behaviors.DynamicTexts != 1 {
		t.Fatalf("localized runtime placeholder was not exposed for controlled text simulation: %+v", result.Preview.HTML.Behaviors)
	}
	for _, expected := range []string{
		`id="ck3-language"`, `data-ck3-language="bilingual"`,
		`data-ck3-text-english="Open Bestiary`, `data-ck3-text-simp-chinese="打开动物图鉴`,
		`data-ck3-tooltip-english="Open Bestiary`, `id="ck3-detail-tooltip"`,
	} {
		if !strings.Contains(document, expected) {
			t.Errorf("localized inspector missing %q", expected)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), filepath.Clean(root)) {
		t.Fatal("GUI localization response leaked an absolute localization path")
	}
}

func TestGUIPreviewLocalizationExpandsRecursiveConceptMacrosAndColor(t *testing.T) {
	rows := []guiLocalizationRow{
		{key: "RITUAL_TOOLTIP", language: "godherja", value: "Sort by [aspect_blood].", source: "godherja", rank: 2, path: "localization/a_l_english.yml"},
		{key: "aspect_blood", language: "godherja", value: "$blood_coloring$ $blood_name$#!", source: "godherja", rank: 2, path: "localization/a_l_english.yml"},
		{key: "blood_coloring", language: "godherja", value: "#color:{0.64,0.01,0.01}", source: "godherja", rank: 2, path: "localization/a_l_english.yml"},
		{key: "blood_name", language: "godherja", value: "Blood", source: "godherja", rank: 2, path: "localization/a_l_english.yml"},
	}
	binding := buildGUIPreviewLocalizationBindings(rows, GUIPreviewLanguageEnglish)["RITUAL_TOOLTIP"]
	if binding == nil || binding.English == nil {
		t.Fatalf("recursive binding missing: %+v", binding)
	}
	if got := binding.English.ResolvedValue; !strings.Contains(got, "Blood") || strings.Contains(got, "[aspect_blood]") || strings.Contains(got, "$blood_name$") {
		t.Fatalf("recursive localization value = %q", got)
	}
	if binding.English.Dynamic || binding.English.DisplayText != "Sort by Blood." {
		t.Fatalf("recursive display was not static and formatting-only: %+v", binding.English)
	}
}

func TestQueryGUILocalizationClosureIncludesConditionalBranches(t *testing.T) {
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
	locPath := filepath.Join(root, "localization", "conditional_l_english.yml")
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(1,'godherja',2,?,'localization/conditional_l_english.yml','localization',0,'loc',0)`, locPath); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"CONDITIONAL_ROOT": "[SelectLocalization( IsReady, 'READY_LABEL', 'WAIT_LABEL' )][AddLocalizationIf( ShowWarning, 'WARNING_LABEL' )]",
		"READY_LABEL":      "Ready",
		"WAIT_LABEL":       "Waiting",
		"WARNING_LABEL":    "Warning",
	}
	line := 1
	for key, value := range values {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?, 'english', ?, 1, 'godherja', 2, ?, ?, 0)`, key, value, locPath, line); err != nil {
			t.Fatal(err)
		}
		line++
	}
	rows, err := db.queryGUIPreviewLocalizationClosure(ctx, map[string]struct{}{"CONDITIONAL_ROOT": {}}, true)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, row := range rows {
		found[row.key] = true
	}
	for _, key := range []string{"CONDITIONAL_ROOT", "READY_LABEL", "WAIT_LABEL", "WARNING_LABEL"} {
		if !found[key] {
			t.Errorf("conditional localization closure omitted %s: %#v", key, found)
		}
	}
}

func TestQueryGUICompilesRawConditionalLocalizationPerLanguage(t *testing.T) {
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
	guiPath := writeGUIQueryFixture(t, root, "base/conditional.gui", `types Demo {
	type conditional_panel = text_single {
		name = "status"
		raw_text = "[SelectLocalization( IsReady, 'READY_LABEL', 'WAIT_LABEL' )]"
	}
}`)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(1,'godherja',2,?,'gui/conditional.gui','script',0,'gui',0)`, guiPath); err != nil {
		t.Fatal(err)
	}
	localizations := []struct {
		id      int
		source  string
		rank    int
		path    string
		ready   string
		waiting string
	}{
		{2, "godherja", 2, filepath.Join(root, "localization", "conditional_l_english.yml"), "Ready", "Waiting"},
		{3, "translation", 4, filepath.Join(root, "localization", "conditional_l_simp_chinese.yml"), "就绪", "等待"},
	}
	for _, item := range localizations {
		rel := filepath.ToSlash(filepath.Join("localization", filepath.Base(item.path)))
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?,?,'localization',0,'loc',0)`, item.id, item.source, item.rank, item.path, rel); err != nil {
			t.Fatal(err)
		}
		for line, value := range []struct {
			key  string
			text string
		}{{"READY_LABEL", item.ready}, {"WAIT_LABEL", item.waiting}} {
			if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?, 'godherja', ?, ?, ?, ?, ?, ?, 0)`, value.key, value.text, item.id, item.source, item.rank, item.path, line+1); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := db.QueryGUI(ctx, GUIQueryOptions{
		Operation: "preview", Symbol: "conditional_panel", AllowProject: true,
		Width: 640, Height: 360, Format: "html", HTMLMode: GUIHTMLModeInspector,
		Language:     GUIPreviewLanguageBilingual,
		RuntimeFacts: []GUIRuntimeFactInput{{Expression: "IsReady", Value: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview == nil || len(result.Preview.Nodes) != 1 {
		t.Fatalf("conditional preview missing: %+v", result.Preview)
	}
	got, ok := resolvedGUIRuntimeText(result.Preview.Nodes[0].Runtime.Text, GUIPreviewLanguageBilingual)
	if !ok || got != "就绪 / Ready" {
		t.Fatalf("conditional localization did not compile per language: %q, %v", got, ok)
	}
	document := result.Preview.HTML.Document
	if !strings.Contains(document, `data-sim-text="就绪 / Ready"`) || !strings.Contains(document, `data-ck3-text-plan-english`) || !strings.Contains(document, `data-ck3-text-plan-simp-chinese`) {
		t.Fatal("conditional localization plans were not exposed to the browser")
	}
}

func TestGUIPreviewLocalizationLanguageInferenceAndDisplay(t *testing.T) {
	if got := canonicalGUIPreviewLocalizationLanguage("godherja", `localization/godherja/a_l_english.yml`); got != GUIPreviewLanguageEnglish {
		t.Fatalf("English suffix inferred as %q", got)
	}
	if got := canonicalGUIPreviewLocalizationLanguage("godherja", `localization/godherja/a_l_simp_chinese.yml`); got != GUIPreviewLanguageSimpChinese {
		t.Fatalf("Simplified Chinese suffix inferred as %q", got)
	}
	display, dynamic := guiLocalizationDisplayText(`#T Title#!\nSee [monsters|E] @warning_icon! [GetPlayer.GetName] $VALUE$.`)
	if !dynamic || strings.Contains(display, "#T") || strings.Contains(display, "@warning_icon!") || !strings.Contains(display, "monsters") || !strings.Contains(display, "<runtime>") {
		t.Fatalf("unexpected localized display projection dynamic=%v value=%q", dynamic, display)
	}
}

func TestGUIPreviewLocalizationHonorsPublicProjectBoundary(t *testing.T) {
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
		{Name: "base", Path: "base", Rank: 2, Role: SourceRoleDependency, Private: false},
		{Name: "project", Path: "project", Rank: 1, Role: SourceRoleProject, Private: true},
	}); err != nil {
		t.Fatal(err)
	}
	guiPath := writeGUIQueryFixture(t, root, "base/panel.gui", `types Demo { type public_panel = text_single { text = PRIVATE_LABEL } }`)
	locPath := filepath.Join(root, "project/private_l_english.yml")
	for _, row := range []struct {
		id     int
		source string
		rank   int
		path   string
		rel    string
		kind   string
	}{
		{1, "base", 2, guiPath, "gui/panel.gui", "script"},
		{2, "project", 1, locPath, "localization/private_l_english.yml", "localization"},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?,?,?,0,'x',0)`, row.id, row.source, row.rank, row.path, row.rel, row.kind); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES('PRIVATE_LABEL','english','private value',2,'project',1,?,1,0)`, locPath); err != nil {
		t.Fatal(err)
	}
	privateResult, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "preview", Symbol: "public_panel", AllowProject: true, Format: "html", Language: GUIPreviewLanguageEnglish})
	if err != nil || privateResult.Preview == nil || privateResult.Preview.Localization.Resolved != 1 {
		t.Fatalf("private localization binding missing: result=%+v err=%v", privateResult.Preview, err)
	}
	publicResult, err := db.QueryGUI(ctx, GUIQueryOptions{Operation: "preview", Symbol: "public_panel", AllowProject: false, Format: "html", Language: GUIPreviewLanguageEnglish})
	if err != nil {
		t.Fatal(err)
	}
	if publicResult.Preview == nil || publicResult.Preview.Localization.Resolved != 0 || publicResult.Preview.Nodes[0].TextLocalization != nil {
		t.Fatalf("public GUI preview leaked project localization: %+v", publicResult.Preview)
	}
}

func BenchmarkBuildGUIPreviewLocalizationBindings(b *testing.B) {
	rows := make([]guiLocalizationRow, 0, 400)
	for index := 0; index < 200; index++ {
		key := fmt.Sprintf("BENCH_LABEL_%d", index)
		rows = append(rows,
			guiLocalizationRow{key: key, language: "godherja", value: "English [GetValue]", source: "godherja", rank: 2, path: "localization/example_l_english.yml"},
			guiLocalizationRow{key: key, language: "godherja", value: "中文 [GetValue]", source: "translation", rank: 4, path: "localization/example_l_simp_chinese.yml"},
		)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		bindings := buildGUIPreviewLocalizationBindings(rows, GUIPreviewLanguageBilingual)
		if len(bindings) != 200 {
			b.Fatal(len(bindings))
		}
	}
}
