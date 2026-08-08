package indexer

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

var regexpDefaultNamedList = regexp.MustCompile(`(?is)\b(\w+)\s*=\s*(?:LIST\s*)?\{([^}]*)\}`)

// mapContractDiagnostic is the shared, persisted form of a map invariant.
// Keep these diagnostics independent from the presentation-only map audit so
// scan, validate, diag_stats, and packaging all see the same facts.
type mapContractDiagnostic struct {
	Severity    string
	Code        string
	Message     string
	Source      string
	Path        string
	Line        int
	Occurrences int
}

type mapContractAggregate struct {
	Severity string
	Code     string
	File     activeMapFile
	Line     int
	Count    int
	Samples  []string
}

type mapValueOccurrence struct {
	File  activeMapFile
	Line  int
	Value string
}

type provinceHistoryKey struct {
	Province int
	Date     int
	Field    string
}

// collectBaseMapContractDiagnostics audits invariants that depend only on the
// active map files. Title-graph and county-history checks are appended later in
// rebuildMapCache once their semantic tables have been built.
func collectBaseMapContractDiagnostics(ctx context.Context, active map[string]activeMapFile) ([]mapContractDiagnostic, map[int]bool) {
	var out []mapContractDiagnostic
	definitions := active["map_data/definition.csv"]
	definedIDs := map[int]bool{}
	if definitions.Path != "" {
		ids, findings := auditDefinitionSequence(definitions)
		definedIDs = ids
		out = append(out, findings...)
	}

	assetResult := MapAssetAuditResult{Counts: map[string]int{}}
	if err := auditProvinceAssets(ctx, active, 8, &assetResult); err != nil {
		out = append(out, mapContractDiagnostic{
			Severity: "error", Code: "map_asset_audit_failed",
			Message: "province asset audit failed: " + err.Error(),
			Source:  definitions.Src.Name, Path: definitions.Rel, Occurrences: 1,
		})
	}
	if err := auditRiverAsset(ctx, active, 8, &assetResult); err != nil {
		rivers := active["map_data/rivers.png"]
		out = append(out, mapContractDiagnostic{
			Severity: "error", Code: "map_asset_audit_failed",
			Message: "river asset audit failed: " + err.Error(),
			Source:  rivers.Src.Name, Path: rivers.Rel, Occurrences: 1,
		})
	}
	for _, finding := range assetResult.Findings {
		message := finding.Message
		if len(finding.Samples) > 0 {
			message += "; samples: " + strings.Join(finding.Samples, ", ")
		}
		count := finding.Count
		if count < 1 {
			count = 1
		}
		out = append(out, mapContractDiagnostic{
			Severity: finding.Severity, Code: finding.Code, Message: message,
			Source: finding.Source, Path: finding.Path, Occurrences: count,
		})
	}

	out = append(out, auditDefaultMapContract(active["map_data/default.map"], definedIDs)...)
	out = append(out, auditProvinceTerrainContract(active, definedIDs)...)
	out = append(out, auditProvinceHistoryContract(active, definedIDs)...)
	out = append(out, auditAdjacencyContract(active["map_data/adjacencies.csv"], definedIDs)...)
	out = append(out, auditMapRegionContract(active, definedIDs)...)
	return out, definedIDs
}

func auditDefinitionSequence(file activeMapFile) (map[int]bool, []mapContractDiagnostic) {
	ids := map[int]bool{}
	f, err := os.Open(file.Path)
	if err != nil {
		return ids, []mapContractDiagnostic{{Severity: "error", Code: "map_definition_unreadable", Message: err.Error(), Source: file.Src.Name, Path: file.Rel, Occurrences: 1}}
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = ';'
	r.FieldsPerRecord = -1
	for {
		record, readErr := r.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil || len(record) == 0 {
			continue
		}
		id, parseErr := strconv.Atoi(strings.TrimSpace(record[0]))
		if parseErr == nil && id > 0 {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return ids, nil
	}
	maxID := 0
	for id := range ids {
		if id > maxID {
			maxID = id
		}
	}
	missing := make([]int, 0)
	for id := 1; id <= maxID; id++ {
		if !ids[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return ids, nil
	}
	samples := missing
	if len(samples) > 8 {
		samples = samples[:8]
	}
	return ids, []mapContractDiagnostic{{
		Severity: "error", Code: "map_definition_non_contiguous_ids", Source: file.Src.Name, Path: file.Rel, Line: 1,
		Message:     fmt.Sprintf("positive province ids must form a continuous 1..%d sequence; %d ids are missing; samples: %s", maxID, len(missing), joinInts(samples)),
		Occurrences: len(missing),
	}}
}

func auditDefaultMapContract(file activeMapFile, definedIDs map[int]bool) []mapContractDiagnostic {
	if file.Path == "" {
		return nil
	}
	data, err := os.ReadFile(file.Path)
	if err != nil {
		return []mapContractDiagnostic{{Severity: "error", Code: "map_default_unreadable", Message: err.Error(), Source: file.Src.Name, Path: file.Rel, Occurrences: 1}}
	}
	text := string(data)
	matches := regexpDefaultNamedList.FindAllStringSubmatchIndex(text, -1)
	seenFields := map[string]int{}
	duplicateLines := map[string][]int{}
	missingIDs := map[string][]int{}
	for _, match := range matches {
		if len(match) < 6 {
			continue
		}
		if mapContractInsideCommentOrString(text, match[0]) {
			continue
		}
		key := canonicalDefaultMapField(strings.ToLower(text[match[2]:match[3]]))
		if key == "" {
			continue
		}
		line := 1 + strings.Count(text[:match[0]], "\n")
		seenFields[key]++
		if seenFields[key] > 1 {
			duplicateLines[key] = append(duplicateLines[key], line)
		}
		for _, id := range mapContractNumericIDs(text[match[4]:match[5]]) {
			if id > 0 && len(definedIDs) > 0 && !definedIDs[id] {
				missingIDs[key] = append(missingIDs[key], id)
			}
		}
	}
	var out []mapContractDiagnostic
	fields := sortedStringKeys(duplicateLines)
	for _, key := range fields {
		lines := duplicateLines[key]
		out = append(out, mapContractDiagnostic{
			Severity: "error", Code: "duplicate_default_map_field", Source: file.Src.Name, Path: file.Rel, Line: lines[0],
			Message:     fmt.Sprintf("default.map defines singleton field %s %d times; merge all province ids into one list", key, seenFields[key]),
			Occurrences: len(lines),
		})
	}
	fields = sortedStringKeys(missingIDs)
	for _, key := range fields {
		ids := uniqueSortedInts(missingIDs[key])
		out = append(out, missingDefinitionDiagnostic(file, 1, "default.map "+key, ids))
	}
	return out
}

func mapContractInsideCommentOrString(text string, offset int) bool {
	quoted, escaped, comment := false, false, false
	for index := 0; index < offset && index < len(text); index++ {
		char := text[index]
		if comment {
			if char == '\n' {
				comment = false
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		if quoted && char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && char == '#' {
			comment = true
		}
	}
	return quoted || comment
}

func mapContractNumericIDs(text string) []int {
	clean := make([]byte, 0, len(text))
	quoted, escaped, comment := false, false, false
	for index := 0; index < len(text); index++ {
		char := text[index]
		if comment {
			if char == '\n' {
				comment = false
				clean = append(clean, ' ')
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		if quoted && char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			clean = append(clean, ' ')
			continue
		}
		if !quoted && char == '#' {
			comment = true
			clean = append(clean, ' ')
			continue
		}
		if quoted {
			continue
		}
		if char >= '0' && char <= '9' {
			clean = append(clean, char)
		} else {
			clean = append(clean, ' ')
		}
	}
	var out []int
	for _, token := range strings.Fields(string(clean)) {
		if id, err := strconv.Atoi(token); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func auditProvinceTerrainContract(active map[string]activeMapFile, definedIDs map[int]bool) []mapContractDiagnostic {
	seen := map[int]mapValueOccurrence{}
	aggregates := map[string]*mapContractAggregate{}
	missingByFile := map[string][]int{}
	filesByRel := map[string]activeMapFile{}
	for _, file := range activeFilesWithPrefix(active, "common/province_terrain/") {
		filesByRel[file.Rel] = file
		data, err := os.ReadFile(file.Path)
		if err != nil {
			addContractAggregate(aggregates, file, "province_terrain_unreadable", "error", 1, err.Error())
			continue
		}
		parsed := script.ParseBytes(data)
		for _, node := range parsed.Nodes {
			id, parseErr := strconv.Atoi(node.Key)
			if parseErr != nil || node.Kind != "atom" || node.Value == "" {
				continue
			}
			if len(definedIDs) > 0 && !definedIDs[id] {
				missingByFile[file.Rel] = append(missingByFile[file.Rel], id)
			}
			current := mapValueOccurrence{File: file, Line: node.Line, Value: node.Value}
			if previous, ok := seen[id]; ok {
				code, severity := "duplicate_province_terrain_assignment", "warning"
				if previous.Value != current.Value {
					code, severity = "conflicting_province_terrain_assignment", "error"
				}
				sample := fmt.Sprintf("%d: %s:%d=%s, %s:%d=%s", id, previous.File.Rel, previous.Line, previous.Value, current.File.Rel, current.Line, current.Value)
				addContractAggregate(aggregates, file, code, severity, node.Line, sample)
			}
			seen[id] = current
		}
	}
	out := aggregatesToDiagnostics(aggregates)
	for _, rel := range sortedStringKeys(missingByFile) {
		ids := uniqueSortedInts(missingByFile[rel])
		out = append(out, missingDefinitionDiagnostic(filesByRel[rel], 1, "province terrain", ids))
	}
	return out
}

func auditProvinceHistoryContract(active map[string]activeMapFile, definedIDs map[int]bool) []mapContractDiagnostic {
	blockSeen := map[int]mapValueOccurrence{}
	fieldSeen := map[provinceHistoryKey]mapValueOccurrence{}
	aggregates := map[string]*mapContractAggregate{}
	missingByFile := map[string][]int{}
	filesByRel := map[string]activeMapFile{}
	for _, file := range activeFilesWithPrefix(active, "history/provinces/") {
		filesByRel[file.Rel] = file
		data, err := os.ReadFile(file.Path)
		if err != nil {
			addContractAggregate(aggregates, file, "province_history_unreadable", "error", 1, err.Error())
			continue
		}
		parsed := script.ParseBytes(data)
		for _, node := range parsed.Nodes {
			id, parseErr := strconv.Atoi(node.Key)
			if parseErr != nil || node.Kind != "block" {
				continue
			}
			if len(definedIDs) > 0 && !definedIDs[id] {
				missingByFile[file.Rel] = append(missingByFile[file.Rel], id)
			}
			currentBlock := mapValueOccurrence{File: file, Line: node.Line}
			if previous, ok := blockSeen[id]; ok {
				sample := fmt.Sprintf("%d at %s:%d and %s:%d", id, previous.File.Rel, previous.Line, file.Rel, node.Line)
				addContractAggregate(aggregates, file, "duplicate_province_history_block", "error", node.Line, sample)
			}
			blockSeen[id] = currentBlock
			for _, field := range provinceHistoryFieldOccurrences(node) {
				key := provinceHistoryKey{Province: id, Date: field.Date, Field: field.Field}
				current := mapValueOccurrence{File: file, Line: field.Line, Value: field.Value}
				if previous, ok := fieldSeen[key]; ok {
					code, severity := "duplicate_province_history_field", "warning"
					if previous.Value != current.Value {
						code, severity = "conflicting_province_history_field", "error"
					}
					sample := fmt.Sprintf("%d/%d/%s: %s:%d=%s, %s:%d=%s", id, field.Date, field.Field, previous.File.Rel, previous.Line, previous.Value, file.Rel, field.Line, field.Value)
					addContractAggregate(aggregates, file, code, severity, field.Line, sample)
				}
				fieldSeen[key] = current
			}
		}
	}
	out := aggregatesToDiagnostics(aggregates)
	for _, rel := range sortedStringKeys(missingByFile) {
		ids := uniqueSortedInts(missingByFile[rel])
		out = append(out, missingDefinitionDiagnostic(filesByRel[rel], 1, "province history", ids))
	}
	return out
}

func auditAdjacencyContract(file activeMapFile, definedIDs map[int]bool) []mapContractDiagnostic {
	if file.Path == "" || len(definedIDs) == 0 {
		return nil
	}
	f, err := os.Open(file.Path)
	if err != nil {
		return []mapContractDiagnostic{{Severity: "error", Code: "map_adjacency_unreadable", Message: err.Error(), Source: file.Src.Name, Path: file.Rel, Occurrences: 1}}
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = ';'
	r.FieldsPerRecord = -1
	line, firstMissingLine := 0, 0
	var missing []int
	for {
		record, readErr := r.Read()
		if readErr == io.EOF {
			break
		}
		line++
		if readErr != nil || len(record) < 2 {
			continue
		}
		indexes := []int{0, 1}
		if len(record) > 3 {
			indexes = append(indexes, 3)
		}
		for _, index := range indexes {
			id, parseErr := strconv.Atoi(strings.TrimSpace(record[index]))
			if parseErr == nil && id > 0 && !definedIDs[id] {
				missing = append(missing, id)
				if firstMissingLine == 0 {
					firstMissingLine = line
				}
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []mapContractDiagnostic{missingDefinitionDiagnostic(file, firstMissingLine, "adjacencies.csv", uniqueSortedInts(missing))}
}

func auditMapRegionContract(active map[string]activeMapFile, definedIDs map[int]bool) []mapContractDiagnostic {
	if len(definedIDs) == 0 {
		return nil
	}
	var files []activeMapFile
	for _, file := range activeFilesWithPrefix(active, "map_data/geographical_regions/") {
		files = append(files, file)
	}
	for _, rel := range []string{"map_data/geographical_regions.txt", "map_data/island_region.txt", "map_data/climate.txt"} {
		if file := active[rel]; file.Path != "" {
			files = append(files, file)
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	var out []mapContractDiagnostic
	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			out = append(out, mapContractDiagnostic{Severity: "error", Code: "map_region_unreadable", Message: err.Error(), Source: file.Src.Name, Path: file.Rel, Occurrences: 1})
			continue
		}
		parsed := script.ParseBytes(data)
		var missing []int
		firstLine := 0
		var visit func([]*script.Node)
		visit = func(nodes []*script.Node) {
			for _, node := range nodes {
				checkValues := node.Key == "provinces" || file.Rel == "map_data/climate.txt" || file.Rel == "map_data/island_region.txt"
				if checkValues && node.Kind == "block" {
					for _, value := range listBlockValues(node) {
						id, parseErr := strconv.Atoi(value)
						if parseErr == nil && id > 0 && !definedIDs[id] {
							missing = append(missing, id)
							if firstLine == 0 {
								firstLine = node.Line
							}
						}
					}
				}
				visit(node.Children)
			}
		}
		visit(parsed.Nodes)
		if len(missing) > 0 {
			out = append(out, missingDefinitionDiagnostic(file, firstLine, "map region", uniqueSortedInts(missing)))
		}
	}
	return out
}

type provinceHistoryFieldOccurrence struct {
	Date  int
	Field string
	Value string
	Line  int
}

func provinceHistoryFieldOccurrences(root *script.Node) []provinceHistoryFieldOccurrence {
	wanted := map[string]bool{
		"culture": true, "religion": true, "holding": true, "buildings": true,
		"special_building": true, "special_building_slot": true,
		"duchy_building": true, "development": true, "development_level": true,
	}
	var out []provinceHistoryFieldOccurrence
	add := func(nodes []*script.Node, date int) {
		for _, node := range nodes {
			field := normalizeProvinceHistoryField(node.Key)
			if !wanted[field] {
				continue
			}
			value := node.Value
			if node.Kind == "block" {
				value = strings.Join(listBlockValues(node), " ")
			}
			if value != "" {
				out = append(out, provinceHistoryFieldOccurrence{Date: date, Field: field, Value: value, Line: node.Line})
			}
		}
	}
	add(root.Children, 0)
	for _, child := range root.Children {
		if child.Kind == "block" {
			if date, ok := parseDateKey(child.Key); ok {
				add(child.Children, date)
			}
		}
	}
	return out
}

func missingDefinitionDiagnostic(file activeMapFile, line int, consumer string, ids []int) mapContractDiagnostic {
	samples := ids
	if len(samples) > 8 {
		samples = samples[:8]
	}
	return mapContractDiagnostic{
		Severity: "error", Code: "province_reference_missing_definition", Source: file.Src.Name, Path: file.Rel, Line: line,
		Message:     fmt.Sprintf("%s references %d province ids absent from definition.csv; samples: %s", consumer, len(ids), joinInts(samples)),
		Occurrences: len(ids),
	}
}

func addContractAggregate(all map[string]*mapContractAggregate, file activeMapFile, code, severity string, line int, sample string) {
	key := file.Src.Name + "\x00" + file.Rel + "\x00" + code
	aggregate := all[key]
	if aggregate == nil {
		aggregate = &mapContractAggregate{Severity: severity, Code: code, File: file, Line: line}
		all[key] = aggregate
	}
	aggregate.Count++
	if aggregate.Line == 0 || line < aggregate.Line {
		aggregate.Line = line
	}
	if len(aggregate.Samples) < 8 && sample != "" {
		aggregate.Samples = append(aggregate.Samples, sample)
	}
}

func aggregatesToDiagnostics(all map[string]*mapContractAggregate) []mapContractDiagnostic {
	keys := sortedStringKeys(all)
	out := make([]mapContractDiagnostic, 0, len(keys))
	for _, key := range keys {
		aggregate := all[key]
		message := fmt.Sprintf("%s occurred %d times", strings.ReplaceAll(aggregate.Code, "_", " "), aggregate.Count)
		if len(aggregate.Samples) > 0 {
			message += "; samples: " + strings.Join(aggregate.Samples, "; ")
		}
		out = append(out, mapContractDiagnostic{
			Severity: aggregate.Severity, Code: aggregate.Code, Message: message,
			Source: aggregate.File.Src.Name, Path: aggregate.File.Rel, Line: aggregate.Line, Occurrences: aggregate.Count,
		})
	}
	return out
}

func replaceMapContractDiagnostics(ctx context.Context, tx *sql.Tx, diagnostics []mapContractDiagnostic) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics WHERE source='map-integrity'`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col,source_layer,confidence,fingerprint,occurrences)
		VALUES('map-integrity',?,?,?,?,?,?,0,?,'high',?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, diagnostic := range diagnostics {
		occurrences := diagnostic.Occurrences
		if occurrences < 1 {
			occurrences = 1
		}
		var fileID any
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM files WHERE source_name=? AND lower(rel_path)=lower(?) AND overridden=0 ORDER BY source_rank,id LIMIT 1`, diagnostic.Source, diagnostic.Path).Scan(&id)
		if err == nil {
			fileID = id
		} else if err != sql.ErrNoRows {
			return err
		}
		fingerprint := fmt.Sprintf("map-integrity:%s:%s:%s:%d", diagnostic.Code, diagnostic.Source, diagnostic.Path, diagnostic.Line)
		if _, err := stmt.ExecContext(ctx, diagnostic.Severity, diagnostic.Code, diagnostic.Message, fileID, diagnostic.Path, diagnostic.Line, diagnostic.Source, fingerprint, occurrences); err != nil {
			return err
		}
	}
	return nil
}

func titleMapContractDiagnostics(titles map[string]*mapTitleBuild, provinces map[int]*mapProvinceBuild, definedIDs map[int]bool, issues []MapIntegrityIssue) []mapContractDiagnostic {
	var out []mapContractDiagnostic
	for _, issue := range issues {
		// The ordinary title-integrity pass already owns these two diagnostics.
		if issue.Code == "duplicate_title_id" || issue.Code == "duplicate_barony_province" {
			continue
		}
		severity := issue.Severity
		if severity == "" {
			severity = "warning"
		}
		out = append(out, mapContractDiagnostic{
			Severity: severity, Code: issue.Code, Message: issue.Message,
			Source: issue.Source, Path: issue.Path, Line: issue.Line, Occurrences: 1,
		})
	}

	ordered := make([]*mapTitleBuild, 0, len(titles))
	for _, title := range titles {
		ordered = append(ordered, title)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	missingByFile := map[string][]int{}
	filesByRel := map[string]activeMapFile{}
	for _, title := range ordered {
		if title.CapitalTitle != "" {
			capital := titles[title.CapitalTitle]
			if capital == nil {
				out = append(out, mapContractDiagnostic{
					Severity: "error", Code: "invalid_title_capital_reference", Source: title.Source, Path: title.Rel, Line: title.Line,
					Message: fmt.Sprintf("title %s declares missing capital %s", title.ID, title.CapitalTitle), Occurrences: 1,
				})
			} else if title.Type == "c" && (capital.Type != "b" || capital.Parent != title.ID) {
				out = append(out, mapContractDiagnostic{
					Severity: "error", Code: "invalid_title_capital_reference", Source: title.Source, Path: title.Rel, Line: title.Line,
					Message: fmt.Sprintf("county %s declares capital %s, which is not its direct barony", title.ID, title.CapitalTitle), Occurrences: 1,
				})
			}
		}
		if title.Type != "b" || title.ProvinceID <= 0 || len(definedIDs) == 0 || definedIDs[title.ProvinceID] {
			continue
		}
		missingByFile[title.Rel] = append(missingByFile[title.Rel], title.ProvinceID)
		filesByRel[title.Rel] = activeMapFile{Rel: title.Rel, Src: Source{Name: title.Source}}
	}
	for _, rel := range sortedStringKeys(missingByFile) {
		ids := uniqueSortedInts(missingByFile[rel])
		out = append(out, missingDefinitionDiagnostic(filesByRel[rel], 1, "landed-title baronies", ids))
	}

	for _, title := range ordered {
		if title.Type != "c" || title.CapitalTitle == "" {
			continue
		}
		firstBarony := ""
		for _, childID := range title.Children {
			if child := titles[childID]; child != nil && child.Type == "b" {
				firstBarony = childID
				break
			}
		}
		if firstBarony != "" && firstBarony != title.CapitalTitle {
			out = append(out, mapContractDiagnostic{
				Severity: "warning", Code: "county_history_anchor_mismatch", Source: title.Source, Path: title.Rel, Line: title.Line,
				Message: fmt.Sprintf("county %s declares capital %s, but CK3 province-history coloring uses first direct barony %s", title.ID, title.CapitalTitle, firstBarony), Occurrences: 1,
			})
		}
	}

	// A definition may exist without a raster component. The asset audit owns
	// that global finding; this narrower message points at a playable barony.
	for _, title := range ordered {
		if title.Type != "b" || title.ProvinceID <= 0 || !definedIDs[title.ProvinceID] {
			continue
		}
		if province := provinces[title.ProvinceID]; province == nil || province.Area <= 0 {
			out = append(out, mapContractDiagnostic{
				Severity: "error", Code: "barony_province_missing_pixels", Source: title.Source, Path: title.Rel, Line: title.Line,
				Message: fmt.Sprintf("barony %s references province %d, which has no pixels in provinces.png", title.ID, title.ProvinceID), Occurrences: 1,
			})
		}
	}
	return out
}

func countyHistoryAnchorDiagnostics(ctx context.Context, tx *sql.Tx, anchors map[string]countyHistoryAnchor) []mapContractDiagnostic {
	counties := sortedStringKeys(anchors)
	out := make([]mapContractDiagnostic, 0)
	for _, county := range counties {
		anchor := anchors[county]
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT field FROM map_province_history WHERE province_id=? AND field IN ('culture','religion','holding')`, anchor.ProvinceID)
		if err != nil {
			out = append(out, mapContractDiagnostic{Severity: "error", Code: "county_history_anchor_check_failed", Message: err.Error(), Source: anchor.Source, Path: anchor.Path, Line: anchor.Line, Occurrences: 1})
			continue
		}
		present := map[string]bool{}
		for rows.Next() {
			var field string
			if scanErr := rows.Scan(&field); scanErr == nil {
				present[field] = true
			}
		}
		rows.Close()
		missing := make([]string, 0, 3)
		for _, field := range []string{"culture", "religion", "holding"} {
			if !present[field] {
				missing = append(missing, field)
			}
		}
		if len(missing) == 0 {
			continue
		}
		out = append(out, mapContractDiagnostic{
			Severity: "warning", Code: "county_history_anchor_missing", Source: anchor.Source, Path: anchor.Path, Line: anchor.Line,
			Message:     fmt.Sprintf("county %s history anchor %s (province %d) has no %s assignment at any indexed date", county, anchor.BaronyID, anchor.ProvinceID, strings.Join(missing, ", ")),
			Occurrences: len(missing),
		})
	}
	return out
}

func canonicalDefaultMapField(key string) string {
	switch key {
	case "sea_zones", "sea_zone":
		return "sea_zones"
	case "coastal_seas", "coastal_sea":
		return "coastal_seas"
	case "lakes", "lake":
		return "lakes"
	case "river_provinces", "river_province":
		return "river_provinces"
	case "impassable_seas", "impassable_sea":
		return "impassable_seas"
	case "impassable_mountains", "impassable_mountain":
		return "impassable_mountains"
	default:
		return ""
	}
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (db *DB) mapIntegrityIssues(ctx context.Context, titleID string, provinces map[int]bool) ([]MapIntegrityIssue, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT code,title_id,province_id,message,source_name,path,line
		FROM map_integrity_issues ORDER BY code,province_id,title_id,path,line`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MapIntegrityIssue
	seen := map[string]bool{}
	for rows.Next() {
		var issue MapIntegrityIssue
		if err := rows.Scan(&issue.Code, &issue.TitleID, &issue.ProvinceID, &issue.Message, &issue.Source, &issue.Path, &issue.Line); err != nil {
			return nil, err
		}
		if titleID != "" && issue.TitleID != titleID && !provinces[issue.ProvinceID] {
			continue
		}
		if titleID == "" && len(provinces) > 0 && !provinces[issue.ProvinceID] {
			continue
		}
		key := issue.Code + "\x00" + issue.Message + "\x00" + issue.TitleID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	return out, rows.Err()
}

func integrityProvinceSet(issues []MapIntegrityIssue) map[int]bool {
	out := map[int]bool{}
	for _, issue := range issues {
		if issue.ProvinceID > 0 {
			out[issue.ProvinceID] = true
		}
	}
	return out
}

func integrityMessages(issues []MapIntegrityIssue) []string {
	seen := map[string]bool{}
	var out []string
	for _, issue := range issues {
		if seen[issue.Message] {
			continue
		}
		seen[issue.Message] = true
		out = append(out, issue.Message)
	}
	sort.Strings(out)
	return out
}

func (db *DB) renderIntegrityOverlay(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA, v renderViewport, issues []MapIntegrityIssue) (int, error) {
	count := 0
	for _, pid := range sortedProvinceIDs(integrityProvinceSet(issues)) {
		runs, err := db.mapProvinceRuns(ctx, scratch, pid, false)
		if err != nil {
			return count, err
		}
		if len(runs) == 0 {
			continue
		}
		// Conflicts are hatched in red ink rather than filled, so the disputed
		// area and its rightful tint both stay readable.
		drawInkHatch(canvas, v, runs, color.RGBA{R: 168, G: 50, B: 74, A: 205}, 7)
		count++
	}
	return count, nil
}
