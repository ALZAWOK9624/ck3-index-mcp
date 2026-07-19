package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

type ObjectQuery struct {
	Query             string                 `json:"query"`
	Definitions       []ObjectDef            `json:"definitions"`
	Overrides         []ObjectDef            `json:"overrides"`
	Resolution        []DefinitionResolution `json:"resolution,omitempty"`
	FileOverrides     []FileOverrideEvidence `json:"file_overrides,omitempty"`
	EventProfiles     []EventProfile         `json:"event_profiles,omitempty"`
	CharacterProfiles []CharacterProfile     `json:"character_profiles,omitempty"`
}

// EventProfile exposes the direct, indexed event fields that describe how an
// event is presented and scheduled. The source range remains on ObjectDef;
// this is deliberately a view over object_fields rather than a second event
// database.
type EventProfile struct {
	Name   string               `json:"name"`
	Source string               `json:"source"`
	Rank   int                  `json:"rank"`
	Path   string               `json:"path"`
	Fields []EventFieldEvidence `json:"fields"`
}

type EventFieldEvidence struct {
	Field string `json:"field"`
	Shape string `json:"shape"`
	Line  int    `json:"line"`
	Raw   string `json:"raw"`
}

// CharacterProfile exposes static identity fields and dated lifecycle entries
// from history/characters without creating a second character database.
type CharacterProfile struct {
	Name         string                   `json:"name"`
	Source       string                   `json:"source"`
	Rank         int                      `json:"rank"`
	Path         string                   `json:"path"`
	StaticFields []CharacterFieldEvidence `json:"static_fields,omitempty"`
	Timeline     []CharacterTimelineEntry `json:"timeline,omitempty"`
}

type CharacterFieldEvidence struct {
	Field string `json:"field"`
	Shape string `json:"shape"`
	Line  int    `json:"line"`
	Raw   string `json:"raw"`
}

type CharacterTimelineEntry struct {
	Date    string                   `json:"date"`
	DateKey int                      `json:"date_key"`
	Fields  []CharacterFieldEvidence `json:"fields"`
}

type DefinitionResolution struct {
	Type           string `json:"type"`
	Name           string `json:"name"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	CandidateCount int    `json:"candidate_count"`
	ActiveCount    int    `json:"active_count"`
}

type FileOverrideEvidence struct {
	LogicalPath      string `json:"logical_path"`
	Source           string `json:"source"`
	Rank             int    `json:"rank"`
	Path             string `json:"path"`
	Overridden       bool   `json:"overridden"`
	OverrideReason   string `json:"override_reason,omitempty"`
	OverrideBySource string `json:"override_by_source,omitempty"`
	OverrideByRank   int    `json:"override_by_rank,omitempty"`
	OverrideRule     string `json:"override_rule,omitempty"`
}

type ObjectTypeSummary struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

func (db *DB) QueryObjectTypes(ctx context.Context) ([]ObjectTypeSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,COUNT(*)
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0
		GROUP BY o.object_type ORDER BY COUNT(*) DESC, o.object_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectTypeSummary
	for rows.Next() {
		var item ObjectTypeSummary
		if err := rows.Scan(&item.Type, &item.Count); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type ObjectDef struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Source      string `json:"source"`
	Rank        int    `json:"rank"`
	Path        string `json:"path"`
	LogicalPath string `json:"logical_path"`
	Line        int    `json:"line"`
	Column      int    `json:"column"`
	EndLine     int    `json:"end_line,omitempty"`
	EndColumn   int    `json:"end_column,omitempty"`
	Status      string `json:"status,omitempty"`
}

func (db *DB) QueryObject(ctx context.Context, id string) (ObjectQuery, error) {
	q := ObjectQuery{Query: id}
	typ, name, typed := splitTypedID(id)
	sqlText := `SELECT o.object_type,o.name,o.value,o.source_name,o.source_rank,o.path,f.rel_path,o.line,o.col,o.end_line,o.end_col
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE o.name=? AND f.overridden=0
		ORDER BY o.object_type,o.name,o.source_rank`
	args := []any{id}
	if typed {
		sqlText = `SELECT o.object_type,o.name,o.value,o.source_name,o.source_rank,o.path,f.rel_path,o.line,o.col,o.end_line,o.end_col
			FROM objects o JOIN files f ON f.id=o.file_id
			WHERE o.object_type=? AND o.name=? AND f.overridden=0
			ORDER BY o.object_type,o.name,o.source_rank`
		args = []any{typ, name}
	}
	rows, err := db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var d ObjectDef
		if err := rows.Scan(&d.Type, &d.Name, &d.Value, &d.Source, &d.Rank, &d.Path, &d.LogicalPath, &d.Line, &d.Column, &d.EndLine, &d.EndColumn); err != nil {
			return q, err
		}
		q.Definitions = append(q.Definitions, d)
	}
	if err := rows.Err(); err != nil {
		return q, err
	}
	q.Resolution = resolveDefinitionCandidates(q.Definitions)
	q.Overrides = make([]ObjectDef, len(q.Definitions))
	copy(q.Overrides, q.Definitions)
	fileOverrides, err := db.queryFileOverrideEvidence(ctx, q.Definitions)
	if err != nil {
		return q, err
	}
	q.FileOverrides = fileOverrides
	eventProfiles, err := db.queryEventProfiles(ctx, q.Definitions)
	if err != nil {
		return q, err
	}
	q.EventProfiles = eventProfiles
	characterProfiles, err := db.queryCharacterProfiles(ctx, q.Definitions)
	if err != nil {
		return q, err
	}
	q.CharacterProfiles = characterProfiles
	return q, nil
}

func (db *DB) queryEventProfiles(ctx context.Context, definitions []ObjectDef) ([]EventProfile, error) {
	var out []EventProfile
	for _, definition := range definitions {
		if definition.Type != "event" {
			continue
		}
		rows, err := db.sql.QueryContext(ctx, `SELECT field,value_shape,line,raw
			FROM object_fields
			WHERE object_type='event' AND object_name=? AND source_name=? AND source_rank=? AND path=? AND date_key=0
			ORDER BY line,field`, definition.Name, definition.Source, definition.Rank, definition.Path)
		if err != nil {
			return nil, err
		}
		profile := EventProfile{Name: definition.Name, Source: definition.Source, Rank: definition.Rank, Path: definition.Path}
		for rows.Next() {
			var field EventFieldEvidence
			if err := rows.Scan(&field.Field, &field.Shape, &field.Line, &field.Raw); err != nil {
				rows.Close()
				return nil, err
			}
			profile.Fields = append(profile.Fields, field)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(profile.Fields) > 0 {
			out = append(out, profile)
		}
	}
	return out, nil
}

func (db *DB) queryCharacterProfiles(ctx context.Context, definitions []ObjectDef) ([]CharacterProfile, error) {
	var out []CharacterProfile
	for _, definition := range definitions {
		if definition.Type != "character" {
			continue
		}
		rows, err := db.sql.QueryContext(ctx, `SELECT date_key,field,value_shape,line,raw
			FROM object_fields
			WHERE object_type='character' AND object_name=? AND source_name=? AND source_rank=? AND path=?
			ORDER BY date_key,line,field`, definition.Name, definition.Source, definition.Rank, definition.Path)
		if err != nil {
			return nil, err
		}
		profile := CharacterProfile{Name: definition.Name, Source: definition.Source, Rank: definition.Rank, Path: definition.Path}
		timelineIndexes := map[int]int{}
		for rows.Next() {
			var dateKey int
			var field CharacterFieldEvidence
			if err := rows.Scan(&dateKey, &field.Field, &field.Shape, &field.Line, &field.Raw); err != nil {
				rows.Close()
				return nil, err
			}
			if dateKey == 0 {
				profile.StaticFields = append(profile.StaticFields, field)
				continue
			}
			index, exists := timelineIndexes[dateKey]
			if !exists {
				index = len(profile.Timeline)
				timelineIndexes[dateKey] = index
				profile.Timeline = append(profile.Timeline, CharacterTimelineEntry{Date: formatDateKey(dateKey), DateKey: dateKey})
			}
			profile.Timeline[index].Fields = append(profile.Timeline[index].Fields, field)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(profile.StaticFields) > 0 || len(profile.Timeline) > 0 {
			out = append(out, profile)
		}
	}
	return out, nil
}

func formatDateKey(dateKey int) string {
	if dateKey <= 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", dateKey/10000, (dateKey/100)%100, dateKey%100)
}

func resolveDefinitionCandidates(definitions []ObjectDef) []DefinitionResolution {
	groups := map[string][]int{}
	var order []string
	for i := range definitions {
		key := definitions[i].Type + "\x00" + definitions[i].Name + "\x00" + definitionResolutionDomain(definitions[i])
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}
	var out []DefinitionResolution
	for _, key := range order {
		indexes := groups[key]
		first := definitions[indexes[0]]
		resolution := DefinitionResolution{Type: first.Type, Name: first.Name, Mode: "replace", CandidateCount: len(indexes)}
		if first.Type == "on_action" {
			resolution.Mode = "merge"
			resolution.Status = "merged"
			resolution.Reason = "current CK3 _on_actions.info permits declarations across files; direct trigger/effect conflicts still require diagnostics"
			resolution.ActiveCount = len(indexes)
			for _, index := range indexes {
				definitions[index].Status = "merged"
			}
			out = append(out, resolution)
			continue
		}
		bestRank := definitions[indexes[0]].Rank
		for _, index := range indexes[1:] {
			if definitions[index].Rank < bestRank {
				bestRank = definitions[index].Rank
			}
		}
		var best []int
		for _, index := range indexes {
			if definitions[index].Rank == bestRank {
				best = append(best, index)
			}
		}
		if len(best) == 1 {
			resolution.ActiveCount = 1
			definitions[best[0]].Status = "active"
			for _, index := range indexes {
				if index != best[0] {
					definitions[index].Status = "shadowed_by_source_priority"
				}
			}
			if len(indexes) == 1 {
				resolution.Status = "unique"
				resolution.Reason = "one active indexed definition"
			} else {
				resolution.Status = "source_priority"
				resolution.Reason = "the lowest source rank is the unique highest-priority candidate"
			}
		} else {
			resolution.Status = "ambiguous"
			resolution.Reason = "multiple candidates share the highest source priority; inspect paths and the database-specific CK3 load rule"
			for _, index := range indexes {
				if definitions[index].Rank == bestRank {
					definitions[index].Status = "ambiguous_top_priority"
				} else {
					definitions[index].Status = "shadowed_by_source_priority"
				}
			}
		}
		out = append(out, resolution)
	}
	return out
}

func definitionResolutionDomain(definition ObjectDef) string {
	if definition.Type != "title" {
		return ""
	}
	logicalPath := strings.ToLower(strings.ReplaceAll(definition.LogicalPath, "\\", "/"))
	switch {
	case strings.HasPrefix(logicalPath, "common/landed_titles/"):
		return "landed_titles"
	case strings.HasPrefix(logicalPath, "history/titles/"):
		return "title_history"
	default:
		return logicalPath
	}
}

func (db *DB) queryFileOverrideEvidence(ctx context.Context, definitions []ObjectDef) ([]FileOverrideEvidence, error) {
	seen := map[string]bool{}
	var out []FileOverrideEvidence
	for _, definition := range definitions {
		if definition.LogicalPath == "" || seen[definition.LogicalPath] {
			continue
		}
		seen[definition.LogicalPath] = true
		rows, err := db.sql.QueryContext(ctx, `SELECT rel_path,source_name,source_rank,path,overridden,
			override_reason,override_by_source,override_by_rank,override_rule
			FROM files WHERE rel_path=? ORDER BY source_rank,path`, definition.LogicalPath)
		if err != nil {
			return nil, err
		}
		var chain []FileOverrideEvidence
		for rows.Next() {
			var item FileOverrideEvidence
			var overridden int
			if err := rows.Scan(&item.LogicalPath, &item.Source, &item.Rank, &item.Path, &overridden,
				&item.OverrideReason, &item.OverrideBySource, &item.OverrideByRank, &item.OverrideRule); err != nil {
				rows.Close()
				return nil, err
			}
			item.Overridden = overridden != 0
			chain = append(chain, item)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(chain) > 1 {
			out = append(out, chain...)
		}
	}
	return out, nil
}

type RefQuery struct {
	Query             string   `json:"query"`
	Incoming          []RefHit `json:"incoming"`
	Outgoing          []RefHit `json:"outgoing"`
	IncomingTotal     int      `json:"incoming_total"`
	OutgoingTotal     int      `json:"outgoing_total"`
	IncomingTruncated bool     `json:"incoming_truncated,omitempty"`
	OutgoingTruncated bool     `json:"outgoing_truncated,omitempty"`
}

type RefHit struct {
	FromType         string `json:"from_type,omitempty"`
	FromName         string `json:"from_name,omitempty"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Raw              string `json:"raw"`
	Resolved         bool   `json:"resolved"`
	Resolution       string `json:"resolution"`
	ResolutionReason string `json:"resolution_reason"`
	Relation         string `json:"relation,omitempty"`
	Phase            string `json:"phase,omitempty"`
	Confidence       string `json:"confidence,omitempty"`
	Source           string `json:"source,omitempty"`
	Path             string `json:"path"`
	Line             int    `json:"line"`
	Column           int    `json:"column"`
}

func (db *DB) QueryRefs(ctx context.Context, id string) (RefQuery, error) {
	q := RefQuery{Query: id}
	typ, name, typed := splitTypedID(id)
	inWhere := "r.ref_name=?"
	inArgs := []any{id}
	if typed {
		inWhere = "r.ref_kind=? AND r.ref_name=?"
		inArgs = []any{typ, name}
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE `+inWhere+` AND f.overridden=0`, inArgs...).Scan(&q.IncomingTotal); err != nil {
		return q, err
	}
	inSQL := `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,r.resolution_reason,
		r.relation,r.phase,r.confidence,f.source_name,f.path,r.line,r.col
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE ` + inWhere + ` AND f.overridden=0
		ORDER BY f.source_rank,f.path,r.line LIMIT 500`
	in, err := db.sql.QueryContext(ctx, inSQL, inArgs...)
	if err != nil {
		return q, err
	}
	defer in.Close()
	for in.Next() {
		h, err := scanRef(in)
		if err != nil {
			return q, err
		}
		q.Incoming = append(q.Incoming, h)
	}
	if err := in.Err(); err != nil {
		return q, err
	}
	q.IncomingTruncated = q.IncomingTotal > len(q.Incoming)
	outWhere := "r.from_object_name=?"
	outArgs := []any{id}
	if typed {
		outWhere = "r.from_object_type=? AND r.from_object_name=?"
		outArgs = []any{typ, name}
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE `+outWhere+` AND f.overridden=0`, outArgs...).Scan(&q.OutgoingTotal); err != nil {
		return q, err
	}
	outSQL := `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,r.resolution_reason,
		r.relation,r.phase,r.confidence,f.source_name,f.path,r.line,r.col
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE ` + outWhere + ` AND f.overridden=0
		ORDER BY f.source_rank,f.path,r.line LIMIT 500`
	out, err := db.sql.QueryContext(ctx, outSQL, outArgs...)
	if err != nil {
		return q, err
	}
	defer out.Close()
	for out.Next() {
		h, err := scanRef(out)
		if err != nil {
			return q, err
		}
		q.Outgoing = append(q.Outgoing, h)
	}
	if err := out.Err(); err != nil {
		return q, err
	}
	q.OutgoingTruncated = q.OutgoingTotal > len(q.Outgoing)
	return q, nil
}

func scanRef(rows *sql.Rows) (RefHit, error) {
	var h RefHit
	var ft, fn sql.NullString
	var resolved int
	err := rows.Scan(&ft, &fn, &h.Kind, &h.Name, &h.Raw, &resolved, &h.ResolutionReason,
		&h.Relation, &h.Phase, &h.Confidence, &h.Source, &h.Path, &h.Line, &h.Column)
	if ft.Valid {
		h.FromType = ft.String
	}
	if fn.Valid {
		h.FromName = fn.String
	}
	h.Resolved = resolved != 0
	if h.Resolved {
		h.Resolution = "resolved"
	} else if h.ResolutionReason == "runtime_scope" || h.ResolutionReason == "unverified_runtime_symbol" {
		h.Resolution = "dynamic"
	} else {
		h.Resolution = "unresolved"
	}
	return h, err
}

type LocalizationQuery struct {
	Key    string            `json:"key"`
	Values []LocalizationHit `json:"values"`
}

type LocalizationHit struct {
	Language string `json:"language"`
	Value    string `json:"value"`
	Source   string `json:"source"`
	Rank     int    `json:"rank"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Replace  bool   `json:"replace"`
}

func (db *DB) QueryLocalization(ctx context.Context, key string) (LocalizationQuery, error) {
	q := LocalizationQuery{Key: key}
	rows, err := db.sql.QueryContext(ctx, `SELECT l.language,l.value,l.source_name,l.source_rank,l.path,l.line,l.replace_dir
		FROM localization l JOIN files f ON f.id=l.file_id
		WHERE l.key=? AND f.overridden=0
		ORDER BY l.language,l.source_rank,l.replace_dir DESC`, key)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var h LocalizationHit
		var repl int
		if err := rows.Scan(&h.Language, &h.Value, &h.Source, &h.Rank, &h.Path, &h.Line, &repl); err != nil {
			return q, err
		}
		h.Replace = repl != 0
		q.Values = append(q.Values, h)
	}
	return q, rows.Err()
}

type ResourceQuery struct {
	Query      string        `json:"query"`
	Resources  []ResourceHit `json:"resources"`
	References []RefHit      `json:"references"`
	Partial    bool          `json:"partial_match,omitempty"`
}

type ResourceHit struct {
	ResourcePath string `json:"resource_path"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Rank         int    `json:"rank"`
	Path         string `json:"path"`
}

func (db *DB) QueryResource(ctx context.Context, id string) (ResourceQuery, error) {
	q := ResourceQuery{Query: id}
	resID := normalizeResource(filepathSlash(id))
	base := filepath.Base(resID)
	rows, err := db.sql.QueryContext(ctx, `SELECT r.resource_path,r.kind,r.source_name,r.source_rank,r.path
		FROM resources r JOIN files f ON f.id=r.file_id
		WHERE r.resource_path=? AND f.overridden=0
		ORDER BY r.source_rank,r.path LIMIT 200`, resID)
	if err != nil {
		return q, err
	}
	if err := scanResourceRows(rows, &q); err != nil {
		return q, err
	}
	if len(q.Resources) == 0 && base != "" && base != "." && base != "/" && base != resID {
		rows, err = db.sql.QueryContext(ctx, `SELECT r.resource_path,r.kind,r.source_name,r.source_rank,r.path
			FROM resources r JOIN files f ON f.id=r.file_id
			WHERE r.resource_path=? AND f.overridden=0
			ORDER BY r.source_rank,r.path LIMIT 200`, base)
		if err != nil {
			return q, err
		}
		if err := scanResourceRows(rows, &q); err != nil {
			return q, err
		}
	}
	if len(q.Resources) == 0 {
		needle := "%" + strings.ReplaceAll(resID, "*", "%") + "%"
		rows, err = db.sql.QueryContext(ctx, `SELECT r.resource_path,r.kind,r.source_name,r.source_rank,r.path
			FROM resources r JOIN files f ON f.id=r.file_id
			WHERE r.resource_path LIKE ? AND f.overridden=0
			ORDER BY r.source_rank,r.path LIMIT 50`, needle)
		if err != nil {
			return q, err
		}
		q.Partial = true
		if err := scanResourceRows(rows, &q); err != nil {
			return q, err
		}
	}
	refs, err := db.QueryRefs(ctx, id)
	if err == nil {
		q.References = refs.Incoming
	}
	return q, nil
}

func scanResourceRows(rows *sql.Rows, q *ResourceQuery) error {
	defer rows.Close()
	for rows.Next() {
		var h ResourceHit
		if err := rows.Scan(&h.ResourcePath, &h.Kind, &h.Source, &h.Rank, &h.Path); err != nil {
			return err
		}
		q.Resources = append(q.Resources, h)
	}
	return rows.Err()
}

func filepathSlash(s string) string {
	return strings.ReplaceAll(s, "\\", "/")
}

func splitTypedID(id string) (string, string, bool) {
	typ, name, ok := strings.Cut(id, ":")
	if !ok || typ == "" || name == "" || strings.Contains(name, ":") {
		return "", id, false
	}
	return typ, name, true
}

type ValidationReport struct {
	Diagnostics []Diagnostic   `json:"diagnostics"`
	Counts      map[string]int `json:"counts"`
}

type Diagnostic struct {
	Source      string `json:"source"`
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
	Column      int    `json:"column,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
	RuleSource  string `json:"rule_source,omitempty"`
	SourceLayer string `json:"source_layer,omitempty"`
	Confidence  string `json:"confidence"`
	Fingerprint string `json:"fingerprint"`
	Occurrences int    `json:"occurrences"`
}

func (db *DB) Validate(ctx context.Context) (ValidationReport, error) {
	if err := db.runCompilerChecks(ctx); err != nil {
		return ValidationReport{}, err
	}
	return db.CachedValidation(ctx)
}

func (db *DB) CachedValidation(ctx context.Context) (ValidationReport, error) {
	return db.cachedValidation(ctx, "")
}

func (db *DB) CachedValidationForSource(ctx context.Context, source string) (ValidationReport, error) {
	return db.cachedValidation(ctx, source)
}

func (db *DB) cachedValidation(ctx context.Context, source string) (ValidationReport, error) {
	rep := ValidationReport{Counts: map[string]int{}}
	countRows, err := db.sql.QueryContext(ctx, `SELECT d.severity,COUNT(*)
		FROM diagnostics d LEFT JOIN files f ON f.id=d.file_id
		WHERE (?='' OR f.source_name=? OR d.file_id IS NULL)
		GROUP BY d.severity`, source, source)
	if err != nil {
		return rep, err
	}
	for countRows.Next() {
		var severity string
		var count int
		if err := countRows.Scan(&severity, &count); err != nil {
			countRows.Close()
			return rep, err
		}
		rep.Counts[severity] = count
	}
	if err := countRows.Close(); err != nil {
		return rep, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT d.source,d.severity,d.code,d.message,COALESCE(d.path,''),COALESCE(d.line,0),COALESCE(d.col,0),COALESCE(f.source_name,d.source_layer,''),d.confidence,d.fingerprint,d.occurrences
		FROM diagnostics d LEFT JOIN files f ON f.id=d.file_id
		WHERE (?='' OR f.source_name=? OR d.file_id IS NULL)
		ORDER BY d.path,d.line LIMIT 10000`, source, source)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column, &d.SourceLayer, &d.Confidence, &d.Fingerprint, &d.Occurrences); err != nil {
			return rep, err
		}
		d.Suggestion, d.RuleSource = diagnosticHint(d.Code, d.Message)
		if d.Confidence == "medium" {
			d.Confidence = diagnosticConfidence(d.Code, d.Severity)
		}
		if d.Fingerprint == "" {
			d.Fingerprint = diagnosticFingerprint(d)
		}
		if d.Occurrences < 1 {
			d.Occurrences = 1
		}
		rep.Diagnostics = append(rep.Diagnostics, d)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	rep.Diagnostics = aggregateDiagnostics(rep.Diagnostics)
	return rep, nil
}
