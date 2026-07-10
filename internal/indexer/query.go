package indexer

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
)

type ObjectQuery struct {
	Query       string      `json:"query"`
	Definitions []ObjectDef `json:"definitions"`
	Overrides   []ObjectDef `json:"overrides"`
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
	Type   string `json:"type"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Rank   int    `json:"rank"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

func (db *DB) QueryObject(ctx context.Context, id string) (ObjectQuery, error) {
	q := ObjectQuery{Query: id}
	typ, name, typed := splitTypedID(id)
	sqlText := `SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line,o.col
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE o.name=? AND f.overridden=0
		ORDER BY o.object_type,o.name,o.source_rank`
	args := []any{id}
	if typed {
		sqlText = `SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line,o.col
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
		if err := rows.Scan(&d.Type, &d.Name, &d.Source, &d.Rank, &d.Path, &d.Line, &d.Column); err != nil {
			return q, err
		}
		q.Definitions = append(q.Definitions, d)
	}
	if err := rows.Err(); err != nil {
		return q, err
	}
	q.Overrides = make([]ObjectDef, len(q.Definitions))
	copy(q.Overrides, q.Definitions)
	return q, nil
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
	FromType string `json:"from_type,omitempty"`
	FromName string `json:"from_name,omitempty"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Raw      string `json:"raw"`
	Resolved bool   `json:"resolved,omitempty"`
	Source   string `json:"source,omitempty"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
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
	inSQL := `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
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
	outSQL := `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
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
	err := rows.Scan(&ft, &fn, &h.Kind, &h.Name, &h.Raw, &resolved, &h.Source, &h.Path, &h.Line, &h.Column)
	if ft.Valid {
		h.FromType = ft.String
	}
	if fn.Valid {
		h.FromName = fn.String
	}
	h.Resolved = resolved != 0
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
