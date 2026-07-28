package migrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// rebaseVerifyRenameDomain proves that the migration copy can actually be
// renamed onto the formal project path.
//
// Comparing filepath.VolumeName is not sufficient. It identifies a drive
// letter on Windows, but on Linux and macOS every absolute path has an empty
// volume name, so two directories on different mount points compare equal.
// Promotion then passes its first rename (project -> backup) and fails the
// second one with EXDEV, leaving the formal path temporarily absent and only
// recoverable through the backup.
//
// Two cases are accepted:
//
//   - The output is a sibling of the project. Promotion is then a rename
//     inside one directory, which no filesystem can refuse for being
//     cross-device.
//   - An actual probe rename from the output's parent into the project's
//     parent succeeds. This is the only portable way to observe a rename
//     domain, and it also proves the write access that promotion needs in the
//     project's parent directory.
func rebaseVerifyRenameDomain(outputDir, projectPath string) error {
	output, err := resolveRebasePath(outputDir)
	if err != nil {
		return fmt.Errorf("unsafe migration copy output: %w", err)
	}
	project, err := resolveRebasePath(projectPath)
	if err != nil {
		return fmt.Errorf("unsafe formal project path: %w", err)
	}
	outputParent := filepath.Dir(output)
	projectParent := filepath.Dir(project)
	if sameRebaseDirectory(outputParent, projectParent) {
		return nil
	}
	// A different drive letter is a certain failure on Windows and is worth
	// reporting without touching the filesystem.
	if volume := filepath.VolumeName(output); !strings.EqualFold(volume, filepath.VolumeName(project)) {
		return fmt.Errorf("migration copy output is on a different volume than the formal project; atomic promotion requires one filesystem")
	}
	if _, err := ensureRebaseDirectory(outputParent); err != nil {
		return fmt.Errorf("prepare migration copy output parent: %w", err)
	}
	if err := rebaseRequireRegularDirectory(projectParent); err != nil {
		return fmt.Errorf("formal project parent: %w", err)
	}
	probe, err := os.MkdirTemp(outputParent, ".ck3-rebase-rename-probe-")
	if err != nil {
		return fmt.Errorf("probe migration copy rename domain: %w", err)
	}
	moved := filepath.Join(projectParent, filepath.Base(probe))
	if err := os.Rename(probe, moved); err != nil {
		_ = os.RemoveAll(probe)
		return fmt.Errorf("migration copy output and the formal project are not in one filesystem rename domain (%v); promotion could move the project to its backup and then fail to publish the copy, so choose an output_dir beside the formal project", err)
	}
	if err := os.RemoveAll(moved); err != nil {
		return fmt.Errorf("clean migration copy rename probe: %w", err)
	}
	return nil
}

func sameRebaseDirectory(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if strings.EqualFold(filepath.VolumeName(left), filepath.VolumeName(right)) && filepath.VolumeName(left) != "" {
		// Windows paths are case-insensitive; a case-only spelling difference
		// still names one directory.
		return strings.EqualFold(left, right)
	}
	return left == right
}
