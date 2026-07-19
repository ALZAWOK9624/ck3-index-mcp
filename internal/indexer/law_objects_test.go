package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestExtractLawGroupsAndNestedLaws(t *testing.T) {
	parsed := script.Parse(`crown_authority = {
	default = crown_authority_1
	cumulative = yes
	can_change_law_group = { always = yes }
	crown_authority_0 = { flag = uses_crown_authority }
	crown_authority_1 = { can_pass = { always = yes } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/laws/test_laws.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 3 {
		t.Fatalf("law extraction returned %d objects, want group plus two laws: %+v", len(objects), objects)
	}
	want := []struct{ typ, name string }{
		{"law_group", "crown_authority"},
		{"law", "crown_authority_0"},
		{"law", "crown_authority_1"},
	}
	for index := range want {
		if objects[index].Type != want[index].typ || objects[index].Name != want[index].name {
			t.Fatalf("law object %d=%s:%s want %s:%s", index, objects[index].Type, objects[index].Name, want[index].typ, want[index].name)
		}
	}
}

func TestLawPatternsSeparateGroupFieldsFromNestedLaws(t *testing.T) {
	parsed := script.Parse(`authority = {
	default = authority_1
	can_change_law_group = { always = yes }
	authority_0 = { flag = low }
	authority_1 = { can_pass = { always = yes } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/laws/test_laws.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	fields := extractObjectFields(rec, parsed.Nodes, objects)
	groupFields := map[string]bool{}
	lawFields := map[string]bool{}
	for _, field := range fields {
		switch field.Type {
		case "law_group":
			groupFields[field.Field] = true
		case "law":
			lawFields[field.Field] = true
		}
	}
	if !groupFields["default"] || !groupFields["can_change_law_group"] || groupFields["authority_0"] || groupFields["authority_1"] {
		t.Fatalf("law-group patterns contain nested law ids or miss group fields: %+v", groupFields)
	}
	if !lawFields["flag"] || !lawFields["can_pass"] {
		t.Fatalf("nested law fields were not learned as law patterns: %+v", lawFields)
	}
}

func TestExtractLawGroupStructuralReferences(t *testing.T) {
	parsed := script.Parse(`authority = {
	default = authority_1
	authority_0 = {}
	authority_1 = {}
}`)
	rec := fileRecord{ID: 1, RelPath: "common/laws/test_laws.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"law_group:authority->law:authority_1": false,
		"law:authority_0->law_group:authority": false,
		"law:authority_1->law_group:authority": false,
	}
	for _, ref := range refs {
		key := ref.FromType + ":" + ref.FromName + "->" + ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing structural law reference %s: %+v", key, refs)
		}
	}
}
