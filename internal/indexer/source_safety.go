package indexer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// validateSourceRoots rejects symlinked source roots before a scan can follow
// a path outside the configured CK3 source tree. The indexer reads source
// files only; accepting a link here would silently widen that read boundary.
func validateSourceRoots(sources []Source) error {
	for _, source := range sources {
		info, err := os.Lstat(source.Path)
		if err != nil {
			return fmt.Errorf("scan source %q: %w", source.Name, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("source %q root must not be a symbolic link", source.Name)
		}
		if !info.IsDir() {
			return fmt.Errorf("source %q root is not a directory", source.Name)
		}
	}
	return nil
}

func sourceRegularFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("symbolic links are not allowed in source trees: %s", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source path is not a regular file: %s", filepath.Base(path))
	}
	return info, nil
}

// sourceRegularFileAt validates every component between root and rel before
// opening a source file. Lstat on only the final file is insufficient: an
// intermediate directory symlink can otherwise redirect an incremental read
// outside the configured source root.
func sourceRegularFileAt(root, rel string) (string, os.FileInfo, error) {
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", nil, fmt.Errorf("source path must be relative")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("source path escapes its root")
	}
	full := filepath.Join(root, clean)
	contained, err := filepath.Rel(root, full)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("source path escapes its root")
	}
	current := root
	parts := strings.Split(clean, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", nil, fmt.Errorf("invalid source path component")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("symbolic links are not allowed in source trees: %s", part)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", nil, fmt.Errorf("source path component is not a directory: %s", part)
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return "", nil, fmt.Errorf("source path is not a regular file: %s", filepath.Base(current))
			}
			return full, info, nil
		}
	}
	return "", nil, fmt.Errorf("source path has no file component")
}
