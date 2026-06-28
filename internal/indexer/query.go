package indexer

import (
	"context"
	"database/sql"
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
	rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line,o.col
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE (o.name=? OR o.object_type || ':' || o.name=?) AND f.overridden=0
		ORDER BY o.object_type,o.name,o.source_rank`, id, id)
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
	Query    string   `json:"query"`
	Incoming []RefHit `json:"incoming"`
	Outgoing []RefHit `json:"outgoing"`
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
	in, err := db.sql.QueryContext(ctx, `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE (r.ref_name=? OR r.ref_kind || ':' || r.ref_name=?)
		AND f.overridden=0
		ORDER BY f.source_rank,f.path,r.line LIMIT 500`, id, id)
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
	out, err := db.sql.QueryContext(ctx, `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE (r.from_object_name=? OR r.from_object_type || ':' || r.from_object_name=?)
		AND f.overridden=0
		ORDER BY f.source_rank,f.path,r.line LIMIT 500`, id, id)
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
	return q, out.Err()
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
	needle := "%" + strings.ReplaceAll(filepathSlash(id), "*", "%") + "%"
	rows, err := db.sql.QueryContext(ctx, `SELECT r.resource_path,r.kind,r.source_name,r.source_rank,r.path
		FROM resources r JOIN files f ON f.id=r.file_id
		WHERE r.resource_path LIKE ? AND f.overridden=0
		ORDER BY r.source_rank,r.path LIMIT 200`, needle)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var h ResourceHit
		if err := rows.Scan(&h.ResourcePath, &h.Kind, &h.Source, &h.Rank, &h.Path); err != nil {
			return q, err
		}
		q.Resources = append(q.Resources, h)
	}
	refs, err := db.QueryRefs(ctx, id)
	if err == nil {
		q.References = refs.Incoming
	}
	return q, rows.Err()
}

func filepathSlash(s string) string {
	return strings.ReplaceAll(s, "\\", "/")
}

type ValidationReport struct {
	Diagnostics []Diagnostic   `json:"diagnostics"`
	Counts      map[string]int `json:"counts"`
}

type Diagnostic struct {
	Source   string `json:"source"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

func (db *DB) Validate(ctx context.Context) (ValidationReport, error) {
	if err := db.runCompilerChecks(ctx); err != nil {
		return ValidationReport{}, err
	}
	return db.CachedValidation(ctx)
}

func (db *DB) CachedValidation(ctx context.Context) (ValidationReport, error) {
	rep := ValidationReport{Counts: map[string]int{}}
	countRows, err := db.sql.QueryContext(ctx, `SELECT severity,COUNT(*) FROM diagnostics GROUP BY severity`)
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
	rows, err := db.sql.QueryContext(ctx, `SELECT source,severity,code,message,COALESCE(path,''),COALESCE(line,0),COALESCE(col,0)
		FROM diagnostics ORDER BY CASE severity WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, path,line LIMIT 2000`)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column); err != nil {
			return rep, err
		}
		rep.Diagnostics = append(rep.Diagnostics, d)
	}
	return rep, rows.Err()
}
