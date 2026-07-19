package migrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/indexer"
)

func CreateSnapshot(ctx context.Context, cfg indexer.Config, spec SnapshotSpec) (SnapshotResult, error) {
	project, err := sourceByName(cfg, spec.Project)
	if err != nil {
		return SnapshotResult{}, err
	}
	base, err := sourceByName(cfg, spec.Base)
	if err != nil {
		return SnapshotResult{}, err
	}
	if strings.EqualFold(project.Name, base.Name) {
		return SnapshotResult{}, fmt.Errorf("project and base must be different configured sources")
	}
	projectPath, _ := filepath.Abs(project.Path)
	basePath, _ := filepath.Abs(base.Path)
	if strings.EqualFold(filepath.Clean(projectPath), filepath.Clean(basePath)) {
		return SnapshotResult{}, fmt.Errorf("project and base must use different configured directories")
	}
	if project.Rank >= base.Rank {
		return SnapshotResult{}, fmt.Errorf("project must have a higher load priority than base")
	}
	if strings.TrimSpace(cfg.MigrationSnapshotRoot) == "" {
		return SnapshotResult{}, fmt.Errorf("migration snapshot root is not configured")
	}
	if err := ensureStorageOutsideSources(cfg.MigrationSnapshotRoot, cfg.Sources); err != nil {
		return SnapshotResult{}, err
	}
	projectFiles, _, err := collectFiles(project.Path)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("project inventory: %w", err)
	}
	baseFiles, _, err := collectFiles(base.Path)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("base inventory: %w", err)
	}
	for i := range baseFiles {
		baseFiles[i].Blob = ""
	}
	activeMap, err := collectActiveSnapshotMap(project, base)
	if err != nil {
		return SnapshotResult{}, err
	}
	manifest := SnapshotManifest{SchemaVersion: SchemaVersion, Project: project.Name, Base: base.Name, ProjectFiles: projectFiles, BaseFiles: baseFiles, ActiveMapFiles: activeMap}
	manifest.ActiveMapSHA256 = combinedFileHash(activeMap)
	snapshotID, err := snapshotIDForManifest(manifest)
	if err != nil {
		return SnapshotResult{}, err
	}
	finalRoot := filepath.Join(cfg.MigrationSnapshotRoot, snapshotID)
	if _, statErr := os.Stat(finalRoot); statErr == nil {
		existing, loadErr := loadSnapshotManifest(finalRoot)
		if loadErr != nil {
			return SnapshotResult{}, fmt.Errorf("existing migration snapshot is invalid: %w", loadErr)
		}
		if existing.ActiveMapSHA256 == manifest.ActiveMapSHA256 {
			return snapshotResult(snapshotID, finalRoot, existing, true), nil
		}
		return SnapshotResult{}, fmt.Errorf("snapshot collision: %s", snapshotID)
	} else if !os.IsNotExist(statErr) {
		return SnapshotResult{}, statErr
	}
	if err := os.MkdirAll(cfg.MigrationSnapshotRoot, 0o755); err != nil {
		return SnapshotResult{}, err
	}
	stage, err := os.MkdirTemp(cfg.MigrationSnapshotRoot, ".migration-snapshot-stage-")
	if err != nil {
		return SnapshotResult{}, err
	}
	defer os.RemoveAll(stage)

	projectSet := fileMap(projectFiles)
	blobSeen := map[string]bool{}
	for i := range manifest.BaseFiles {
		if ctx.Err() != nil {
			return SnapshotResult{}, ctx.Err()
		}
		old := &manifest.BaseFiles[i]
		if !old.Text || projectSet[strings.ToLower(old.Path)] == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base.Path, filepath.FromSlash(old.Path)))
		if err != nil {
			return SnapshotResult{}, err
		}
		blob := hashBytes(data)
		if blob != old.SHA256 {
			return SnapshotResult{}, fmt.Errorf("base file changed while snapshot was being created: %s", old.Path)
		}
		old.Blob = blob
		if !blobSeen[blob] {
			blobSeen[blob] = true
			if err := os.MkdirAll(filepath.Join(stage, "blobs"), 0o755); err != nil {
				return SnapshotResult{}, err
			}
			if err := os.WriteFile(filepath.Join(stage, "blobs", blob), data, 0o600); err != nil {
				return SnapshotResult{}, err
			}
		}
	}
	for _, file := range activeMap {
		source := activeMapSourcePath(project, base, file.Path)
		target := filepath.Join(stage, "active_map", filepath.FromSlash(file.Path))
		if err := copyFile(source, target); err != nil {
			return SnapshotResult{}, err
		}
		copiedHash, _, err := hashPath(target)
		if err != nil {
			return SnapshotResult{}, err
		}
		if copiedHash != file.SHA256 {
			return SnapshotResult{}, fmt.Errorf("active map file changed while snapshot was being created: %s", file.Path)
		}
	}
	manifestData, err := canonicalJSON(manifest)
	if err != nil {
		return SnapshotResult{}, err
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(stage, "snapshot.json"), manifestData, 0o600); err != nil {
		return SnapshotResult{}, err
	}
	if _, err := os.Stat(finalRoot); err == nil {
		existing, loadErr := loadSnapshotManifest(finalRoot)
		if loadErr == nil && existing.ActiveMapSHA256 == manifest.ActiveMapSHA256 {
			return snapshotResult(snapshotID, finalRoot, existing, true), nil
		}
		return SnapshotResult{}, fmt.Errorf("snapshot collision: %s", snapshotID)
	} else if !os.IsNotExist(err) {
		return SnapshotResult{}, err
	}
	if err := os.Rename(stage, finalRoot); err != nil {
		return SnapshotResult{}, err
	}
	return snapshotResult(snapshotID, finalRoot, manifest, false), nil
}

func collectActiveSnapshotMap(project, base indexer.Source) ([]SnapshotFile, error) {
	var out []SnapshotFile
	for _, rel := range []string{"map_data/definition.csv", "map_data/provinces.png", "map_data/default.map"} {
		full := activeMapSourcePath(project, base, rel)
		if rel == "map_data/provinces.png" {
			if _, err := os.Stat(full); os.IsNotExist(err) {
				rel = "map_data/provinces.bmp"
				full = activeMapSourcePath(project, base, rel)
			}
		}
		_, err := os.Stat(full)
		if err != nil {
			if rel == "map_data/default.map" && os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("active old map requires %s", rel)
		}
		hash, size, err := hashPath(full)
		if err != nil {
			return nil, err
		}
		out = append(out, SnapshotFile{Path: rel, SHA256: hash, Size: size, Text: isTextPath(rel)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func activeMapSourcePath(project, base indexer.Source, rel string) string {
	projectPath := filepath.Join(project.Path, filepath.FromSlash(rel))
	if _, err := os.Stat(projectPath); err == nil {
		return projectPath
	}
	return filepath.Join(base.Path, filepath.FromSlash(rel))
}

func fileMap(files []SnapshotFile) map[string]*SnapshotFile {
	out := make(map[string]*SnapshotFile, len(files))
	for i := range files {
		file := files[i]
		out[strings.ToLower(file.Path)] = &file
	}
	return out
}

func combinedFileHash(files []SnapshotFile) string {
	h := sha256.New()
	for _, file := range files {
		_, _ = h.Write([]byte(file.Path + "\x00" + file.SHA256 + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func loadSnapshotManifest(root string) (SnapshotManifest, error) {
	manifestPath := filepath.Join(root, "snapshot.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return SnapshotManifest{}, fmt.Errorf("migration snapshot manifest is not a regular file")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return SnapshotManifest{}, err
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return SnapshotManifest{}, err
	}
	if manifest.SchemaVersion != SchemaVersion {
		return SnapshotManifest{}, fmt.Errorf("unsupported migration snapshot schema %d", manifest.SchemaVersion)
	}
	if err := validateSnapshotManifest(root, manifest); err != nil {
		return SnapshotManifest{}, err
	}
	if err := verifySnapshotStorage(root, manifest); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

func snapshotIDForManifest(manifest SnapshotManifest) (string, error) {
	identityManifest := manifest
	identityManifest.BaseFiles = append([]SnapshotFile(nil), manifest.BaseFiles...)
	for i := range identityManifest.BaseFiles {
		identityManifest.BaseFiles[i].Blob = ""
	}
	identity, err := canonicalJSON(identityManifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(identity)
	return "map-snapshot-" + hex.EncodeToString(sum[:8]), nil
}

func validateSnapshotManifest(root string, manifest SnapshotManifest) error {
	if strings.TrimSpace(manifest.Project) == "" || strings.TrimSpace(manifest.Base) == "" ||
		strings.ContainsAny(manifest.Project+manifest.Base, "\x00\r\n") {
		return fmt.Errorf("migration snapshot source names are invalid")
	}
	if strings.EqualFold(manifest.Project, manifest.Base) {
		return fmt.Errorf("migration snapshot project and base must be different")
	}
	if err := validateSnapshotInventory("project", manifest.ProjectFiles, false); err != nil {
		return err
	}
	if err := validateSnapshotInventory("base", manifest.BaseFiles, true); err != nil {
		return err
	}
	if err := validateSnapshotActiveMap(manifest.ActiveMapFiles); err != nil {
		return err
	}
	if !validSHA256(manifest.ActiveMapSHA256) || combinedFileHash(manifest.ActiveMapFiles) != manifest.ActiveMapSHA256 {
		return fmt.Errorf("migration snapshot active map fingerprint is invalid")
	}
	expectedID, err := snapshotIDForManifest(manifest)
	if err != nil {
		return err
	}
	if filepath.Base(filepath.Clean(root)) != expectedID {
		return fmt.Errorf("migration snapshot content identity does not match snapshot_id")
	}
	return nil
}

func validateSnapshotInventory(label string, files []SnapshotFile, allowBlobs bool) error {
	seen := map[string]bool{}
	for _, file := range files {
		normalized, err := normalizeRel(file.Path)
		if err != nil || normalized != file.Path || !supportedRel(file.Path) {
			return fmt.Errorf("migration snapshot %s path is invalid: %q", label, file.Path)
		}
		folded := strings.ToLower(file.Path)
		if seen[folded] {
			return fmt.Errorf("migration snapshot %s contains duplicate path %q", label, file.Path)
		}
		seen[folded] = true
		if !validSHA256(file.SHA256) || file.Size < 0 {
			return fmt.Errorf("migration snapshot %s file metadata is invalid: %s", label, file.Path)
		}
		if file.Text != isTextPath(file.Path) {
			return fmt.Errorf("migration snapshot %s text classification is invalid: %s", label, file.Path)
		}
		if file.Blob == "" {
			continue
		}
		if !allowBlobs || !file.Text || !validSHA256(file.Blob) || file.Blob != file.SHA256 {
			return fmt.Errorf("migration snapshot %s blob metadata is invalid: %s", label, file.Path)
		}
	}
	return nil
}

func validateSnapshotActiveMap(files []SnapshotFile) error {
	allowed := map[string]bool{
		"map_data/definition.csv": true,
		"map_data/provinces.png":  true,
		"map_data/provinces.bmp":  true,
		"map_data/default.map":    true,
	}
	seen := map[string]bool{}
	for _, file := range files {
		if !allowed[file.Path] || seen[file.Path] || !validSHA256(file.SHA256) || file.Size < 0 ||
			file.Blob != "" || file.Text != isTextPath(file.Path) {
			return fmt.Errorf("migration snapshot active map entry is invalid: %q", file.Path)
		}
		seen[file.Path] = true
	}
	if !seen["map_data/definition.csv"] || seen["map_data/provinces.png"] == seen["map_data/provinces.bmp"] {
		return fmt.Errorf("migration snapshot active map inventory is incomplete")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func verifySnapshotStorage(root string, manifest SnapshotManifest) error {
	verifiedBlobs := map[string]bool{}
	for _, file := range manifest.BaseFiles {
		if file.Blob == "" || verifiedBlobs[file.Blob] {
			continue
		}
		verifiedBlobs[file.Blob] = true
		if err := verifySnapshotStoredFile(root, filepath.Join("blobs", file.Blob), file.SHA256, file.Size); err != nil {
			return fmt.Errorf("verify migration snapshot blob %s: %w", file.Blob, err)
		}
	}
	for _, file := range manifest.ActiveMapFiles {
		rel := filepath.Join("active_map", filepath.FromSlash(file.Path))
		if err := verifySnapshotStoredFile(root, rel, file.SHA256, file.Size); err != nil {
			return fmt.Errorf("verify migration snapshot active map %s: %w", file.Path, err)
		}
	}
	return nil
}

func verifySnapshotStoredFile(root, rel, expectedHash string, expectedSize int64) error {
	full, err := snapshotStoredFilePath(root, rel)
	if err != nil {
		return err
	}
	hash, size, err := hashPath(full)
	if err != nil {
		return err
	}
	if hash != expectedHash || size != expectedSize {
		return fmt.Errorf("stored content hash or size does not match the manifest")
	}
	return nil
}

func snapshotStoredFilePath(root, rel string) (string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("migration snapshot root is not a regular directory")
	}
	full := filepath.Join(root, rel)
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(full))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("migration snapshot storage path escapes its root")
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("migration snapshot storage entry is not a regular file")
	}
	return full, nil
}

func readSnapshotBaseline(root string, file SnapshotFile) ([]byte, error) {
	if file.Blob == "" || !file.Text || !validSHA256(file.Blob) || file.Blob != file.SHA256 || file.Size < 0 {
		return nil, fmt.Errorf("snapshot baseline metadata is invalid")
	}
	full, err := snapshotStoredFilePath(root, filepath.Join("blobs", file.Blob))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != file.Size {
		return nil, fmt.Errorf("snapshot baseline size does not match the manifest")
	}
	maxInt := int(^uint(0) >> 1)
	if uint64(file.Size) > uint64(maxInt) {
		return nil, fmt.Errorf("snapshot baseline is too large to read")
	}
	data := make([]byte, int(file.Size))
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, err
	}
	var extra [1]byte
	if n, err := f.Read(extra[:]); n != 0 || (err != nil && err != io.EOF) {
		if err != nil && err != io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("snapshot baseline grew while it was being read")
	}
	if hashBytes(data) != file.SHA256 {
		return nil, fmt.Errorf("snapshot baseline hash does not match the manifest")
	}
	return data, nil
}

func snapshotResult(id, root string, manifest SnapshotManifest, reused bool) SnapshotResult {
	var bytes int64
	blobs := map[string]bool{}
	for _, file := range manifest.BaseFiles {
		if file.Blob != "" && !blobs[file.Blob] {
			blobs[file.Blob] = true
			bytes += file.Size
		}
	}
	for _, file := range manifest.ActiveMapFiles {
		bytes += file.Size
	}
	return SnapshotResult{Status: "ready", SnapshotID: id, Project: manifest.Project, Base: manifest.Base,
		FileCount: len(manifest.ProjectFiles) + len(manifest.BaseFiles), BaselineBlobCount: len(blobs), StoredBytes: bytes,
		ActiveMapSHA256: manifest.ActiveMapSHA256, SnapshotRelPath: filepath.Base(root), Reused: reused}
}
