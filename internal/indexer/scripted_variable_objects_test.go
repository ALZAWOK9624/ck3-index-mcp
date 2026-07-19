package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/script"
)

func TestScriptedVariablesReuseObjectAndReferenceGraph(t *testing.T) {
	parsed := script.Parse(`@illustration = "gfx/interface/test.dds"
@score = 25
test_building = {
	@duration = 0.5
	illustration = @illustration
	value = @score
	duration = @duration
	ai_value = @NAI|SOME_ENGINE_DEFINE
	computed = @[score + 1]
	raw_text = "@warning_icon! #X Not a variable#!"
}`)
	rec := fileRecord{ID: 1, RelPath: "common/buildings/test.txt", SourceName: "project", SourceRank: 1}
	objects := extractObjects(rec, parsed.Nodes)
	wantObjects := map[string]bool{
		"scripted_variable:@illustration": false,
		"scripted_variable:@score":        false,
		"scripted_variable:@duration":     false,
		"building:test_building":          false,
	}
	for _, object := range objects {
		key := object.Type + ":" + object.Name
		if _, ok := wantObjects[key]; ok {
			wantObjects[key] = true
		}
	}
	for key, found := range wantObjects {
		if !found {
			t.Errorf("missing object %s: %+v", key, objects)
		}
	}

	refs := extractRefs(rec, parsed.Nodes, objects)
	wantRefs := map[string]bool{
		"scripted_variable:@illustration": false,
		"scripted_variable:@score":        false,
		"scripted_variable:@duration":     false,
		"define:@NAI|SOME_ENGINE_DEFINE":  false,
	}
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, ok := wantRefs[key]; ok {
			wantRefs[key] = true
		}
		if ref.Name == "@[score + 1]" {
			t.Errorf("arithmetic expression was indexed as a variable or define ref: %+v", ref)
		}
		if ref.Name == "@warning_icon! #X Not a variable#!" {
			t.Errorf("GUI raw text was indexed as a variable ref: %+v", ref)
		}
	}
	for key, found := range wantRefs {
		if !found {
			t.Errorf("missing ref %s: %+v", key, refs)
		}
	}
}

func TestScriptedVariableQueryIncludesValue(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "buildings", "test.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("@score = 25\ntest_building = { value = @score }\n"), 0644); err != nil {
		t.Fatal(err)
	}
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

	object, err := db.QueryObject(context.Background(), "scripted_variable:@score")
	if err != nil {
		t.Fatal(err)
	}
	if len(object.Definitions) != 1 || object.Definitions[0].Value != "25" {
		t.Fatalf("scripted variable value missing from existing object query: %+v", object)
	}
	refs, err := db.QueryRefs(context.Background(), "scripted_variable:@score")
	if err != nil {
		t.Fatal(err)
	}
	if refs.IncomingTotal != 1 || len(refs.Incoming) != 1 || !refs.Incoming[0].Resolved {
		t.Fatalf("scripted variable ref did not resolve through existing graph: %+v", refs)
	}
}
