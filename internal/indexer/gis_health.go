package indexer

import (
	"context"
	"strings"
)

// CachedGISSidecarStatus returns the sidecar verification persisted with the
// current database generation. It deliberately performs no filesystem hash,
// publication, or subprocess work, so quick MCP health remains an ordinary
// read. Deep health uses GISSidecarStatus to verify the configured binary.
func (db *DB) CachedGISSidecarStatus(ctx context.Context, cfg Config) GISSidecarStatus {
	status := newGISSidecarStatus(cfg)
	if !cfg.GISEnabled {
		status.Reason = "GIS analysis is disabled by configuration."
		return status
	}
	if db == nil || !db.tableExists(ctx, "meta") {
		status.Reason = "No persisted GIS sidecar verification is available; use deep health to verify the configured binary."
		return status
	}

	cached := db.cachedGISSidecarStatus(ctx)
	status.Version = cached.Version
	status.SHA256 = strings.ToLower(strings.TrimSpace(cached.SHA256))
	status.AnalysisStatus = cached.AnalysisStatus
	if status.AnalysisStatus == "" {
		status.AnalysisStatus = "not_cached"
	}
	expected := strings.ToLower(strings.TrimSpace(cfg.GISSidecarSHA256))
	switch {
	case !cached.Enabled:
		status.Reason = "The persisted database generation did not enable GIS analysis."
	case !validSHA256Hex(expected):
		status.Reason = "The configured WhiteboxTools SHA-256 is missing or invalid."
	case status.SHA256 != expected:
		status.Reason = "The persisted GIS sidecar verification does not match the configured trusted SHA-256."
	case cached.Platform != "" && cached.Platform != status.Platform:
		status.Reason = "The persisted GIS sidecar verification belongs to a different platform."
	case cached.Analysis != "" && cached.Analysis != status.Analysis:
		status.Reason = "The persisted GIS analysis mode differs from the active configuration."
		status.AnalysisStatus = "stale"
	case !cached.Available:
		status.Reason = cached.Reason
		if status.Reason == "" {
			status.Reason = "The persisted database generation did not verify an available GIS sidecar."
		}
	default:
		status.Available = true
		status.Reason = cached.Reason
	}
	return status
}
