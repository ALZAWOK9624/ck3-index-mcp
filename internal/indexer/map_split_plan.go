package indexer

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Turning split geometry into a usable mod change is the part a standalone
// image editor cannot do. Recolouring provinces.png is only the visible step;
// CK3 also needs a definition row per new colour, a history entry per new
// province, and a barony to hold it. Miss one and the map loads with a province
// that has no owner, no culture, or -- worst -- silently merges into its
// neighbour because two regions share an RGB.
//
// The planner therefore emits every downstream edit together, and refuses
// outright when it cannot guarantee uniqueness.

// MapSplitEmit selects which downstream files a plan covers. Splitting only
// the pixel geometry is legitimate when the modder maintains the rest by hand,
// so this is opt-in per file rather than all-or-nothing.
type MapSplitEmit struct {
	Definition   bool `json:"definition"`
	History      bool `json:"history"`
	LandedTitles bool `json:"landed_titles"`
}

// MapSplitContext supplies the workspace facts a plan needs in order to stay
// unique. The caller fills it from the index, which keeps the planner a pure
// function and therefore testable without a database.
type MapSplitContext struct {
	UsedProvinceIDs map[int]bool
	UsedColors      map[uint32]bool
	// SourceName, SourceCulture, SourceReligion and SourceHolding are inherited
	// by every new province: a split part is the same land as its parent, so
	// starting from the parent's values is the only defensible default.
	SourceName     string
	SourceCulture  string
	SourceReligion string
	SourceHolding  string
	// SourceCounty is the landed title that owns the province being split. New
	// baronies must join it, or the split land leaves its de jure county.
	SourceCounty string
	// SourceColor is the province's indexed RGB. It is the reference the image
	// step checks pixels against, and it has to come from the index rather than
	// from the image being edited: a check that reads its expectation out of the
	// thing it is checking cannot detect that the thing has changed.
	SourceColor      uint32
	RetainX          int
	RetainY          int
	HasRetainPoint   bool
	ExistingTitleIDs map[string]bool `json:"-"`
}

// MapSplitNewProvince is one province that did not exist before the split.
type MapSplitNewProvince struct {
	PartIndex  int    `json:"part_index"`
	ID         int    `json:"id"`
	R          int    `json:"r"`
	G          int    `json:"g"`
	B          int    `json:"b"`
	Name       string `json:"name"`
	Barony     string `json:"barony,omitempty"`
	PixelCount int    `json:"pixel_count"`
}

// MapSplitFileEdit is a proposed change to one file. Content is the text to add;
// nothing is written here.
type MapSplitFileEdit struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Content string `json:"content"`
	Note    string `json:"note,omitempty"`
}

// MapSplitPlan is the reviewable output: what the split produces, what must be
// edited, and what stops it from being applied.
type MapSplitPlan struct {
	ProvinceID          int                   `json:"province_id"`
	RetainedPart        int                   `json:"retained_part_index"`
	RetainedPixels      int                   `json:"retained_pixel_count"`
	SourceColor         uint32                `json:"source_color"`
	NewProvinces        []MapSplitNewProvince `json:"new_provinces"`
	Files               []MapSplitFileEdit    `json:"files"`
	Blockers            []string              `json:"blockers,omitempty"`
	Warnings            []string              `json:"warnings,omitempty"`
	MissingDependencies []string              `json:"missing_dependencies,omitempty"`
}

// colorStride is odd and therefore coprime with 2^24, so repeatedly adding it
// visits every RGB value before repeating. Using a large stride near the golden
// ratio of the space also keeps consecutive allocations far apart in colour, so
// a modder eyeballing provinces.png can tell new neighbours apart.
const colorStride = 0x9E3779

// PlanProvinceSplit converts split geometry into the downstream edits CK3 needs.
// The largest part keeps the original province id and colour so the existing
// definition, history and title entries stay valid and only the remaining parts
// need new identities.
func PlanProvinceSplit(result MapSplitResult, context MapSplitContext, emit MapSplitEmit) (MapSplitPlan, error) {
	if len(result.Parts) < 2 {
		return MapSplitPlan{}, fmt.Errorf("a split plan needs at least 2 parts, got %d", len(result.Parts))
	}
	plan := MapSplitPlan{
		ProvinceID:  result.ProvinceID,
		SourceColor: context.SourceColor & 0xFFFFFF,
		Warnings:    append([]string(nil), result.Warnings...),
	}

	retained := -1
	if result.RetainSeed != nil {
		for i, part := range result.Parts {
			if part.Index == *result.RetainSeed {
				retained = i
				break
			}
		}
		if retained < 0 {
			return MapSplitPlan{}, fmt.Errorf("retain_seed %d does not identify a split part", *result.RetainSeed)
		}
	} else if context.HasRetainPoint {
		for i, part := range result.Parts {
			if mapSplitPartContains(part, context.RetainX, context.RetainY) {
				retained = i
				break
			}
		}
		if retained < 0 {
			plan.Warnings = append(plan.Warnings, "the indexed holding locator did not land inside any result part; the largest part retained the original identity")
		}
	}
	if retained < 0 {
		// With no explicit choice or holding locator, retaining the largest part
		// minimises raster churn but is reported as a fallback, not silently
		// treated as settlement semantics.
		retained = 0
		for i, part := range result.Parts {
			if part.PixelCount > result.Parts[retained].PixelCount {
				retained = i
			}
		}
		plan.Warnings = append(plan.Warnings, "no retain_seed or indexed holding locator was available; the largest part retained the original identity")
	}
	plan.RetainedPart = retained
	plan.RetainedPixels = result.Parts[retained].PixelCount

	// Only pixels that could not be attached at all are fatal. A disconnected
	// piece that was attached to its nearest seed is not: provinces painted in
	// several pieces are ordinary in CK3, and refusing them would reject most of
	// the map's islands and exclaves.
	if result.Unreachable > 0 {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf(
			"%d pixel(s) belong to no part; applying this would erase them from provinces.png", result.Unreachable))
	}

	usedIDs := map[int]bool{}
	for id := range context.UsedProvinceIDs {
		usedIDs[id] = true
	}
	usedColors := map[uint32]bool{}
	for color := range context.UsedColors {
		usedColors[color] = true
	}
	if len(usedIDs) == 0 {
		plan.Blockers = append(plan.Blockers,
			"no existing province ids were supplied, so a new id cannot be proven unique")
	}
	if len(usedColors) == 0 {
		plan.Blockers = append(plan.Blockers,
			"no existing province colours were supplied, so a new colour cannot be proven unique")
	}

	nextID := 1
	for id := range usedIDs {
		if id >= nextID {
			nextID = id + 1
		}
	}
	colorCursor := uint32(result.ProvinceID*colorStride) & 0xFFFFFF

	baseName := strings.TrimSpace(context.SourceName)
	if baseName == "" {
		baseName = fmt.Sprintf("province_%d", result.ProvinceID)
	}
	usedNames := map[string]bool{}
	usedBaronies := map[string]bool{}
	for id := range context.ExistingTitleIDs {
		usedBaronies[strings.ToLower(strings.TrimSpace(id))] = true
	}

	for i, part := range result.Parts {
		if i == retained {
			continue
		}
		if part.PixelCount == 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"part %d claimed no pixels and was left out of the plan", i))
			continue
		}
		color, ok := nextFreeColor(colorCursor, usedColors)
		if !ok {
			plan.Blockers = append(plan.Blockers, "the 24-bit province colour space is exhausted")
			break
		}
		colorCursor = (color + colorStride) & 0xFFFFFF
		usedColors[color] = true
		for usedIDs[nextID] {
			nextID++
		}
		id := nextID
		usedIDs[id] = true

		name := strings.TrimSpace(part.Seed.Name)
		if name == "" {
			name = fmt.Sprintf("%s_%d", baseName, i+1)
		}
		if !validProvinceDefinitionName(name) {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("part %d name contains a definition.csv delimiter or control character", part.Index))
			name = fmt.Sprintf("province_%d", id)
		}
		nameKey := strings.ToLower(name)
		if usedNames[nameKey] {
			original := name
			name = fmt.Sprintf("%s_%d", name, id)
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("duplicate province name %q was made unique as %q", original, name))
		}
		usedNames[strings.ToLower(name)] = true
		province := MapSplitNewProvince{
			PartIndex:  i,
			ID:         id,
			R:          int(color >> 16 & 0xFF),
			G:          int(color >> 8 & 0xFF),
			B:          int(color & 0xFF),
			Name:       name,
			PixelCount: part.PixelCount,
		}
		if emit.LandedTitles {
			baseKey := sanitizeTitleKey(name)
			barony := "b_" + baseKey
			if baseKey == "split" || usedBaronies[strings.ToLower(barony)] {
				barony = fmt.Sprintf("b_%s_%d", baseKey, id)
			}
			for suffix := 2; usedBaronies[strings.ToLower(barony)]; suffix++ {
				barony = fmt.Sprintf("b_%s_%d_%d", baseKey, id, suffix)
			}
			usedBaronies[strings.ToLower(barony)] = true
			province.Barony = barony
		}
		plan.NewProvinces = append(plan.NewProvinces, province)
	}

	if len(plan.NewProvinces) == 0 && len(plan.Blockers) == 0 {
		plan.Blockers = append(plan.Blockers, "the split produced no new provinces, so there is nothing to apply")
	}
	if !emit.Definition {
		plan.MissingDependencies = append(plan.MissingDependencies, "map_data/definition.csv rows for every new province")
	}
	if !emit.History {
		plan.MissingDependencies = append(plan.MissingDependencies, "history/provinces entries for every new province")
	}
	if !emit.LandedTitles {
		plan.MissingDependencies = append(plan.MissingDependencies, "landed-title ownership or another explicit province consumer for every new province")
	}

	if emit.Definition && len(plan.NewProvinces) > 0 {
		var rows strings.Builder
		for _, province := range plan.NewProvinces {
			fmt.Fprintf(&rows, "%d;%d;%d;%d;%s;x\n", province.ID, province.R, province.G, province.B, province.Name)
		}
		plan.Files = append(plan.Files, MapSplitFileEdit{
			Path: "map_data/definition.csv", Mode: "append", Content: rows.String(),
			Note: "one row per new province; the retained part keeps the original row",
		})
	}

	if emit.History && len(plan.NewProvinces) > 0 {
		culture, religion, holding := context.SourceCulture, context.SourceReligion, context.SourceHolding
		if culture == "" || religion == "" {
			plan.Warnings = append(plan.Warnings,
				"the source province has no indexed culture or religion, so history entries are emitted with placeholders that must be filled in")
		}
		if culture == "" {
			plan.MissingDependencies = append(plan.MissingDependencies, "source culture needed to replace CULTURE_TO_FILL")
		}
		if religion == "" {
			plan.MissingDependencies = append(plan.MissingDependencies, "source faith needed to replace RELIGION_TO_FILL")
		}
		if holding == "" {
			holding = "none"
		}
		var entries strings.Builder
		for _, province := range plan.NewProvinces {
			fmt.Fprintf(&entries, "%d = {\n\tculture = %s\n\treligion = %s\n\tholding = %s\n}\n\n",
				province.ID, orPlaceholder(culture, "CULTURE_TO_FILL"), orPlaceholder(religion, "RELIGION_TO_FILL"), holding)
		}
		plan.Files = append(plan.Files, MapSplitFileEdit{
			Path: fmt.Sprintf("history/provinces/split_%d.txt", result.ProvinceID), Mode: "create",
			Content: entries.String(),
			Note:    "values inherited from the province being split; review before shipping",
		})
	}

	if emit.LandedTitles && len(plan.NewProvinces) > 0 {
		if strings.TrimSpace(context.SourceCounty) == "" {
			plan.Blockers = append(plan.Blockers,
				"the county that owns this province is unknown, so new baronies cannot be attached to the right de jure title")
			plan.MissingDependencies = append(plan.MissingDependencies, "owning county for the new baronies")
		} else {
			var baronies strings.Builder
			fmt.Fprintf(&baronies, "# Add inside %s:\n", context.SourceCounty)
			for _, province := range plan.NewProvinces {
				fmt.Fprintf(&baronies, "%s = {\n\tprovince = %d\n}\n", province.Barony, province.ID)
			}
			plan.Files = append(plan.Files, MapSplitFileEdit{
				Path: fmt.Sprintf("common/landed_titles/split_%d.txt", result.ProvinceID), Mode: "manual",
				Content: baronies.String(),
				Note:    "landed_titles is a nested tree, so these baronies must be pasted inside the owning county rather than appended",
			})
		}
	}

	sort.SliceStable(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	return plan, nil
}

func mapSplitPartContains(part MapSplitPart, x, y int) bool {
	for _, run := range part.Runs {
		if int(run.Y) == y && x >= int(run.X0) && x <= int(run.X1) {
			return true
		}
	}
	return false
}

func validProvinceDefinitionName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, ";\r\n") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// nextFreeColor scans forward from cursor for an unused RGB, skipping pure
// black because CK3 treats it as undefined territory.
func nextFreeColor(cursor uint32, used map[uint32]bool) (uint32, bool) {
	color := cursor & 0xFFFFFF
	for attempt := 0; attempt < 1<<24; attempt++ {
		if color != 0 && !used[color] {
			return color, true
		}
		color = (color + colorStride) & 0xFFFFFF
	}
	return 0, false
}

func orPlaceholder(value, placeholder string) string {
	if strings.TrimSpace(value) == "" {
		return placeholder
	}
	return value
}

// sanitizeTitleKey reduces a display name to the lowercase ASCII-ish form CK3
// title keys use, so a seed named from localisation still yields a valid key.
func sanitizeTitleKey(name string) string {
	var out strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && out.Len() > 0 {
				out.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	key := strings.Trim(out.String(), "_")
	if key == "" {
		key = "split"
	}
	return key
}
