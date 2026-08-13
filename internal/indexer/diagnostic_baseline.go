package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// A diagnostic baseline is a recorded set of findings that the caller has
// decided not to look at again. A mod built on top of an upstream project
// inherits every diagnostic the upstream already carries, and those drown the
// handful the current work introduced; recording them once turns the next
// report into "what did I just break" instead of "what is wrong with the whole
// workspace".
//
// The table is deliberately absent from semanticIndexTableCatalog. That
// catalog is what a full rebuild drops and republishes, and a baseline is a
// caller's stated intent rather than derived index data — it has to outlive the
// rebuild that reproduces the findings it was taken against.

// DiagnosticBaseline describes one recorded snapshot.
type DiagnosticBaseline struct {
	Name      string `json:"name"`
	Count     int    `json:"count"`
	CreatedAt string `json:"created_at"`
}

const defaultDiagnosticBaselineName = "default"

func normalizeBaselineName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return defaultDiagnosticBaselineName
	}
	return trimmed
}

// diagnosticBaselineKey identifies a finding across rebuilds. Diagnostic rows
// carry no stable identity — ids are reassigned on every scan and most rows
// leave fingerprint empty for the reader to derive — so the key is derived the
// same way here as everywhere else that compares findings over time.
func diagnosticBaselineKey(d Diagnostic) string {
	fingerprint := d.Fingerprint
	if fingerprint == "" {
		fingerprint = diagnosticFingerprint(d)
	}
	return d.Source + "\x00" + fingerprint
}

func (db *DB) allDiagnosticsForBaseline(ctx context.Context) ([]Diagnostic, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT source,severity,code,message,COALESCE(path,''),COALESCE(line,0),COALESCE(col,0),fingerprint
		FROM diagnostics`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column, &d.Fingerprint); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SaveDiagnosticBaseline records every diagnostic the index currently holds
// under name, replacing any earlier snapshot of the same name.
func (db *DB) SaveDiagnosticBaseline(ctx context.Context, name string) (DiagnosticBaseline, error) {
	key := normalizeBaselineName(name)
	diagnostics, err := db.allDiagnosticsForBaseline(ctx)
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	recorded := time.Now().UTC().Format(time.RFC3339)
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostic_baselines WHERE name=?`, key); err != nil {
		return DiagnosticBaseline{}, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO diagnostic_baselines(name,fingerprint,code,severity,created_at) VALUES(?,?,?,?,?)`)
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	defer stmt.Close()
	unique := map[string]bool{}
	for _, d := range diagnostics {
		fingerprint := diagnosticBaselineKey(d)
		if unique[fingerprint] {
			continue
		}
		unique[fingerprint] = true
		if _, err := stmt.ExecContext(ctx, key, fingerprint, d.Code, d.Severity, recorded); err != nil {
			return DiagnosticBaseline{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return DiagnosticBaseline{}, err
	}
	return DiagnosticBaseline{Name: key, Count: len(unique), CreatedAt: recorded}, nil
}

// ListDiagnosticBaselines reports every recorded snapshot, newest first.
func (db *DB) ListDiagnosticBaselines(ctx context.Context) ([]DiagnosticBaseline, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT name,COUNT(*),MAX(created_at)
		FROM diagnostic_baselines GROUP BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiagnosticBaseline
	for rows.Next() {
		var b DiagnosticBaseline
		if err := rows.Scan(&b.Name, &b.Count, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ClearDiagnosticBaseline forgets one snapshot and reports how many findings it
// held, so a caller can tell a real deletion from a name that never existed.
func (db *DB) ClearDiagnosticBaseline(ctx context.Context, name string) (DiagnosticBaseline, error) {
	key := normalizeBaselineName(name)
	var count int
	var created sql.NullString
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*),MAX(created_at) FROM diagnostic_baselines WHERE name=?`, key).Scan(&count, &created); err != nil {
		return DiagnosticBaseline{}, err
	}
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM diagnostic_baselines WHERE name=?`, key); err != nil {
		return DiagnosticBaseline{}, err
	}
	return DiagnosticBaseline{Name: key, Count: count, CreatedAt: created.String}, nil
}

// diagnosticBaselineSet loads one snapshot for filtering. An unknown name is an
// error rather than an empty set: silently reporting every finding when the
// caller asked for only the new ones would read as "nothing regressed".
func (db *DB) diagnosticBaselineSet(ctx context.Context, name string) (map[string]bool, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	key := normalizeBaselineName(name)
	rows, err := db.sql.QueryContext(ctx, `SELECT fingerprint FROM diagnostic_baselines WHERE name=?`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var fingerprint string
		if err := rows.Scan(&fingerprint); err != nil {
			return nil, err
		}
		set[fingerprint] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("diagnostic baseline %q does not exist; record it first with operation=baseline_save", key)
	}
	return set, nil
}

// filterDiagnosticsAgainstBaseline drops findings the snapshot already held.
func filterDiagnosticsAgainstBaseline(in []Diagnostic, baseline map[string]bool) []Diagnostic {
	if len(baseline) == 0 {
		return in
	}
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		if baseline[diagnosticBaselineKey(d)] {
			continue
		}
		out = append(out, d)
	}
	return out
}

func countDiagnosticsBySeverity(in []Diagnostic) map[string]int {
	counts := map[string]int{}
	for _, d := range in {
		counts[d.Severity] += maxInt(1, d.Occurrences)
	}
	return counts
}
