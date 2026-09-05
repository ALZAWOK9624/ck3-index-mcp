package indexer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const publishedDatabasePointerSuffix = ".current"

func publishedDatabasePointerPath(anchor string) string {
	return anchor + publishedDatabasePointerSuffix
}

func removeOrphanedPublishedDatabaseGenerations(anchor string) {
	current, err := resolvePublishedDatabasePath(anchor)
	if err != nil {
		return
	}
	dir := filepath.Dir(anchor)
	pattern := filepath.Join(dir, "."+filepath.Base(anchor)+".generation-*.sqlite*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	roots := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		name := filepath.Base(match)
		marker := strings.LastIndex(name, ".sqlite")
		if marker < 0 {
			continue
		}
		root := filepath.Join(dir, name[:marker+len(".sqlite")])
		if canonicalConfigPath(root) != canonicalConfigPath(current) {
			roots[root] = struct{}{}
		}
	}
	for root := range roots {
		var newest time.Time
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if info, statErr := os.Stat(root + suffix); statErr == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		if newest.IsZero() || time.Since(newest) < orphanedStagedDatabaseAge {
			continue
		}
		if cleanupErr := removeStagedDatabase(root); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "[scan] retired database generation cleanup deferred: %v\n", cleanupErr)
		}
	}
}

func resolvePublishedDatabasePath(anchor string) (string, error) {
	pointerPath := publishedDatabasePointerPath(anchor)
	data, err := os.ReadFile(pointerPath)
	if os.IsNotExist(err) {
		return anchor, nil
	}
	if err != nil {
		return "", fmt.Errorf("read published database pointer: %w", err)
	}
	name := strings.TrimSpace(string(data))
	if !validPublishedDatabaseGenerationName(filepath.Base(anchor), name) {
		return "", fmt.Errorf("published database pointer is invalid")
	}
	path := filepath.Join(filepath.Dir(anchor), name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("resolve published database generation: %w", err)
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("published database generation is not a file")
	}
	return filepath.Clean(path), nil
}

func validPublishedDatabaseGenerationName(anchorBase, name string) bool {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return false
	}
	prefix := "." + anchorBase + ".generation-"
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".sqlite")
}

func publishedDatabaseGenerationPath(anchor string, generation int64, revision string) string {
	hash := sha256.Sum256([]byte(revision))
	token := fmt.Sprintf("%x", hash[:8])
	name := "." + filepath.Base(anchor) + ".generation-" + strconv.FormatInt(generation, 10) + "-" + token + ".sqlite"
	return filepath.Join(filepath.Dir(anchor), name)
}

func publishDatabasePointer(anchor, generationPath string) error {
	dir := filepath.Dir(anchor)
	name := filepath.Base(generationPath)
	if !validPublishedDatabaseGenerationName(filepath.Base(anchor), name) || filepath.Clean(filepath.Dir(generationPath)) != filepath.Clean(dir) {
		return fmt.Errorf("published database generation is outside its anchor directory")
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(anchor)+".current-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.WriteString(name + "\n"); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, publishedDatabasePointerPath(anchor)); err != nil {
		return err
	}
	committed = true
	return nil
}

// RemoveRetiredDatabaseGeneration reclaims a database that is no longer named
// by the publication pointer. It accepts only the configured anchor or one of
// that anchor's generated siblings, and refuses to remove the current target.
// Callers must close their DB handle first; another process may still keep the
// file alive on Windows, in which case the returned error is retryable later.
func RemoveRetiredDatabaseGeneration(cfg Config, retiredPath string) error {
	anchor, err := ConfiguredDatabaseAnchorPath(cfg)
	if err != nil {
		return err
	}
	current, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return err
	}
	retiredPath = filepath.Clean(retiredPath)
	if canonicalConfigPath(retiredPath) == canonicalConfigPath(current) {
		return fmt.Errorf("refusing to remove the current published database generation")
	}
	validTarget := canonicalConfigPath(retiredPath) == canonicalConfigPath(anchor)
	if !validTarget {
		validTarget = filepath.Clean(filepath.Dir(retiredPath)) == filepath.Clean(filepath.Dir(anchor)) &&
			validPublishedDatabaseGenerationName(filepath.Base(anchor), filepath.Base(retiredPath))
	}
	if !validTarget {
		return fmt.Errorf("retired database path does not belong to the configured publication anchor")
	}
	return removeStagedDatabase(retiredPath)
}
