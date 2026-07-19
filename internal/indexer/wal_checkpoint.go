package indexer

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// WALCheckpointResult is SQLite's three-column checkpoint result. Busy is
// non-zero when a reader prevented the requested mode from completing.
type WALCheckpointResult struct {
	Mode               string `json:"mode"`
	Busy               int    `json:"busy"`
	LogFrames          int    `json:"log_frames"`
	CheckpointedFrames int    `json:"checkpointed_frames"`
}

func (result WALCheckpointResult) FullyCheckpointed() bool {
	return result.Busy == 0 && result.LogFrames == result.CheckpointedFrames
}

// CheckpointWAL reads SQLite's checkpoint result instead of discarding it via
// Exec. A busy reader is an observable condition, not a scan failure.
func (db *DB) CheckpointWAL(ctx context.Context, mode string) (WALCheckpointResult, error) {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	switch mode {
	case "PASSIVE", "TRUNCATE":
	default:
		return WALCheckpointResult{}, fmt.Errorf("unsupported WAL checkpoint mode %q", mode)
	}
	result := WALCheckpointResult{Mode: strings.ToLower(mode)}
	if err := db.sql.QueryRowContext(ctx, `PRAGMA wal_checkpoint(`+mode+`)`).Scan(&result.Busy, &result.LogFrames, &result.CheckpointedFrames); err != nil {
		return result, err
	}
	return result, nil
}

// checkpointWALAfterScan first performs a non-blocking checkpoint. It only
// attempts a short TRUNCATE when all frames were already checkpointed, so a
// long-lived MCP reader cannot turn a successful scan into a hanging scan.
func (db *DB) checkpointWALAfterScan(ctx context.Context) (WALCheckpointResult, error) {
	passive, err := db.CheckpointWAL(ctx, "PASSIVE")
	if err != nil || !passive.FullyCheckpointed() || passive.LogFrames == 0 {
		return passive, err
	}
	truncateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	truncate, err := db.CheckpointWAL(truncateCtx, "TRUNCATE")
	if err != nil {
		return passive, err
	}
	return truncate, nil
}
