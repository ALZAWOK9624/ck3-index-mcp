package savefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPath is returned when a requested save lies outside every configured
// root, or is reached through something other than plain directories.
const ErrPath ErrorKind = "path_not_allowed"

// ResolveUnderRoots resolves one requested save path against the configured
// roots and refuses anything that escapes them.
//
// Saves arrive from untrusted uploads, so the caller supplies a name and the
// roots decide what that name is allowed to mean. A path may be given
// relative to a root or as an absolute path already inside one; either way it
// must land on a regular file reached only through real directories, so a
// symlink cannot redirect a read outside the roots.
func ResolveUnderRoots(roots []string, candidate string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		return "", newError(ErrPath, "no save path was given")
	}
	if strings.ContainsRune(candidate, 0) {
		return "", newError(ErrPath, "the save path contains a null byte")
	}
	if len(roots) == 0 {
		return "", newError(ErrPath, "no save roots are configured, so no save may be read")
	}

	var lastErr error
	for _, root := range roots {
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			lastErr = err
			continue
		}
		target := candidate
		if !filepath.IsAbs(target) {
			target = filepath.Join(absoluteRoot, target)
		}
		resolved, err := confineToRoot(absoluteRoot, target)
		if err != nil {
			lastErr = err
			continue
		}
		return resolved, nil
	}
	if lastErr != nil {
		// Report the most specific refusal seen rather than a generic one,
		// so "passes through a symlink" is not flattened into "not allowed".
		return "", lastErr
	}
	return "", newError(ErrPath, "the save path is not inside any configured save root")
}

// confineToRoot verifies that target is a regular file inside root, reached
// only through directories that are not symlinks.
func confineToRoot(root, target string) (string, error) {
	relative, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil {
		// A different volume, which Rel cannot express.
		return "", newError(ErrPath, "the save path is not inside any configured save root")
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", newError(ErrPath, "the save path escapes its configured save root")
	}
	if relative == "." {
		return "", newError(ErrPath, "the save path names a directory, not a save")
	}

	info, err := os.Lstat(root)
	if err != nil {
		return "", newError(ErrPath, "a configured save root does not exist")
	}
	if !info.IsDir() {
		return "", newError(ErrPath, "a configured save root is not a directory")
	}

	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." {
			return "", newError(ErrPath, "the save path has an empty component")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", newError(ErrPath, "the requested save does not exist")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", newError(ErrPath,
				fmt.Sprintf("the save path passes through a symlink at depth %d", index+1))
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return "", newError(ErrPath, "the requested save is not a regular file")
			}
			return current, nil
		}
		if !info.IsDir() {
			return "", newError(ErrPath, "the save path descends through a non-directory")
		}
	}
	return "", newError(ErrPath, "the save path is not inside any configured save root")
}
