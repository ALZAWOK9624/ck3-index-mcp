package indexer

import (
	"context"
	"crypto/rand"
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
	if !db.tableExists(ctx, "meta") {
		return IndexState{Status: "initializing"}, nil
	}
	return readIndexState(ctx, db.sql)
}

func readIndexState(ctx context.Context, queryer integrityQueryExecer) (IndexState, error) {
	state := IndexState{Status: "initializing"}
	rows, err := queryer.QueryContext(ctx, `SELECT key,value FROM meta
		WHERE key IN ('scan_generation','scan_revision','scan_committed_at','scan_status')`)
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
