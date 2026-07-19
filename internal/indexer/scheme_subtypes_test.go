package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestSchemeSubdirectoriesUseIndependentObjectTypes(t *testing.T) {
	want := map[string]string{
		"common/schemes/scheme_types/murder_scheme.txt":                "scheme_type",
		"common/schemes/agent_types/00_agent_types.txt":                "scheme_agent_type",
		"common/schemes/pulse_actions/00_scheme_pulse_actions.txt":     "scheme_pulse_action",
		"common/schemes/scheme_countermeasures/00_countermeasures.txt": "scheme_countermeasure",
	}
	for path, typ := range want {
		if got := objectTypeForPath(path); got != typ {
			t.Errorf("object type for %s=%q want %q", path, got, typ)
		}
	}
}

func TestExtractSchemeTypeAndAgentReferences(t *testing.T) {
	parsed := script.Parse(`test_effect = {
	scheme_type = murder
	can_start_scheme = { type = befriend target = scope:target }
	start_scheme = { type = abduct target_character = scope:target }
	add_agent_slot = agent_infiltrator
}`)
	rec := fileRecord{ID: 1, RelPath: "common/scripted_effects/test.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"scheme_type:murder":                  false,
		"scheme_type:befriend":                false,
		"scheme_type:abduct":                  false,
		"scheme_agent_type:agent_infiltrator": false,
	}
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing scheme reference %s: %+v", key, refs)
		}
	}
}

func TestExtractSchemePulseActionEntries(t *testing.T) {
	parsed := script.Parse(`murder = {
	pulse_actions = {
		entries = { murder_success murder_failure }
	}
}`)
	rec := fileRecord{ID: 1, RelPath: "common/schemes/scheme_types/murder_scheme.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"scheme_pulse_action:murder_success": false,
		"scheme_pulse_action:murder_failure": false,
	}
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing scheme pulse-action reference %s: %+v", key, refs)
		}
	}
}
