package indexer

import (
	"context"
	"database/sql"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// probePublishedUnchanged is read-only and runs under the publication lock.
// Failure to prove the complete current contract falls through to the normal
// staged scan; --clean never enters this path. Both paths share scan planning
// and the same content readers, rather than trusting a filesystem timestamp.
func probePublishedUnchanged(ctx context.Context, cfg Config, path string, base PublicationBase, bundle *EngineBundle) (stats ScanStats, unchanged bool, err error) {
	started := time.Now()
	stats = ScanStats{Database: path, BySource: map[string]int{}, TimingsMillis: map[string]int64{}}
	defer func() { stats.TimingsMillis["noop_probe"] = time.Since(started).Milliseconds() }()
	if !publishedSeedCompatible(ctx, cfg, path, bundle.Fingerprint) {
		return stats, false, ctx.Err()
	}
	if err := validateSources(cfg.Sources); err != nil {
		return stats, false, err
	}
	if err := validateSourceRoots(cfg.Sources); err != nil {
		return stats, false, err
	}
	db, err := OpenReadOnlyWithOptions(path, cfg.SQLiteReadOptions())
	if err != nil {
		return stats, false, err
	}
	defer db.Close()
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return stats, false, err
	}
	defer tx.Rollback()
	// A normal scan repairs missing schema and clears failure metadata. Those
	// requests must take that path even if every source byte remains identical.
	var failed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta WHERE key='last_scan_error_code' AND value<>''`).Scan(&failed); err != nil || failed != 0 {
		return stats, false, err
	}
	loadStart := time.Now()
	existing, err := loadExistingScanFiles(ctx, tx)
	stats.TimingsMillis["load_existing_index"] = time.Since(loadStart).Milliseconds()
	if err != nil {
		return stats, false, err
	}
	walkStart := time.Now()
	jobs, overridden, metadataCurrent, err := collectScanFileJobs(ctx, cfg, existing, true)
	stats.TimingsMillis["walk_sources"] = time.Since(walkStart).Milliseconds()
	if err != nil {
		return stats, false, err
	}
	if len(jobs) != len(existing) || !metadataCurrent {
		return stats, false, nil
	}
	for i := range jobs {
		j := &jobs[i]
		p := j.prev
		if p.ID == 0 || p.SHA == "" || p.RelPath != j.rel || p.Kind != j.kind ||
			p.SourceName != j.src.Name || p.SourceRank != j.src.Rank || p.Overridden != j.overridden ||
			p.OverrideReason != j.overrideReason || p.OverrideBySource != j.overrideBySource ||
			p.OverrideByRank != j.overrideByRank || p.OverrideRule != j.overrideRule {
			return stats, false, nil
		}
		j.verifyContent = true
		j.verifyOnly = true
	}
	stats.Overridden = overridden
	current, err := scanSchemaCurrent(ctx, tx)
	if err != nil || !current {
		return stats, false, err
	}
	current, err = searchFTSCacheMatches(ctx, tx)
	if err != nil || !current {
		return stats, false, err
	}
	vanilla := vanillaOnActionsForConfig(cfg)
	fingerprint, err := vanilla.contentFingerprint()
	if err != nil {
		return stats, false, err
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, vanillaOnActionFingerprintKey).Scan(&stored); err != nil {
		if err == sql.ErrNoRows {
			return stats, false, nil
		}
		return stats, false, err
	}
	if stored != fingerprint {
		return stats, false, nil
	}
	if ok, err := verifyUnchangedScanJobs(ctx, jobs, &stats); err != nil || !ok {
		return stats, false, err
	}
	mapStart := time.Now()
	manifest, err := collectMapInputManifest(ctx, cfg)
	if err != nil {
		return stats, false, err
	}
	current, err = mapCacheMatchesInput(ctx, tx, manifest.Fingerprint, manifest.Reusable, manifest.Active)
	stats.TimingsMillis["map_context_reused"] = time.Since(mapStart).Milliseconds()
	if err != nil || !current {
		return stats, false, err
	}
	loaded, err := loadScanStatsTotals(ctx, tx, &stats)
	if err != nil || !loaded {
		return stats, false, err
	}
	state, err := readIndexState(ctx, tx)
	if err != nil {
		return stats, false, err
	}
	if !samePublicationBase(base, PublicationBase{Generation: state.Generation, Revision: state.Revision, Status: state.Status, DatabasePath: path}) {
		return stats, false, ErrConflictingGeneration
	}
	if err := tx.Rollback(); err != nil {
		return stats, false, err
	}
	if cfg.afterFullNoopProbe != nil {
		cfg.afterFullNoopProbe()
	}
	if err := ctx.Err(); err != nil {
		return stats, false, err
	}
	// Re-read outside the snapshot, so a pointer or generation change cannot
	// be hidden by the read transaction even if a writer bypassed our lock.
	currentPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return stats, false, err
	}
	now, err := readPublicationBase(ctx, currentPath, cfg.SQLiteReadOptions())
	if err != nil {
		return stats, false, err
	}
	if !samePublicationBase(base, now) {
		return stats, false, &PublicationConflictError{Base: base, Current: now}
	}
	if err := ctx.Err(); err != nil {
		return stats, false, err
	}
	publishEngineRules(bundle, state)
	stats.Noop, stats.ReusedGeneration = true, true
	return stats, true, nil
}

func scanSchemaCurrent(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type IN ('index','trigger')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, statement := range indexStmts {
		_, tail, ok := strings.Cut(statement, "IF NOT EXISTS ")
		if !ok || !present[strings.Fields(tail)[0]] {
			return false, nil
		}
	}
	for _, names := range [][]string{scriptTextTriggerNames, trigramLocTriggerNames} {
		for _, name := range names {
			if !present[name] {
				return false, nil
			}
		}
	}
	return true, nil
}

func verifyUnchangedScanJobs(ctx context.Context, jobs []fileJob, stats *ScanStats) (bool, error) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	workers := min(max(runtime.GOMAXPROCS(0), 1), 16)
	inputs, results := make(chan fileJob, workers*2), make(chan fileResult, workers*2)
	var peak atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers + 1)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for job := range inputs {
				if ctx.Err() != nil {
					return
				}
				result := parseOneFile(job)
				// Drain every completed read, including those already in flight
				// when a mismatch stops the producer, so I/O counts are truthful.
				results <- result
				for n := int64(len(results)); ; {
					previous := peak.Load()
					if n <= previous || peak.CompareAndSwap(previous, n) {
						break
					}
				}
			}
		}()
	}
	go func() {
		defer wg.Done()
		defer close(inputs)
		for _, job := range jobs {
			select {
			case inputs <- job:
			case <-ctx.Done():
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(results); close(done) }()
	defer func() { cancel(); <-done }()
	var totals fileWorkTotals
	defer func() { totals.applyTimings(stats); stats.PeakQueuedResults = int(peak.Load()) }()
	unchanged := true
	var firstError error
	for res := range results {
		totals.add(stats, res.work)
		stats.Files++
		stats.BySource[res.job.src.Name]++
		if res.err != nil {
			if firstError == nil {
				firstError = res.err
			}
			unchanged = false
			cancel()
			continue
		}
		if !res.skip || res.info == nil || res.info.Size() != res.job.prev.Size || res.info.ModTime().UnixNano() != res.job.prev.MTime {
			unchanged = false
			cancel()
			continue
		}
		// Match the pathname identity after hashing as well as its content.
		after, err := sourceRegularFileInfo(res.job.path)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			unchanged = false
			cancel()
			continue
		}
		if !os.SameFile(res.info, after) || after.Size() != res.info.Size() || !after.ModTime().Equal(res.info.ModTime()) {
			unchanged = false
			cancel()
		}
	}
	if firstError != nil {
		return false, firstError
	}
	if parent.Err() != nil {
		return false, parent.Err()
	}
	if !unchanged {
		return false, nil
	}
	return true, ctx.Err()
}
