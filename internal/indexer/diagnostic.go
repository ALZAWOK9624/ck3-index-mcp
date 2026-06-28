package indexer

import "context"

func (db *DB) ExplainDiagnostic(ctx context.Context, code string) ([]Diagnostic, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT source,severity,code,message,COALESCE(path,''),COALESCE(line,0),COALESCE(col,0)
		FROM diagnostics WHERE code=? ORDER BY severity,path,line LIMIT 200`, code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
