package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupPreflightPatchDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/common/traits/base_traits.txt", `existing_trait = { desc = existing_trait_desc }`)
	write("project/localization/english/base_l_english.yml", `l_english:
 existing_trait_desc:0 "Existing trait"
 existing_decision.desc:0 "Existing decision desc"
`)
	write("project/gfx/interface/icons/existing.dds", "fake")

	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte(`database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache/test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPreflightPatchParseErrorDoesNotWriteDB(t *testing.T) {
	db := setupPreflightPatchDB(t)
	var before int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM objects`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	got, err := db.LLMPreflightPatch(context.Background(), []PatchFileInput{{
		Path:    "common/traits/patch_traits.txt",
		Content: `patch_parse_trait = { desc = `,
	}}, LLMOptions{AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.NeedsScan {
		t.Fatalf("expected needs_scan=true, got %+v", got)
	}
	if got.Counts["diagnostics"] == 0 || got.Counts["blocking_risks"] == 0 {
		t.Fatalf("expected parse diagnostic blocker, got %+v", got)
	}
	foundParse := false
	for _, ev := range got.Evidence {
		if strings.Contains(ev.Detail, "parse_error") {
			foundParse = true
		}
	}
	if !foundParse {
		t.Fatalf("expected parse_error evidence, got %+v", got.Evidence)
	}
	var after int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM objects`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("preflight_patch wrote DB objects: before=%d after=%d", before, after)
	}
	obj, err := db.QueryObject(context.Background(), "patch_parse_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Definitions) != 0 {
		t.Fatalf("patch object should not be persisted, got %+v", obj.Definitions)
	}
}

func TestPreflightPatchOverlayResolvesPatchAndDBSymbols(t *testing.T) {
	db := setupPreflightPatchDB(t)
	got, err := db.LLMPreflightPatch(context.Background(), []PatchFileInput{
		{
			Path: "common/traits/patch_traits.txt",
			Content: `patch_trait = {
	desc = patch_trait_desc
}`,
		},
		{
			Path: "common/decisions/patch_decisions.txt",
			Content: `patch_decision = {
	title = patch_decision.t
	desc = existing_decision.desc
	icon = "gfx/interface/icons/existing.dds"
	is_shown = {
		has_trait = patch_trait
		has_trait = existing_trait
	}
}`,
		},
		{
			Path: "localization/english/patch_l_english.yml",
			Content: `l_english:
 patch_trait_desc:0 "Patch trait"
 patch_decision.t:0 "Patch decision"
`,
		},
	}, LLMOptions{AllowProject: true, Limit: 12})
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["unresolved_refs"] != 0 || got.Counts["blocking_risks"] != 0 {
		t.Fatalf("expected patch and DB symbols to resolve, got %+v", got)
	}
	if got.Counts["definitions"] != 2 || got.Counts["localization"] != 2 {
		t.Fatalf("expected patch definitions/localization counts, got %+v", got.Counts)
	}
	foundObject := false
	foundLoc := false
	for _, ev := range got.Evidence {
		if ev.Kind == "patch_defined_object" && ev.Name == "patch_decision" {
			foundObject = true
		}
		if ev.Kind == "patch_localization" && ev.Name == "patch_decision.t" {
			foundLoc = true
		}
	}
	if !foundObject || !foundLoc {
		t.Fatalf("expected patch object and localization evidence, got %+v", got.Evidence)
	}
	obj, err := db.QueryObject(context.Background(), "patch_decision")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Definitions) != 0 {
		t.Fatalf("patch decision should not be persisted, got %+v", obj.Definitions)
	}
}

func TestPreflightPatchReportsMAACanRecruitScopeMismatch(t *testing.T) {
	db := setupPreflightPatchDB(t)
	got, err := db.LLMPreflightPatch(context.Background(), []PatchFileInput{{
		Path: "common/men_at_arms_types/patch_maa.txt",
		Content: `bad_patch_maa = {
	type = heavy_infantry
	can_recruit = {
		has_cultural_parameter = unlock_bad_patch_maa
	}
}`,
	}}, LLMOptions{AllowProject: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["diagnostics"] == 0 || got.Counts["nonblocking_risks"] == 0 {
		t.Fatalf("expected scope mismatch warning, got %+v", got)
	}
	text := ""
	for _, ev := range got.Evidence {
		text += ev.Detail + "\n"
	}
	if !strings.Contains(text, "scope_mismatch") || !strings.Contains(text, "culture = { ... }") {
		t.Fatalf("expected can_recruit culture scope guidance, got %+v", got.Evidence)
	}
}

func TestPreflightPatchReportsMissingRefs(t *testing.T) {
	db := setupPreflightPatchDB(t)
	got, err := db.LLMPreflightPatch(context.Background(), []PatchFileInput{{
		Path: "common/decisions/missing_patch_decisions.txt",
		Content: `missing_patch_decision = {
	title = missing_patch_decision.t
	icon = "gfx/interface/icons/missing_patch.dds"
	is_shown = { has_trait = missing_patch_trait }
	effect = { soundeffect = "event:/CK3_INDEX/PATCH_MISSING_SOUND" }
}`,
	}}, LLMOptions{AllowProject: true, Limit: 12})
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["unresolved_refs"] < 3 || got.Counts["blocking_risks"] == 0 {
		t.Fatalf("expected missing object/resource/sound refs, got %+v", got)
	}
	text := ""
	for _, ev := range got.Evidence {
		text += ev.Detail + "\n"
	}
	for _, want := range []string{"missing_patch_trait", "missing_patch.dds", "PATCH_MISSING_SOUND"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected unresolved evidence for %s, got %+v", want, got.Evidence)
		}
	}
}

func TestDirtyPatchFilesIgnoresNonLoadRootsAndDetectsDeletes(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) string {
		t.Helper()
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	loaded := write("common/traits/loaded.txt", "loaded_trait = {}\n")
	write(".map-editor-backups/one/history/characters/backup.txt", "backup_char = {}\n")
	write("tools/generated/events/ignored.txt", "ignored.1 = {}\n")
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := os.Remove(loaded); err != nil {
		t.Fatal(err)
	}
	dirty, err := db.DirtyPatchFiles(context.Background(), cfg, 8)
	if err != nil {
		t.Fatal(err)
	}
	if dirty.Total != 1 || dirty.Deleted != 1 || len(dirty.Files) != 1 || dirty.Files[0].Op != "delete" || dirty.Files[0].Path != "common/traits/loaded.txt" {
		t.Fatalf("unexpected dirty set: %+v", dirty)
	}
}
