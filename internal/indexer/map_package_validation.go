package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// MapPackageDiagnostics returns persisted map-contract diagnostics only when
// every supplied map-contract file still matches the active project index.
// Packaging unscanned map bytes is blocked instead of mixing fresh archive
// content with stale cross-file conclusions.
func (db *DB) MapPackageDiagnostics(ctx context.Context, files []PatchFileInput) ([]Diagnostic, error) {
	relevant := map[string]PatchFileInput{}
	for _, file := range files {
		rel := strings.ToLower(filepath.ToSlash(strings.TrimSpace(file.Path)))
		if isMapContractPackagePath(rel) {
			relevant[rel] = file
		}
	}
	if len(relevant) == 0 {
		return nil, nil
	}

	status, err := db.MapDatabaseStatus(ctx)
	if err != nil {
		return nil, err
	}
	if !status.Complete {
		return []Diagnostic{{
			Source: "map-integrity", Severity: "error", Code: "map_package_index_stale",
			Message:    "map package validation requires a complete current map database; run a full scan first",
			Confidence: "high", Fingerprint: "map-package:index-stale", Occurrences: 1,
		}}, nil
	}
	projectSource, err := db.projectSourceName(ctx)
	if err != nil {
		return nil, err
	}

	paths := sortedStringKeys(relevant)
	var stale []Diagnostic
	for _, rel := range paths {
		file := relevant[rel]
		digest := sha256.Sum256([]byte(file.Content))
		actual := hex.EncodeToString(digest[:])
		var indexed string
		err := db.sql.QueryRowContext(ctx, `SELECT sha256 FROM files WHERE source_name=? AND lower(rel_path)=? AND overridden=0 ORDER BY source_rank,id LIMIT 1`, projectSource, rel).Scan(&indexed)
		if err == sql.ErrNoRows || (err == nil && !strings.EqualFold(indexed, actual)) {
			stale = append(stale, Diagnostic{
				Source: "map-integrity", Severity: "error", Code: "map_package_index_stale", Path: file.Path,
				Message:     "packaged map-contract file does not match the active project index; scan this file before packaging",
				SourceLayer: projectSource, Confidence: "high",
				Fingerprint: fmt.Sprintf("map-package:index-stale:%s", rel), Occurrences: 1,
			})
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	if len(stale) > 0 {
		return stale, nil
	}

	rows, err := db.sql.QueryContext(ctx, `SELECT severity,code,message,COALESCE(path,''),COALESCE(line,0),source_layer,confidence,fingerprint,occurrences
		FROM diagnostics WHERE source='map-integrity' AND source_layer=? ORDER BY path,line,code`, projectSource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var diagnostic Diagnostic
		if err := rows.Scan(&diagnostic.Severity, &diagnostic.Code, &diagnostic.Message, &diagnostic.Path, &diagnostic.Line,
			&diagnostic.SourceLayer, &diagnostic.Confidence, &diagnostic.Fingerprint, &diagnostic.Occurrences); err != nil {
			return nil, err
		}
		diagnostic.Source = "map-integrity"
		if diagnostic.Path != "" {
			if _, packaged := relevant[strings.ToLower(filepath.ToSlash(diagnostic.Path))]; !packaged {
				continue
			}
		}
		out = append(out, diagnostic)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Code < out[j].Code
	})
	return out, nil
}

func isMapContractPackagePath(rel string) bool {
	return strings.HasPrefix(rel, "map_data/") ||
		strings.HasPrefix(rel, "common/landed_titles/") ||
		strings.HasPrefix(rel, "common/province_terrain/") ||
		strings.HasPrefix(rel, "history/provinces/")
}
