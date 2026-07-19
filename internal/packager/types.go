package packager

import (
	"context"
	"time"
)

const (
	SchemaVersion         = 1
	DefaultRetentionHours = 7 * 24
)

type Metadata struct {
	Name             string   `json:"name"`
	Slug             string   `json:"slug"`
	Version          string   `json:"version"`
	SupportedVersion string   `json:"supported_version"`
	Tags             []string `json:"tags"`
	Kind             string   `json:"kind,omitempty"`
	Dependencies     []string `json:"dependencies,omitempty"`
	ReplacePaths     []string `json:"replace_paths,omitempty"`
}

type FileInput struct {
	Path          string  `json:"path"`
	Content       *string `json:"content,omitempty"`
	ContentBase64 *string `json:"content_base64,omitempty"`
}

type Request struct {
	Metadata Metadata    `json:"metadata"`
	Files    []FileInput `json:"files"`
}

type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

var MCPLimits = Limits{MaxFiles: 256, MaxFileBytes: 8 << 20, MaxTotalBytes: 32 << 20}
var DirectoryLimits = Limits{MaxFiles: 10000, MaxFileBytes: 512 << 20, MaxTotalBytes: 2 << 30}

type PreparedFile struct {
	Path string
	Data []byte
}

type Diagnostic struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Column     int    `json:"column,omitempty"`
	Confidence string `json:"confidence,omitempty"`
}

type ValidationReport struct {
	Blocked     bool           `json:"blocked"`
	Summary     string         `json:"summary,omitempty"`
	Blockers    int            `json:"blockers"`
	Warnings    int            `json:"warnings"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	MissingLoc  []string       `json:"missing_localization,omitempty"`
	MissingRes  []string       `json:"missing_resources,omitempty"`
	Counts      map[string]int `json:"counts,omitempty"`
	Fixes       []string       `json:"fixes,omitempty"`
}

type Validator interface {
	Validate(context.Context, []PreparedFile) (ValidationReport, error)
}

type BuildOptions struct {
	ArtifactRoot  string
	Retention     time.Duration
	Limits        Limits
	Validator     Validator
	ExcludedFiles []string
}

type Result struct {
	Status          string           `json:"status"`
	ArtifactID      string           `json:"artifact_id,omitempty"`
	ArtifactRelPath string           `json:"artifact_relpath,omitempty"`
	ArchiveName     string           `json:"archive_name,omitempty"`
	SHA256          string           `json:"sha256,omitempty"`
	Size            int64            `json:"size,omitempty"`
	FileCount       int              `json:"file_count,omitempty"`
	ExpiresAt       string           `json:"expires_at,omitempty"`
	Validation      ValidationReport `json:"validation"`
	ExcludedFiles   []string         `json:"excluded_files,omitempty"`
	Repairs         []string         `json:"repairs,omitempty"`
}

type ManifestEntry struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	Metadata      Metadata        `json:"metadata"`
	Files         []ManifestEntry `json:"files"`
	Validation    struct {
		Blockers int    `json:"blockers"`
		Warnings int    `json:"warnings"`
		Summary  string `json:"summary,omitempty"`
	} `json:"validation"`
}
