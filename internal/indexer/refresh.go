package indexer

import (
	"context"
	"errors"
	"os"
	"time"
)

// RefreshProjectStatus intentionally reports source state without returning a
// physical path. MCP callers need to know whether a project can be refreshed,
// not where it resides on the host.
type RefreshProjectStatus struct {
	Configured  bool `json:"configured"`
	Accessible  bool `json:"accessible"`
	Writable    bool `json:"writable"`
	Refreshable bool `json:"refreshable"`
	Private     bool `json:"private"`
}

type RefreshEngineStatus struct {
	Available bool `json:"available"`
	Current   bool `json:"current"`
}

// RefreshScanError is a durable, path-free scan failure marker. It is kept in
// the rebuildable cache so `ck3_refresh status` survives an MCP process
// restart without exposing a source path or raw parser error.
type RefreshScanError struct {
	Code string `json:"code"`
	At   string `json:"at"`
}

// RefreshStatus is a read-only preflight for the MCP refresh operations. It
// tells clients why incremental files is unavailable without initiating a
// partial full scan or disclosing host paths.
type RefreshStatus struct {
	Status            string               `json:"status"`
	Index             IndexState           `json:"index"`
	Project           RefreshProjectStatus `json:"project"`
	EngineRules       RefreshEngineStatus  `json:"engine_rules"`
	LastScanError     *RefreshScanError    `json:"last_scan_error,omitempty"`
	NeedsFullScan     bool                 `json:"needs_full_scan"`
	FullScanAvailable bool                 `json:"full_scan_available"`
	FullScanGuidance  string               `json:"full_scan_guidance,omitempty"`
}

func (db *DB) RefreshStatus(ctx context.Context, cfg Config) (RefreshStatus, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return RefreshStatus{}, err
	}
	project, err := ProjectSource(normalized)
	if err != nil {
		return RefreshStatus{}, err
	}
	status := RefreshStatus{
		Project:           RefreshProjectStatus{Configured: true, Private: project.Private},
		FullScanAvailable: true,
		FullScanGuidance:  "Call ck3_refresh with operation=full to rebuild in a staged cache. The previous ready generation remains readable until the replacement commits.",
	}
	if info, rootErr := os.Stat(project.Path); rootErr == nil && info.IsDir() {
		status.Project.Accessible = validateSourceRoots([]Source{project}) == nil
		status.Project.Writable = info.Mode().Perm()&0222 != 0
		status.Project.Refreshable = status.Project.Accessible
	}
	state, err := db.IndexState(ctx)
	if err != nil {
		return RefreshStatus{}, err
	}
	status.Index = state
	lastError, lastErrorErr := db.lastScanError(ctx)
	if lastErrorErr != nil {
		return RefreshStatus{}, lastErrorErr
	}
	status.LastScanError = lastError
	status.NeedsFullScan = !state.Ready()
	status.EngineRules.Available = true
	status.EngineRules.Current = true
	if db.tableExists(ctx, "meta") {
		version, metaErr := db.metaValue(ctx, "index_rule_version")
		if metaErr != nil {
			return RefreshStatus{}, metaErr
		}
		if version != indexRuleVersion {
			status.NeedsFullScan = true
		}
		currentFingerprint, fingerprintErr := engineDataFingerprint(normalized.EngineLogs)
		if fingerprintErr != nil {
			status.EngineRules.Available = false
			status.EngineRules.Current = false
			status.NeedsFullScan = true
		} else {
			cachedFingerprint, cacheErr := db.metaValue(ctx, "engine_data_fingerprint")
			if cacheErr != nil {
				return RefreshStatus{}, cacheErr
			}
			status.EngineRules.Current = cachedFingerprint == currentFingerprint
			if !status.EngineRules.Current {
				status.NeedsFullScan = true
			}
		}
	} else {
		status.EngineRules.Available = false
		status.EngineRules.Current = false
		status.NeedsFullScan = true
	}
	switch {
	case !status.Project.Refreshable:
		status.Status = "project_unavailable"
	case status.NeedsFullScan:
		status.Status = "full_scan_required"
	default:
		status.Status = "ready"
	}
	return status, nil
}

func (db *DB) recordScanFailure(ctx context.Context, err error) {
	if err == nil || !db.tableExists(ctx, "meta") {
		return
	}
	code := scanFailureCode(err)
	_, _ = db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('last_scan_error_code',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, code)
	_, _ = db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('last_scan_error_at',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339Nano))
}

func (db *DB) clearScanFailure(ctx context.Context) {
	if !db.tableExists(ctx, "meta") {
		return
	}
	_, _ = db.sql.ExecContext(ctx, `DELETE FROM meta WHERE key IN ('last_scan_error_code','last_scan_error_at')`)
}

func (db *DB) lastScanError(ctx context.Context) (*RefreshScanError, error) {
	if !db.tableExists(ctx, "meta") {
		return nil, nil
	}
	code, err := db.metaValue(ctx, "last_scan_error_code")
	if err != nil || code == "" {
		return nil, err
	}
	at, err := db.metaValue(ctx, "last_scan_error_at")
	if err != nil {
		return nil, err
	}
	return &RefreshScanError{Code: code, At: at}, nil
}

func scanFailureCode(err error) string {
	var fullRequired *FullScanRequiredError
	switch {
	case errors.As(err, &fullRequired):
		return "FULL_SCAN_REQUIRED"
	case errors.Is(err, context.Canceled):
		return "OPERATION_CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return "OPERATION_TIMEOUT"
	default:
		return "INTERNAL_ERROR"
	}
}
