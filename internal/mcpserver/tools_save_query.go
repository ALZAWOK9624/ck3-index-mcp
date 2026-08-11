package mcpserver

import (
	"context"
	"fmt"
	"strconv"
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
	// PlayedCharacter is the save id operation=character profiles by default.
	// The pass this audit already makes reads it, and it is the only id a
	// caller holding nothing but the file can start from.
	PlayedCharacter int64  `json:"played_character,omitempty"`
	Interpretation  string `json:"interpretation"`
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
	// RequestedCharacter is what the caller asked for, empty when the call
	// named no character and the played one answered it.
	RequestedCharacter string `json:"requested_character,omitempty"`
	ResolvedCharacter  int64  `json:"resolved_character"`
	// DefaultedToPlayed records that the id came from the save itself.
	DefaultedToPlayed  bool  `json:"defaulted_to_played,omitempty"`
	PlayedCharacter    int64 `json:"played_character,omitempty"`
	GamestateBytesRead int64 `json:"gamestate_bytes_read"`
	Passes             int   `json:"gamestate_passes"`
}

// saveDocumentResult is one bounded view of a position in the save.
//
// It is the answer to "what else is in here": a save carries far more than
// the four projections above read, and no fixed set of them can cover a
// format that changes every patch. Navigation costs one streaming pass and
// everything off the path costs only the tokens needed to skip it.
type saveDocumentResult struct {
	Save     saveFileReport     `json:"save"`
	Document *savefile.Document `json:"document"`
	Guidance string             `json:"guidance"`
}

// saveTimelineResult is the dated record of what the save says happened.
type saveTimelineResult struct {
	Save   saveFileReport           `json:"save"`
	Events []savefile.TimelineEvent `json:"events"`
	// Total counts every matching event, which max_events may have capped
	// Events below, and Totals breaks that down by source.
	Total  int            `json:"total"`
	Totals map[string]int `json:"totals_by_kind"`
	// Examined counts what each source block held, so an empty answer from a
	// source is distinguishable from a source that was not there.
	Examined       map[string]int `json:"examined_by_kind"`
	Character      int64          `json:"character,omitempty"`
	Truncated      []string       `json:"truncated,omitempty"`
	Interpretation string         `json:"interpretation"`
	// Unread names blocks a reader might expect here and why they are absent,
	// so a thin answer is not mistaken for a quiet save.
	Unread map[string]string `json:"unread_sources"`
}

func handleSaveDocument(_ context.Context, runtime *Runtime, args ck3SaveArgs) (toolOutput, error) {
	prepared, err := openSaveForQuery(runtime, args.Path)
	if err != nil {
		return toolOutput{}, err
	}
	defer prepared.close()

	path, err := savefile.ParseDocumentPath(args.DocumentPath)
	if err != nil {
		return toolOutput{}, newToolError(ErrorSaveUnreadable, "invalid_arguments", err.Error(), false,
			map[string]any{"field": "document_path"},
			map[string]any{"guidance": "Name a path as a.b.c, using a[2] to pick among repeated keys."})
	}
	bounds := savefile.DefaultDocumentLimits()
	depth := args.Depth
	if depth > bounds.MaxDepth {
		return toolOutput{}, invalidArgument("depth",
			fmt.Sprintf("depth must not exceed %d; read a deeper path instead of a deeper subtree", bounds.MaxDepth))
	}

	reader, err := prepared.envelope.GamestateReader(prepared.source, prepared.limits)
	if err != nil {
		return toolOutput{}, saveToolError(err)
	}
	defer reader.Close()
	document, err := savefile.ReadDocument(prepared.envelope.Encoding, reader, prepared.resolver,
		path, depth, prepared.limits, bounds)
	if err != nil {
		return toolOutput{}, saveToolError(err)
	}
	return toolOutput{Value: saveDocumentResult{
		Save:     prepared.report(nil),
		Document: document,
		Guidance: "children lists what is directly below this path and is always complete to its cap; " +
			"raise depth to materialise the subtree, or extend the path to walk further. A null inside " +
			"value means the node was not materialised at this depth, never that the save omits it.",
	}}, nil
}

func handleSaveTimeline(_ context.Context, runtime *Runtime, args ck3SaveArgs) (toolOutput, error) {
	prepared, err := openSaveForQuery(runtime, args.Path)
	if err != nil {
		return toolOutput{}, err
	}
	defer prepared.close()

	var character int64
	if requested := strings.TrimSpace(args.Character); requested != "" {
		if character, err = parseCharacterID(requested); err != nil {
			return toolOutput{}, err
		}
	}
	reader, err := prepared.envelope.GamestateReader(prepared.source, prepared.limits)
	if err != nil {
		return toolOutput{}, saveToolError(err)
	}
	defer reader.Close()
	kinds, err := parseTimelineKinds(args.EventKinds)
	if err != nil {
		return toolOutput{}, err
	}
	scan, err := savefile.ScanTimeline(prepared.envelope.Encoding, reader, prepared.resolver,
		savefile.TimelineQuery{
			Character: character, Kinds: kinds,
			MaxEvents: boundedTimelineLimit(args.MaxEvents),
		}, prepared.limits)
	if err != nil {
		return toolOutput{}, saveToolError(err)
	}
	result := saveTimelineResult{
		Save:      prepared.report(nil),
		Events:    scan.Events,
		Total:     scan.Total,
		Totals:    timelineTotals(scan),
		Examined:  scan.Examined(),
		Character: character,
		Truncated: scan.Truncated,
		Interpretation: "A save records a position, not a chronicle. These are every place it dates " +
			"anything: title succession, character memories, running schemes, court appointments, " +
			"vassal contracts, house relation changes, and struggles. The type on each event is the " +
			"save's own label; why it happened is a reading of that label, not something the save states. " +
			"house_relation events carry the save's own written reason, formatting codes and all.",
		Unread: map[string]string{
			"secrets, relations, opinions, council_task_manager, raid, armies": "read but not collected: " +
				"these carry no date at all, so they are standing facts rather than events and a " +
				"timeline entry for them would have to invent one. Read them with operation=document.",
			"wars": "not read: no save with an active war has been available to confirm the shape of " +
				"its dated fields, and guessing one would produce confident wrong dates; " +
				"read them with operation=document document_path=wars",
		},
	}
	if result.Events == nil {
		result.Events = []savefile.TimelineEvent{}
	}
	return toolOutput{Value: result}, nil
}

// boundedTimelineLimit is wider than the shared evidence limit because a
// chronology of eight entries is not one.
func boundedTimelineLimit(requested int) int {
	const fallback, max = 50, 500
	if requested <= 0 {
		return fallback
	}
	if requested > max {
		return max
	}
	return requested
}

// timelineTotals renders the per-kind counts, always naming every source that
// was read so one with nothing to report is still visibly present.
func timelineTotals(scan *savefile.TimelineScan) map[string]int {
	totals := make(map[string]int, len(savefile.TimelineKinds))
	for _, kind := range savefile.TimelineKinds {
		totals[kind] = 0
	}
	for kind, count := range scan.Totals {
		totals[kind] = count
	}
	return totals
}

// parseTimelineKinds validates the requested event kinds.
//
// An unknown kind is refused rather than ignored: silently collecting
// everything would answer a question the caller did not ask.
func parseTimelineKinds(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	kinds := make([]string, 0, len(requested))
	for _, raw := range requested {
		kind := strings.ToLower(strings.TrimSpace(raw))
		if kind == "" {
			continue
		}
		known := false
		for _, candidate := range savefile.TimelineKinds {
			if candidate == kind {
				known = true
				break
			}
		}
		if !known {
			return nil, invalidArgument("event_kinds",
				fmt.Sprintf("%q is not a timeline event kind; the kinds are %s",
					raw, strings.Join(savefile.TimelineKinds, ", ")))
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
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
	result.PlayedCharacter = scan.PlayedCharacter

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
	prepared, err := openSaveForQuery(runtime, args.Path)
	if err != nil {
		return toolOutput{}, err
	}
	defer prepared.close()

	// A caller that has only just received a save holds no character ids at
	// all: the metadata carries the player's display name, never their save
	// id. Defaulting to the save's own played character is what makes the
	// operation reachable from a bare upload.
	var (
		id        int64
		defaulted bool
		passes    int
		located   int64
	)
	if requested == "" {
		locate, err := prepared.scan(savefile.GamestateQuery{})
		if err != nil {
			return toolOutput{}, err
		}
		passes++
		located = locate.BytesRead
		if locate.PlayedCharacter == 0 {
			return toolOutput{}, newToolError(ErrorObjectNotFound, "not_found",
				"this save records no played character, so there is no default to profile", false,
				map[string]any{"field": "character"},
				map[string]any{"guidance": "Supply a character save id; ck3_save operation=audit reports the played character when the save names one."})
		}
		id, defaulted = locate.PlayedCharacter, true
	} else if id, err = parseCharacterID(requested); err != nil {
		return toolOutput{}, err
	}

	scan, err := prepared.scan(savefile.GamestateQuery{Character: id, TitlesHeldBy: id})
	if err != nil {
		return toolOutput{}, err
	}
	passes++
	scan.BytesRead += located
	if scan.Character == nil {
		return toolOutput{}, newToolError(ErrorObjectNotFound, "not_found",
			fmt.Sprintf("character %d is not in this save", id), false,
			map[string]any{"field": "character", "character": id},
			map[string]any{"guidance": "Omit character to profile the played character, or supply a save id that exists."})
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
			ResolvedCharacter:  id,
			DefaultedToPlayed:  defaulted,
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
	scan, err := savefile.ScanGamestateFor(p.envelope.Encoding, reader, p.resolver, query, p.limits)
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
		Encoding:           string(p.envelope.Encoding),
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
	metadata, err := savefile.ReadMetadataFor(envelope.Encoding, section, maps, limits)
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
	// A text save names its own fields, so it is navigable with no map at all.
	if prepared.resolver == nil && envelope.Encoding != savefile.EncodingText {
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
	id, err := strconv.ParseInt(strings.TrimSpace(requested), 10, 64)
	if err != nil || id <= 0 {
		return 0, invalidArgument("character",
			"character must be a positive save id; names are not resolvable from the save alone because it stores name keys, not display text")
	}
	return id, nil
}
