package migrator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ck3-index/internal/indexer"
)

const (
	rebaseTransactionPrefix = "ck3-rebase-"
	rebaseManifestName      = "transaction.json"
	rebaseResolutionName    = "resolutions.json"
	rebaseReportName        = "report.html"
)

func rebaseArtifactRoot(cfg indexer.Config, opts RebaseOptions) (string, error) {
	root := strings.TrimSpace(opts.ArtifactRoot)
	if root == "" {
		root = cfg.ArtifactRoot
	}
	if root == "" {
		return "", fmt.Errorf("artifact root is not configured")
	}
	root = filepath.Join(root, "rebase-transactions")
	var err error
	root, err = resolveRebasePath(root)
	if err != nil {
		return "", fmt.Errorf("unsafe rebase artifact root: %w", err)
	}
	if err := ensureStorageOutsideSources(root, cfg.Sources); err != nil {
		return "", err
	}
	root, err = ensureRebaseDirectory(root)
	if err != nil {
		return "", err
	}
	// Recheck after creation so a concurrent or pre-existing symbolic link
	// cannot redirect subsequent transaction writes into a configured source.
	if err := ensureStorageOutsideSources(root, cfg.Sources); err != nil {
		return "", err
	}
	return root, nil
}

func newRebaseTransactionID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return rebaseTransactionPrefix + hex.EncodeToString(value[:]), nil
}

func validateRebaseTransactionID(id string) error {
	if !strings.HasPrefix(id, rebaseTransactionPrefix) || len(id) != len(rebaseTransactionPrefix)+16 {
		return fmt.Errorf("invalid rebase transaction id")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(id, rebaseTransactionPrefix)); err != nil {
		return fmt.Errorf("invalid rebase transaction id")
	}
	return nil
}

func rebaseTransactionDir(root, id string) (string, error) {
	if err := validateRebaseTransactionID(id); err != nil {
		return "", err
	}
	resolvedRoot, err := resolveRebasePath(root)
	if err != nil {
		return "", fmt.Errorf("unsafe rebase transaction root: %w", err)
	}
	full, err := safeRebaseContainedPath(resolvedRoot, filepath.Join(resolvedRoot, id))
	if err != nil {
		return "", fmt.Errorf("rebase transaction path escapes its root: %w", err)
	}
	return full, nil
}

func ensureRebaseDirectory(path string) (string, error) {
	safePath, err := resolveRebasePath(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(safePath, 0o755); err != nil {
		return "", err
	}
	// MkdirAll may have traversed a path changed between the first check and
	// creation. Resolve again and require a real directory before writing.
	safePath, err = resolveRebasePath(safePath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(safePath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("rebase storage path is not a real directory: %s", safePath)
	}
	return safePath, nil
}

func writeRebaseTransaction(root string, transaction *RebaseTransaction) error {
	dir, err := rebaseTransactionDir(root, transaction.ID)
	if err != nil {
		return err
	}
	dir, err = ensureRebaseDirectory(dir)
	if err != nil {
		return err
	}
	transaction.touch(time.Now())
	return writeJSONAtomic(filepath.Join(dir, rebaseManifestName), transaction, 0o600)
}

func LoadRebaseTransaction(cfg indexer.Config, id string, opts RebaseOptions) (RebaseTransaction, error) {
	root, err := rebaseArtifactRoot(cfg, opts)
	if err != nil {
		return RebaseTransaction{}, err
	}
	return loadRebaseTransactionFromRoot(root, id)
}

func loadRebaseTransactionFromRoot(root, id string) (RebaseTransaction, error) {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return RebaseTransaction{}, err
	}
	path := filepath.Join(dir, rebaseManifestName)
	info, err := os.Lstat(path)
	if err != nil {
		return RebaseTransaction{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return RebaseTransaction{}, fmt.Errorf("rebase transaction manifest is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return RebaseTransaction{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var transaction RebaseTransaction
	if err := decoder.Decode(&transaction); err != nil {
		return RebaseTransaction{}, err
	}
	if transaction.SchemaVersion != RebaseSchemaVersion || transaction.ID != id {
		return RebaseTransaction{}, fmt.Errorf("rebase transaction identity or schema is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return RebaseTransaction{}, fmt.Errorf("rebase transaction manifest contains trailing data")
		}
		return RebaseTransaction{}, fmt.Errorf("rebase transaction manifest has invalid trailing data: %w", err)
	}
	return transaction, nil
}

func writeRebaseResolutions(root, id string, resolutions []RebaseResolution) error {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return err
	}
	dir, err = ensureRebaseDirectory(dir)
	if err != nil {
		return err
	}
	sort.Slice(resolutions, func(i, j int) bool { return resolutions[i].ConflictID < resolutions[j].ConflictID })
	return writeJSONAtomic(filepath.Join(dir, rebaseResolutionName), resolutions, 0o600)
}

func loadRebaseResolutions(root, id string) ([]RebaseResolution, error) {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, rebaseResolutionName)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rebase resolutions are not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var resolutions []RebaseResolution
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&resolutions); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("rebase resolutions contain trailing data")
		}
		return nil, fmt.Errorf("rebase resolutions have invalid trailing data: %w", err)
	}
	return resolutions, nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	parent, err := ensureRebaseDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	path, err = safeRebaseContainedPath(parent, filepath.Join(parent, filepath.Base(path)))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".rebase-json-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceRebaseFileAtomically(path, tmpName)
}

// replaceRebaseFileAtomically publishes a fully-synced temporary file without
// deleting the previous manifest first.  Windows cannot rename over a regular
// file, so the previous generation is first moved to a recoverable sibling.
// A process interruption can therefore leave a .previous file, but never a
// deliberate delete-before-replace gap.
func replaceRebaseFileAtomically(path, replacement string) error {
	parent, err := ensureRebaseDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	path, err = safeRebaseContainedPath(parent, filepath.Join(parent, filepath.Base(path)))
	if err != nil {
		return err
	}
	replacement, err = safeRebaseContainedPath(parent, replacement)
	if err != nil {
		return fmt.Errorf("replacement path escapes rebase storage: %w", err)
	}
	previous := path + ".previous"
	if info, err := os.Lstat(previous); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("previous rebase file is not a regular file: %s", previous)
		}
		if _, currentErr := os.Lstat(path); os.IsNotExist(currentErr) {
			if err := os.Rename(previous, path); err != nil {
				return fmt.Errorf("restore interrupted previous rebase file: %w", err)
			}
		} else if currentErr != nil {
			return currentErr
		} else if err := os.Remove(previous); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	movedPrevious := false
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("rebase file is not a regular file: %s", path)
		}
		if err := os.Rename(path, previous); err != nil {
			return err
		}
		movedPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(replacement, path); err != nil {
		if movedPrevious {
			if restoreErr := os.Rename(previous, path); restoreErr != nil {
				return fmt.Errorf("publish rebase file: %v; restore previous generation: %w", err, restoreErr)
			}
		}
		return err
	}
	if movedPrevious {
		if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
			// The new generation is already authoritative. Leaving a regular
			// sibling is recoverable and will be cleaned on the next write; do
			// not report a failed transaction after its new manifest is live.
		}
	}
	return nil
}

func inventoryFingerprint(files []SnapshotFile) string {
	ordered := append([]SnapshotFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return strings.ToLower(ordered[i].Path) < strings.ToLower(ordered[j].Path) })
	return combinedFileHash(ordered)
}

func fileState(file *SnapshotFile) RebaseFileState {
	if file == nil {
		return RebaseFileState{}
	}
	return RebaseFileState{Exists: true, SHA256: file.SHA256, Size: file.Size, Text: file.Text}
}
