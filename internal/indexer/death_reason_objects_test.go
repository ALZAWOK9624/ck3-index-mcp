package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestDeathReasonDirectoryUsesCanonicalObjectType(t *testing.T) {
	if got := objectTypeForPath("common/deathreasons/00_natural_deaths.txt"); got != "death_reason" {
		t.Fatalf("death-reason directory object type=%q want death_reason", got)
	}
	parsed := script.Parse(`death_old_age = { natural_death_trigger = { age >= 60 } }`)
	rec := fileRecord{ID: 1, RelPath: "common/deathreasons/00_natural_deaths.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 1 || objects[0].Type != "death_reason" || objects[0].Name != "death_old_age" {
		t.Fatalf("unexpected death-reason objects: %+v", objects)
	}
}

func TestExtractDeathReasonReferences(t *testing.T) {
	parsed := script.Parse(`test_event = {
	trigger = { death_reason = death_old_age }
	immediate = { death = { death_reason = death_battle } }
}`)
	rec := fileRecord{ID: 1, RelPath: "events/test_events.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"death_reason:death_old_age": false,
		"death_reason:death_battle":  false,
	}
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing death-reason reference %s: %+v", key, refs)
		}
	}
}
