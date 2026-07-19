package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestExtractActivityDatabaseReferences(t *testing.T) {
	parsed := script.Parse(`feast = {
	activity_group_type = grand
	host_intents = {
		intents = { reduce_stress_intent murder_attendee_intent }
		default = reduce_stress_intent
	}
	guest_invite_rules = { rules = { 2 = activity_invite_rule_rivals } }
	pulse_actions = { entries = { feast_good feast_bad } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/activities/activity_types/feast.txt", SourceName: "game", SourceRank: 3}
	refs := extractRefs(rec, parsed.Nodes, extractObjects(rec, parsed.Nodes))
	want := map[string]bool{
		"activity_group_type:grand":                              false,
		"activity_intent:reduce_stress_intent":                   false,
		"activity_intent:murder_attendee_intent":                 false,
		"activity_guest_invite_rule:activity_invite_rule_rivals": false,
		"activity_pulse_action:feast_good":                       false,
		"activity_pulse_action:feast_bad":                        false,
	}
	assertReferenceKeys(t, refs, want)
}

func TestExtractCurrentSubdatabaseRelations(t *testing.T) {
	cases := []struct {
		path string
		body string
		want map[string]bool
	}{
		{
			path: "common/artifacts/types/test.txt",
			body: `sword = { required_features = { blade_metal } optional_features = { hilt_decoration } default_visuals = sword_visual }`,
			want: map[string]bool{"artifact_feature:blade_metal": false, "artifact_feature:hilt_decoration": false, "artifact_visual:sword_visual": false},
		},
		{
			path: "common/bookmarks/bookmarks/test.txt",
			body: `test_bookmark = { group = test_group }`,
			want: map[string]bool{"bookmark_group:test_group": false},
		},
		{
			path: "common/subject_contracts/groups/test.txt",
			body: `test_group = { contracts = { taxes levies } }`,
			want: map[string]bool{"subject_contract:taxes": false, "subject_contract:levies": false},
		},
		{
			path: "common/tax_slots/types/test.txt",
			body: `test_slot = { default_obligation = default_tax obligations = { default_tax strict_tax } }`,
			want: map[string]bool{"tax_obligation:default_tax": false, "tax_obligation:strict_tax": false},
		},
		{
			path: "common/situation/situations/test.txt",
			body: `test_situation = { situation_group_type = major }`,
			want: map[string]bool{"situation_group_type:major": false},
		},
		{
			path: "common/legends/legend_seeds/test.txt",
			body: `test_seed = { type = heroic }`,
			want: map[string]bool{"legend:heroic": false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			parsed := script.Parse(tc.body)
			rec := fileRecord{ID: 1, RelPath: tc.path, SourceName: "game", SourceRank: 3}
			refs := extractRefs(rec, parsed.Nodes, extractObjects(rec, parsed.Nodes))
			assertReferenceKeys(t, refs, tc.want)
		})
	}
}

func assertReferenceKeys(t *testing.T, refs []refRow, want map[string]bool) {
	t.Helper()
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing reference %s: %+v", key, refs)
		}
	}
}
