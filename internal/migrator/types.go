package migrator

import (
	"time"

	"ck3-index/internal/indexer"
)

const SchemaVersion = 1

type SnapshotSpec struct {
	Project string `json:"project"`
	Base    string `json:"base"`
}

type SnapshotResult struct {
	Status            string `json:"status"`
	SnapshotID        string `json:"snapshot_id,omitempty"`
	Project           string `json:"project,omitempty"`
	Base              string `json:"base,omitempty"`
	FileCount         int    `json:"file_count,omitempty"`
	BaselineBlobCount int    `json:"baseline_blob_count,omitempty"`
	StoredBytes       int64  `json:"stored_bytes,omitempty"`
	ActiveMapSHA256   string `json:"active_map_sha256,omitempty"`
	SnapshotRelPath   string `json:"snapshot_relpath,omitempty"`
	Reused            bool   `json:"reused,omitempty"`
}

type Resolution struct {
	ConflictID      string `json:"conflict_id,omitempty"`
	SourceProvince  int    `json:"source_province,omitempty"`
	Action          string `json:"action"`
	TargetProvinces []int  `json:"target_provinces,omitempty"`
	AllowTypeChange bool   `json:"allow_type_change,omitempty"`
}

type MigrationSpec struct {
	SnapshotID    string                    `json:"snapshot_id"`
	Target        string                    `json:"target"`
	OutputName    string                    `json:"output_name,omitempty"`
	ControlPoints []indexer.MapControlPoint `json:"control_points,omitempty"`
	Resolutions   []Resolution              `json:"resolutions,omitempty"`
	DeletePaths   []string                  `json:"delete_paths,omitempty"`
}

type Conflict struct {
	ID              string `json:"conflict_id"`
	Code            string `json:"code"`
	Message         string `json:"message"`
	Path            string `json:"path,omitempty"`
	Line            int    `json:"line,omitempty"`
	SourceProvince  int    `json:"source_province,omitempty"`
	Severity        string `json:"severity"`
	SuggestedAction string `json:"suggested_action,omitempty"`
}

type FileResult struct {
	Path         string `json:"path"`
	Origin       string `json:"origin"`
	Merge        string `json:"merge,omitempty"`
	Replacements int    `json:"replacements,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Size         int64  `json:"size,omitempty"`
}

type Validation struct {
	Blocked           bool           `json:"blocked"`
	ParseErrors       int            `json:"parse_errors"`
	MissingTargets    int            `json:"missing_targets"`
	DuplicateHistory  int            `json:"duplicate_history"`
	DuplicateTerrain  int            `json:"duplicate_terrain"`
	DuplicateBarony   int            `json:"duplicate_barony"`
	InvalidAdjacency  int            `json:"invalid_adjacency"`
	InvalidDefaultMap int            `json:"invalid_default_map"`
	InvalidRegion     int            `json:"invalid_region"`
	SuspiciousNumeric int            `json:"suspicious_numeric"`
	PreflightCounts   map[string]int `json:"preflight_counts,omitempty"`
	MapAuditErrors    int            `json:"map_audit_errors"`
	MapAuditWarnings  int            `json:"map_audit_warnings"`
}

type MigrationResult struct {
	Status           string                            `json:"status"`
	ArtifactID       string                            `json:"artifact_id,omitempty"`
	ArtifactRelPath  string                            `json:"artifact_relpath,omitempty"`
	OutputName       string                            `json:"output_name,omitempty"`
	SnapshotID       string                            `json:"snapshot_id"`
	Target           string                            `json:"target"`
	ExpiresAt        string                            `json:"expires_at,omitempty"`
	Mapping          indexer.MapProvinceMappingSummary `json:"mapping"`
	ChangedFiles     int                               `json:"changed_files"`
	ReplacementCount int                               `json:"replacement_count"`
	ConflictCount    int                               `json:"conflict_count"`
	Conflicts        []Conflict                        `json:"conflicts,omitempty"`
	Diagnostics      []Conflict                        `json:"diagnostics,omitempty"`
	Files            []FileResult                      `json:"files,omitempty"`
	ExcludedFiles    []string                          `json:"excluded_files,omitempty"`
	Validation       Validation                        `json:"validation"`
	Guidance         []string                          `json:"guidance"`
}

type BuildOptions struct {
	ArtifactRoot string
	OutputDir    string
	Retention    time.Duration
	DB           *indexer.DB
}

type SnapshotManifest struct {
	SchemaVersion   int            `json:"schema_version"`
	Project         string         `json:"project"`
	Base            string         `json:"base"`
	ProjectFiles    []SnapshotFile `json:"project_files"`
	BaseFiles       []SnapshotFile `json:"base_files"`
	ActiveMapFiles  []SnapshotFile `json:"active_map_files"`
	ActiveMapSHA256 string         `json:"active_map_sha256"`
}

type SnapshotFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Text   bool   `json:"text,omitempty"`
	Blob   string `json:"blob,omitempty"`
}
