package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestAchievementGroupFileUsesNamedGroupObjects(t *testing.T) {
	const path = "common/achievement_groups.txt"
	if got := objectTypeForPath(path); got != "achievement_group" {
		t.Fatalf("achievement group file object type=%q want achievement_group", got)
	}
	parsed := script.Parse(`group = {
	name = "easy_achievements"
	order = {
		"first_achievement"
		"second_achievement"
	}
}`)
	rec := fileRecord{ID: 1, RelPath: path, SourceName: "game", SourceRank: 3}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 1 || objects[0].Type != "achievement_group" || objects[0].Name != "easy_achievements" {
		t.Fatalf("unexpected achievement group objects: %+v", objects)
	}
	refs := extractRefs(rec, parsed.Nodes, objects)
	want := map[string]bool{
		"achievement:first_achievement":  false,
		"achievement:second_achievement": false,
	}
	for _, ref := range refs {
		key := ref.Kind + ":" + ref.Name
		if _, exists := want[key]; exists {
			want[key] = true
			if ref.FromType != "achievement_group" || ref.FromName != "easy_achievements" {
				t.Errorf("unexpected achievement ref owner: %+v", ref)
			}
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing achievement group member ref %s: %+v", key, refs)
		}
	}
}
