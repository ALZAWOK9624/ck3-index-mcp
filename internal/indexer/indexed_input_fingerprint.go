package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const indexedInputFingerprintMetaKey = "indexed_input_fingerprint"

// IndexedInputFingerprint identifies the complete configured input whose rows
// a published database represents. It intentionally excludes output locations
// (the live and base database paths), operational limits, and engine logs: the
// engine bundle has its own content fingerprint. A staged scan therefore gets
// the same identity as the live database it will replace, while moving or
// reclassifying any source root produces a different identity.
func IndexedInputFingerprint(cfg Config) string {
	if normalized, err := NormalizeConfig(cfg); err == nil {
		cfg = normalized
	}
	sources := append([]Source(nil), cfg.Sources...)
	sort.Slice(sources, func(i, j int) bool {
		leftName := strings.ToLower(strings.TrimSpace(sources[i].Name))
		rightName := strings.ToLower(strings.TrimSpace(sources[j].Name))
		if leftName != rightName {
			return leftName < rightName
		}
		if sources[i].Rank != sources[j].Rank {
			return sources[i].Rank < sources[j].Rank
		}
		return canonicalIndexedInputPath(sources[i].Path) < canonicalIndexedInputPath(sources[j].Path)
	})

	hash := sha256.New()
	fmt.Fprintf(hash, "indexed-input-v1\x00")
	fmt.Fprintf(hash, "gis_enabled\x00%t\x00gis_analysis\x00%s\x00gis_sidecar_sha256\x00%s\x00",
		cfg.GISEnabled,
		strings.ToLower(strings.TrimSpace(cfg.GISAnalysis)),
		strings.ToLower(strings.TrimSpace(cfg.GISSidecarSHA256)))
	fmt.Fprintf(hash, "sources\x00%d\x00", len(sources))
	for _, source := range sources {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%s\x00%t\x00%t\x00",
			strings.ToLower(strings.TrimSpace(source.Name)),
			canonicalIndexedInputPath(source.Path),
			source.Rank,
			strings.ToLower(strings.TrimSpace(string(source.Role))),
			source.Private,
			source.ResourceOnly)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// canonicalIndexedInputPath resolves an existing symlink or junction before
// hashing it. The lexical fallback keeps status and migration checks usable
// when a root is temporarily unavailable; the separate accessibility checks
// still prevent a scan from reading that unavailable root.
func canonicalIndexedInputPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	return canonicalConfigPath(value)
}

func storeIndexedInputFingerprint(ctx context.Context, execer contextExecer, cfg Config) error {
	_, err := execer.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, indexedInputFingerprintMetaKey, IndexedInputFingerprint(cfg))
	return err
}

func (db *DB) indexedInputFingerprintCurrent(ctx context.Context, cfg Config) (bool, error) {
	if !db.tableExists(ctx, "meta") {
		return false, nil
	}
	stored, err := db.metaValue(ctx, indexedInputFingerprintMetaKey)
	if err != nil {
		return false, err
	}
	return stored != "" && stored == IndexedInputFingerprint(cfg), nil
}
