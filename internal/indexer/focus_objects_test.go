package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestFocusDirectoryUsesCanonicalObjectType(t *testing.T) {
	if got := objectTypeForPath("common/focuses/00_lifestyle_focuses.txt"); got != "focus" {
		t.Fatalf("focus directory object type=%q want focus", got)
	}
	parsed := script.Parse(`martial_chivalry_focus = { lifestyle = martial_lifestyle }`)
	rec := fileRecord{ID: 1, RelPath: "common/focuses/00_lifestyle_focuses.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 1 || objects[0].Type != "focus" || objects[0].Name != "martial_chivalry_focus" {
		t.Fatalf("unexpected focus objects: %+v", objects)
	}
}

func TestExtractFocusAndLifestyleReferences(t *testing.T) {
	parsed := script.Parse(`test_effect = {
	has_focus = martial_chivalry_focus
	set_focus = martial_strategy_focus
	lifestyle = martial_lifestyle
}`)
	rec := fileRecord{ID: 1, RelPath: "common/scripted_effects/test.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"focus:martial_chivalry_focus": false,
		"focus:martial_strategy_focus": false,
		"lifestyle:martial_lifestyle":  false,
	}
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing focus/lifestyle reference %s: %+v", key, refs)
		}
	}
}
