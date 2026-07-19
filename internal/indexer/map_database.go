package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	MapDatabaseIncompleteCode = "MAP_DATABASE_INCOMPLETE"
	CurrentSchemaVersion      = 1
)

var mapCoreTables = []string{
	"map_provinces",
	"map_adjacencies",
	"map_province_geometry",
	"map_titles",
}

// These tables may legitimately contain zero rows, but every current map tool
// assumes their schema exists. Requiring their presence converts an old or
// partially migrated cache into one clear freshness error instead of a later
// tool-specific "no such table" failure.
var mapRequiredSchemaTables = []string{
	"meta", "files", "localization", "resources",
	"map_title_provinces", "map_title_adjacencies",
	"map_strategic_adjacencies", "map_object_instances",
	"map_water_bodies", "map_water_body_provinces", "map_water_body_shores",
	"map_province_history", "map_title_history",
	"map_characters", "map_character_history",
	"map_holy_sites", "map_holy_site_faiths", "map_province_regions",
	"map_physical_rasters", "map_province_physical",
	"map_physical_water_bodies", "map_physical_water_body_provinces",
	"map_major_river_edges",
	"map_surface_rasters", "map_surface_materials", "map_province_materials",
}

// MapDatabaseStatus describes whether the configured cache has enough indexed
// map data to answer topology questions. It deliberately distinguishes a
// missing cache from a valid map with disconnected components.
type MapDatabaseStatus struct {
	Complete          bool           `json:"complete"`
	ErrorCode         string         `json:"error_code,omitempty"`
	SchemaVersion     int            `json:"schema_version"`
	CoreCounts        map[string]int `json:"core_counts"`
	MissingTables     []string       `json:"missing_tables,omitempty"`
	EmptyTables       []string       `json:"empty_tables,omitempty"`
	GeometryAvailable bool           `json:"geometry_available"`
	IndexRuleVersion  string         `json:"index_rule_version,omitempty"`
	ExpectedVersion   string         `json:"expected_index_rule_version,omitempty"`
	Stale             bool           `json:"stale,omitempty"`
	Finalizing        bool           `json:"scan_finalization_pending,omitempty"`
}

type MapDatabaseError struct {
	Status MapDatabaseStatus
}

func (e *MapDatabaseError) Error() string {
	parts := make([]string, 0, 2)
	if len(e.Status.MissingTables) > 0 {
		parts = append(parts, "missing tables: "+strings.Join(e.Status.MissingTables, ", "))
	}
	if len(e.Status.EmptyTables) > 0 {
		parts = append(parts, "empty tables: "+strings.Join(e.Status.EmptyTables, ", "))
	}
	if e.Status.Stale {
		parts = append(parts, fmt.Sprintf("index rule version %q does not match required %q", e.Status.IndexRuleVersion, e.Status.ExpectedVersion))
	}
	if e.Status.Finalizing {
		parts = append(parts, "the previous scan did not complete its finalization transaction")
	}
	if len(parts) == 0 {
		parts = append(parts, "map cache is unavailable")
	}
	return MapDatabaseIncompleteCode + ": " + strings.Join(parts, "; ")
}

func (db *DB) MapDatabaseStatus(ctx context.Context) (MapDatabaseStatus, error) {
	status := MapDatabaseStatus{SchemaVersion: CurrentSchemaVersion, CoreCounts: map[string]int{}}
	rows, err := db.sql.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return status, err
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return status, err
		}
		have[name] = true
	}
	if err := rows.Close(); err != nil {
		return status, err
	}

	required := append(append([]string(nil), mapCoreTables...), mapRequiredSchemaTables...)
	seenRequired := map[string]bool{}
	for _, table := range required {
		if seenRequired[table] {
			continue
		}
		seenRequired[table] = true
		if !have[table] {
			status.MissingTables = append(status.MissingTables, table)
		}
	}
	for _, table := range mapCoreTables {
		if !have[table] {
			continue
		}
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return status, err
		}
		status.CoreCounts[table] = count
		if count == 0 {
			status.EmptyTables = append(status.EmptyTables, table)
		}
	}
	sort.Strings(status.MissingTables)
	if have["meta"] {
		_ = db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='index_rule_version'`).Scan(&status.IndexRuleVersion)
		var scanStatus string
		_ = db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='scan_status'`).Scan(&scanStatus)
		status.Finalizing = scanStatus == "finalizing"
	}
	status.ExpectedVersion = indexRuleVersion
	hasMapRows := false
	for _, count := range status.CoreCounts {
		if count > 0 {
			hasMapRows = true
			break
		}
	}
	status.Stale = hasMapRows && status.IndexRuleVersion != indexRuleVersion
	status.GeometryAvailable = status.CoreCounts["map_province_geometry"] > 0
	status.Complete = len(status.MissingTables) == 0 && len(status.EmptyTables) == 0 && !status.Stale && !status.Finalizing
	if !status.Complete {
		status.ErrorCode = MapDatabaseIncompleteCode
	}
	return status, nil
}

func (db *DB) RequireMapDatabase(ctx context.Context) error {
	status, err := db.MapDatabaseStatus(ctx)
	if err != nil {
		return err
	}
	if !status.Complete {
		return &MapDatabaseError{Status: status}
	}
	return nil
}

func (db *DB) mapDatabaseFingerprint(ctx context.Context, status MapDatabaseStatus) (string, error) {
	var state IndexState
	var geometryFingerprint string
	if db.tableExists(ctx, "meta") {
		var err error
		state, err = db.IndexState(ctx)
		if err != nil {
			return "", err
		}
		geometryFingerprint, err = db.metaValue(ctx, "map_geometry_fingerprint")
		if err != nil {
			return "", err
		}
	}
	keys := make([]string, 0, len(status.CoreCounts))
	for key := range status.CoreCounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var material strings.Builder
	material.WriteString(strconv.Itoa(CurrentSchemaVersion))
	material.WriteByte('\n')
	material.WriteString(indexRuleVersion)
	material.WriteByte('\n')
	material.WriteString(strconv.FormatInt(state.Generation, 10))
	material.WriteByte('\n')
	material.WriteString(geometryFingerprint)
	for _, key := range keys {
		fmt.Fprintf(&material, "\n%s=%d", key, status.CoreCounts[key])
	}
	sum := sha256.Sum256([]byte(material.String()))
	return hex.EncodeToString(sum[:]), nil
}

func sameDatabasePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	left, errLeft := filepath.Abs(a)
	right, errRight := filepath.Abs(b)
	if errLeft != nil || errRight != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
