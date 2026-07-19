package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestExtractDoctrineGroupsAndNestedDoctrines(t *testing.T) {
	parsed := script.Parse(`doctrine_gender = {
	group = "main_group"
	is_available_on_create = { always = yes }
	doctrine_gender_male_dominated = { parameters = { male_dominated_law = yes } }
	doctrine_gender_equal = { parameters = { gender_equal_law = yes } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/religion/doctrines/test_doctrines.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 3 {
		t.Fatalf("doctrine extraction returned %d objects, want group plus two doctrines: %+v", len(objects), objects)
	}
	want := []struct{ typ, name string }{
		{"doctrine_group", "doctrine_gender"},
		{"doctrine", "doctrine_gender_male_dominated"},
		{"doctrine", "doctrine_gender_equal"},
	}
	for index := range want {
		if objects[index].Type != want[index].typ || objects[index].Name != want[index].name {
			t.Fatalf("doctrine object %d=%s:%s want %s:%s", index, objects[index].Type, objects[index].Name, want[index].typ, want[index].name)
		}
	}
}

func TestDoctrinePatternsSeparateGroupFieldsFromNestedDoctrines(t *testing.T) {
	parsed := script.Parse(`gender = {
	group = "main_group"
	is_available_on_create = { always = yes }
	gender_male = { visible = yes parameters = { male_dominated_law = yes } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/religion/doctrines/test_doctrines.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	fields := extractObjectFields(rec, parsed.Nodes, objects)
	groupFields := map[string]bool{}
	doctrineFields := map[string]bool{}
	for _, field := range fields {
		switch field.Type {
		case "doctrine_group":
			groupFields[field.Field] = true
		case "doctrine":
			doctrineFields[field.Field] = true
		}
	}
	if !groupFields["group"] || !groupFields["is_available_on_create"] || groupFields["gender_male"] {
		t.Fatalf("doctrine-group patterns contain nested doctrine ids or miss group fields: %+v", groupFields)
	}
	if !doctrineFields["visible"] || !doctrineFields["parameters"] {
		t.Fatalf("nested doctrine fields were not learned as doctrine patterns: %+v", doctrineFields)
	}
}

func TestExtractDoctrineStructuralAndScriptReferences(t *testing.T) {
	parsed := script.Parse(`gender = {
	is_available_on_create = { has_doctrine = gender_equal }
	gender_male = {}
	gender_equal = {}
}`)
	rec := fileRecord{ID: 1, RelPath: "common/religion/doctrines/test_doctrines.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"doctrine:gender_male->doctrine_group:gender":  false,
		"doctrine:gender_equal->doctrine_group:gender": false,
		"doctrine_group:gender->doctrine:gender_equal": false,
	}
	for _, ref := range refs {
		key := ref.FromType + ":" + ref.FromName + "->" + ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing doctrine reference %s: %+v", key, refs)
		}
	}
}
