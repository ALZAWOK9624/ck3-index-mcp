package savefile

import (
	"strings"
	"testing"
)

// timelineFixture holds every dated source, in the order a save writes them:
// titles, characters, schemes, then the managers. The order matters — a
// character's own memory ids have to be known before the memories are read.
const timelineFixture = `
landed_titles={
	landed_titles={
		7={
			key="c_alpha"
			history={
				1066.9.1={ type=created holder=42 }
				1066.10.1={ type=conquest holder=99 }
				1070.1.1=42
			}
		}
		8={ key="c_beta" history={ 1080.5.2={ type=inheritance holder=42 } } }
	}
}
living={
	42={ alive_data={ memories={ 5 } } }
	99={ alive_data={ memories={ 6 } } }
}
schemes={
	active={
		0={
			type=sway
			status=continue
			owner=42
			target={ type=character target=99 }
			progress=65
			scheme_exposed=no
			date=1066.10.2
		}
		1={
			type=murder
			status=continue
			owner=99
			target={ type=character target=7 }
			progress=12
			scheme_exposed=yes
			date=1068.2.9
		}
	}
}
character_memory_manager={
	database={
		5={
			type=ascended_throne_memory
			participants={ flavor_character=42 }
			creation_date=1066.9.1
			end_date=1221.9.1
		}
		6={
			type=battle_memory
			participants={ enemy=42 commander=99 }
			creation_date=1067.3.4
		}
	}
}
court_positions={
	database={
		0={ court_position=travel_leader_court_position employee=99 employer=42 hire_date=1066.10.6 }
		1={ court_position=antiquarian_court_position employee=7 employer=13 hire_date=1071.4.4 }
	}
}
vassal_contracts={
	database={
		0={ contract_group=feudal_vassal liege=42 vassal=99 date=1066.10.1 }
	}
}
house_relations={
	database={
		0={
			houses={ 6724 6720 }
			type=default_house_relation
			level=default_house_relation_level_neutral
			history={ {
					change_reason="卡利尼科斯与菲洛梅娜结婚"
					date=1066.10.21
					amount=0.1
					level=default_house_relation_level_neutral
				} {
					change_reason="兹罗索斯去世"
					date=1069.6.30
					amount=-0.2
					level=default_house_relation_level_poor
				} }
		}
	}
}
struggle_manager={
	database={
		0={
			struggle_type=iberian_struggle
			struggle_start_date=718.1.1
			struggle_progress_data={
				struggle_iberia_phase_hostility={
					value=1
					history={ {
							value=1
							catalysts=catalyst_passing_of_time
							struggle_phase=struggle_iberia_phase_hostility
							time=1066.10.1
							character=4294967295
						} }
				}
			}
		}
	}
}
`

func TestTimelineCollectsEveryDatedSource(t *testing.T) {
	scan, err := ScanTimeline(EncodingText, strings.NewReader(timelineFixture), nil,
		TimelineQuery{}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	examined := scan.Examined()
	for kind, want := range map[string]int{
		EventTitleHistory: 2, EventCharacterMemory: 2, EventScheme: 2,
		EventCourtPosition: 2, EventVassalContract: 1, EventHouseRelation: 1, EventStruggle: 1,
	} {
		if examined[kind] != want {
			t.Errorf("examined %d %s entries, want %d", examined[kind], kind, want)
		}
	}
	// 4 successions, 2 memories, 2 schemes, 2 appointments, 1 contract,
	// 2 house relation changes, 1 struggle opening and 1 catalyst.
	if scan.Total != 15 {
		t.Fatalf("total = %d, want 15: %+v", scan.Total, scan.Totals)
	}

	// Oldest first, and by component: as text "1066.10.1" sorts before
	// "1066.9.1", which would put the conquest before the creation.
	want := []string{
		"718.1.1", "1066.9.1", "1066.9.1", "1066.10.1", "1066.10.1", "1066.10.1",
		"1066.10.2", "1066.10.6", "1066.10.21", "1067.3.4", "1068.2.9", "1069.6.30",
		"1070.1.1", "1071.4.4", "1080.5.2",
	}
	for index, date := range want {
		if scan.Events[index].Date != date {
			t.Fatalf("event %d is dated %s, want %s (order: %+v)",
				index, scan.Events[index].Date, date, scan.Events)
		}
	}

	sway := findEvent(scan.Events, func(e TimelineEvent) bool {
		return e.Kind == EventScheme && e.Type == "sway"
	})
	if sway.Owner != 42 || sway.Target != 99 || sway.TargetType != "character" ||
		sway.Status != "continue" || sway.Progress != 65 || sway.Exposed {
		t.Errorf("the sway scheme = %+v", sway)
	}

	appointment := findEvent(scan.Events, func(e TimelineEvent) bool {
		return e.Kind == EventCourtPosition && e.Date == "1066.10.6"
	})
	if appointment.Type != "travel_leader_court_position" ||
		appointment.Owner != 42 || appointment.Target != 99 {
		t.Errorf("the court appointment = %+v", appointment)
	}
	contract := findEvent(scan.Events, func(e TimelineEvent) bool {
		return e.Kind == EventVassalContract
	})
	if contract.Type != "feudal_vassal" || contract.Owner != 42 || contract.Target != 99 {
		t.Errorf("the vassal contract = %+v", contract)
	}

	// A house relation change carries the save's own written reason, and the
	// houses it is between, which are named beside the history rather than in
	// it and so can only be attached once the whole entry has been read.
	death := findEvent(scan.Events, func(e TimelineEvent) bool {
		return e.Kind == EventHouseRelation && e.Date == "1069.6.30"
	})
	if death.Reason != "兹罗索斯去世" || death.Amount != -0.2 ||
		death.Level != "default_house_relation_level_poor" {
		t.Errorf("the house relation change = %+v", death)
	}
	if len(death.Houses) != 2 || death.Houses[0] != 6724 || death.Houses[1] != 6720 {
		t.Errorf("the house relation names houses %v, want both", death.Houses)
	}

	opening := findEvent(scan.Events, func(e TimelineEvent) bool {
		return e.Kind == EventStruggle && e.Status == "began"
	})
	if opening.Date != "718.1.1" || opening.Type != "iberian_struggle" {
		t.Errorf("the struggle opening = %+v", opening)
	}
	catalyst := findEvent(scan.Events, func(e TimelineEvent) bool {
		return e.Kind == EventStruggle && e.Status != "began"
	})
	if catalyst.Type != "catalyst_passing_of_time" || catalyst.Date != "1066.10.1" ||
		catalyst.Status != "struggle_iberia_phase_hostility" {
		t.Errorf("the struggle catalyst = %+v", catalyst)
	}
	// The absent-character sentinel must not become a save id.
	if catalyst.Owner != 0 {
		t.Errorf("the catalyst reported character %d, want none", catalyst.Owner)
	}

	conquest := scan.Events[3]
	if conquest.Kind != EventTitleHistory || conquest.Type != "conquest" ||
		conquest.Title != "c_alpha" || conquest.TitleID != 7 || conquest.Holder != 99 {
		t.Errorf("the conquest event = %+v", conquest)
	}
	// The bare `<date>=<holder>` form still dates a succession.
	bare := findEvent(scan.Events, func(e TimelineEvent) bool {
		return e.Kind == EventTitleHistory && e.Date == "1070.1.1"
	})
	if bare.Holder != 42 || bare.Type != "" {
		t.Errorf("the bare succession form = %+v", bare)
	}
	battle := findEvent(scan.Events, func(e TimelineEvent) bool { return e.Type == "battle_memory" })
	if battle.Kind != EventCharacterMemory || battle.MemoryID != 6 ||
		battle.Participants["commander"] != 99 {
		t.Errorf("the battle memory = %+v", battle)
	}
}

func findEvent(events []TimelineEvent, match func(TimelineEvent) bool) TimelineEvent {
	for _, event := range events {
		if match(event) {
			return event
		}
	}
	return TimelineEvent{}
}

// TestTimelineNarrowsToOneCharacter covers both ways a memory reaches a
// character: one they hold, and one they only appear in.
func TestTimelineNarrowsToOneCharacter(t *testing.T) {
	scan, err := ScanTimeline(EncodingText, strings.NewReader(timelineFixture), nil,
		TimelineQuery{Character: 42}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	// A scheme reaches a character from either end: 42 runs the sway, and is
	// the target of nothing, but is the owner of one and target of neither.
	schemes := 0
	for _, event := range scan.Events {
		if event.Kind != EventScheme {
			continue
		}
		schemes++
		if event.Owner != 42 && event.Target != 42 {
			t.Errorf("a scheme naming neither end survived the filter: %+v", event)
		}
	}
	if schemes != 1 {
		t.Errorf("kept %d schemes, want the one 42 owns", schemes)
	}
	for _, event := range scan.Events {
		if event.Kind == EventTitleHistory && event.Holder != 42 {
			t.Errorf("a title event for another holder survived the filter: %+v", event)
		}
	}
	kinds := map[string]int{}
	for _, event := range scan.Events {
		kinds[event.Kind]++
	}
	// Three successions to 42, their own memory 5, and memory 6 which names
	// them as a participant.
	if kinds[EventTitleHistory] != 3 {
		t.Errorf("kept %d title events, want 3: %+v", kinds[EventTitleHistory], scan.Events)
	}
	if kinds[EventCharacterMemory] != 2 {
		t.Errorf("kept %d memories, want 2: %+v", kinds[EventCharacterMemory], scan.Events)
	}

	// A house relation reaches a character only through their own house, and
	// a struggle only through a catalyst that names them. Without that, one
	// person's timeline would carry every house feud and every world event.
	for _, event := range scan.Events {
		switch event.Kind {
		case EventHouseRelation:
			t.Errorf("a house relation for another house survived the filter: %+v", event)
		case EventStruggle:
			if event.Owner != 42 {
				t.Errorf("a world-scale struggle event survived the filter: %+v", event)
			}
		}
	}

	// A character the save does not mention gets an empty answer, not an error.
	empty, err := ScanTimeline(EncodingText, strings.NewReader(timelineFixture), nil,
		TimelineQuery{Character: 777}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning for an absent character: %v", err)
	}
	if len(empty.Events) != 0 {
		t.Errorf("an absent character collected %+v", empty.Events)
	}
	if empty.TitlesSeen == 0 {
		t.Error("the empty answer did not record that the save was examined")
	}
}

// TestTimelineWindowKeepsTheMostRecent covers the bias the cap used to have.
//
// Cutting during the walk kept whatever streamed first, which is always
// landed_titles: with a small cap the memories and schemes further down the
// file were never reached at all, and a save with 2195 successions reported
// no memories rather than reporting that it had 2026 of them.
func TestTimelineWindowKeepsTheMostRecent(t *testing.T) {
	scan, err := ScanTimeline(EncodingText, strings.NewReader(timelineFixture), nil,
		TimelineQuery{MaxEvents: 2}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if len(scan.Events) != 2 {
		t.Fatalf("kept %d events, want the cap of 2", len(scan.Events))
	}
	if scan.Events[0].Date != "1071.4.4" || scan.Events[1].Date != "1080.5.2" {
		t.Errorf("kept %s and %s, want the two most recent in chronological order",
			scan.Events[0].Date, scan.Events[1].Date)
	}
	if scan.Total != 15 {
		t.Errorf("total = %d, want the true 15", scan.Total)
	}
	// Every source stays visible through the totals even when the window
	// holds none of its events.
	want := map[string]int{
		EventTitleHistory: 4, EventCharacterMemory: 2, EventScheme: 2,
		EventCourtPosition: 2, EventVassalContract: 1, EventHouseRelation: 2, EventStruggle: 2,
	}
	for kind, count := range want {
		if scan.Totals[kind] != count {
			t.Errorf("totals[%s] = %d, want %d", kind, scan.Totals[kind], count)
		}
	}
	// The reason is stated once, not once per dropped event.
	if len(scan.Truncated) != 1 || scan.Truncated[0] != "events" {
		t.Errorf("truncated = %+v, want exactly one reason", scan.Truncated)
	}
}

// TestTimelineKindsSkipUnaskedBlocks pins that narrowing the kinds narrows
// the work: a block no kind asks for is skipped whole rather than parsed and
// filtered afterwards.
func TestTimelineKindsSkipUnaskedBlocks(t *testing.T) {
	scan, err := ScanTimeline(EncodingText, strings.NewReader(timelineFixture), nil,
		TimelineQuery{Kinds: []string{EventHouseRelation}}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	examined := scan.Examined()
	if examined[EventHouseRelation] != 1 {
		t.Errorf("examined %d house relations, want 1", examined[EventHouseRelation])
	}
	for _, kind := range TimelineKinds {
		if kind == EventHouseRelation {
			continue
		}
		if examined[kind] != 0 {
			t.Errorf("%s was examined when it was not asked for", kind)
		}
	}
	for _, event := range scan.Events {
		if event.Kind != EventHouseRelation {
			t.Errorf("an unasked kind was collected: %+v", event)
		}
	}
	if scan.Total != 2 {
		t.Errorf("total = %d, want the 2 house relation changes", scan.Total)
	}
}

func TestTimelineSourcesAreSelectable(t *testing.T) {
	titles, err := ScanTimeline(EncodingText, strings.NewReader(timelineFixture), nil,
		TimelineQuery{Kinds: []string{EventTitleHistory}}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning titles: %v", err)
	}
	if titles.MemoriesSeen != 0 {
		t.Errorf("the memory database was read when it was not asked for")
	}
	for _, event := range titles.Events {
		if event.Kind != EventTitleHistory {
			t.Errorf("a non-title event appeared: %+v", event)
		}
	}
	memories, err := ScanTimeline(EncodingText, strings.NewReader(timelineFixture), nil,
		TimelineQuery{Kinds: []string{EventCharacterMemory}}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning memories: %v", err)
	}
	if memories.TitlesSeen != 0 {
		t.Errorf("landed_titles was read when it was not asked for")
	}
}

func TestCompareSaveDates(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"1066.9.1", "1066.10.1", -1},
		{"1066.10.1", "1066.9.1", 1},
		{"1066.10.1", "1066.10.1", 0},
		{"1066.10.1", "1066.10.1.12", -1},
		{"999.1.1", "1000.1.1", -1},
		{"", "1066.1.1", -1},
	}
	for _, testCase := range cases {
		if got := compareSaveDates(testCase.left, testCase.right); got != testCase.want {
			t.Errorf("compare(%q,%q) = %d, want %d",
				testCase.left, testCase.right, got, testCase.want)
		}
	}
}
