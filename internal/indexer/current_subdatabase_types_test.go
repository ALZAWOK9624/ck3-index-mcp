package indexer

import "testing"

func TestCurrentNestedInfoDatabasesKeepIndependentObjectTypes(t *testing.T) {
	want := map[string]string{
		"common/activities/activity_types/test.txt":       "activity",
		"common/activities/activity_group_types/test.txt": "activity_group_type",
		"common/activities/activity_locales/test.txt":     "activity_locale",
		"common/activities/guest_invite_rules/test.txt":   "activity_guest_invite_rule",
		"common/activities/intents/test.txt":              "activity_intent",
		"common/activities/pulse_actions/test.txt":        "activity_pulse_action",
		"common/artifacts/types/test.txt":                 "artifact_type",
		"common/artifacts/slots/test.txt":                 "artifact_slot",
		"common/artifacts/blueprints/test.txt":            "artifact_blueprint",
		"common/artifacts/feature_groups/test.txt":        "artifact_feature_group",
		"common/artifacts/features/test.txt":              "artifact_feature",
		"common/artifacts/templates/test.txt":             "artifact_template",
		"common/artifacts/visuals/test.txt":               "artifact_visual",
		"common/bookmarks/bookmarks/test.txt":             "bookmark",
		"common/bookmarks/challenge_characters/test.txt":  "bookmark_challenge_character",
		"common/bookmarks/groups/test.txt":                "bookmark_group",
		"common/court_positions/types/test.txt":           "court_position",
		"common/court_positions/tasks/test.txt":           "court_position_task",
		"common/diarchies/diarchy_types/test.txt":         "diarchy",
		"common/diarchies/diarchy_mandates/test.txt":      "diarchy_mandate",
		"common/domiciles/types/test.txt":                 "domicile",
		"common/domiciles/buildings/test.txt":             "domicile_building",
		"common/legends/legend_types/test.txt":            "legend",
		"common/legends/chronicles/test.txt":              "legend_chronicle",
		"common/legends/legend_seeds/test.txt":            "legend_seed",
		"common/raids/intents/test.txt":                   "raid_intent",
		"common/situation/situations/test.txt":            "situation",
		"common/situation/catalysts/test.txt":             "situation_catalyst",
		"common/situation/situation_group_types/test.txt": "situation_group_type",
		"common/struggle/struggles/test.txt":              "struggle",
		"common/struggle/catalysts/test.txt":              "struggle_catalyst",
		"common/subject_contracts/contracts/test.txt":     "subject_contract",
		"common/subject_contracts/groups/test.txt":        "subject_contract_group",
		"common/tax_slots/types/test.txt":                 "tax_slot",
		"common/tax_slots/obligations/test.txt":           "tax_obligation",
		"common/travel/point_of_interest_types/test.txt":  "travel_point_of_interest_type",
		"common/travel/travel_options/test.txt":           "travel_option",
	}
	for path, typ := range want {
		if got := objectTypeForPath(path); got != typ {
			t.Errorf("object type for %s=%q want %q", path, got, typ)
		}
	}
}
