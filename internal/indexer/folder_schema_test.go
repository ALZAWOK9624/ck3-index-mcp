package indexer

import (
	"strings"
	"testing"
)

// schemaFromRelPaths builds the engine-layer view through the same recorder
// loadFolderSchema uses, so these tests exercise the real directory bookkeeping
// rather than a hand-written copy of it that could drift -- the copy this
// helper used to carry is what let the missing root level go unnoticed.
func schemaFromRelPaths(relPaths ...string) folderSchema {
	schema := newFolderSchema()
	for _, rel := range relPaths {
		schema.record(rel)
	}
	return schema
}

// The 1.19 rename is the case this check exists for: the old folder still holds
// content, still parses, and is read by nobody.
func TestFolderSchemaFlagsRenamedDispatchFolder(t *testing.T) {
	schema := schemaFromRelPaths(
		"common/religion/religion_types/00_religions.txt",
		"common/religion/holy_site_types/00_holy_sites.txt",
		"common/religion/doctrines/00_doctrines.txt",
	)
	for _, stale := range []string{"common/religion/religions", "common/religion/holy_sites"} {
		verdict, offending := schema.evaluate(stale)
		if !offending {
			t.Fatalf("stale dispatch folder %q was not reported", stale)
		}
		if verdict.code != folderSchemaRenameCode || verdict.severity != "error" {
			t.Fatalf("stale folder %q got %q/%q, want %q/error", stale, verdict.code, verdict.severity, folderSchemaRenameCode)
		}
	}
}

func TestFolderSchemaAcceptsFoldersTheEngineReads(t *testing.T) {
	schema := schemaFromRelPaths(
		"common/religion/religion_types/00_religions.txt",
		"common/traits/00_traits.txt",
		"events/religion_events.txt",
	)
	for _, dir := range []string{"common/religion/religion_types", "common/traits", "events"} {
		if _, offending := schema.evaluate(dir); offending {
			t.Fatalf("folder %q the engine reads was reported as unread", dir)
		}
	}
}

// A folder with no engine neighbour to compare against carries no evidence
// either way, so it must stay silent rather than guess.
func TestFolderSchemaStaysSilentWithoutComparableEvidence(t *testing.T) {
	schema := schemaFromRelPaths("common/traits/00_traits.txt")
	if _, offending := schema.evaluate("common/traits/subfolder/more"); offending {
		t.Fatal("a folder whose parent is itself unknown was reported")
	}
	empty := folderSchema{dirs: map[string]bool{}, children: map[string]map[string]bool{}}
	if _, offending := empty.evaluate("common/religion/religions"); offending {
		t.Fatal("an absent game layer was allowed to declare a project folder wrong")
	}
}

// Vanilla files everything in common/buildings/ flat, but Godherja ships its
// buildings from common/buildings/godherja/ and the game loads them. A mod
// filing content into its own subfolder is not a mistake, and an install that
// happens not to use subfolders is not evidence that the loader refuses them.
func TestFolderSchemaAllowsModOwnedSubfolders(t *testing.T) {
	schema := schemaFromRelPaths(
		"common/buildings/00_buildings.txt",
		"common/scripted_effects/00_effects.txt",
		"events/court_events.txt",
	)
	for _, dir := range []string{
		"common/buildings/godherja",
		"common/scripted_effects/integrated mods",
		"events/my_new_chain",
	} {
		if _, offending := schema.evaluate(dir); offending {
			t.Fatalf("mod-owned subfolder %q was reported as unread", dir)
		}
	}
}

// localization/<lang>/replace/ is a documented CK3 override folder that vanilla
// itself never ships, so it must survive a schema built from the install alone.
func TestFolderSchemaAllowsLocalizationReplaceFolder(t *testing.T) {
	schema := schemaFromRelPaths(
		"localization/english/gui_l_english.yml",
		"localization/english/traits/00_traits_l_english.yml",
		"localization/simp_chinese/gui_l_simp_chinese.yml",
	)
	for _, dir := range []string{"localization/english/replace", "localization/simp_chinese/replace"} {
		if _, offending := schema.evaluate(dir); offending {
			t.Fatalf("localization override folder %q was reported as unread", dir)
		}
	}
}

func TestFolderNameSimilaritySeparatesRenamesFromNeighbours(t *testing.T) {
	similar := [][2]string{
		{"religions", "religion_types"},
		{"holy_sites", "holy_site_types"},
		{"on_actions", "on_action"},
		{"decisons", "decisions"},
	}
	for _, pair := range similar {
		if _, ok := folderNamesSimilar(pair[0], pair[1]); !ok {
			t.Fatalf("%q and %q were not recognized as the same concept", pair[0], pair[1])
		}
	}
	unrelated := [][2]string{
		{"traditions", "innovations"},
		{"traits", "buildings"},
		{"decisions", "decision_group_types"},
		// Same category word, different concept: a mod's own event folder.
		{"godherja_events", "court_events"},
		{"godherja_decisions", "dlc_decisions"},
	}
	for _, pair := range unrelated {
		if _, ok := folderNamesSimilar(pair[0], pair[1]); ok {
			t.Fatalf("%q and %q were wrongly offered as each other's fix", pair[0], pair[1])
		}
	}
}

// Two siblings can both look plausible; the reported one must not depend on Go
// map iteration order or the same index would suggest a different fix per run.
func TestFolderSchemaSuggestionIsStable(t *testing.T) {
	schema := schemaFromRelPaths(
		"common/religion/religion_types/a.txt",
		"common/religion/religion_groups/b.txt",
		"common/religion/doctrines/c.txt",
	)
	first, offending := schema.evaluate("common/religion/religion")
	if !offending {
		t.Fatal("stale folder was not reported")
	}
	for i := 0; i < 32; i++ {
		next, _ := schema.evaluate("common/religion/religion")
		if next.message != first.message {
			t.Fatalf("suggestion changed between runs: %q then %q", first.message, next.message)
		}
	}
}

// A misspelled top-level folder is the same failure as a misspelled one under
// common/, and the worse one: nothing below it reaches a parser either. The
// check used to start one level too deep and let every one of these through.
func TestFolderSchemaFlagsMisspelledTopLevelFolders(t *testing.T) {
	schema := schemaFromRelPaths(
		"common/traits/00_traits.txt",
		"events/court_events.txt",
		"localization/english/gui_l_english.yml",
		"history/characters/00_characters.txt",
	)
	for typo, want := range map[string]string{
		"event":        "events",
		"commmon":      "common",
		"localisation": "localization",
	} {
		verdict, offending := schema.evaluate(typo)
		if !offending {
			t.Fatalf("top-level folder %q was not reported", typo)
		}
		if verdict.code != folderSchemaRenameCode || verdict.severity != "error" {
			t.Fatalf("%q got %q/%q, want %q/error", typo, verdict.code, verdict.severity, folderSchemaRenameCode)
		}
		if !strings.Contains(verdict.message, want+"/") {
			t.Fatalf("%q was pointed at %q, want a suggestion of %q", typo, verdict.message, want)
		}
	}
}

// The root level must not become a source of guesses either: a mod is as free
// to add its own top-level folder as its own subfolder, and the folders the
// engine really reads stay silent.
func TestFolderSchemaAcceptsRealAndModOwnedTopLevelFolders(t *testing.T) {
	schema := schemaFromRelPaths(
		"common/traits/00_traits.txt",
		"events/court_events.txt",
		"gfx/interface/icons/icon.dds",
		"localization/english/gui_l_english.yml",
	)
	for _, dir := range []string{"common", "events", "localization", "tools", "jomini", "notes"} {
		if _, offending := schema.evaluate(dir); offending {
			t.Fatalf("top-level folder %q was reported as unread", dir)
		}
	}
}
