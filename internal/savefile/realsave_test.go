package savefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealSaveMetadata reads an actual CK3 save when one is pointed at.
//
// Real saves are not committed, so this stays skipped by default and
// `go test ./...` remains hermetic. Point it at a save and a token-map
// directory to check this package against the real thing:
//
//	CK3_INDEX_TEST_SAVE=.../autosave_exit.baseline.ck3
//	CK3_INDEX_TEST_TOKEN_MAPS=.../rakaly-oracle
//
// The expectations below are optional so the test is useful for any save;
// set CK3_INDEX_TEST_EXPECT to `field=value` pairs separated by semicolons.
// The save is only ever opened for reading.
func TestRealSaveMetadata(t *testing.T) {
	savePath := os.Getenv("CK3_INDEX_TEST_SAVE")
	mapDir := os.Getenv("CK3_INDEX_TEST_TOKEN_MAPS")
	if savePath == "" || mapDir == "" {
		t.Skip("set CK3_INDEX_TEST_SAVE and CK3_INDEX_TEST_TOKEN_MAPS to check a real save")
	}

	maps, err := LoadTokenMaps(mapDir)
	if err != nil {
		t.Fatalf("loading token maps from %s: %v", mapDir, err)
	}
	if len(maps) == 0 {
		t.Fatalf("no *.tokens.txt files under %s", mapDir)
	}

	raw, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("reading save: %v", err)
	}
	limits := DefaultLimits()
	envelope, err := Analyze(Bytes(raw), limits)
	if err != nil {
		t.Fatalf("analyzing %s: %v", filepath.Base(savePath), err)
	}
	section, err := envelope.Metadata(Bytes(raw), limits)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	metadata, err := ReadMetadata(section, maps, limits)
	if err != nil {
		t.Fatalf("decoding metadata: %v", err)
	}

	t.Logf("layout=%s metadata_bytes=%d token_map=%s coverage=%d/%d",
		envelope.Layout, len(section), metadata.Coverage.TokenMap,
		metadata.Coverage.ResolvedIdentifiers, metadata.Coverage.ObservedIdentifiers)
	t.Logf("version=%s date=%s player=%q title=%q house=%q government=%s players=%d",
		metadata.Version, metadata.Date, metadata.PlayerName, metadata.TitleName,
		metadata.HouseName, metadata.Government, metadata.NumberOfPlayers)
	t.Logf("mods=%v dlcs=%d game_rules=%d", metadata.Mods, len(metadata.DLCs), len(metadata.GameRules))

	if !metadata.Coverage.Complete {
		t.Errorf("token map did not cover the save: %+v", metadata.Coverage)
	}
	if metadata.Version == "" || metadata.Date == "" {
		t.Errorf("version and date must both decode: %+v", metadata)
	}

	for _, expectation := range strings.Split(os.Getenv("CK3_INDEX_TEST_EXPECT"), ";") {
		expectation = strings.TrimSpace(expectation)
		if expectation == "" {
			continue
		}
		field, want, ok := strings.Cut(expectation, "=")
		if !ok {
			t.Fatalf("malformed expectation %q", expectation)
		}
		got := realSaveField(metadata, strings.TrimSpace(field))
		if got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

func realSaveField(m *Metadata, name string) string {
	switch name {
	case "version":
		return m.Version
	case "date":
		return m.Date
	case "real_date":
		return m.RealDate
	case "player_name":
		return m.PlayerName
	case "title_name":
		return m.TitleName
	case "house_name":
		return m.HouseName
	case "government":
		return m.Government
	case "mods":
		return strings.Join(m.Mods, ",")
	case "dlc_count":
		return itoa(len(m.DLCs))
	case "game_rule_count":
		return itoa(len(m.GameRules))
	default:
		return ""
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
