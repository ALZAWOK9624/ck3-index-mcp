package indexer

import (
	"strconv"
	"strings"
	"testing"
)

func planFixture(t *testing.T) (MapSplitResult, MapSplitContext) {
	t.Helper()
	runs := rectRuns(0, 0, 39, 19)
	result, err := SplitProvinceGeometry(runs, MapSplitRequest{
		ProvinceID: 100,
		Seeds:      []MapSplitSeed{{X: 5, Y: 10, Name: "Old Town"}, {X: 34, Y: 10, Name: "New Ford"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context := MapSplitContext{
		UsedProvinceIDs: map[int]bool{100: true, 101: true, 102: true},
		UsedColors:      map[uint32]bool{0x102030: true},
		SourceName:      "Oldshire",
		SourceCulture:   "saxon",
		SourceReligion:  "catholic",
		SourceHolding:   "castle_holding",
		SourceCounty:    "c_oldshire",
	}
	return result, context
}

func TestPlanSplitAllocatesUnusedIdentities(t *testing.T) {
	result, context := planFixture(t)
	plan, err := PlanProvinceSplit(result, context, MapSplitEmit{Definition: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("unexpected blockers: %v", plan.Blockers)
	}
	if len(plan.NewProvinces) != 1 {
		t.Fatalf("a 2-way split should mint exactly 1 new province, got %d", len(plan.NewProvinces))
	}
	created := plan.NewProvinces[0]
	if context.UsedProvinceIDs[created.ID] {
		t.Fatalf("allocated province id %d is already in use", created.ID)
	}
	color := uint32(created.R)<<16 | uint32(created.G)<<8 | uint32(created.B)
	if context.UsedColors[color] {
		t.Fatalf("allocated colour %06x is already in use", color)
	}
	if color == 0 {
		t.Fatal("allocated pure black, which CK3 treats as undefined territory")
	}
	// The larger part must keep the original identity so existing history and
	// title entries stay attached to most of the land.
	if plan.RetainedPixels < created.PixelCount {
		t.Fatalf("retained part (%d px) is smaller than the new one (%d px)", plan.RetainedPixels, created.PixelCount)
	}
}

// TestPlanSplitNeverReusesAColour is the property that matters most: two
// regions sharing an RGB silently merge into one province at load time, which
// is far harder to notice than a hard error.
func TestPlanSplitNeverReusesAColour(t *testing.T) {
	runs := rectRuns(0, 0, 99, 19)
	seeds := []MapSplitSeed{}
	for i := 0; i < 8; i++ {
		seeds = append(seeds, MapSplitSeed{X: 6 + i*12, Y: 10})
	}
	result, err := SplitProvinceGeometry(runs, MapSplitRequest{ProvinceID: 55, Seeds: seeds}, nil)
	if err != nil {
		t.Fatal(err)
	}
	used := map[uint32]bool{}
	// Pre-occupy the colours the allocator would naturally reach first, forcing
	// it to keep searching rather than collide.
	cursor := uint32(55*colorStride) & 0xFFFFFF
	for i := 0; i < 5; i++ {
		used[cursor] = true
		cursor = (cursor + colorStride) & 0xFFFFFF
	}
	plan, err := PlanProvinceSplit(result, MapSplitContext{
		UsedProvinceIDs: map[int]bool{55: true},
		UsedColors:      used,
		SourceName:      "Wide",
		SourceCounty:    "c_wide",
	}, MapSplitEmit{Definition: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint32]bool{}
	seenIDs := map[int]bool{}
	for _, province := range plan.NewProvinces {
		color := uint32(province.R)<<16 | uint32(province.G)<<8 | uint32(province.B)
		if used[color] {
			t.Fatalf("province %d reused an occupied colour %06x", province.ID, color)
		}
		if seen[color] {
			t.Fatalf("colour %06x allocated twice within one plan", color)
		}
		if seenIDs[province.ID] {
			t.Fatalf("id %d allocated twice within one plan", province.ID)
		}
		seen[color], seenIDs[province.ID] = true, true
	}
	if len(plan.NewProvinces) != len(seeds)-1 {
		t.Fatalf("expected %d new provinces, got %d", len(seeds)-1, len(plan.NewProvinces))
	}
}

func TestPlanSplitEmitsOnlyRequestedFiles(t *testing.T) {
	result, context := planFixture(t)
	for _, tc := range []struct {
		name  string
		emit  MapSplitEmit
		want  []string
		avoid []string
	}{
		{"geometry only", MapSplitEmit{}, nil, []string{"definition.csv", "history/provinces", "landed_titles"}},
		{"definition only", MapSplitEmit{Definition: true}, []string{"map_data/definition.csv"}, []string{"history/provinces", "landed_titles"}},
		{"full set", MapSplitEmit{Definition: true, History: true, LandedTitles: true},
			[]string{"map_data/definition.csv", "history/provinces", "common/landed_titles"}, nil},
	} {
		plan, err := PlanProvinceSplit(result, context, tc.emit)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		joined := ""
		for _, file := range plan.Files {
			joined += file.Path + "\n"
		}
		for _, want := range tc.want {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s: missing %s in %q", tc.name, want, joined)
			}
		}
		for _, avoid := range tc.avoid {
			if strings.Contains(joined, avoid) {
				t.Fatalf("%s: unexpectedly emitted %s", tc.name, avoid)
			}
		}
	}
}

func TestPlanSplitContentIsUsable(t *testing.T) {
	result, context := planFixture(t)
	plan, err := PlanProvinceSplit(result, context, MapSplitEmit{Definition: true, History: true, LandedTitles: true})
	if err != nil {
		t.Fatal(err)
	}
	created := plan.NewProvinces[0]
	for _, file := range plan.Files {
		switch {
		case strings.HasSuffix(file.Path, "definition.csv"):
			// CK3 parses this as semicolon-separated id;r;g;b;name.
			row := strings.TrimSpace(file.Content)
			fields := strings.Split(row, ";")
			if len(fields) < 5 {
				t.Fatalf("definition row has %d fields: %q", len(fields), row)
			}
			if fields[0] != strconv.Itoa(created.ID) {
				t.Fatalf("definition row id %q does not match the planned province %d", fields[0], created.ID)
			}
		case strings.Contains(file.Path, "history/provinces"):
			if !strings.Contains(file.Content, "culture = saxon") || !strings.Contains(file.Content, "religion = catholic") {
				t.Fatalf("history did not inherit the source province's values: %q", file.Content)
			}
		case strings.Contains(file.Path, "landed_titles"):
			if !strings.Contains(file.Content, created.Barony) || !strings.Contains(file.Content, "c_oldshire") {
				t.Fatalf("barony entry missing key or owning county: %q", file.Content)
			}
			// landed_titles is a nested tree; a blind append would place the
			// barony at top level and detach it from its county.
			if file.Mode != "manual" {
				t.Fatalf("landed_titles edit mode is %q, expected manual placement", file.Mode)
			}
		}
	}
}

// TestPlanSplitBlocksWhenUniquenessCannotBeProven covers the refusal path: with
// no knowledge of existing ids or colours the planner cannot guarantee it is
// not colliding, and guessing would corrupt the map.
func TestPlanSplitBlocksWhenUniquenessCannotBeProven(t *testing.T) {
	result, _ := planFixture(t)
	plan, err := PlanProvinceSplit(result, MapSplitContext{SourceName: "X"}, MapSplitEmit{Definition: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) < 2 {
		t.Fatalf("expected blockers for unknown ids and colours, got %v", plan.Blockers)
	}
}

// TestPlanSplitAcceptsFragmentedProvince pins the behaviour that a province
// painted in several pieces is planned, not refused. Islands and exclaves are
// how real maps are built -- 987 playable provinces in this workspace are
// fragmented -- so blocking on them would reject ordinary input.
func TestPlanSplitAcceptsFragmentedProvince(t *testing.T) {
	runs := append(rectRuns(0, 0, 9, 9), rectRuns(40, 0, 49, 9)...)
	result, err := SplitProvinceGeometry(runs, MapSplitRequest{
		ProvinceID: 9, Seeds: []MapSplitSeed{{X: 2, Y: 2}, {X: 7, Y: 7}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanProvinceSplit(result, MapSplitContext{
		UsedProvinceIDs: map[int]bool{9: true}, UsedColors: map[uint32]bool{1: true},
		SourceName: "Split", SourceColor: 0x445566,
	}, MapSplitEmit{Definition: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("a fragmented province was blocked: %v", plan.Blockers)
	}
	// It must still be reported, because which part inherits an island is a
	// judgement the caller may want to override with an extra seed.
	found := false
	for _, warning := range plan.Warnings {
		if strings.Contains(warning, "disconnected piece") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the attached island was not reported: %v", plan.Warnings)
	}
}

// TestPlanSplitBlocksTrulyUnattachedPixels keeps the defensive path honest: if
// geometry ever reaches the planner with pixels belonging to no part, applying
// it would erase them from provinces.png, so it must still refuse.
func TestPlanSplitBlocksTrulyUnattachedPixels(t *testing.T) {
	result := MapSplitResult{
		ProvinceID: 9, SourcePixel: 200, Unreachable: 100,
		Parts: []MapSplitPart{
			{Index: 0, PixelCount: 60, Runs: rectRuns(0, 0, 5, 9)},
			{Index: 1, PixelCount: 40, Runs: rectRuns(6, 0, 9, 9)},
		},
	}
	plan, err := PlanProvinceSplit(result, MapSplitContext{
		UsedProvinceIDs: map[int]bool{9: true}, UsedColors: map[uint32]bool{1: true},
		SourceName: "Split", SourceColor: 0x445566,
	}, MapSplitEmit{Definition: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, blocker := range plan.Blockers {
		if strings.Contains(blocker, "erase") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unattached pixels did not block the plan: %v", plan.Blockers)
	}
}

func TestSanitizeTitleKey(t *testing.T) {
	for input, want := range map[string]string{
		"New Ford":     "new_ford",
		"  Öster Bay ": "ster_bay",
		"东部平原":         "split",
		"a--b":         "a_b",
	} {
		if got := sanitizeTitleKey(input); got != want {
			t.Fatalf("sanitizeTitleKey(%q) = %q, want %q", input, got, want)
		}
	}
}
