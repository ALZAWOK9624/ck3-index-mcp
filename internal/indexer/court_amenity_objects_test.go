package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestExtractCourtAmenityCategoriesAndLevels(t *testing.T) {
	parsed := script.Parse(`court_food_quality = {
	default = court_food_quality_default
	court_food_quality_default = { ai_will_do = { value = 100 } }
	court_food_quality_modest = { owner_modifier = { court_grandeur_baseline_add = 1 } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/court_amenities/test.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 3 {
		t.Fatalf("court-amenity extraction returned %d objects, want category plus two levels: %+v", len(objects), objects)
	}
	want := []struct{ typ, name string }{
		{"court_amenity_category", "court_food_quality"},
		{"court_amenity_level", "court_food_quality_default"},
		{"court_amenity_level", "court_food_quality_modest"},
	}
	for index := range want {
		if objects[index].Type != want[index].typ || objects[index].Name != want[index].name {
			t.Fatalf("court-amenity object %d=%s:%s want %s:%s", index, objects[index].Type, objects[index].Name, want[index].typ, want[index].name)
		}
	}
}

func TestCourtAmenityPatternsSeparateCategoryFieldsFromLevels(t *testing.T) {
	parsed := script.Parse(`court_food_quality = {
	default = court_food_quality_default
	court_food_quality_default = { ai_will_do = { value = 100 } }
	court_food_quality_modest = { can_pick = { always = yes } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/court_amenities/test.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	fields := extractObjectFields(rec, parsed.Nodes, objects)
	categoryFields := map[string]bool{}
	levelFields := map[string]bool{}
	for _, field := range fields {
		switch field.Type {
		case "court_amenity_category":
			categoryFields[field.Field] = true
		case "court_amenity_level":
			levelFields[field.Field] = true
		}
	}
	if !categoryFields["default"] || categoryFields["court_food_quality_default"] || categoryFields["court_food_quality_modest"] {
		t.Fatalf("court-amenity-category patterns contain levels or miss default: %+v", categoryFields)
	}
	if !levelFields["ai_will_do"] || !levelFields["can_pick"] {
		t.Fatalf("court-amenity-level fields were not learned: %+v", levelFields)
	}
}

func TestExtractCourtAmenityStructuralAndContextReferences(t *testing.T) {
	definitions := script.Parse(`court_food_quality = {
	default = court_food_quality_default
	court_food_quality_default = {}
	court_food_quality_modest = {}
}`)
	definitionRecord := fileRecord{ID: 1, RelPath: "common/court_amenities/test.txt", SourceName: "game", SourceRank: 3}
	definitionObjects := extractObjects(definitionRecord, definitions.Nodes)
	refs := extractRefs(definitionRecord, definitions.Nodes, definitionObjects)

	scriptFile := script.Parse(`test_effect = {
	amenity_level = { target = court_food_quality value >= 3 }
	set_amenity_level = { type = court_food_quality value = 4 }
	add_amenity_level = { type = court_servants value = 1 }
}`)
	scriptRecord := fileRecord{ID: 2, RelPath: "common/scripted_effects/test.txt", SourceName: "game", SourceRank: 3}
	scriptObjects := extractObjects(scriptRecord, scriptFile.Nodes)
	refs = append(refs, extractRefs(scriptRecord, scriptFile.Nodes, scriptObjects)...)

	want := map[string]bool{
		"court_amenity_category:court_food_quality->court_amenity_level:court_food_quality_default": false,
		"court_amenity_level:court_food_quality_default->court_amenity_category:court_food_quality": false,
		"court_amenity_level:court_food_quality_modest->court_amenity_category:court_food_quality":  false,
		"scripted_effect:test_effect->court_amenity_category:court_food_quality":                    false,
		"scripted_effect:test_effect->court_amenity_category:court_servants":                        false,
	}
	for _, ref := range refs {
		key := ref.FromType + ":" + ref.FromName + "->" + ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing court-amenity reference %s: %+v", key, refs)
		}
	}
}
