package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const noEngineDataFingerprint = "none"

// engineDataFingerprint covers every external engine-log file that feeds the
// semantic cache. Incremental scans must not combine newly loaded in-memory
// rules with stale engine_datatypes or engine_scope_rules in SQLite.
func engineDataFingerprint(logs string) (string, error) {
	if strings.TrimSpace(logs) == "" {
		return noEngineDataFingerprint, nil
	}
	hash := sha256.New()
	writeFile := func(label, path string, optional bool) error {
		data, err := os.ReadFile(path)
		if err != nil {
			if optional && os.IsNotExist(err) {
				_, _ = hash.Write([]byte(label + "\x00<missing>\x00"))
				return nil
			}
			return err
		}
		_, _ = hash.Write([]byte(label + "\x00"))
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
		return nil
	}
	for _, spec := range engineScopeLogSpecs {
		if err := writeFile(spec.name, filepath.Join(logs, spec.name), spec.optional); err != nil {
			return "", fmt.Errorf("fingerprint engine log %s: %w", spec.name, err)
		}
	}
	dataTypes := filepath.Join(logs, "data_types")
	entries, err := os.ReadDir(dataTypes)
	if err != nil {
		return "", fmt.Errorf("fingerprint engine data_types: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		if err := writeFile(filepath.ToSlash(filepath.Join("data_types", name)), filepath.Join(dataTypes, name), false); err != nil {
			return "", fmt.Errorf("fingerprint engine datatype %s: %w", name, err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func storeEngineDataFingerprint(ctx context.Context, tx *sql.Tx, fingerprint string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('engine_data_fingerprint',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fingerprint)
	return err
}
