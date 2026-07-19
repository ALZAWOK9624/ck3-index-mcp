package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ensureSafeGISCacheDirectory(root, candidate string) error {
	exists, err := validateExistingGISCacheDirectory(root)
	if err != nil {
		return err
	}
	if !exists {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
		if _, err := validateExistingGISCacheDirectory(root); err != nil {
			return err
		}
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("GIS cache path escapes configured root")
	}
	if rel != "." {
		current := root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			exists, err := validateExistingGISCacheDirectory(current)
			if err != nil {
				return err
			}
			if !exists {
				break
			}
		}
	}
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		return err
	}
	current := root
	if _, err := validateExistingGISCacheDirectory(current); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		exists, err := validateExistingGISCacheDirectory(current)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("GIS cache directory was not created")
		}
	}
	return nil
}

func validateExistingGISCacheDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	reparse, err := gisPathIsReparsePoint(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || reparse {
		return false, fmt.Errorf("GIS cache path contains a symbolic link or reparse point")
	}
	if !info.IsDir() {
		return false, fmt.Errorf("GIS cache path contains a non-directory component")
	}
	return true, nil
}
