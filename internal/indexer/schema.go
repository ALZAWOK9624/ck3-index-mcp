package indexer

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
)

var infoFieldLine = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_]+)\s*=\s*`)

var ignoredInfoFields = map[string]bool{
	"eg": true, "example": true, "x": true,
}

type RuleQuery struct {
	Type   string      `json:"type"`
	Fields []RuleField `json:"fields"`
}

type RuleField struct {
	Field  string `json:"field"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Raw    string `json:"raw"`
}

type PatternQuery struct {
	Type   string         `json:"type"`
	Fields []PatternField `json:"fields"`
}

type PatternField struct {
	Field  string `json:"field"`
	Shape  string `json:"shape"`
	Count  int    `json:"count"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Raw    string `json:"raw"`
}

func scanSchema(ctx context.Context, tx *sql.Tx, rec fileRecord) (int, error) {
	typ := objectTypeForPath(strings.ToLower(rec.RelPath))
	if typ == "" && strings.Contains(strings.ToLower(rec.RelPath), "events/") {
		typ = "event"
	}
	if typ == "" {
		return 0, nil
	}
	f, err := os.Open(rec.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	count := 0
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		m := infoFieldLine.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		field := m[1]
		lower := strings.ToLower(field)
		if ignoredInfoFields[lower] || strings.Contains(field, "X") {
			continue
		}
		key := typ + "\x00" + field
		if seen[key] {
			continue
		}
		seen[key] = true
		_, err := tx.ExecContext(ctx, `INSERT INTO schema_fields(object_type,field,file_id,source_name,source_rank,path,line,raw) VALUES(?,?,?,?,?,?,?,?)`,
			typ, field, rec.ID, rec.SourceName, rec.SourceRank, rec.Path, line, strings.TrimSpace(raw))
		if err != nil {
			return count, err
		}
		count++
	}
	return count, sc.Err()
}

func (db *DB) QueryRules(ctx context.Context, typ string) (RuleQuery, error) {
	q := RuleQuery{Type: typ}
	rows, err := db.sql.QueryContext(ctx, `SELECT s.field,s.source_name,s.path,s.line,s.raw
		FROM schema_fields s JOIN files f ON f.id=s.file_id
		WHERE s.object_type=? AND f.overridden=0
		ORDER BY s.source_rank,s.path,s.line,s.field`, typ)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var field RuleField
		if err := rows.Scan(&field.Field, &field.Source, &field.Path, &field.Line, &field.Raw); err != nil {
			return q, err
		}
		q.Fields = append(q.Fields, field)
	}
	return q, rows.Err()
}

func (db *DB) QueryPatterns(ctx context.Context, typ string) (PatternQuery, error) {
	q := PatternQuery{Type: typ}
	rows, err := db.sql.QueryContext(ctx, `SELECT of.field,of.value_shape,COUNT(*)
		FROM object_fields of JOIN files f ON f.id=of.file_id
		WHERE of.object_type=? AND f.overridden=0
		GROUP BY of.field,of.value_shape
		ORDER BY COUNT(*) DESC,of.field,of.value_shape
		LIMIT 200`, typ)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var item PatternField
		if err := rows.Scan(&item.Field, &item.Shape, &item.Count); err != nil {
			return q, err
		}
		if err := db.sql.QueryRowContext(ctx, `SELECT of.source_name,of.path,of.line,of.raw
			FROM object_fields of JOIN files f ON f.id=of.file_id
			WHERE of.object_type=? AND of.field=? AND of.value_shape=? AND f.overridden=0
			ORDER BY of.source_rank,of.path,of.line
			LIMIT 1`, typ, item.Field, item.Shape).Scan(&item.Source, &item.Path, &item.Line, &item.Raw); err != nil && err != sql.ErrNoRows {
			return q, err
		}
		q.Fields = append(q.Fields, item)
	}
	return q, rows.Err()
}
