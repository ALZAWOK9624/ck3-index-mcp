package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"ck3-index/internal/savefile"
)

// saveCardResult answers "what is this save" from the metadata section alone.
type saveCardResult struct {
	Save          saveFileReport `json:"save"`
	Version       string         `json:"version,omitempty"`
	Date          string         `json:"date,omitempty"`
	RealDate      string         `json:"real_date,omitempty"`
	PlayerName    string         `json:"player_name,omitempty"`
	TitleName     string         `json:"title_name,omitempty"`
	PlayerTier    uint64         `json:"player_tier,omitempty"`
	HouseName     string         `json:"house_name,omitempty"`
	Government    string         `json:"government,omitempty"`
	PlayerCount   uint64         `json:"number_of_players,omitempty"`
	Achievements  *bool          `json:"achievements_enabled,omitempty"`
	ModCount      int            `json:"mod_count"`
	DLCCount      int            `json:"dlc_count"`
	GameRuleCount int            `json:"game_rule_count"`
}

// saveCompatibilityResult reports exactly what the save declares about the
// content it needs. It deliberately draws no conclusion about whether a given
// server can load it: the save names Workshop mod files, the index is
// configured with local directories, and mapping one onto the other without
// reading mod descriptors would produce confident wrong answers.
type saveCompatibilityResult struct {
	Save            saveFileReport `json:"save"`
	Version         string         `json:"version,omitempty"`
	SaveGameVersion uint64         `json:"save_game_version,omitempty"`
	Mods            []string       `json:"mods"`
	DLCs            []string       `json:"dlcs"`
	GameRules       []string       `json:"game_rules"`
	Truncated       []string       `json:"truncated,omitempty"`
	Comparison      string         `json:"comparison"`
}

// saveFileReport describes the file itself and how confidently it was read.
type saveFileReport struct {
	Name               string            `json:"name"`
	Bytes              int64             `json:"bytes"`
	Layout             string            `json:"layout"`
	MetadataBytes      int               `json:"metadata_bytes"`
	ArchiveEntries     []string          `json:"archive_entries,omitempty"`
	TokenMapCoverage   savefile.Coverage `json:"token_map_coverage"`
	GamestateInspected bool              `json:"gamestate_inspected"`
}

func handleSave(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3SaveArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	operation := strings.ToLower(strings.TrimSpace(args.Operation))
	if operation == "" {
		operation = "card"
	}
	switch operation {
	case "card", "compatibility":
	case "audit":
		return handleSaveAudit(ctx, runtime, args)
	case "character":
		return handleSaveCharacter(ctx, runtime, args)
	default:
		return toolOutput{}, unknownOperation(operation)
	}
	if strings.TrimSpace(args.Path) == "" {
		return toolOutput{}, missingArgument("path")
	}

	metadata, report, err := readSaveMetadata(runtime, args.Path)
	if err != nil {
		return toolOutput{}, err
	}

	if operation == "compatibility" {
		return toolOutput{Value: saveCompatibilityResult{
			Save:            report,
			Version:         metadata.Version,
			SaveGameVersion: metadata.SaveGameVersion,
			Mods:            metadata.Mods,
			DLCs:            metadata.DLCs,
			GameRules:       metadata.GameRules,
			Truncated:       metadata.Truncated,
			Comparison: "This is what the save declares. Compare it against the server's own " +
				"mod set; ck3_save does not decide whether the save will load.",
		}}, nil
	}
	return toolOutput{Value: saveCardResult{
		Save:          report,
		Version:       metadata.Version,
		Date:          metadata.Date,
		RealDate:      metadata.RealDate,
		PlayerName:    metadata.PlayerName,
		TitleName:     metadata.TitleName,
		PlayerTier:    metadata.PlayerTier,
		HouseName:     metadata.HouseName,
		Government:    metadata.Government,
		PlayerCount:   metadata.NumberOfPlayers,
		Achievements:  metadata.AchievementsEnabled,
		ModCount:      len(metadata.Mods),
		DLCCount:      len(metadata.DLCs),
		GameRuleCount: len(metadata.GameRules),
	}}, nil
}

// readSaveMetadata resolves, bounds, and decodes one save's metadata section.
//
// Only the header and the metadata are read; the gamestate is never touched,
// which is what keeps this cheap enough for the shared read task class.
func readSaveMetadata(runtime *Runtime, requested string) (*savefile.Metadata, saveFileReport, error) {
	cfg := runtime.Config
	if len(cfg.SaveRoots) == 0 {
		return nil, saveFileReport{}, newToolError(ErrorSaveUnavailable, "unavailable",
			"reading save files is not configured", false,
			map[string]any{"setting": "save_roots"},
			map[string]any{"guidance": "Configure save_roots and save_token_map_root, then restart the server."})
	}

	path, err := savefile.ResolveUnderRoots(cfg.SaveRoots, requested)
	if err != nil {
		return nil, saveFileReport{}, saveToolError(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, saveFileReport{}, newToolError(ErrorSaveNotFound, "invalid_arguments",
			"the requested save could not be opened", false,
			map[string]any{"field": "path"},
			map[string]any{"guidance": "Name a save that exists inside a configured save root."})
	}

	limits := savefile.DefaultLimits()
	if cfg.SaveMaxBytes > 0 {
		limits.MaxFileBytes = cfg.SaveMaxBytes
	}
	if info.Size() > limits.MaxFileBytes {
		return nil, saveFileReport{}, newToolError(ErrorSaveUnreadable, "invalid_arguments",
			fmt.Sprintf("the save is %d bytes, over the configured %d-byte limit", info.Size(), limits.MaxFileBytes),
			false, map[string]any{"bytes": info.Size(), "max_bytes": limits.MaxFileBytes},
			map[string]any{"guidance": "Raise save_max_bytes only if this file really is a CK3 save."})
	}

	// The save is read through a random-access handle rather than loaded
	// whole: a card only needs the header and the metadata, and an uploaded
	// late-game save can be hundreds of megabytes.
	source, handle, err := savefile.Open(path)
	if err != nil {
		return nil, saveFileReport{}, newToolError(ErrorSaveNotFound, "invalid_arguments",
			"the requested save could not be read", false,
			map[string]any{"field": "path"}, nil)
	}
	defer handle.Close()

	envelope, err := savefile.Analyze(source, limits)
	if err != nil {
		return nil, saveFileReport{}, saveToolError(err)
	}
	section, err := envelope.Metadata(source, limits)
	if err != nil {
		return nil, saveFileReport{}, saveToolError(err)
	}

	var maps []*savefile.TokenMap
	if root := strings.TrimSpace(cfg.SaveTokenMapRoot); root != "" {
		maps, err = savefile.LoadTokenMaps(root)
		if err != nil {
			return nil, saveFileReport{}, saveToolError(err)
		}
	}
	metadata, err := savefile.ReadMetadata(section, maps, limits)
	if err != nil {
		return nil, saveFileReport{}, saveToolError(err)
	}

	entries, err := envelope.ArchiveEntryNames(source)
	if err != nil {
		return nil, saveFileReport{}, saveToolError(err)
	}
	report := saveFileReport{
		// Only the base name is echoed: the full path is server-side
		// configuration, not something a response should disclose.
		Name:               baseName(requested),
		Bytes:              info.Size(),
		Layout:             string(envelope.Layout),
		MetadataBytes:      len(section),
		ArchiveEntries:     entries,
		TokenMapCoverage:   metadata.Coverage,
		GamestateInspected: false,
	}
	return metadata, report, nil
}

func baseName(requested string) string {
	trimmed := strings.TrimRight(requested, "/\\")
	if index := strings.LastIndexAny(trimmed, "/\\"); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// saveToolError maps a typed savefile refusal onto the stable tool codes.
func saveToolError(err error) *ToolError {
	switch savefile.KindOf(err) {
	case savefile.ErrPath:
		return newToolError(ErrorSaveNotFound, "invalid_arguments", err.Error(), false,
			map[string]any{"field": "path"},
			map[string]any{"guidance": "Name a save inside a configured save root, without traversal or symlinks."})
	case savefile.ErrTokenMap:
		return newToolError(ErrorSaveTokenMapUnavailable, "unavailable", err.Error(), false,
			map[string]any{"setting": "save_token_map_root"},
			map[string]any{"guidance": "Add a token map generated for this CK3 version to save_token_map_root."})
	case savefile.ErrUnsupportedLayout, savefile.ErrContainerMismatch, savefile.ErrHeader,
		savefile.ErrBounds, savefile.ErrTruncated, savefile.ErrMalformedToken,
		savefile.ErrArchive, savefile.ErrTooLarge:
		return newToolError(ErrorSaveUnreadable, "invalid_arguments", err.Error(), false,
			map[string]any{"reason": string(savefile.KindOf(err))},
			map[string]any{"guidance": "Supply an uncorrupted binary CK3 save."})
	default:
		return newToolError(ErrorSaveUnreadable, "invalid_arguments", err.Error(), false, nil, nil)
	}
}
