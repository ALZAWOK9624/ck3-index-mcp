package mcpserver

import (
	"context"
	"path/filepath"
	"testing"

	"ck3-index/internal/indexer"
)

// TestSaveCharacterDefaultsToThePlayedCharacter pins the one step that makes
// the operation reachable at all.
//
// A caller that has just been handed a .ck3 holds no character ids: the
// metadata carries the player's display name, never their save id, and the id
// only exists inside the gamestate. Requiring the argument made the operation
// answerable only by a caller that had already read the save some other way.
func TestSaveCharacterDefaultsToThePlayedCharacter(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result := callToolForTest(t, db, cfg, "ck3_save",
		map[string]any{"path": "fixture.ck3", "operation": "character"})
	if result["isError"] == true {
		t.Fatalf("character without an id failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)

	character, ok := body["character"].(map[string]any)
	if !ok {
		t.Fatalf("no character was profiled: %+v", body)
	}
	if got := character["save_id"]; got != float64(fixtureSaveCharacter) {
		t.Errorf("profiled character %v, want the played character %d", got, fixtureSaveCharacter)
	}
	if got := character["first_name_key"]; got != "Fixture" {
		t.Errorf("first_name_key = %v", got)
	}

	lookup, ok := body["lookup"].(map[string]any)
	if !ok {
		t.Fatalf("no lookup summary: %+v", body)
	}
	if lookup["defaulted_to_played"] != true {
		t.Errorf("the answer did not disclose that it defaulted: %+v", lookup)
	}
	if got := lookup["resolved_character"]; got != float64(fixtureSaveCharacter) {
		t.Errorf("resolved_character = %v", got)
	}
	if _, echoed := lookup["requested_character"]; echoed {
		t.Errorf("a call that named no character echoed one: %+v", lookup)
	}
	// played_character sits after living in the stream, so the default costs a
	// locating pass the explicit form does not.
	if got := lookup["gamestate_passes"]; got != float64(2) {
		t.Errorf("gamestate_passes = %v, want 2", got)
	}

	// The explicit form must still work, and must cost only the one pass.
	explicit := callToolForTest(t, db, cfg, "ck3_save",
		map[string]any{"path": "fixture.ck3", "operation": "character", "character": "42"})
	if explicit["isError"] == true {
		t.Fatalf("character with an id failed: %+v", explicit)
	}
	explicitLookup := explicit["structuredContent"].(map[string]any)["lookup"].(map[string]any)
	if explicitLookup["requested_character"] != "42" {
		t.Errorf("requested_character = %v", explicitLookup["requested_character"])
	}
	if _, defaulted := explicitLookup["defaulted_to_played"]; defaulted {
		t.Errorf("an explicit id was reported as a default: %+v", explicitLookup)
	}
	if got := explicitLookup["gamestate_passes"]; got != float64(1) {
		t.Errorf("gamestate_passes = %v, want 1", got)
	}
}

// TestSaveCharacterRefusesAnIdThatIsNotInTheSave keeps the not-found answer
// distinct from the defaulting path: an id the caller invented must fail, and
// the guidance must name the way out that actually exists.
func TestSaveCharacterRefusesAnIdThatIsNotInTheSave(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result := callToolForTest(t, db, cfg, "ck3_save",
		map[string]any{"path": "fixture.ck3", "operation": "character", "character": "999999"})
	if result["isError"] != true {
		t.Fatalf("an absent character was answered instead of refused: %+v", result)
	}
}
