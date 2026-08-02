package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

// UpstreamSourceFingerprint identifies every configured layer except the
// project. Two workspaces that share the same game and mod trees produce the
// same value, which is what makes one workspace's cache a valid seed for the
// other. The project source is excluded precisely because it is the layer that
// differs; rank and role are included because they decide override precedence,
// so a base built with a different precedence order is not interchangeable.
//
// Like ScanConfigFingerprint this hashes canonical absolute paths rather than
// the shortened display form: two roots can share trailing segments while
// naming entirely different trees.
func UpstreamSourceFingerprint(cfg Config) string {
	sources := make([]Source, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if source.Role == SourceRoleProject {
			continue
		}
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool {
		if !strings.EqualFold(sources[i].Name, sources[j].Name) {
			return strings.ToLower(sources[i].Name) < strings.ToLower(sources[j].Name)
		}
		return sources[i].Rank < sources[j].Rank
	})
	hash := sha256.New()
	fmt.Fprintf(hash, "upstream-sources-v1\x00%d\x00", len(sources))
	for _, source := range sources {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%s\x00%t\x00%t\x00",
			strings.ToLower(strings.TrimSpace(source.Name)), canonicalConfigPath(source.Path),
			source.Rank, string(source.Role), source.Private, source.ResourceOnly)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// projectRank reports the rank reserved for the project layer.
func projectRank(cfg Config) (int, error) {
	project, err := ProjectSource(cfg)
	if err != nil {
		return 0, err
	}
	return project.Rank, nil
}

// seedStagedScanFromBase copies a prebuilt upstream index into the staging
// location so the following scan only has to parse the project layer. It
// returns the reason the base was rejected instead of an error whenever the
// caller can still fall back to a full parse, because that fallback produces
// the same index and only costs time.
//
// The scan that runs afterwards must be the ordinary incremental one, not a
// clean rebuild. That is what makes seeding correct rather than merely fast:
// the existing override pass recomputes precedence across every layer, and
// parseOneFile already refuses to treat a file whose override state changed as
// unchanged, so upstream rows hidden by the project lose their derived rows the
// same way a full scan would drop them.
func seedStagedScanFromBase(ctx context.Context, cfg Config, stagePath, engineFingerprint string) (bool, string) {
	basePath := strings.TrimSpace(cfg.BaseDatabase)
	if basePath == "" {
		return false, ""
	}
	if err := onlineBackupDatabase(ctx, basePath, stagePath); err != nil {
		return false, "base index could not be copied into the staging cache"
	}
	origin, reason, err := inspectBaseSeed(ctx, cfg, stagePath, engineFingerprint)
	if err != nil {
		removeDatabaseSnapshot(stagePath)
		return false, "copied base index could not be inspected: " + displayPath(basePath)
	}
	if reason != "" {
		removeDatabaseSnapshot(stagePath)
		return false, reason
	}
	if err := recordBaseSeedOrigin(ctx, stagePath, origin); err != nil {
		removeDatabaseSnapshot(stagePath)
		return false, "base index provenance could not be recorded in the staging cache"
	}
	return true, ""
}

type baseSeedOrigin struct {
	Generation  int64
	Revision    string
	Fingerprint string
}

// baseSeedRejection returns a human-readable reason the base cannot seed this
// workspace, or an empty string when it can.
func baseSeedRejection(ctx context.Context, cfg Config, basePath, engineFingerprint string) (string, error) {
	_, reason, err := inspectBaseSeed(ctx, cfg, basePath, engineFingerprint)
	return reason, err
}

func inspectBaseSeed(ctx context.Context, cfg Config, basePath, engineFingerprint string) (baseSeedOrigin, string, error) {
	if info, err := os.Stat(basePath); err != nil || info.IsDir() {
		return baseSeedOrigin{}, "base index does not exist at the configured path", nil
	}
	base, err := OpenReadOnly(basePath)
	if err != nil {
		return baseSeedOrigin{}, "base index could not be opened", nil
	}
	defer base.Close()
	if !base.tableExists(ctx, "meta") {
		return baseSeedOrigin{}, "base index has no meta table", nil
	}
	state, err := base.IndexState(ctx)
	if err != nil {
		return baseSeedOrigin{}, "", err
	}
	if !state.Ready() {
		return baseSeedOrigin{}, fmt.Sprintf("base index is not a published ready generation (status %q)", state.Status), nil
	}
	ruleVersion, err := base.metaValue(ctx, "index_rule_version")
	if err != nil {
		return baseSeedOrigin{}, "", err
	}
	if ruleVersion != indexRuleVersion {
		return baseSeedOrigin{}, fmt.Sprintf("base index rule version %q does not match %q", ruleVersion, indexRuleVersion), nil
	}
	baseEngine, err := base.metaValue(ctx, "engine_data_fingerprint")
	if err != nil {
		return baseSeedOrigin{}, "", err
	}
	if baseEngine != engineFingerprint {
		return baseSeedOrigin{}, "base index was built from different engine log rules", nil
	}
	baseUpstream, err := base.metaValue(ctx, "upstream_source_fingerprint")
	if err != nil {
		return baseSeedOrigin{}, "", err
	}
	if baseUpstream == "" {
		return baseSeedOrigin{}, "base index predates upstream source fingerprinting; rebuild it", nil
	}
	if baseUpstream != UpstreamSourceFingerprint(cfg) {
		return baseSeedOrigin{}, "base index was built from different upstream sources", nil
	}
	// A base must carry no project rows. Seeding relies on every upstream file
	// starting out active, so that becoming hidden by this project is always an
	// active-to-overridden transition. A base that already had some other
	// project layered on it would instead need the reverse transition, and a
	// file that was never parsed because it was overridden cannot be revived by
	// metadata alone.
	var projectFiles int
	if err := base.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM files f
		JOIN source_layers s ON lower(s.name)=lower(f.source_name) WHERE s.role=?`, SourceRoleProject).Scan(&projectFiles); err != nil {
		return baseSeedOrigin{}, "", err
	}
	if projectFiles > 0 {
		return baseSeedOrigin{}, fmt.Sprintf("base index already contains %d file(s) from a project role; build it against an empty project directory", projectFiles), nil
	}
	origin := baseSeedOrigin{Generation: state.Generation, Revision: state.Revision}
	hash := sha256.New()
	fmt.Fprintf(hash, "base-origin-v1\x00%d\x00%s\x00%s\x00%s\x00", state.Generation, state.Revision, baseEngine, baseUpstream)
	origin.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	return origin, "", nil
}

type sqliteBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

// onlineBackupDatabase copies one coherent SQLite snapshot, including pages
// that are still resident in the source WAL. Copying only the main file can
// validate one generation and seed a different, older one.
func onlineBackupDatabase(ctx context.Context, src, dst string) error {
	removeDatabaseSnapshot(dst)
	base, err := OpenReadOnly(src)
	if err != nil {
		return err
	}
	defer base.Close()
	conn, err := base.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	err = conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(sqliteBackuper)
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		backup, err := backuper.NewBackup(dst)
		if err != nil {
			return err
		}
		var stepErr error
		for more := true; more; {
			if err := ctx.Err(); err != nil {
				stepErr = err
				break
			}
			more, stepErr = backup.Step(256)
			if stepErr != nil {
				break
			}
		}
		finishErr := backup.Finish()
		if stepErr != nil {
			return stepErr
		}
		return finishErr
	})
	if err != nil {
		removeDatabaseSnapshot(dst)
		return err
	}
	return nil
}

func recordBaseSeedOrigin(ctx context.Context, stagePath string, origin baseSeedOrigin) error {
	stage, err := Open(stagePath)
	if err != nil {
		return err
	}
	defer stage.Close()
	tx, err := stage.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{
		"base_origin_generation":  strconv.FormatInt(origin.Generation, 10),
		"base_origin_revision":    origin.Revision,
		"base_origin_fingerprint": origin.Fingerprint,
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func removeDatabaseSnapshot(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}
