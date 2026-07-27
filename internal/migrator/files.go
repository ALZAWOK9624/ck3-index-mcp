package migrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/indexer"
)

var loadRoots = map[string]bool{
	"common": true, "events": true, "history": true, "gui": true,
	"localization": true, "gfx": true, "map_data": true, "sound": true,
}

var prunedDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true, ".map-editor-backups": true,
	"cache": true, "tmp": true, "temp": true, "tools": true, "backups": true,
	"logs": true, "node_modules": true, "__pycache__": true,
}

func normalizeRel(raw string) (string, error) {
	p := strings.TrimSpace(filepath.ToSlash(raw))
	if p == "" || strings.ContainsRune(p, '\x00') || filepath.IsAbs(raw) || strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return "", fmt.Errorf("path must be source-root relative: %q", raw)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path must stay inside the source root: %q", raw)
	}
	for _, part := range strings.Split(clean, "/") {
		upper := strings.ToUpper(strings.TrimSuffix(strings.TrimRight(part, ". "), path.Ext(part)))
		if part != strings.TrimRight(part, ". ") || upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" ||
			(len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9') {
			return "", fmt.Errorf("path contains a Windows-reserved name: %q", raw)
		}
	}
	return clean, nil
}

func supportedRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) == 1 {
		lower := strings.ToLower(parts[0])
		return lower == "descriptor.mod" || strings.HasSuffix(lower, ".mod") || lower == "thumbnail.png"
	}
	return loadRoots[strings.ToLower(parts[0])]
}

func collectFiles(root string) ([]SnapshotFile, []string, error) {
	var files []SnapshotFile
	var excluded []string
	seen := map[string]string{}
	err := filepath.WalkDir(root, func(full string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == root {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := normalizeRel(rel); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", rel)
		}
		if entry.IsDir() {
			if prunedDirs[strings.ToLower(entry.Name())] {
				excluded = append(excluded, rel+"/")
				return filepath.SkipDir
			}
			rootName := strings.ToLower(strings.Split(rel, "/")[0])
			if !loadRoots[rootName] {
				excluded = append(excluded, rel+"/")
				return filepath.SkipDir
			}
			return nil
		}
		if !supportedRel(rel) || isExcludedFile(rel) {
			excluded = append(excluded, rel)
			return nil
		}
		folded := strings.ToLower(rel)
		if previous, ok := seen[folded]; ok && previous != rel {
			return fmt.Errorf("case-insensitive duplicate paths are not supported: %s and %s", previous, rel)
		}
		seen[folded] = rel
		hash, size, err := hashPath(full)
		if err != nil {
			return err
		}
		files = append(files, SnapshotFile{Path: rel, SHA256: hash, Size: size, Text: isTextPath(rel)})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(excluded)
	return files, excluded, err
}

// collectRebaseOverlayFiles inventories the complete project tree that a
// rebase build carries into its isolated output.  It is deliberately not used
// for CK3 semantic planning: only collectFiles defines the load-stack scope.
//
// The inventory includes ordinary non-load-root content (for example
// README.md, tools/, source/, .git/, backups/, and map-editor material) so an
// atomic promotion cannot silently discard project-owned material. There are
// deliberately no directory-name exclusions here: artifact and staging roots
// are already required to live outside configured sources, and a matching
// directory inside the formal project belongs to the user. It rejects links
// and special files instead of copying an ambiguous filesystem object into the
// replacement project.
func collectRebaseOverlayFiles(root string) ([]SnapshotFile, []string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, nil, fmt.Errorf("rebase overlay root must be a regular directory")
	}

	var files []SnapshotFile
	var excluded []string
	seen := map[string]string{}
	err = filepath.WalkDir(root, func(full string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if full == root {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := normalizeRel(rel); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", rel)
		}
		if entry.IsDir() {
			if !info.IsDir() {
				return fmt.Errorf("rebase overlay entry is not a directory: %s", rel)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("special files are not supported: %s", rel)
		}
		folded := strings.ToLower(rel)
		if previous, ok := seen[folded]; ok && previous != rel {
			return fmt.Errorf("case-insensitive duplicate paths are not supported: %s and %s", previous, rel)
		}
		seen[folded] = rel
		hash, size, err := hashPath(full)
		if err != nil {
			return err
		}
		files = append(files, SnapshotFile{Path: rel, SHA256: hash, Size: size, Text: isTextPath(rel)})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(excluded)
	return files, excluded, err
}

func isExcludedFile(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	base := path.Base(lower)
	return strings.HasSuffix(base, ".zip") || strings.HasSuffix(base, ".7z") || strings.HasSuffix(base, ".rar") ||
		strings.HasSuffix(base, ".log") || strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".bak") ||
		strings.Contains(lower, "/backups/") || strings.Contains(lower, "/.map-editor-backups/")
}

func isTextPath(rel string) bool {
	switch strings.ToLower(path.Ext(filepath.ToSlash(rel))) {
	case ".txt", ".gui", ".map", ".csv", ".mod", ".yml", ".yaml", ".json", ".asset", ".settings":
		return true
	default:
		return false
	}
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashPath(file string) (string, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func copyFile(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("copy source is not a regular file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func sourceByName(cfg indexer.Config, name string) (indexer.Source, error) {
	for _, source := range cfg.Sources {
		if strings.EqualFold(strings.TrimSpace(source.Name), strings.TrimSpace(name)) {
			return source, nil
		}
	}
	return indexer.Source{}, fmt.Errorf("configured source %q was not found", name)
}

func ensureStorageOutsideSources(storage string, sources []indexer.Source) error {
	storageAbs, err := resolveRebasePath(storage)
	if err != nil {
		return fmt.Errorf("unsafe migration storage path: %w", err)
	}
	for _, source := range sources {
		sourceAbs, err := resolveRebasePath(source.Path)
		if err != nil {
			return fmt.Errorf("unsafe configured source %q path: %w", source.Name, err)
		}
		if pathsOverlapResolved(storageAbs, sourceAbs) {
			return fmt.Errorf("migration storage must not overlap configured source %q", source.Name)
		}
	}
	return nil
}

// resolveRebasePath makes a path absolute and walks every existing ancestor,
// rejecting a symbolic-link component before it can redirect the remainder.
// Because links are prohibited, the checked absolute prefix is the containment
// identity used for all following joins; no EvalSymlinks call is needed (and on
// Windows it can fail for otherwise ordinary Mod metadata directories).
func resolveRebasePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if !filepath.IsAbs(abs) {
		return "", fmt.Errorf("path is not absolute after normalization: %q", raw)
	}

	volume := filepath.VolumeName(abs)
	root := volume + string(filepath.Separator)
	if volume == "" {
		root = string(filepath.Separator)
	}
	remaining := strings.TrimPrefix(abs, volume)
	remaining = strings.TrimLeft(remaining, string(filepath.Separator))
	parts := make([]string, 0)
	if remaining != "" {
		parts = strings.FieldsFunc(remaining, func(r rune) bool { return r == filepath.Separator || r == '/' || r == '\\' })
	}

	current := root
	for index, part := range parts {
		next := filepath.Join(current, part)
		info, statErr := os.Lstat(next)
		if os.IsNotExist(statErr) {
			resolved := current
			for _, missing := range parts[index:] {
				resolved = filepath.Join(resolved, missing)
			}
			return filepath.Clean(resolved), nil
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path contains a symbolic-link component: %s", next)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("path component is not a directory: %s", next)
		}
		current = next
	}
	return filepath.Clean(current), nil
}

// safeRebaseContainedPath returns the resolved candidate only when it remains
// within the resolved root.  It is deliberately stricter than filepath.Join:
// existing symlink components are a hard error rather than an alias to follow.
func safeRebaseContainedPath(root, candidate string) (string, error) {
	resolvedRoot, err := resolveRebasePath(root)
	if err != nil {
		return "", fmt.Errorf("unsafe containment root: %w", err)
	}
	resolvedCandidate, err := resolveRebasePath(candidate)
	if err != nil {
		return "", fmt.Errorf("unsafe containment candidate: %w", err)
	}
	if !pathContainsResolved(resolvedRoot, resolvedCandidate) {
		return "", fmt.Errorf("path escapes its root")
	}
	return resolvedCandidate, nil
}

func pathsOverlap(a, b string) bool {
	resolvedA, err := resolveRebasePath(a)
	if err != nil {
		return false
	}
	resolvedB, err := resolveRebasePath(b)
	if err != nil {
		return false
	}
	return pathsOverlapResolved(resolvedA, resolvedB)
}

func pathsOverlapResolved(a, b string) bool {
	return pathContainsResolved(a, b) || pathContainsResolved(b, a)
}

func pathContains(root, candidate string) bool {
	_, err := safeRebaseContainedPath(root, candidate)
	return err == nil
}

func pathContainsResolved(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
