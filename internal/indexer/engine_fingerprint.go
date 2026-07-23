package indexer

import (
	"context"
	"database/sql"
)

const noEngineDataFingerprint = "none"

// engineDataFingerprint covers every external engine-log file that feeds the
// semantic cache. Incremental scans must not combine newly loaded in-memory
// rules with stale engine_datatypes or engine_scope_rules in SQLite.
func engineDataFingerprint(logs string) (string, error) {
	bundle, err := LoadEngineBundle(context.Background(), logs)
	if err != nil {
		return "", err
	}
	return bundle.Fingerprint, nil
}

func storeEngineDataFingerprint(ctx context.Context, tx *sql.Tx, fingerprint string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('engine_data_fingerprint',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fingerprint)
	return err
}
