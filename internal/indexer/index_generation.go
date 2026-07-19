package indexer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

type IndexState struct {
	Generation  int64  `json:"scan_generation"`
	Revision    string `json:"scan_revision,omitempty"`
	CommittedAt string `json:"scan_committed_at,omitempty"`
	Status      string `json:"scan_status,omitempty"`
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
	state := IndexState{Status: "initializing"}
	if !db.tableExists(ctx, "meta") {
		return state, nil
	}
	var hasGeneration bool
	var raw string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='scan_generation'`).Scan(&raw)
	if err == nil {
		hasGeneration = true
		state.Generation, _ = strconv.ParseInt(raw, 10, 64)
	} else if err != sql.ErrNoRows {
		return state, err
	}
	var hasRevision bool
	err = db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='scan_revision'`).Scan(&state.Revision)
	if err == nil {
		hasRevision = strings.TrimSpace(state.Revision) != ""
	} else if err != sql.ErrNoRows {
		return state, err
	}
	err = db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='scan_committed_at'`).Scan(&state.CommittedAt)
	if err != nil && err != sql.ErrNoRows {
		return state, err
	}
	var rawStatus string
	err = db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='scan_status'`).Scan(&rawStatus)
	if err == nil {
		state.Status = strings.TrimSpace(rawStatus)
	} else if err != sql.ErrNoRows {
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
