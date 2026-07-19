package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestExtractLifestyleAndPerkScriptReferences(t *testing.T) {
	parsed := script.Parse(`test_effect = {
	has_lifestyle = martial_lifestyle
	refund_perks = intrigue_lifestyle
	has_perk = stalwart_leader_perk
	add_perk = faithful_perk
	remove_perk = defensive_negotiations_perk
}`)
	rec := fileRecord{ID: 1, RelPath: "common/scripted_effects/test.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"lifestyle:martial_lifestyle":                false,
		"lifestyle:intrigue_lifestyle":               false,
		"lifestyle_perk:stalwart_leader_perk":        false,
		"lifestyle_perk:faithful_perk":               false,
		"lifestyle_perk:defensive_negotiations_perk": false,
	}
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing lifestyle/perk reference %s: %+v", key, refs)
		}
	}
}
