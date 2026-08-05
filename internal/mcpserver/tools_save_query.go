package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"ck3-index/internal/savefile"
)

// saveAuditFinding is one id a save references that the index cannot account
// for. It names the object type so the reader knows what to go look for.
type saveAuditFinding struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Where says which part of the save carried the id.
	Where string `json:"where"`
}

// saveAuditResult reports what a save asks for against what the mod defines.
type saveAuditResult struct {
	Save    saveFileReport `json:"save"`
	Checked struct {
		Traits int `json:"traits"`
		Titles int `json:"titles"`
		Houses int `json:"houses"`
	} `json:"checked"`
	UndefinedCount int                `json:"undefined_count"`
	Findings       []saveAuditFinding `json:"findings"`
	Truncated      bool               `json:"truncated"`
	IndexedSymbols int                `json:"indexed_symbols"`
	Interpretation string             `json:"interpretation"`
}

// saveCharacterResult is the dossier a biography is written from.
//
// It carries decoded values and the raw script ids side by side: the ids are
// what a caller feeds back into ck3_search and ck3_inspect to pull the mod's
// own definitions and localization, which is where the setting lives.
type saveCharacterResult struct {
	Save      saveFileReport             `json:"save"`
	Character *savefile.CharacterRecord  `json:"character"`
	House     *savefile.HouseRecord      `json:"house,omitempty"`
	Titles    []savefile.TitleRecord     `json:"titles"`
	Undefined []saveAuditFinding         `json:"undefined,omitempty"`
	Lookup    saveCharacterLookupSummary `json:"lookup"`
	NextSteps []string                   `json:"next_steps"`
}

type saveCharacterLookupSummary struct {
	RequestedCharacter string `json:"requested_character"`
	PlayedCharacter    int64  `json:"played_character,omitempty"`
	GamestateBytesRead int64  `json:"gamestate_bytes_read"`
	Passes             int    `json:"gamestate_passes"`
}

func handleSaveAudit(ctx context.Context, runtime *Runtime, args ck3SaveArgs) (toolOutput, error) {
	prepared, err := openSaveForQuery(runtime, args.Path)
	if err != nil {
		return toolOutput{}, err
	}
	defer prepared.close()

	scan, err := prepared.scan(savefile.GamestateQuery{Inventory: true})
	if err != nil {
		return toolOutput{}, err
	}

	symbols, err := runtime.DB.ActiveSymbols(ctx)
	if err != nil {
		return toolOutput{}, newToolError(ErrorInternal, "internal",
			"the indexed symbol set could not be loaded", true, nil,
			map[string]any{"guidance": "Run ck3_refresh status to check the index, then retry."})
	}

	// An empty index would mark every id in the save as undefined, which is
	// a confident falsehood rather than a finding. Refuse instead.
	if symbols.Size() == 0 {
		return toolOutput{}, newToolError(ErrorIndexNotReady, "unavailable",
			"the index defines no objects, so every id in the save would be reported as undefined", false,
			map[string]any{"indexed_symbols": 0},
			map[string]any{"guidance": "Run ck3_refresh full, then retry the audit."})
	}

	limit := boundedMCPResultLimit(args.Limit)
	result := saveAuditResult{Save: prepared.report(scan)}
	result.Checked.Traits = len(scan.TraitsLookup)
	result.Checked.Titles = len(scan.TitleKeys)
	result.Checked.Houses = len(scan.HouseNameKeys)
	result.IndexedSymbols = symbols.Size()

	// The three vocabularies a save carries in full, cheaply: every trait it
	// can name, every title it holds, every house it created.
	for _, group := range []struct {
		kind  string
		where string
		ids   []string
	}{
		{"trait", "traits_lookup", scan.TraitsLookup},
		{"title", "landed_titles", scan.TitleKeys},
		{"dynasty_house", "dynasties", scan.HouseNameKeys},
	} {
		for _, id := range group.ids {
			if symbols.Defined(group.kind, id) {
				continue
			}
			result.UndefinedCount++
			if len(result.Findings) >= limit {
				result.Truncated = true
				continue
			}
			result.Findings = append(result.Findings, saveAuditFinding{
				Kind: group.kind, ID: id, Where: group.where,
			})
		}
	}

	result.Interpretation = "Each finding is an id this save carries that no active indexed source defines. " +
		"That is expected for vanilla content when only a Mod is indexed, and it is a real defect when the " +
		"id belongs to content this Mod is supposed to provide. Use ck3_inspect on an id to see whether it " +
		"was renamed or removed."
	return toolOutput{Value: result}, nil
}

func handleSaveCharacter(ctx context.Context, runtime *Runtime, args ck3SaveArgs) (toolOutput, error) {
	requested := strings.TrimSpace(args.Character)
	if requested == "" {
		return toolOutput{}, missingArgument("character")
	}
	prepared, err := openSaveForQuery(runtime, args.Path)
	if err != nil {
		return toolOutput{}, err
	}
	defer prepared.close()

	id, err := parseCharacterID(requested)
	if err != nil {
		return toolOutput{}, err
	}

	passes := 1
	scan, err := prepared.scan(savefile.GamestateQuery{Character: id, TitlesHeldBy: id})
	if err != nil {
		return toolOutput{}, err
	}
	if scan.Character == nil {
		return toolOutput{}, newToolError(ErrorObjectNotFound, "not_found",
			fmt.Sprintf("character %d is not in this save", id), false,
			map[string]any{"field": "character", "character": id},
			map[string]any{"guidance": "Use ck3_save operation=card to see the played character, or supply a save id that exists."})
	}

	// The stream passes dynasties before it reaches characters, so a house
	// can only be resolved once the character has named it.
	if scan.Character.HouseID != 0 {
		houses, err := prepared.scan(savefile.GamestateQuery{
			House: scan.Character.HouseID, HouseValid: true,
		})
		if err != nil {
			return toolOutput{}, err
		}
		passes++
		scan.House = houses.House
		scan.BytesRead += houses.BytesRead
	}

	symbols, err := runtime.DB.ActiveSymbols(ctx)
	if err != nil {
		symbols = nil
	}
	result := saveCharacterResult{
		Save:      prepared.report(scan),
		Character: scan.Character,
		House:     scan.House,
		Titles:    scan.Titles,
		Lookup: saveCharacterLookupSummary{
			RequestedCharacter: requested,
			PlayedCharacter:    scan.PlayedCharacter,
			GamestateBytesRead: scan.BytesRead,
			Passes:             passes,
		},
	}
	if result.Titles == nil {
		result.Titles = []savefile.TitleRecord{}
	}

	// The same resolution the audit does, narrowed to this character, so a
	// dossier shows a trait the Mod no longer defines rather than hiding it.
	if symbols != nil {
		for _, trait := range scan.Character.Traits {
			if !symbols.Defined("trait", trait) {
				result.Undefined = append(result.Undefined,
					saveAuditFinding{Kind: "trait", ID: trait, Where: "character.traits"})
			}
		}
		for _, title := range scan.Titles {
			if title.Key != "" && !symbols.Defined("title", title.Key) {
				result.Undefined = append(result.Undefined,
					saveAuditFinding{Kind: "title", ID: title.Key, Where: "character.titles"})
			}
		}
	}

	result.NextSteps = []string{
		"The save stores name and house as localization keys, not display text. Resolve first_name_key, " +
			"name_key and motto_key with ck3_inspect operation=localization.",
		"culture_id and faith_id are save-local numbers, not script ids. Read them as identity only; " +
			"the Mod's culture and faith definitions come from ck3_search.",
		"Dates are the save's own calendar. Apply whatever era offset this Mod uses; ck3_save does not.",
	}
	return toolOutput{Value: result}, nil
}

// preparedSave holds an opened save so several passes share one handle.
type preparedSave struct {
	source        savefile.Reader
	handle        interface{ Close() error }
	envelope      *savefile.Envelope
	resolver      *savefile.TokenMap
	coverage      savefile.Coverage
	limits        savefile.Limits
	name          string
	bytes         int64
	metadataBytes int
	entries       []string
}

func (p *preparedSave) close() {
	if p.handle != nil {
		p.handle.Close()
	}
}

// scan makes one bounded streaming pass over the gamestate.
func (p *preparedSave) scan(query savefile.GamestateQuery) (*savefile.GamestateScan, error) {
	reader, err := p.envelope.GamestateReader(p.source, p.limits)
	if err != nil {
		return nil, saveToolError(err)
	}
	defer reader.Close()
	scan, err := savefile.ScanGamestate(reader, p.resolver, query, p.limits)
	if err != nil {
		return nil, saveToolError(err)
	}
	return scan, nil
}

func (p *preparedSave) report(scan *savefile.GamestateScan) saveFileReport {
	return saveFileReport{
		Name:               p.name,
		Bytes:              p.bytes,
		Layout:             string(p.envelope.Layout),
		MetadataBytes:      p.metadataBytes,
		ArchiveEntries:     p.entries,
		TokenMapCoverage:   p.coverage,
		GamestateInspected: true,
	}
}

// openSaveForQuery resolves, bounds and opens a save, and picks the token map
// that covers it. The gamestate itself is not touched here.
func openSaveForQuery(runtime *Runtime, requested string) (*preparedSave, error) {
	cfg := runtime.Config
	if len(cfg.SaveRoots) == 0 {
		return nil, newToolError(ErrorSaveUnavailable, "unavailable",
			"reading save files is not configured", false,
			map[string]any{"setting": "save_roots"},
			map[string]any{"guidance": "Configure save_roots and save_token_map_root, then restart the server."})
	}
	if strings.TrimSpace(requested) == "" {
		return nil, missingArgument("path")
	}
	path, err := savefile.ResolveUnderRoots(cfg.SaveRoots, requested)
	if err != nil {
		return nil, saveToolError(err)
	}

	limits := savefile.DefaultLimits()
	if cfg.SaveMaxBytes > 0 {
		limits.MaxFileBytes = cfg.SaveMaxBytes
	}
	source, handle, err := savefile.Open(path)
	if err != nil {
		return nil, saveToolError(err)
	}
	prepared := &preparedSave{
		source: source, handle: handle, limits: limits,
		name: baseName(requested), bytes: source.Size,
	}
	envelope, err := savefile.Analyze(source, limits)
	if err != nil {
		prepared.close()
		return nil, saveToolError(err)
	}
	prepared.envelope = envelope

	// The token map is chosen by metadata coverage: the save's own version
	// field cannot pick it, because reading that field already needs a map.
	section, err := envelope.Metadata(source, limits)
	if err != nil {
		prepared.close()
		return nil, saveToolError(err)
	}
	var maps []*savefile.TokenMap
	if root := strings.TrimSpace(cfg.SaveTokenMapRoot); root != "" {
		maps, err = savefile.LoadTokenMaps(root)
		if err != nil {
			prepared.close()
			return nil, saveToolError(err)
		}
	}
	metadata, err := savefile.ReadMetadata(section, maps, limits)
	if err != nil {
		prepared.close()
		return nil, saveToolError(err)
	}
	prepared.coverage = metadata.Coverage
	prepared.metadataBytes = len(section)
	for _, candidate := range maps {
		if candidate.Label == metadata.Coverage.TokenMap {
			prepared.resolver = candidate
			break
		}
	}
	if prepared.resolver == nil {
		prepared.close()
		return nil, newToolError(ErrorSaveTokenMapUnavailable, "unavailable",
			"no token map covers this save's fields", false,
			map[string]any{"setting": "save_token_map_root"}, nil)
	}
	if entries, err := envelope.ArchiveEntryNames(source); err == nil {
		prepared.entries = entries
	}
	return prepared, nil
}

func parseCharacterID(requested string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(requested, "%d", &id); err != nil || id <= 0 {
		return 0, invalidArgument("character",
			"character must be a positive save id; names are not resolvable from the save alone because it stores name keys, not display text")
	}
	return id, nil
}
