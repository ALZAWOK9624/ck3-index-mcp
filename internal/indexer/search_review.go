package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type SearchOptions struct {
	Query      string
	Kind       string
	Source     string
	PathPrefix string
	Page       int
	LLMOptions
}

// LLMInspectSmart is the broad one-id entry point. It deliberately uses the
// aggregate diagnosis path so objects, localization, resources, references,
// sounds, and diagnostics are checked in one call.
func (db *DB) LLMInspectSmart(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	result, err := db.LLMDiagnoseKey(ctx, id, opts)
	if err != nil {
		return LLMResult{}, err
	}
	result.Intent = "ck3_inspect"
	if datatypes, derr := db.LookupDatatype(ctx, id, opts.normalizedLimit()); derr == nil {
		for _, d := range datatypes {
			result.Evidence = append([]LLMEvidence{{Kind: "datatype", Name: d.Name, Source: "engine_logs", Path: evidencePath(d.Source), Detail: d.Signature + " -> " + d.ReturnType + "; " + trimText(d.Description, 180)}}, result.Evidence...)
		}
		if len(datatypes) > 0 {
			if result.Counts == nil {
				result.Counts = map[string]int{}
			}
			result.Counts["datatypes"] = len(datatypes)
			result.Summary = fmt.Sprintf("Found %d engine datatype match(es) for %q. ", len(datatypes), id) + result.Summary
		}
	}
	result.Guidance = append([]string{
		"Use this aggregate result before choosing a specialized query tool.",
	}, result.Guidance...)
	for i := range result.NextQueries {
		if result.NextQueries[i].Tool == "inspect_object" {
			result.NextQueries[i].Tool = "prepare_edit"
			result.NextQueries[i].Reason = "load edit-specific examples and schema when a change is planned"
		}
	}
	return result, nil
}

// LLMReview is the high-level code review entry point. Proposed complete file
// contents use the virtual patch analyzer; with no files it reviews dirty
// current-project files from disk.
func (db *DB) LLMReview(ctx context.Context, cfg Config, files []PatchFileInput, opts LLMOptions) (LLMResult, error) {
	var result LLMResult
	var err error
	if len(files) > 0 {
		result, err = db.LLMPreflightPatch(ctx, files, opts)
	} else {
		if cfg.ConfigPath == "" {
			return LLMResult{}, fmt.Errorf("ck3_review without files requires server config")
		}
		result, err = db.LLMPreflightDirty(ctx, cfg, opts)
	}
	if err != nil {
		return LLMResult{}, err
	}
	result.Intent = "ck3_review"
	result.Guidance = append([]string{
		"Treat scope_mismatch as proven only when the diagnostic includes a concrete root/current scope trace.",
		"Resolve parser and proven scope errors before writing or rescanning files.",
	}, result.Guidance...)
	return result, nil
}

// LLMSearch is the broad semantic discovery entry point. It searches indexed
// identifiers and AST keys before callers fall back to raw filesystem search.
func (db *DB) LLMSearch(ctx context.Context, opts SearchOptions) (LLMResult, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return LLMResult{}, fmt.Errorf("ck3_search requires query")
	}
	limit := opts.normalizedLimit()
	page := opts.Page
	if page <= 0 {
		page = 1
	}
	// A page is never wider than the normal evidence limit. Fetch enough
	// ranked candidates to prove whether one subsequent page exists, while
	// keeping the broad multi-kind search bounded even for a deep page.
	fetchLimit := page*limit + 1
	if fetchLimit > 501 {
		fetchLimit = 501
	}
	prefix := escapeLike(query) + "%"
	result := LLMResult{
		Query:  query,
		Intent: "ck3_search",
		Counts: map[string]int{},
		Guidance: []string{
			"Use ck3_inspect on the best semantic match before opening raw files.",
			"Use rg only to inspect exact paths returned as evidence or when indexed semantic search has no suitable match.",
		},
	}

	type searcher struct {
		kind string
		fn   func() ([]LLMEvidence, error)
	}
	searchers := []searcher{
		{"object", func() ([]LLMEvidence, error) { return db.searchObjects(ctx, query, prefix, opts, fetchLimit) }},
		{"reference", func() ([]LLMEvidence, error) { return db.searchRefs(ctx, query, prefix, opts, fetchLimit) }},
		{"localization", func() ([]LLMEvidence, error) { return db.searchLocalizationKeys(ctx, query, prefix, opts, fetchLimit) }},
		{"resource", func() ([]LLMEvidence, error) { return db.searchResources(ctx, query, prefix, opts, fetchLimit) }},
		{"diagnostic", func() ([]LLMEvidence, error) { return db.searchDiagnostics(ctx, query, prefix, opts, fetchLimit) }},
		{"script_key", func() ([]LLMEvidence, error) { return db.searchScriptKeys(ctx, query, prefix, opts, fetchLimit) }},
		{"datatype", func() ([]LLMEvidence, error) { return db.searchDatatypes(ctx, query, prefix, opts, fetchLimit) }},
	}
	for _, search := range searchers {
		if opts.Kind != "" && opts.Kind != search.kind {
			continue
		}
		evidence, err := search.fn()
		if err != nil {
			return LLMResult{}, err
		}
		result.Counts[search.kind] = len(evidence)
		result.Evidence = append(result.Evidence, evidence...)
	}
	sort.SliceStable(result.Evidence, func(i, j int) bool {
		ri, rj := 1, 1
		if result.Evidence[i].Name == query {
			ri = 0
		}
		if result.Evidence[j].Name == query {
			rj = 0
		}
		return ri < rj
	})
	result.Evidence = dedupeSearchEvidence(result.Evidence)
	if len(result.Evidence) > fetchLimit {
		result.Evidence = result.Evidence[:fetchLimit]
	}
	if len(result.Evidence) < fetchLimit {
		fts, err := db.searchFTS(ctx, query, opts, fetchLimit-len(result.Evidence))
		if err != nil {
			return LLMResult{}, err
		}
		result.Counts["fts"] = len(fts)
		result.Evidence = appendUniqueEvidence(result.Evidence, fts, fetchLimit)
	}
	if len(result.Evidence) == 0 {
		contains, err := db.searchContains(ctx, query, opts, fetchLimit-len(result.Evidence))
		if err != nil {
			return LLMResult{}, err
		}
		result.Counts["contains"] = len(contains)
		result.Evidence = appendUniqueEvidence(result.Evidence, contains, fetchLimit)
	}
	result = result.withPublicFilter(opts.LLMOptions)
	result = paginateLLMResult(result, page, limit)
	result.Summary = fmt.Sprintf("Semantic search for %q returned %d evidence item(s) on page %d.", query, len(result.Evidence), page)
	if len(result.Evidence) > 0 && !opts.publicMode() {
		best := result.Evidence[0]
		id := best.Name
		if best.Type != "" && best.Name != "" {
			id = best.Type + ":" + best.Name
		}
		result.NextQueries = []LLMNextQuery{{Tool: "ck3_inspect", ID: id, Reason: "inspect the highest-ranked semantic match"}}
	}
	return result, nil
}

func dedupeSearchEvidence(in []LLMEvidence) []LLMEvidence {
	return appendUniqueEvidence(nil, in, len(in))
}
func appendUniqueEvidence(dst, src []LLMEvidence, limit int) []LLMEvidence {
	seen := map[string]bool{}
	for _, ev := range dst {
		seen[ev.Kind+"\x00"+ev.Name+"\x00"+ev.Path] = true
	}
	for _, ev := range src {
		k := ev.Kind + "\x00" + ev.Name + "\x00" + ev.Path
		if !seen[k] {
			dst = append(dst, ev)
			seen[k] = true
		}
		if len(dst) >= limit {
			break
		}
	}
	return dst
}

func (db *DB) searchDatatypes(ctx context.Context, query, prefix string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	if opts.Source != "" && opts.Source != "engine_logs" {
		return nil, nil
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT name,signature,COALESCE(description,''),COALESCE(return_type,''),source_path FROM engine_datatypes WHERE name>=? AND name<? ORDER BY CASE WHEN name=? THEN 0 ELSE 1 END,name LIMIT ?`, query, query+"\uffff", query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		var sig, desc, ret string
		ev.Kind = "datatype"
		ev.Source = "engine_logs"
		if err := rows.Scan(&ev.Name, &sig, &desc, &ret, &ev.Path); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		ev.Detail = sig + " -> " + ret + "; " + trimText(desc, 160)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchFTS(ctx context.Context, query string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	if len([]rune(query)) < 2 {
		return nil, nil
	}
	match := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := db.sql.QueryContext(ctx, `SELECT kind,name,text,source,path,bm25(search_fts) FROM search_fts WHERE search_fts MATCH ? AND (?='' OR kind=?) AND (?='' OR source=?) AND (?='' OR path LIKE ?) ORDER BY bm25(search_fts),name LIMIT ?`, match, opts.Kind, opts.Kind, opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("FTS5 query failed: %w", err)
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		var score float64
		if err := rows.Scan(&ev.Kind, &ev.Name, &ev.Detail, &ev.Source, &ev.Path, &score); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		ev.Detail = trimText(ev.Detail, 180)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchContains(ctx context.Context, query string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	var out []LLMEvidence
	if opts.Kind == "" || opts.Kind == "object" {
		rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,o.name,o.source_name,f.rel_path,o.line FROM objects o JOIN files f ON f.id=o.file_id WHERE f.overridden=0 AND instr(o.name,?)>0 AND (?='' OR o.source_name=?) AND (?='' OR f.rel_path LIKE ?) ORDER BY o.source_rank,length(o.name),o.name LIMIT ?`, query, opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix)+"%", limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ev LLMEvidence
			ev.Kind = "object"
			if err := rows.Scan(&ev.Type, &ev.Name, &ev.Source, &ev.Path, &ev.Line); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, ev)
		}
		rows.Close()
	}
	if len(out) < limit && (opts.Kind == "" || opts.Kind == "resource") {
		rows, err := db.sql.QueryContext(ctx, `SELECT r.kind,r.resource_path,r.source_name,f.rel_path FROM resources r JOIN files f ON f.id=r.file_id WHERE f.overridden=0 AND instr(r.resource_path,?)>0 AND (?='' OR r.source_name=?) AND (?='' OR f.rel_path LIKE ?) ORDER BY r.source_rank,length(r.resource_path),r.resource_path LIMIT ?`, query, opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix)+"%", limit-len(out))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ev LLMEvidence
			ev.Kind = "resource"
			if err := rows.Scan(&ev.Type, &ev.Name, &ev.Source, &ev.Path); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, ev)
		}
		rows.Close()
	}
	if len(out) < limit {
		loc, err := db.searchLocalizationValues(ctx, query, opts, limit-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, loc...)
	}
	return out, nil
}

func (db *DB) searchLocalizationValues(ctx context.Context, query string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	if opts.Kind != "" && opts.Kind != "localization" {
		return nil, nil
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT l.key,l.source_name,f.rel_path,l.line,l.language,l.value FROM localization l JOIN files f ON f.id=l.file_id WHERE f.overridden=0 AND instr(l.value,?)>0 AND (?='' OR l.source_name=?) AND (?='' OR f.rel_path LIKE ?) ORDER BY l.source_rank,l.key LIMIT ?`, query, opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		var lang, val string
		ev.Kind = "localization"
		if err := rows.Scan(&ev.Name, &ev.Source, &ev.Path, &ev.Line, &lang, &val); err != nil {
			return nil, err
		}
		ev.Detail = lang + ": " + trimText(val, 180)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchObjects(ctx context.Context, query, prefix string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	sqlText := `SELECT o.object_type,o.name,o.source_name,o.path,o.line,o.col
		FROM objects o INDEXED BY idx_objects_name JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0 AND o.name>=? AND o.name<?
		ORDER BY CASE WHEN o.name=? THEN 0 ELSE 1 END,o.source_rank,o.name LIMIT ?`
	args := []any{query, query + "\uffff", query, limit}
	if opts.Source != "" || opts.PathPrefix != "" {
		sqlText = `SELECT o.object_type,o.name,o.source_name,o.path,o.line,o.col FROM objects o INDEXED BY idx_objects_name JOIN files f ON f.id=o.file_id WHERE f.overridden=0 AND o.name>=? AND o.name<? AND (?='' OR o.source_name=?) AND (?='' OR f.rel_path LIKE ? ESCAPE '\') ORDER BY CASE WHEN o.name=? THEN 0 ELSE 1 END,o.source_rank,o.name LIMIT ?`
		args = []any{query, query + "\uffff", opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix) + "%", query, limit}
	}
	rows, err := db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		ev.Kind = "object"
		if err := rows.Scan(&ev.Type, &ev.Name, &ev.Source, &ev.Path, &ev.Line, &ev.Column); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchRefs(ctx context.Context, query, prefix string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	sqlText := `SELECT r.ref_kind,r.ref_name,f.source_name,f.path,r.line,r.col,r.raw
		FROM refs r INDEXED BY idx_refs_name JOIN files f ON f.id=r.file_id
		WHERE f.overridden=0 AND r.ref_name>=? AND r.ref_name<?
		ORDER BY CASE WHEN r.ref_name=? THEN 0 ELSE 1 END,f.source_rank,r.ref_name LIMIT ?`
	args := []any{query, query + "\uffff", query, limit}
	if opts.Source != "" || opts.PathPrefix != "" {
		sqlText = `SELECT r.ref_kind,r.ref_name,f.source_name,f.path,r.line,r.col,r.raw FROM refs r INDEXED BY idx_refs_name JOIN files f ON f.id=r.file_id WHERE f.overridden=0 AND r.ref_name>=? AND r.ref_name<? AND (?='' OR f.source_name=?) AND (?='' OR f.rel_path LIKE ? ESCAPE '\') ORDER BY CASE WHEN r.ref_name=? THEN 0 ELSE 1 END,f.source_rank,r.ref_name LIMIT ?`
		args = []any{query, query + "\uffff", opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix) + "%", query, limit}
	}
	rows, err := db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		ev.Kind = "reference"
		if err := rows.Scan(&ev.Type, &ev.Name, &ev.Source, &ev.Path, &ev.Line, &ev.Column, &ev.Detail); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchLocalizationKeys(ctx context.Context, query, prefix string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	sqlText := `SELECT l.key,l.source_name,l.path,l.line,l.language,l.value
		FROM localization l INDEXED BY idx_loc_key JOIN files f ON f.id=l.file_id
		WHERE f.overridden=0 AND l.key>=? AND l.key<?
		ORDER BY CASE WHEN l.key=? THEN 0 ELSE 1 END,l.source_rank,l.key LIMIT ?`
	args := []any{query, query + "\uffff", query, limit}
	if opts.Source != "" || opts.PathPrefix != "" {
		sqlText = `SELECT l.key,l.source_name,l.path,l.line,l.language,l.value FROM localization l INDEXED BY idx_loc_key JOIN files f ON f.id=l.file_id WHERE f.overridden=0 AND l.key>=? AND l.key<? AND (?='' OR l.source_name=?) AND (?='' OR f.rel_path LIKE ? ESCAPE '\') ORDER BY CASE WHEN l.key=? THEN 0 ELSE 1 END,l.source_rank,l.key LIMIT ?`
		args = []any{query, query + "\uffff", opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix) + "%", query, limit}
	}
	rows, err := db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		var language, value string
		ev.Kind = "localization"
		if err := rows.Scan(&ev.Name, &ev.Source, &ev.Path, &ev.Line, &language, &value); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		ev.Detail = language + ": " + trimText(value, 180)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchResources(ctx context.Context, query, prefix string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	sqlText := `SELECT r.kind,r.resource_path,r.source_name,r.path
		FROM resources r INDEXED BY idx_res_path JOIN files f ON f.id=r.file_id
		WHERE f.overridden=0 AND r.resource_path>=? AND r.resource_path<?
		ORDER BY CASE WHEN r.resource_path=? THEN 0 ELSE 1 END,r.source_rank,r.resource_path LIMIT ?`
	args := []any{query, query + "\uffff", query, limit}
	if opts.Source != "" || opts.PathPrefix != "" {
		sqlText = `SELECT r.kind,r.resource_path,r.source_name,r.path FROM resources r INDEXED BY idx_res_path JOIN files f ON f.id=r.file_id WHERE f.overridden=0 AND r.resource_path>=? AND r.resource_path<? AND (?='' OR r.source_name=?) AND (?='' OR f.rel_path LIKE ? ESCAPE '\') ORDER BY CASE WHEN r.resource_path=? THEN 0 ELSE 1 END,r.source_rank,r.resource_path LIMIT ?`
		args = []any{query, query + "\uffff", opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix) + "%", query, limit}
	}
	rows, err := db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		ev.Kind = "resource"
		if err := rows.Scan(&ev.Type, &ev.Name, &ev.Source, &ev.Path); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchDiagnostics(ctx context.Context, query, prefix string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	sqlText := `SELECT d.code,COALESCE(f.source_name,''),COALESCE(d.path,''),COALESCE(d.line,0),COALESCE(d.col,0),d.severity,d.message
		FROM diagnostics d LEFT JOIN files f ON f.id=d.file_id
		WHERE d.code>=? AND d.code<?
		ORDER BY CASE WHEN d.code=? THEN 0 ELSE 1 END,d.code LIMIT ?`
	args := []any{query, query + "\uffff", query, limit}
	if opts.Source != "" || opts.PathPrefix != "" {
		sqlText = `SELECT d.code,COALESCE(f.source_name,''),COALESCE(d.path,''),COALESCE(d.line,0),COALESCE(d.col,0),d.severity,d.message FROM diagnostics d LEFT JOIN files f ON f.id=d.file_id WHERE d.code>=? AND d.code<? AND (?='' OR f.source_name=?) AND (?='' OR f.rel_path LIKE ? ESCAPE '\') ORDER BY CASE WHEN d.code=? THEN 0 ELSE 1 END,d.code LIMIT ?`
		args = []any{query, query + "\uffff", opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix) + "%", query, limit}
	}
	rows, err := db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		var severity, message string
		ev.Kind = "diagnostic"
		if err := rows.Scan(&ev.Name, &ev.Source, &ev.Path, &ev.Line, &ev.Column, &severity, &message); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		ev.Detail = severity + ": " + message
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (db *DB) searchScriptKeys(ctx context.Context, query, prefix string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	sqlText := `SELECT o.field,o.object_type,o.object_name,o.source_name,o.path,o.line,o.raw
		FROM object_fields o INDEXED BY idx_object_fields_field JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0 AND o.field>=? AND o.field<?
		ORDER BY CASE WHEN o.field=? THEN 0 ELSE 1 END,o.source_rank,o.field LIMIT ?`
	args := []any{query, query + "\uffff", query, limit}
	if opts.Source != "" || opts.PathPrefix != "" {
		sqlText = `SELECT o.field,o.object_type,o.object_name,o.source_name,o.path,o.line,o.raw FROM object_fields o INDEXED BY idx_object_fields_field JOIN files f ON f.id=o.file_id WHERE f.overridden=0 AND o.field>=? AND o.field<? AND (?='' OR o.source_name=?) AND (?='' OR f.rel_path LIKE ? ESCAPE '\') ORDER BY CASE WHEN o.field=? THEN 0 ELSE 1 END,o.source_rank,o.field LIMIT ?`
		args = []any{query, query + "\uffff", opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix) + "%", query, limit}
	}
	rows, err := db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var ev LLMEvidence
		var objectType, objectName, raw string
		ev.Kind = "script_key"
		if err := rows.Scan(&ev.Name, &objectType, &objectName, &ev.Source, &ev.Path, &ev.Line, &raw); err != nil {
			return nil, err
		}
		ev.Path = evidencePath(ev.Path)
		ev.Type = objectType
		ev.Detail = objectName + ": " + trimText(raw, 180)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
