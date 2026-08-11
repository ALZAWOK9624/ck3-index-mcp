package savefile

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestRealSaveTimeline extracts dated events from a real save when one is
// pointed at. Skipped by default like the other real-save tests; set
// CK3_INDEX_TEST_SAVE, CK3_INDEX_TEST_TOKEN_MAPS, and optionally
// CK3_INDEX_TEST_CHARACTER to narrow to one character.
func TestRealSaveTimeline(t *testing.T) {
	savePath := os.Getenv("CK3_INDEX_TEST_SAVE")
	mapDir := os.Getenv("CK3_INDEX_TEST_TOKEN_MAPS")
	if savePath == "" || mapDir == "" {
		t.Skip("set CK3_INDEX_TEST_SAVE and CK3_INDEX_TEST_TOKEN_MAPS to build a timeline")
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
	var resolver *TokenMap
	if envelope.Encoding != EncodingText {
		maps, err := LoadTokenMaps(mapDir)
		if err != nil {
			t.Fatalf("loading token maps: %v", err)
		}
		section, err := envelope.Metadata(source, limits)
		if err != nil {
			t.Fatalf("reading metadata: %v", err)
		}
		observed, err := observedIdentifiers(section, limits)
		if err != nil {
			t.Fatalf("inventorying metadata: %v", err)
		}
		if resolver, _, err = SelectTokenMap(maps, observed); err != nil {
			t.Fatalf("selecting token map: %v", err)
		}
	}

	var character int64
	if raw := os.Getenv("CK3_INDEX_TEST_CHARACTER"); raw != "" {
		if character, err = strconv.ParseInt(raw, 10, 64); err != nil {
			t.Fatalf("CK3_INDEX_TEST_CHARACTER: %v", err)
		}
	}

	gamestate, err := envelope.GamestateReader(source, limits)
	if err != nil {
		t.Fatalf("opening gamestate: %v", err)
	}
	defer gamestate.Close()

	started := time.Now()
	scan, err := ScanTimeline(envelope.Encoding, gamestate, resolver, TimelineQuery{
		Character: character, MaxEvents: 25,
	}, limits)
	if err != nil {
		t.Fatalf("scanning timeline: %v", err)
	}
	t.Logf("%d events (%d kept) from %d titles and %d memories in %s (%d bytes, truncated=%v)",
		scan.Total, len(scan.Events), scan.TitlesSeen, scan.MemoriesSeen,
		time.Since(started).Round(time.Millisecond), scan.BytesRead, scan.Truncated)

	if scan.TitlesSeen == 0 {
		t.Error("no titles were examined; landed_titles was not reached")
	}
	for _, event := range scan.Events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("  %s", encoded)
	}
	// Every event must be dated, or it cannot take part in a chronology.
	for _, event := range scan.Events {
		if event.Date == "" {
			t.Errorf("an undated event was kept: %+v", event)
		}
	}
	for index := 1; index < len(scan.Events); index++ {
		if compareSaveDates(scan.Events[index-1].Date, scan.Events[index].Date) > 0 {
			t.Errorf("events are out of order: %s then %s",
				scan.Events[index-1].Date, scan.Events[index].Date)
		}
	}
}
