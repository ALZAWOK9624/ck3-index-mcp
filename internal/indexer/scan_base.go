package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
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
	reason, err := baseSeedRejection(ctx, cfg, basePath, engineFingerprint)
	if err != nil {
		return false, "base index could not be inspected: " + displayPath(basePath)
	}
	if reason != "" {
		return false, reason
	}
	if err := copyDatabaseFile(basePath, stagePath); err != nil {
		return false, "base index could not be copied into the staging cache"
	}
	return true, ""
}

// baseSeedRejection returns a human-readable reason the base cannot seed this
// workspace, or an empty string when it can.
func baseSeedRejection(ctx context.Context, cfg Config, basePath, engineFingerprint string) (string, error) {
	if info, err := os.Stat(basePath); err != nil || info.IsDir() {
		return "base index does not exist at the configured path", nil
	}
	base, err := OpenReadOnly(basePath)
	if err != nil {
		return "base index could not be opened", nil
	}
	defer base.Close()
	if !base.tableExists(ctx, "meta") {
		return "base index has no meta table", nil
	}
	state, err := base.IndexState(ctx)
	if err != nil {
		return "", err
	}
	if !state.Ready() {
		return fmt.Sprintf("base index is not a published ready generation (status %q)", state.Status), nil
	}
	ruleVersion, err := base.metaValue(ctx, "index_rule_version")
	if err != nil {
		return "", err
	}
	if ruleVersion != indexRuleVersion {
		return fmt.Sprintf("base index rule version %q does not match %q", ruleVersion, indexRuleVersion), nil
	}
	baseEngine, err := base.metaValue(ctx, "engine_data_fingerprint")
	if err != nil {
		return "", err
	}
	if baseEngine != engineFingerprint {
		return "base index was built from different engine log rules", nil
	}
	baseUpstream, err := base.metaValue(ctx, "upstream_source_fingerprint")
	if err != nil {
		return "", err
	}
	if baseUpstream == "" {
		return "base index predates upstream source fingerprinting; rebuild it", nil
	}
	if baseUpstream != UpstreamSourceFingerprint(cfg) {
		return "base index was built from different upstream sources", nil
	}
	// A base must carry no project rows. Seeding relies on every upstream file
	// starting out active, so that becoming hidden by this project is always an
	// active-to-overridden transition. A base that already had some other
	// project layered on it would instead need the reverse transition, and a
	// file that was never parsed because it was overridden cannot be revived by
	// metadata alone.
	rank, err := projectRank(cfg)
	if err != nil {
		return "", err
	}
	var projectFiles int
	if err := base.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE source_rank=?`, rank).Scan(&projectFiles); err != nil {
		return "", err
	}
	if projectFiles > 0 {
		return fmt.Sprintf("base index already contains %d file(s) at the project rank; build it against an empty project directory", projectFiles), nil
	}
	return "", nil
}

// copyDatabaseFile materializes the base as the staging cache. The base is
// opened read-only and its WAL is not copied: a ready published generation has
// already been checkpointed into the main database file, and a stale sidecar
// would otherwise be replayed over the copy.
func copyDatabaseFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	// A copied database must not inherit sidecars from a previous staging run.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dst + suffix)
	}
	return nil
}
