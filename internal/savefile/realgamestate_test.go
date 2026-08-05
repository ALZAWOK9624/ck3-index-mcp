package savefile

import (
	"os"
	"testing"
	"time"
)

// TestRealSaveGamestateScan streams a real gamestate when one is pointed at.
//
// Skipped by default for the same reason as TestRealSaveMetadata: real saves
// are not committed. Set CK3_INDEX_TEST_SAVE and CK3_INDEX_TEST_TOKEN_MAPS.
// The save is only ever opened for reading.
func TestRealSaveGamestateScan(t *testing.T) {
	savePath := os.Getenv("CK3_INDEX_TEST_SAVE")
	mapDir := os.Getenv("CK3_INDEX_TEST_TOKEN_MAPS")
	if savePath == "" || mapDir == "" {
		t.Skip("set CK3_INDEX_TEST_SAVE and CK3_INDEX_TEST_TOKEN_MAPS to scan a real gamestate")
	}
	maps, err := LoadTokenMaps(mapDir)
	if err != nil {
		t.Fatalf("loading token maps: %v", err)
	}

	source, handle, err := Open(savePath)
	if err != nil {
		t.Fatalf("opening save: %v", err)
	}
	defer handle.Close()

	limits := DefaultLimits()
	envelope, err := Analyze(source, limits)
	if err != nil {
		t.Fatalf("analyzing: %v", err)
	}
	metadataSection, err := envelope.Metadata(source, limits)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	observed, err := observedIdentifiers(metadataSection, limits)
	if err != nil {
		t.Fatalf("inventorying metadata: %v", err)
	}
	resolver, _, err := SelectTokenMap(maps, observed)
	if err != nil {
		t.Fatalf("selecting token map: %v", err)
	}

	// Pass one: the inventory an audit needs, plus the played character.
	started := time.Now()
	gamestate, err := envelope.GamestateReader(source, limits)
	if err != nil {
		t.Fatalf("opening gamestate: %v", err)
	}
	inventory, err := ScanGamestate(gamestate, resolver, GamestateQuery{Inventory: true}, limits)
	gamestate.Close()
	if err != nil {
		t.Fatalf("inventory scan: %v", err)
	}
	t.Logf("inventory pass: %d bytes in %s — %d traits, %d title keys, %d house name keys, played=%d",
		inventory.BytesRead, time.Since(started).Round(time.Millisecond),
		len(inventory.TraitsLookup), len(inventory.TitleKeys), len(inventory.HouseNameKeys),
		inventory.PlayedCharacter)

	if len(inventory.TraitsLookup) == 0 {
		t.Error("traits_lookup came back empty; the trait vocabulary is the audit's whole basis")
	}
	if len(inventory.TitleKeys) == 0 {
		t.Error("no title keys were collected")
	}
	if inventory.PlayedCharacter == 0 {
		t.Error("no played character was found")
	}

	// Pass two: the played character plus the titles they hold.
	target := inventory.PlayedCharacter
	started = time.Now()
	gamestate, err = envelope.GamestateReader(source, limits)
	if err != nil {
		t.Fatalf("reopening gamestate: %v", err)
	}
	dossier, err := ScanGamestate(gamestate, resolver,
		GamestateQuery{Character: target, TitlesHeldBy: target}, limits)
	gamestate.Close()
	if err != nil {
		t.Fatalf("character scan: %v", err)
	}
	if dossier.Character == nil {
		t.Fatalf("character %d was not found in the gamestate", target)
	}
	character := dossier.Character
	t.Logf("character pass: %d bytes in %s", dossier.BytesRead, time.Since(started).Round(time.Millisecond))
	t.Logf("character %d: name_key=%q birth=%s nickname=%q/%q culture=%d faith=%d house=%d alive=%v",
		character.SaveID, character.FirstNameKey, character.Birth,
		character.NicknameKey, character.NicknameText,
		character.CultureID, character.FaithID, character.HouseID, character.Alive)
	t.Logf("  skills=%v traits=%v", character.Skills, character.Traits)
	t.Logf("  gold=%.2f piety=%.2f prestige=%.2f health=%.5f",
		character.Gold, character.Piety, character.Prestige, character.Health)
	t.Logf("  spouse=%d children=%v employer=%d", character.PrimarySpouse, character.Children, character.Employer)
	for _, title := range dossier.Titles {
		t.Logf("  title %s %q since %s", title.Key, title.Name, title.Since)
	}

	if character.FirstNameKey == "" {
		t.Error("the character has no first name")
	}
	if len(character.TraitIndices) > 0 && len(character.Traits) == 0 {
		t.Error("trait indices did not resolve through traits_lookup")
	}

	// Pass three: the character's house, which the stream passes before it
	// reaches the character and so cannot be resolved in one go.
	if character.HouseID != 0 {
		gamestate, err = envelope.GamestateReader(source, limits)
		if err != nil {
			t.Fatalf("reopening gamestate: %v", err)
		}
		houses, err := ScanGamestate(gamestate, resolver,
			GamestateQuery{House: character.HouseID, HouseValid: true}, limits)
		gamestate.Close()
		if err != nil {
			t.Fatalf("house scan: %v", err)
		}
		if houses.House == nil {
			t.Errorf("house %d was not found", character.HouseID)
		} else {
			t.Logf("house %d: name_key=%q localized=%q motto=%q founded=%s head=%d",
				houses.House.SaveID, houses.House.NameKey, houses.House.LocalizedName,
				houses.House.MottoKey, houses.House.FoundDate, houses.House.HeadOfHouse)
		}
	}
}
