package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

type scanFileQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadExistingScanFiles(ctx context.Context, queryer scanFileQueryer) (map[string]fileRecord, error) {
	existing := map[string]fileRecord{}
	rows, err := queryer.QueryContext(ctx, `SELECT id, source_name, source_rank, path, rel_path, kind, mtime, file_size, sha256, overridden,
		override_reason,override_by_source,override_by_rank,override_rule FROM files`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rec fileRecord
		var recOvr int
		if err := rows.Scan(&rec.ID, &rec.SourceName, &rec.SourceRank, &rec.Path, &rec.RelPath, &rec.Kind, &rec.MTime, &rec.Size, &rec.SHA, &recOvr,
			&rec.OverrideReason, &rec.OverrideBySource, &rec.OverrideByRank, &rec.OverrideRule); err != nil {
			rows.Close()
			return nil, err
		}
		rec.Overridden = recOvr != 0
		existing[rec.Path] = rec
	}
	rows.Close()
	return existing, rows.Err()
}

// The write scan and read-only no-change proof share the exact file selection,
// pruning, source precedence and descriptor override rules.
func collectScanFileJobs(ctx context.Context, cfg Config, existing map[string]fileRecord, checkMetadata bool) ([]fileJob, int, bool, error) {
	metadataCurrent := true
	var jobs []fileJob
	for _, src := range cfg.Sources {
		if src.Name == "" || src.Path == "" {
			continue
		}
		if err := filepath.WalkDir(src.Path, func(path string, d os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			rel, relErr := filepath.Rel(src.Path, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if shouldPruneSourceDirForSource(rel, src.ResourceOnly) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("source %q contains symbolic link at %s", src.Name, rel)
			}
			kind := classifyRel(rel)
			if kind == "" || (src.ResourceOnly && kind != "resource") {
				return nil
			}
			// Windows directory enumeration already carries size and mtime.
			// These are only rejection hints: acceptance still requires fresh
			// byte hashes and the before/after file identity checks.
			if checkMetadata && metadataCurrent {
				info, err := d.Info()
				if err != nil {
					return err
				}
				previous := existing[path]
				metadataCurrent = previous.ID != 0 && info.Size() == previous.Size && info.ModTime().UnixNano() == previous.MTime
			}
			jobs = append(jobs, fileJob{
				src:           src,
				path:          path,
				rel:           rel,
				kind:          kind,
				prev:          existing[path],
				verifyContent: cfg.verifyContent,
			})
			return nil
		}); err != nil {
			return nil, 0, false, fmt.Errorf("scan source %q: %w", src.Name, err)
		}
	}

	// Override pass: files with the same rel_path across sources.
	// The source with the lowest rank (highest priority) wins; others
	// are skipped entirely (only a file record is stored, no parsing).
	replacePaths, err := collectSourceReplacePaths(cfg.Sources)
	if err != nil {
		return nil, 0, false, err
	}
	overrideWinners := map[string]Source{} // rel_path -> highest-priority source
	for _, j := range jobs {
		if winner, ok := overrideWinners[j.rel]; !ok || j.src.Rank < winner.Rank {
			overrideWinners[j.rel] = j.src
		}
	}
	sourceNameByRank := map[int]string{}
	for _, source := range cfg.Sources {
		sourceNameByRank[source.Rank] = source.Name
	}
	overriddenCount := 0
	for i := range jobs {
		winner := overrideWinners[jobs[i].rel]
		if jobs[i].src.Rank > winner.Rank {
			jobs[i].overridden = true
			jobs[i].overrideReason = "same_relative_path"
			jobs[i].overrideBySource = winner.Name
			jobs[i].overrideByRank = winner.Rank
			jobs[i].overrideRule = jobs[i].rel
			overriddenCount++
		} else if rank, rule, ok := replacePathEvidence(jobs[i].rel, jobs[i].src.Rank, replacePaths); ok {
			jobs[i].overridden = true
			jobs[i].overrideReason = "descriptor_replace_path"
			jobs[i].overrideBySource = sourceNameByRank[rank]
			jobs[i].overrideByRank = rank
			jobs[i].overrideRule = rule
			overriddenCount++
		}
	}
	return jobs, overriddenCount, metadataCurrent, nil
}
