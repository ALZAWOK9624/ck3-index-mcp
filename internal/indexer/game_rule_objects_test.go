package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestExtractGameRulesAndNestedSettings(t *testing.T) {
	parsed := script.Parse(`gender_equality = {
	categories = { game_modes }
	default = default_gender_equality
	default_gender_equality = {}
	full_gender_equality = { flag = blocks_achievements }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/game_rules/test_game_rules.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 3 {
		t.Fatalf("game-rule extraction returned %d objects, want rule plus two settings: %+v", len(objects), objects)
	}
	want := []struct{ typ, name string }{
		{"game_rule", "gender_equality"},
		{"game_rule_setting", "default_gender_equality"},
		{"game_rule_setting", "full_gender_equality"},
	}
	for index := range want {
		if objects[index].Type != want[index].typ || objects[index].Name != want[index].name {
			t.Fatalf("game-rule object %d=%s:%s want %s:%s", index, objects[index].Type, objects[index].Name, want[index].typ, want[index].name)
		}
	}
}

func TestGameRulePatternsSeparateRuleFieldsFromSettings(t *testing.T) {
	parsed := script.Parse(`difficulty = {
	categories = { difficulty }
	default = normal_difficulty
	normal_difficulty = {}
	hard_difficulty = { apply_modifier = player:hard }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/game_rules/test_game_rules.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	fields := extractObjectFields(rec, parsed.Nodes, objects)
	ruleFields := map[string]bool{}
	settingFields := map[string]bool{}
	for _, field := range fields {
		switch field.Type {
		case "game_rule":
			ruleFields[field.Field] = true
		case "game_rule_setting":
			settingFields[field.Field] = true
		}
	}
	if !ruleFields["categories"] || !ruleFields["default"] || ruleFields["normal_difficulty"] || ruleFields["hard_difficulty"] {
		t.Fatalf("game-rule patterns contain setting ids or miss rule fields: %+v", ruleFields)
	}
	if !settingFields["apply_modifier"] {
		t.Fatalf("nested setting fields were not learned as game-rule-setting patterns: %+v", settingFields)
	}
}

func TestExtractGameRuleStructuralAndScriptReferences(t *testing.T) {
	parsed := script.Parse(`gender_equality = {
	default = default_gender_equality
	default_gender_equality = {}
	full_gender_equality = { trigger = { has_game_rule = default_gender_equality } }
}`)
	rec := fileRecord{ID: 1, RelPath: "common/game_rules/test_game_rules.txt", SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"game_rule:gender_equality->game_rule_setting:default_gender_equality":              false,
		"game_rule_setting:default_gender_equality->game_rule:gender_equality":              false,
		"game_rule_setting:full_gender_equality->game_rule:gender_equality":                 false,
		"game_rule_setting:full_gender_equality->game_rule_setting:default_gender_equality": false,
	}
	for _, ref := range refs {
		key := ref.FromType + ":" + ref.FromName + "->" + ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing game-rule reference %s: %+v", key, refs)
		}
	}
}
