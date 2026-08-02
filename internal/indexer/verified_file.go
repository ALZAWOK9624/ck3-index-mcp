package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type IndexedFileSnapshot struct {
	FileID     int64
	Source     string
	Path       string
	RelPath    string
	Size       int64
	SHA256     string
	Generation int64
	Revision   string
}

// SourceChangedError means the live file can no longer be proved identical to
// the generation that supplied its indexed metadata. Callers must refresh the
// named relative path before deriving semantic evidence from its bytes.
type SourceChangedError struct {
	Path              string
	Reason            string
	ExpectedSize      int64
	ActualSize        int64
	ExpectedSHA256    string
	ActualSHA256      string
	IndexedGeneration int64
	IndexedRevision   string
}

func (e *SourceChangedError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("indexed source %q changed after generation %d (%s)", e.Path, e.IndexedGeneration, e.Reason)
}

func (db *DB) ReadVerifiedIndexedFile(ctx context.Context, fileID int64) ([]byte, IndexedFileSnapshot, error) {
	if fileID <= 0 {
		return nil, IndexedFileSnapshot{}, fmt.Errorf("verified indexed read requires a positive file id")
	}
	stateBefore, err := db.IndexState(ctx)
	if err != nil {
		return nil, IndexedFileSnapshot{}, err
	}
	var snapshot IndexedFileSnapshot
	var overridden int
	snapshot.FileID = fileID
	if err := db.sql.QueryRowContext(ctx, `SELECT source_name,path,rel_path,file_size,sha256,overridden FROM files WHERE id=?`, fileID).
		Scan(&snapshot.Source, &snapshot.Path, &snapshot.RelPath, &snapshot.Size, &snapshot.SHA256, &overridden); err != nil {
		return nil, IndexedFileSnapshot{}, err
	}
	snapshot.Generation = stateBefore.Generation
	snapshot.Revision = stateBefore.Revision
	changed := func(reason string, actualSize int64, actualSHA string) error {
		return &SourceChangedError{
			Path: snapshot.RelPath, Reason: reason,
			ExpectedSize: snapshot.Size, ActualSize: actualSize,
			ExpectedSHA256: snapshot.SHA256, ActualSHA256: actualSHA,
			IndexedGeneration: stateBefore.Generation, IndexedRevision: stateBefore.Revision,
		}
	}
	if !stateBefore.Ready() {
		return nil, snapshot, changed("index generation is not ready", -1, "")
	}
	if overridden != 0 {
		return nil, snapshot, changed("indexed file is no longer active", -1, "")
	}
	root, err := indexedSourceRoot(snapshot.Path, snapshot.RelPath)
	if err != nil {
		return nil, snapshot, changed("indexed path no longer matches its source root", -1, "")
	}
	if err := validateSourceRoots([]Source{{Name: snapshot.Source, Path: root}}); err != nil {
		return nil, snapshot, changed("source root is no longer safe", -1, "")
	}
	safePath, info, err := sourceRegularFileAt(root, snapshot.RelPath)
	if err != nil || filepath.Clean(safePath) != filepath.Clean(snapshot.Path) {
		return nil, snapshot, changed("source file is missing or was replaced", -1, "")
	}
	if snapshot.Size < 0 || info.Size() != snapshot.Size {
		return nil, snapshot, changed("file size differs from the index", info.Size(), "")
	}
	if err := ctx.Err(); err != nil {
		return nil, snapshot, err
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		return nil, snapshot, changed("source file could not be read", -1, "")
	}
	digest := sha256.Sum256(data)
	actualSHA := hex.EncodeToString(digest[:])
	if snapshot.SHA256 == "" || !strings.EqualFold(actualSHA, snapshot.SHA256) {
		return nil, snapshot, changed("file hash differs from the index", int64(len(data)), actualSHA)
	}
	stateAfter, err := db.IndexState(ctx)
	if err != nil {
		return nil, snapshot, err
	}
	if !samePublishedIndexState(stateBefore, stateAfter) {
		return nil, snapshot, changed("published generation changed during the read", int64(len(data)), actualSHA)
	}
	return data, snapshot, nil
}
