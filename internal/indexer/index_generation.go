package indexer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// IndexStatusStale marks an index whose indexed content is known to no longer
// describe the files on disk. It is set deliberately — for example just before
// a migration promotion replaces the whole project directory — so no caller
// can keep reading a ready generation that silently describes the old tree.
const IndexStatusStale = "stale"

type IndexState struct {
	Generation  int64  `json:"scan_generation"`
	Revision    string `json:"scan_revision,omitempty"`
	CommittedAt string `json:"scan_committed_at,omitempty"`
	Status      string `json:"scan_status,omitempty"`
	// StaleReason explains a deliberate invalidation. It is short, stable, and
	// path-free so it can be reported straight to an MCP caller.
	StaleReason string `json:"scan_stale_reason,omitempty"`
	// RequiredAction names the recovery a caller must perform before the index
	// can be trusted again.
	RequiredAction string `json:"scan_required_action,omitempty"`
}

func bumpScanGeneration(ctx context.Context, q integrityQueryExecer) error {
	// A clean scan recreates the meta table and can therefore restart the
	// numeric generation at 1. Keep a random revision alongside the counter so
	// long-lived readers can never mistake a replacement cache for an older
	// generation with the same integer value.
	if err := ensureScanRevision(ctx, q); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(meta.value AS INTEGER)+1 AS TEXT)`); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_committed_at',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func ensureScanRevision(ctx context.Context, q integrityQueryExecer) error {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_revision',?) ON CONFLICT(key) DO NOTHING`, hex.EncodeToString(bytes))
	return err
}

// Ready reports whether callers may rely on the indexed data being a fully
// published generation. Empty legacy status is treated as ready only when the
// cache already has a generation or revision; an empty freshly reset meta
// table remains unavailable until its first successful publication.
func (state IndexState) Ready() bool {
	return state.Status == "ready"
}

func samePublishedIndexState(left, right IndexState) bool {
	return left.Generation == right.Generation && left.Revision == right.Revision && left.Status == right.Status
}

func (db *DB) IndexState(ctx context.Context) (IndexState, error) {
	if !db.tableExists(ctx, "meta") {
		return IndexState{Status: "initializing"}, nil
	}
	return readIndexState(ctx, db.sql)
}

// MarkIndexStale invalidates the published generation without deleting it.
// The rows stay readable for forensics, but every readiness check fails until
// a full refresh republishes the index. Callers use it when they are about to
// change the indexed tree outside the scanner — replacing the formal project
// during a migration promotion or rollback is the motivating case, where a
// still-ready index would answer with the previous project's objects while
// re-reading the new files from disk.
func (db *DB) MarkIndexStale(ctx context.Context, reason, requiredAction string) error {
	if !db.tableExists(ctx, "meta") {
		// Nothing has ever been published, so there is no ready generation that
		// could mislead a caller.
		return nil
	}
	for key, value := range map[string]string{
		"scan_status":          IndexStatusStale,
		"scan_stale_reason":    strings.TrimSpace(reason),
		"scan_required_action": strings.TrimSpace(requiredAction),
		"scan_stale_at":        time.Now().UTC().Format(time.RFC3339Nano),
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	return nil
}

func clearIndexStaleMarkers(ctx context.Context, q integrityQueryExecer) error {
	_, err := q.ExecContext(ctx, `DELETE FROM meta WHERE key IN ('scan_stale_reason','scan_required_action','scan_stale_at')`)
	return err
}

func readIndexState(ctx context.Context, queryer integrityQueryExecer) (IndexState, error) {
	state := IndexState{Status: "initializing"}
	rows, err := queryer.QueryContext(ctx, `SELECT key,value FROM meta
		WHERE key IN ('scan_generation','scan_revision','scan_committed_at','scan_status','scan_stale_reason','scan_required_action')`)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	hasGeneration := false
	hasRevision := false
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return state, err
		}
		switch key {
		case "scan_generation":
			hasGeneration = true
			state.Generation, _ = strconv.ParseInt(value, 10, 64)
		case "scan_revision":
			state.Revision = value
			hasRevision = strings.TrimSpace(value) != ""
		case "scan_committed_at":
			state.CommittedAt = value
		case "scan_status":
			state.Status = strings.TrimSpace(value)
		case "scan_stale_reason":
			state.StaleReason = strings.TrimSpace(value)
		case "scan_required_action":
			state.RequiredAction = strings.TrimSpace(value)
		}
	}
	if err := rows.Err(); err != nil {
		return state, err
	}
	if state.Status == "" {
		if hasGeneration || hasRevision {
			// Compatibility with indexes created before scan_status existed.
			state.Status = "ready"
		} else {
			state.Status = "initializing"
		}
	}
	return state, nil
}
