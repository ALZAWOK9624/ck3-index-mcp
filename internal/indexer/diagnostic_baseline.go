package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
	// Existed answers the question a count of zero cannot: whether the name
	// was recorded at all. It is always serialized, because on clear its false
	// value is the whole answer and an omitted field would leave the caller
	// reading a successful deletion of nothing.
	Existed bool `json:"existed"`
}

const defaultDiagnosticBaselineName = "default"

// Baseline writes must follow the current publication pointer while holding
// the same anchor lock as refresh. MCP query handles are read-only leases.
func UpdateDiagnosticBaseline(ctx context.Context, cfg Config, name string, clear bool) (DiagnosticBaseline, error) {
	anchor, err := ConfiguredDatabaseAnchorPath(cfg)
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	lock, err := acquirePublicationLock(ctx, anchor)
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	defer lock.Close()
	path, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	db, err := OpenWithOptions(path, cfg.SQLiteReadOptions())
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		return DiagnosticBaseline{}, err
	}
	if clear {
		return db.ClearDiagnosticBaseline(ctx, name)
	}
	return db.SaveDiagnosticBaseline(ctx, name)
}

// Copy caller decisions from the live workspace, never from an upstream seed.
// The caller holds the publication lock and a transaction on the staged DB.
func copyPreservedPublicationTables(ctx context.Context, dst *sql.Conn, livePath string) error {
	var live *DB
	if _, err := os.Stat(livePath); err == nil {
		live, err = OpenReadOnly(livePath)
		if err != nil {
			return err
		}
		defer live.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	for table := range rebuildPreservedTables {
		if _, err := dst.ExecContext(ctx, `DELETE FROM `+quoteSQLiteIdentifier(table)); err != nil {
			return err
		}
		if live == nil || !live.tableExists(ctx, table) {
			continue
		}
		rows, err := live.sql.QueryContext(ctx, `SELECT * FROM `+quoteSQLiteIdentifier(table))
		if err != nil {
			return err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		quoted := make([]string, len(cols))
		placeholders := make([]string, len(cols))
		for i, col := range cols {
			quoted[i] = quoteSQLiteIdentifier(col)
			placeholders[i] = "?"
		}
		stmt, err := dst.PrepareContext(ctx, `INSERT INTO `+quoteSQLiteIdentifier(table)+`(`+strings.Join(quoted, ",")+`) VALUES(`+strings.Join(placeholders, ",")+`)`)
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			values := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				break
			}
			if _, err = stmt.ExecContext(ctx, values...); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		stmt.Close()
		if err != nil {
			return err
		}
	}
	// Adopt older snapshots that recorded only entry rows.
	_, err := dst.ExecContext(ctx, `INSERT OR IGNORE INTO diagnostic_baseline_snapshots(name,created_at) SELECT name,COALESCE(MAX(created_at),'') FROM diagnostic_baselines GROUP BY name`)
	return err
}

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
	// The snapshot row is what makes the name exist. Recording it independently
	// of the entries is what lets a clean project record "nothing is wrong here
	// yet" and have that decision survive as a real baseline holding zero
	// findings.
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_baseline_snapshots(name,created_at) VALUES(?,?)
		ON CONFLICT(name) DO UPDATE SET created_at=excluded.created_at`, key, recorded); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('baseline_revision','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(meta.value AS INTEGER)+1 AS TEXT)`); err != nil {
		return DiagnosticBaseline{}, err
	}
	if err := tx.Commit(); err != nil {
		return DiagnosticBaseline{}, err
	}
	return DiagnosticBaseline{Name: key, Count: len(unique), CreatedAt: recorded, Existed: true}, nil
}

// ListDiagnosticBaselines reports every recorded snapshot, newest first. The
// listing is driven by the snapshot rows rather than by the findings, so a
// baseline that recorded zero findings is still listed as the record it is.
func (db *DB) ListDiagnosticBaselines(ctx context.Context) ([]DiagnosticBaseline, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT s.name,
			(SELECT COUNT(*) FROM diagnostic_baselines e WHERE e.name=s.name),
			s.created_at
		FROM diagnostic_baseline_snapshots s`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiagnosticBaseline
	for rows.Next() {
		b := DiagnosticBaseline{Existed: true}
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
// held. Existed is what distinguishes a real deletion from a name that was
// never recorded, which the count cannot: a baseline taken on a clean project
// legitimately holds nothing.
func (db *DB) ClearDiagnosticBaseline(ctx context.Context, name string) (DiagnosticBaseline, error) {
	key := normalizeBaselineName(name)
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return DiagnosticBaseline{}, err
	}
	defer tx.Rollback()
	var created sql.NullString
	existed := true
	switch err := tx.QueryRowContext(ctx, `SELECT created_at FROM diagnostic_baseline_snapshots WHERE name=?`, key).Scan(&created); {
	case errors.Is(err, sql.ErrNoRows):
		existed = false
	case err != nil:
		return DiagnosticBaseline{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnostic_baselines WHERE name=?`, key).Scan(&count); err != nil {
		return DiagnosticBaseline{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostic_baselines WHERE name=?`, key); err != nil {
		return DiagnosticBaseline{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostic_baseline_snapshots WHERE name=?`, key); err != nil {
		return DiagnosticBaseline{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('baseline_revision','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(meta.value AS INTEGER)+1 AS TEXT)`); err != nil {
		return DiagnosticBaseline{}, err
	}
	if err := tx.Commit(); err != nil {
		return DiagnosticBaseline{}, err
	}
	return DiagnosticBaseline{Name: key, Count: count, CreatedAt: created.String, Existed: existed}, nil
}

// diagnosticBaselineSet loads one snapshot for filtering. An unknown name is an
// error rather than an empty set: silently reporting every finding when the
// caller asked for only the new ones would read as "nothing regressed".
//
// Existence is decided by the snapshot row. A baseline recorded on a clean
// project holds no fingerprints and still exists -- and it is the most useful
// one there is, because every finding that appears afterwards is new by
// construction.
func (db *DB) diagnosticBaselineSet(ctx context.Context, name string) (map[string]bool, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	key := normalizeBaselineName(name)
	var recorded string
	switch err := db.sql.QueryRowContext(ctx, `SELECT created_at FROM diagnostic_baseline_snapshots WHERE name=?`, key).Scan(&recorded); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("diagnostic baseline %q does not exist; record it first with operation=baseline_save", key)
	case err != nil:
		return nil, err
	}
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
