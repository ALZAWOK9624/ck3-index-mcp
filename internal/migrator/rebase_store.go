package migrator

import (
	"bytes"
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
	rebaseProgressName      = "progress.json"
	rebaseDecisionsName     = "decisions.jsonl"
	rebaseResolutionName    = "resolutions.json"
	rebaseReportName        = "report.html"
	// rebaseProgressStride bounds how often the fast-moving progress file is
	// rewritten during a per-file loop. Progress is observability only: the
	// manifest still records every durable phase transition.
	rebaseProgressStride = 25
)

// rebaseManifestDocument is the on-disk shape of transaction.json. The
// embedded transaction supplies every durable field except Files, which the
// shadowing (always nil) member removes from the encoded manifest: keeping the
// full decision list in the manifest made planning and building rewrite the
// entire transaction once per file, which is quadratic in the file count. The
// decisions live in their own journal and are reattached on load.
//
// The shadow field also makes the decoder tolerate a "files" key, so a
// manifest written by a mixed-version toolchain is rejected on its schema
// version rather than on an unknown-field error.
type rebaseManifestDocument struct {
	RebaseTransaction
	Files json.RawMessage `json:"files,omitempty"`
}

// rebaseProgressDocument pins live progress to the manifest generation that
// produced it, so a stale progress file left by an interrupted run can never
// be shown against a newer transaction state.
type rebaseProgressDocument struct {
	Revision int64          `json:"revision"`
	Progress RebaseProgress `json:"progress"`
}

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

// writeRebaseTransaction publishes the manifest under a compare-and-set on the
// monotonic revision. A caller whose in-memory copy is older than the manifest
// on disk is refused instead of overwriting the newer state; that is the last
// defence behind the per-transaction lock when two processes still overlap.
func writeRebaseTransaction(root string, transaction *RebaseTransaction) error {
	dir, err := rebaseTransactionDir(root, transaction.ID)
	if err != nil {
		return err
	}
	dir, err = ensureRebaseDirectory(dir)
	if err != nil {
		return err
	}
	current, err := readRebaseManifestRevision(dir)
	if err != nil {
		return err
	}
	if current != transaction.Revision {
		return fmt.Errorf("rebase transaction %s was modified concurrently (expected revision %d, found %d); reload the transaction before writing", transaction.ID, transaction.Revision, current)
	}
	transaction.touch(time.Now())
	next := transaction.Revision + 1
	document := rebaseManifestDocument{RebaseTransaction: *transaction}
	document.Revision = next
	if err := writeJSONAtomic(filepath.Join(dir, rebaseManifestName), document, 0o600); err != nil {
		return err
	}
	transaction.Revision = next
	// The progress file is a projection of the manifest. Refreshing it here
	// keeps a reader from seeing progress pinned to a superseded revision.
	_ = writeRebaseProgress(dir, *transaction)
	return nil
}

func readRebaseManifestRevision(dir string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, rebaseManifestName))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var head struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return 0, fmt.Errorf("rebase transaction manifest is unreadable: %w", err)
	}
	return head.Revision, nil
}

// writeRebaseProgress rewrites only the small progress projection. It is
// deliberately not fsynced: losing the last few percent of a progress counter
// after a crash costs nothing, while fsyncing it once per file was a large
// part of the old per-file manifest cost.
func writeRebaseProgress(dir string, transaction RebaseTransaction) error {
	document := rebaseProgressDocument{Revision: transaction.Revision, Progress: transaction.Progress}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rebaseProgressName), append(data, '\n'), 0o600)
}

// checkpointRebaseProgress records live per-file progress without touching the
// manifest. Callers invoke it inside tight loops, so it throttles itself.
func checkpointRebaseProgress(root string, transaction RebaseTransaction, completed, total int) error {
	if completed != total && completed%rebaseProgressStride != 0 {
		return nil
	}
	dir, err := rebaseTransactionDir(root, transaction.ID)
	if err != nil {
		return err
	}
	return writeRebaseProgress(dir, transaction)
}

func loadRebaseProgress(dir string, revision int64) (RebaseProgress, bool) {
	data, err := os.ReadFile(filepath.Join(dir, rebaseProgressName))
	if err != nil {
		return RebaseProgress{}, false
	}
	var document rebaseProgressDocument
	if err := json.Unmarshal(data, &document); err != nil || document.Revision != revision {
		return RebaseProgress{}, false
	}
	return document.Progress, true
}

// writeRebaseDecisions replaces the decision journal in one atomic write. It is
// called at phase boundaries rather than per file, which is what turns the
// planning and build passes from quadratic into linear manifest traffic.
func writeRebaseDecisions(root, id string, decisions []RebaseFileDecision) error {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return err
	}
	dir, err = ensureRebaseDirectory(dir)
	if err != nil {
		return err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	for index := range decisions {
		if err := encoder.Encode(decisions[index]); err != nil {
			return err
		}
	}
	return writeBytesAtomic(filepath.Join(dir, rebaseDecisionsName), encoded.Bytes(), 0o600)
}

func loadRebaseDecisions(dir string) ([]RebaseFileDecision, error) {
	path := filepath.Join(dir, rebaseDecisionsName)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rebase decision journal is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var decisions []RebaseFileDecision
	for {
		var decision RebaseFileDecision
		if err := decoder.Decode(&decision); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("rebase decision journal is invalid: %w", err)
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
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
	var document rebaseManifestDocument
	if err := decoder.Decode(&document); err != nil {
		return RebaseTransaction{}, err
	}
	transaction := document.RebaseTransaction
	if transaction.SchemaVersion != RebaseSchemaVersion || transaction.ID != id {
		return RebaseTransaction{}, fmt.Errorf("rebase transaction identity or schema is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return RebaseTransaction{}, fmt.Errorf("rebase transaction manifest contains trailing data")
		}
		return RebaseTransaction{}, fmt.Errorf("rebase transaction manifest has invalid trailing data: %w", err)
	}
	decisions, err := loadRebaseDecisions(dir)
	if err != nil {
		return RebaseTransaction{}, err
	}
	transaction.Files = decisions
	// Live progress is only trusted when it belongs to this exact manifest
	// generation; the manifest itself always carries the durable phase.
	if progress, ok := loadRebaseProgress(dir, transaction.Revision); ok {
		transaction.Progress = progress
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
	return writeBytesAtomic(path, append(data, '\n'), mode)
}

func writeBytesAtomic(path string, data []byte, mode os.FileMode) error {
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
