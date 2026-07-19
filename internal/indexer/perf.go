package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BenchReport struct {
	Tables        map[string]int `json:"tables"`
	Queries       []BenchQuery   `json:"queries"`
	IndexRisks    []string       `json:"index_risks,omitempty"`
	ElapsedMillis int64          `json:"elapsed_ms"`
}

type BenchQuery struct {
	Name          string `json:"name"`
	Sample        string `json:"sample,omitempty"`
	ElapsedMillis int64  `json:"elapsed_ms"`
	ResultCount   int    `json:"result_count,omitempty"`
}

type HealthReport struct {
	Status                string            `json:"status"`
	Database              string            `json:"-"`
	DatabaseMB            float64           `json:"database_mb"`
	DatabaseVersion       string            `json:"database_version,omitempty"`
	DatabaseFingerprint   string            `json:"database_fingerprint,omitempty"`
	AuthoritativeDatabase bool              `json:"authoritative_database"`
	SchemaVersion         int               `json:"schema_version"`
	MapDatabase           MapDatabaseStatus `json:"map_database"`
	Tables                map[string]int    `json:"tables"`
	IndexRuleVersion      string            `json:"index_rule_version,omitempty"`
	ScanGeneration        int64             `json:"scan_generation,omitempty"`
	ScanRevision          string            `json:"scan_revision,omitempty"`
	ScanCommittedAt       string            `json:"scan_committed_at,omitempty"`
	ScanStatus            string            `json:"scan_status,omitempty"`
	MissingIndexes        []string          `json:"missing_indexes,omitempty"`
	WALFiles              []HealthFile      `json:"wal_files,omitempty"`
	MCPConfigured         bool              `json:"mcp_configured"`
	FTS5Available         bool              `json:"fts5_available"`
	GIS                   *GISSidecarStatus `json:"gis,omitempty"`
	Guidance              []string          `json:"guidance,omitempty"`
}

type HealthFile struct {
	// Path is retained for local diagnostics, but must never be serialized by
	// CLI or MCP health responses.
	Path   string  `json:"-"`
	Name   string  `json:"name"`
	Exists bool    `json:"exists"`
	SizeMB float64 `json:"size_mb,omitempty"`
}

func (db *DB) Bench(ctx context.Context) (BenchReport, error) {
	start := time.Now()
	tables, err := db.tableCounts(ctx)
	if err != nil {
		return BenchReport{}, err
	}
	sampleObject, _ := db.firstScalar(ctx, `SELECT o.name
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0
		ORDER BY f.source_rank,o.object_type,o.name LIMIT 1`)
	sampleResource, _ := db.firstScalar(ctx, `SELECT r.resource_path
		FROM resources r JOIN files f ON f.id=r.file_id
		WHERE f.overridden=0
		ORDER BY r.source_rank,r.resource_path LIMIT 1`)
	sampleRegion, _ := db.firstScalar(ctx, `SELECT region_id FROM map_province_regions ORDER BY region_id LIMIT 1`)
	var physicalProvinceCount int
	_ = db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_province_physical`).Scan(&physicalProvinceCount)
	report := BenchReport{Tables: tables}
	report.Queries = append(report.Queries, timeQuery("architecture_overview", "", func() (int, error) {
		q, err := db.LLMArchitectureOverview(ctx, LLMOptions{AllowProject: true})
		return len(q.Evidence), err
	}))
	if sampleObject != "" {
		report.Queries = append(report.Queries, timeQuery("query_object", sampleObject, func() (int, error) {
			q, err := db.QueryObject(ctx, sampleObject)
			return len(q.Definitions), err
		}))
		report.Queries = append(report.Queries, timeQuery("find_refs", sampleObject, func() (int, error) {
			q, err := db.QueryRefs(ctx, sampleObject)
			return len(q.Incoming) + len(q.Outgoing), err
		}))
		report.Queries = append(report.Queries, timeQuery("inspect_object", sampleObject, func() (int, error) {
			q, err := db.LLMInspectObject(ctx, sampleObject, LLMOptions{AllowProject: true})
			return len(q.Evidence), err
		}))
		report.Queries = append(report.Queries, timeQuery("preflight_code", sampleObject, func() (int, error) {
			q, err := db.LLMPreflight(ctx, sampleObject, LLMOptions{AllowProject: true})
			return len(q.Evidence), err
		}))
		report.Queries = append(report.Queries, timeQuery("dependency_graph", sampleObject, func() (int, error) {
			q, err := db.LLMDependencyGraph(ctx, sampleObject, LLMOptions{AllowProject: true, Depth: 1})
			return len(q.Evidence), err
		}))
	}
	if sampleResource != "" {
		report.Queries = append(report.Queries, timeQuery("query_resource", sampleResource, func() (int, error) {
			q, err := db.QueryResource(ctx, sampleResource)
			return len(q.Resources) + len(q.References), err
		}))
	}
	if sampleRegion != "" && physicalProvinceCount > 0 {
		report.Queries = append(report.Queries, timeQuery("physical_region_adjacent_water", sampleRegion, func() (int, error) {
			q, err := db.LLMMapPhysicalContext(ctx, MapPhysicalContextSpec{
				TargetType: "region", Target: sampleRegion, Operation: "oceanography", IncludeAdjacentWater: true,
			}, LLMOptions{AllowProject: true, Limit: 6})
			if q.AdjacentWater == nil {
				return 0, err
			}
			return q.AdjacentWater.WaterProvinceCount, err
		}))
	}
	risks, err := db.queryPlanRisks(ctx, sampleObject)
	if err != nil {
		return BenchReport{}, err
	}
	report.IndexRisks = risks
	report.ElapsedMillis = time.Since(start).Milliseconds()
	return report, nil
}

func timeQuery(name, sample string, fn func() (int, error)) BenchQuery {
	start := time.Now()
	n, err := fn()
	q := BenchQuery{Name: name, Sample: sample, ElapsedMillis: time.Since(start).Milliseconds(), ResultCount: n}
	if err != nil {
		q.ResultCount = -1
	}
	return q
}

func (db *DB) Health(ctx context.Context) (HealthReport, error) {
	return db.health(ctx, "")
}

// HealthConfigured adds the authority decision made from the same parsed
// configuration used to open the CLI or MCP database.
func (db *DB) HealthConfigured(ctx context.Context, cfg Config) (HealthReport, error) {
	configuredPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return HealthReport{}, err
	}
	return db.health(ctx, configuredPath)
}

func (db *DB) health(ctx context.Context, configuredPath string) (HealthReport, error) {
	dbPath := db.path
	mapStatus, err := db.MapDatabaseStatus(ctx)
	if err != nil {
		return HealthReport{}, err
	}
	tables, err := db.tableCounts(ctx)
	if err != nil {
		return HealthReport{}, err
	}
	var version string
	if db.tableExists(ctx, "meta") {
		version, err = db.metaValue(ctx, "index_rule_version")
		if err != nil {
			return HealthReport{}, err
		}
	}
	missing, err := db.missingPerformanceIndexes(ctx)
	if err != nil {
		return HealthReport{}, err
	}
	fingerprint, err := db.mapDatabaseFingerprint(ctx, mapStatus)
	if err != nil {
		return HealthReport{}, err
	}
	report := HealthReport{
		Status:                "ok",
		Database:              dbPath,
		DatabaseMB:            fileSizeMB(dbPath),
		DatabaseVersion:       version,
		DatabaseFingerprint:   fingerprint,
		AuthoritativeDatabase: sameDatabasePath(dbPath, configuredPath),
		SchemaVersion:         CurrentSchemaVersion,
		MapDatabase:           mapStatus,
		Tables:                tables,
		IndexRuleVersion:      version,
		MissingIndexes:        missing,
		WALFiles: []HealthFile{
			{Name: "wal", Path: dbPath + "-wal", Exists: fileExists(dbPath + "-wal"), SizeMB: fileSizeMB(dbPath + "-wal")},
			{Name: "shm", Path: dbPath + "-shm", Exists: fileExists(dbPath + "-shm"), SizeMB: fileSizeMB(dbPath + "-shm")},
		},
		MCPConfigured: codexMCPConfigured(),
	}
	if db.tableExists(ctx, "meta") {
		state, err := db.IndexState(ctx)
		if err != nil {
			return HealthReport{}, err
		}
		report.ScanGeneration = state.Generation
		report.ScanRevision = state.Revision
		report.ScanCommittedAt = state.CommittedAt
		report.ScanStatus = state.Status
		if !state.Ready() {
			if report.Status != "error" {
				report.Status = "degraded"
			}
			report.Guidance = append(report.Guidance, "Index publication is not ready; index-backed queries will wait until a complete scan generation is available.")
		}
	}
	if !mapStatus.Complete {
		report.Status = "error"
		report.Guidance = append(report.Guidance, "MAP_DATABASE_INCOMPLETE: the configured cache does not contain the required map topology and geometry tables; run a full scan against the configured database.")
	}
	if configuredPath != "" && !report.AuthoritativeDatabase {
		report.Status = "error"
		report.Guidance = append(report.Guidance, "The opened database is not the database selected by the active configuration.")
	}
	if db.tableExists(ctx, "search_fts") && db.sql.QueryRowContext(ctx, `SELECT count(*) FROM search_fts WHERE search_fts MATCH 'ck3indexhealthtoken'`).Scan(new(int)) == nil {
		report.FTS5Available = true
	} else {
		report.Status = "error"
		report.Guidance = append(report.Guidance, "FTS5 is unavailable or the semantic index is missing; rebuild with a SQLite build that includes FTS5.")
	}
	if len(missing) > 0 {
		if report.Status != "error" {
			report.Status = "warning"
		}
		report.Guidance = append(report.Guidance, "Run ck3-index scan or health again after this binary update to ensure all performance indexes exist.")
	}
	if version != indexRuleVersion {
		if report.Status != "error" {
			report.Status = "warning"
		}
		report.Guidance = append(report.Guidance, "Index rule version differs from the binary; run ck3-index scan to refresh diagnostics.")
	}
	walMB := report.WALFiles[0].SizeMB
	if walHealthDegraded(report.DatabaseMB, walMB) {
		if report.Status != "error" {
			report.Status = "degraded"
		}
		report.Guidance = append(report.Guidance, fmt.Sprintf("SQLite WAL is unusually large (%.1f MB); a long-lived reader may be delaying checkpoints. MCP remains read-only and will not checkpoint automatically.", walMB))
	}
	if !report.MCPConfigured {
		if report.Status == "ok" {
			report.Status = "warning"
		}
		report.Guidance = append(report.Guidance, "Codex MCP server ck3_index is not present in the local Codex config.")
	}
	if report.Status == "ok" {
		report.Guidance = []string{"Index cache and performance indexes are healthy; ck3_index MCP configuration is present. Configuration alone does not prove that the current model session attached the server."}
	}
	return report, nil
}

func walHealthDegraded(databaseMB, walMB float64) bool {
	return walMB > 256 || (databaseMB > 0 && walMB > databaseMB*0.20)
}

func (db *DB) tableCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, table := range []string{"files", "objects", "refs", "localization", "resources", "schema_fields", "object_fields", "diagnostics", "engine_datatypes", "engine_scope_rules", "search_fts"} {
		if !db.tableExists(ctx, table) {
			out[table] = 0
			continue
		}
		var n int
		if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}

func (db *DB) tableExists(ctx context.Context, table string) bool {
	var one int
	return db.sql.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type IN ('table','view') AND name=?`, table).Scan(&one) == nil
}

func (db *DB) firstScalar(ctx context.Context, query string, args ...any) (string, error) {
	var s sql.NullString
	err := db.sql.QueryRowContext(ctx, query, args...).Scan(&s)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil || !s.Valid {
		return "", err
	}
	return s.String, nil
}

func (db *DB) missingPerformanceIndexes(ctx context.Context) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='index'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		have[name] = true
	}
	required := []string{
		"idx_objects_name", "idx_objects_type_name", "idx_objects_file_id",
		"idx_refs_name", "idx_refs_kind_name", "idx_refs_from_name", "idx_refs_from_type_name", "idx_refs_file_id",
		"idx_loc_key", "idx_loc_file_id",
		"idx_res_path", "idx_res_path_rank", "idx_res_file_id",
		"idx_schema_file_id", "idx_object_fields_file_id", "idx_diag_file_id",
		"idx_scope_file_id", "idx_var_file_id",
	}
	var missing []string
	for _, name := range required {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	return missing, rows.Err()
}

func (db *DB) queryPlanRisks(ctx context.Context, sample string) ([]string, error) {
	if sample == "" {
		return nil, nil
	}
	checks := []struct {
		name  string
		query string
		args  []any
	}{
		{"refs_incoming", `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE r.ref_name=? AND f.overridden=0
			ORDER BY f.source_rank,f.path,r.line LIMIT 500`, []any{sample}},
		{"refs_outgoing", `SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE r.from_object_name=? AND f.overridden=0
			ORDER BY f.source_rank,f.path,r.line LIMIT 500`, []any{sample}},
		{"object", `SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line,o.col
			FROM objects o JOIN files f ON f.id=o.file_id
			WHERE o.name=? AND f.overridden=0
			ORDER BY o.object_type,o.name,o.source_rank`, []any{sample}},
	}
	var risks []string
	for _, check := range checks {
		rows, err := db.sql.QueryContext(ctx, "EXPLAIN QUERY PLAN "+check.query, check.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				rows.Close()
				return nil, err
			}
			if strings.Contains(detail, "SCAN r") || strings.Contains(detail, "SCAN o") {
				risks = append(risks, check.name+": "+detail)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return risks, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSizeMB(path string) float64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(info.Size()) / 1024 / 1024
}

func codexMCPConfigured() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		return false
	}
	text := string(data)
	if strings.Contains(text, "[mcp_servers.ck3_index]") {
		return true
	}
	return strings.Contains(text, `[plugins."ck3-index@personal"]`) && strings.Contains(text, "enabled = true")
}
