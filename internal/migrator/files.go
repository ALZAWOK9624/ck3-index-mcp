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
	info, err := os.Stat(source)
	if err != nil {
		return err
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
	storageAbs, err := filepath.Abs(storage)
	if err != nil {
		return err
	}
	for _, source := range sources {
		sourceAbs, err := filepath.Abs(source.Path)
		if err != nil {
			return err
		}
		if pathsOverlap(storageAbs, sourceAbs) {
			return fmt.Errorf("migration storage must not overlap configured source %q", source.Name)
		}
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	return pathContains(a, b) || pathContains(b, a)
}

func pathContains(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
