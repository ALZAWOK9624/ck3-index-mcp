package indexer

import (
	"strings"
	"testing"
)

func TestMergePolicyForPathSeparatesTheFourBehaviours(t *testing.T) {
	cases := []struct {
		rel  string
		want MergePolicy
	}{
		{"common/traits/00_traits.txt", MergePolicyOverride},
		{"common/decisions/godherja/gh_decisions.txt", MergePolicyOverride},
		{"events/court_events.txt", MergePolicyOverride},
		{"common/on_action/00_game_start.txt", MergePolicyContainerMerge},
		{"common/defines/00_defines.txt", MergePolicyPerKeyOverride},
		{"common/defines/graphic/00_graphics.txt", MergePolicyPerKeyOverride},
		{"history/titles/k_test.txt", MergePolicyPerKeyOverride},
		{"localization/english/traits_l_english.yml", MergePolicyPerKeyOverride},
		{"gui/window_county_view.gui", MergePolicyFirstInWins},
	}
	for _, tc := range cases {
		if got := MergePolicyForPath(tc.rel); got != tc.want {
			t.Fatalf("%s got policy %q, want %q", tc.rel, got, tc.want)
		}
	}
}

// An unknown folder must claim loss rather than promise a merge: a reader who
// trusts a wrong "it merges" ships the break, a reader who checks a wrong "it
// is replaced" loses a minute.
func TestMergePolicyDefaultsToTheConservativeAnswer(t *testing.T) {
	if got := MergePolicyForPath("common/some_future_1_20_folder/x.txt"); got != MergePolicyOverride {
		t.Fatalf("unknown folder got policy %q, want %q", got, MergePolicyOverride)
	}
	if MergePolicyForPath("").Consequence() == "" {
		t.Fatal("every policy must carry a consequence sentence")
	}
}

func TestLoadOrderPrefixReadsBothDirections(t *testing.T) {
	cases := []struct {
		name       string
		wantTarget string
		wantIntent LoadOrderIntent
	}{
		{"00_traits.txt", "traits.txt", LoadOrderIntentFirst},
		{"0_GH_cave_walls.txt", "GH_cave_walls.txt", LoadOrderIntentFirst},
		{"zzz_traits.txt", "traits.txt", LoadOrderIntentLast},
		{"z_traits.txt", "traits.txt", LoadOrderIntentLast},
		{"common/traits/zz_traits.txt", "traits.txt", LoadOrderIntentLast},
		{"GH_titles.txt", "", LoadOrderIntentNone},
		{"window_county_view.gui", "", LoadOrderIntentNone},
		{"traits.txt", "", LoadOrderIntentNone},
	}
	for _, tc := range cases {
		target, intent := LoadOrderPrefix(tc.name)
		if intent != tc.wantIntent || target != tc.wantTarget {
			t.Fatalf("%s got (%q,%q), want (%q,%q)", tc.name, target, intent, tc.wantTarget, tc.wantIntent)
		}
	}
}

// gui/ keeps the first definition it reads, so the zzz_ habit that works
// everywhere else aims the file away from winning. Saying so is the whole
// point of pairing the prefix with the policy.
func TestOverrideAnnotationFlagsPrefixPointingAwayFromWinning(t *testing.T) {
	finding := OverrideDriftFile{Path: "gui/zzz_window_county_view.gui"}
	annotateOverrideMergePolicy(&finding)
	if finding.MergePolicy != MergePolicyFirstInWins {
		t.Fatalf("gui finding got policy %q, want %q", finding.MergePolicy, MergePolicyFirstInWins)
	}
	if !strings.Contains(finding.LoadOrderNote, "00_") {
		t.Fatalf("gui zzz_ finding did not name the direction that wins: %q", finding.LoadOrderNote)
	}

	winning := OverrideDriftFile{Path: "gui/00_window_county_view.gui"}
	annotateOverrideMergePolicy(&winning)
	if strings.Contains(winning.LoadOrderNote, "aims away") {
		t.Fatalf("gui 00_ finding was reported as aiming the wrong way: %q", winning.LoadOrderNote)
	}
}

func TestOverrideAnnotationCarriesConsequenceForEveryFinding(t *testing.T) {
	for _, rel := range []string{
		"common/traits/00_traits.txt",
		"common/on_action/00_game_start.txt",
		"common/defines/00_defines.txt",
		"gui/window_county_view.gui",
	} {
		finding := OverrideDriftFile{Path: rel}
		annotateOverrideMergePolicy(&finding)
		if finding.PolicyConsequence == "" {
			t.Fatalf("%s carried no policy consequence", rel)
		}
	}
	onAction := OverrideDriftFile{Path: "common/on_action/00_game_start.txt"}
	annotateOverrideMergePolicy(&onAction)
	if !strings.Contains(onAction.PolicyConsequence, "appended") {
		t.Fatalf("on_action consequence does not mention the appended entries a replacement discards: %q", onAction.PolicyConsequence)
	}
}

func TestOnActionMergeContainersCoverTheListFields(t *testing.T) {
	for _, key := range []string{"events", "on_actions", "random_events", "first_valid"} {
		if !onActionMergeContainers[key] {
			t.Fatalf("%q is a merging on_action container but is not listed as one", key)
		}
	}
	for _, key := range []string{"effect", "trigger", "weight_multiplier", "fallback"} {
		if onActionMergeContainers[key] {
			t.Fatalf("%q is a single-slot on_action field but is listed as merging", key)
		}
	}
}
