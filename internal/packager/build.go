package packager

import (
	"archive/zip"
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
	"time"
)

var deterministicZipTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	artifactRecordSchemaVersion = 1
	artifactRecordPrefix        = ".ck3-package-artifact-"
	artifactRecordSuffix        = ".json"
	artifactStagePrefix         = ".ck3-package-stage-"
)

// artifactRecord is a local ownership marker for a generated archive. It is
// deliberately kept outside the ZIP so expiry cleanup never has to treat a
// filename alone as proof that ck3-index owns it.
type artifactRecord struct {
	SchemaVersion int    `json:"schema_version"`
	ArtifactID    string `json:"artifact_id"`
	ArchiveName   string `json:"archive_name"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	CreatedAt     string `json:"created_at"`
}

func Build(ctx context.Context, request Request, options BuildOptions) (Result, error) {
	if strings.TrimSpace(options.ArtifactRoot) == "" {
		return Result{}, fmt.Errorf("artifact root is required")
	}
	if options.Retention <= 0 {
		options.Retention = DefaultRetentionHours * time.Hour
	}
	meta, contentFiles, repairs, normalization := normalizeRequest(request, options.Limits)
	result := Result{Status: "blocked", Validation: normalization, ExcludedFiles: append([]string(nil), options.ExcludedFiles...), Repairs: repairs}
	if normalization.Blocked {
		return result, nil
	}
	if options.Validator == nil {
		return Result{}, fmt.Errorf("package validator is required")
	}
	validation, err := options.Validator.Validate(ctx, contentFiles)
	if err != nil {
		return Result{}, err
	}
	result.Validation = validation
	if validation.Blocked {
		return result, nil
	}
	if err := os.MkdirAll(options.ArtifactRoot, 0o755); err != nil {
		return Result{}, err
	}
	if err := cleanupArtifacts(options.ArtifactRoot, options.Retention, time.Now()); err != nil {
		return Result{}, fmt.Errorf("clean expired package artifacts: %w", err)
	}
	stage, err := os.MkdirTemp(options.ArtifactRoot, ".ck3-package-stage-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)

	archiveEntries := buildArchiveEntries(meta, contentFiles, validation)
	temporaryArchive := filepath.Join(stage, "package.partial")
	if err := writeDeterministicZip(temporaryArchive, archiveEntries); err != nil {
		return Result{}, err
	}
	hash, size, err := hashFile(temporaryArchive)
	if err != nil {
		return Result{}, err
	}
	artifactID := meta.Slug + "-" + hash[:16]
	archiveName := artifactID + ".zip"
	finalPath := filepath.Join(options.ArtifactRoot, archiveName)
	if info, err := os.Stat(finalPath); err == nil {
		if !info.Mode().IsRegular() {
			return Result{}, fmt.Errorf("artifact collision is not a regular file: %s", archiveName)
		}
		existingHash, existingSize, err := hashFile(finalPath)
		if err != nil {
			return Result{}, err
		}
		if existingHash != hash || existingSize != size {
			return Result{}, fmt.Errorf("artifact collision has unexpected content: %s", archiveName)
		}
		now := time.Now()
		if err := os.Chtimes(finalPath, now, now); err != nil {
			return Result{}, err
		}
	} else if !os.IsNotExist(err) {
		return Result{}, err
	} else if err := os.Rename(temporaryArchive, finalPath); err != nil {
		return Result{}, err
	}
	now := time.Now()
	if err := writeArtifactRecord(options.ArtifactRoot, artifactRecord{
		SchemaVersion: artifactRecordSchemaVersion,
		ArtifactID:    artifactID,
		ArchiveName:   archiveName,
		SHA256:        hash,
		Size:          size,
		CreatedAt:     now.UTC().Format(time.RFC3339),
	}); err != nil {
		return Result{}, fmt.Errorf("record package artifact: %w", err)
	}
	result.Status = "ready"
	result.ArtifactID = artifactID
	result.ArtifactRelPath = archiveName
	result.ArchiveName = archiveName
	result.SHA256 = hash
	result.Size = size
	result.FileCount = len(archiveEntries)
	result.ExpiresAt = now.Add(options.Retention).UTC().Format(time.RFC3339)
	return result, nil
}

func buildArchiveEntries(meta Metadata, content []PreparedFile, validation ValidationReport) []PreparedFile {
	var entries []PreparedFile
	launcherPath := meta.Slug + ".mod"
	entries = append(entries, PreparedFile{Path: launcherPath, Data: descriptorContent(meta, true)})
	entries = append(entries, PreparedFile{Path: meta.Slug + "/descriptor.mod", Data: descriptorContent(meta, false)})
	for _, file := range content {
		entries = append(entries, PreparedFile{Path: meta.Slug + "/" + file.Path, Data: file.Data})
	}
	install := installText(meta)
	entries = append(entries, PreparedFile{Path: "INSTALL.txt", Data: []byte(install)})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	manifest := Manifest{SchemaVersion: SchemaVersion, Metadata: meta}
	manifest.Validation.Blockers = validation.Blockers
	manifest.Validation.Warnings = validation.Warnings
	manifest.Validation.Summary = validation.Summary
	for _, file := range entries {
		sum := sha256.Sum256(file.Data)
		manifest.Files = append(manifest.Files, ManifestEntry{Path: file.Path, Size: len(file.Data), SHA256: hex.EncodeToString(sum[:])})
	}
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	manifestData = append(manifestData, '\n')
	entries = append(entries, PreparedFile{Path: "ck3-package-manifest.json", Data: manifestData})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func installText(meta Metadata) string {
	var b strings.Builder
	b.WriteString("CK3 Mod installation\n\n")
	b.WriteString("1. Extract this ZIP directly into your Crusader Kings III mod directory.\n")
	b.WriteString("2. Confirm that both " + meta.Slug + ".mod and the " + meta.Slug + " folder are present.\n")
	b.WriteString("3. Add " + meta.Name + " to a launcher playset and enable it.\n\n")
	b.WriteString("Supported CK3 version: " + meta.SupportedVersion + "\n")
	if len(meta.Dependencies) > 0 {
		b.WriteString("Dependencies:\n")
		for _, dependency := range meta.Dependencies {
			b.WriteString("- " + dependency + "\n")
		}
	}
	return b.String()
}

func writeDeterministicZip(path string, files []PreparedFile) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for _, file := range files {
		header := &zip.FileHeader{Name: filepathSlash(file.Path), Method: zip.Deflate}
		header.SetModTime(deterministicZipTime)
		header.SetMode(0o644)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			zw.Close()
			f.Close()
			return err
		}
		if _, err := writer.Write(file.Data); err != nil {
			zw.Close()
			f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
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

func cleanupArtifacts(root string, retention time.Duration, now time.Time) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	cutoff := now.Add(-retention)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), artifactStagePrefix) {
			if err := cleanupExpiredStage(root, entry, cutoff); err != nil {
				return err
			}
			continue
		}
		if !isGeneratedArchiveName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect artifact %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		record, ok := ownedArtifactRecord(root, entry.Name())
		if !ok {
			continue
		}
		archivePath := filepath.Join(root, entry.Name())
		hash, size, err := hashFile(archivePath)
		if err != nil {
			return fmt.Errorf("verify artifact %s: %w", entry.Name(), err)
		}
		if record.SHA256 != hash || record.Size != size {
			continue
		}
		if err := os.Remove(archivePath); err != nil {
			return fmt.Errorf("remove expired artifact %s: %w", entry.Name(), err)
		}
		if err := os.Remove(filepath.Join(root, artifactRecordName(entry.Name()))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove artifact record %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func cleanupExpiredStage(root string, entry os.DirEntry, cutoff time.Time) error {
	if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("inspect package stage %s: %w", entry.Name(), err)
	}
	if !info.ModTime().Before(cutoff) {
		return nil
	}
	if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
		return fmt.Errorf("remove expired package stage %s: %w", entry.Name(), err)
	}
	return nil
}

func writeArtifactRecord(root string, record artifactRecord) error {
	if !validArtifactRecord(record, record.ArchiveName) {
		return fmt.Errorf("invalid artifact record")
	}
	path := filepath.Join(root, artifactRecordName(record.ArchiveName))
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("artifact record collision is not a regular file: %s", filepath.Base(path))
		}
		existing, ok := ownedArtifactRecord(root, record.ArchiveName)
		if !ok || existing.SHA256 != record.SHA256 || existing.Size != record.Size {
			return fmt.Errorf("artifact record collision has unexpected content: %s", filepath.Base(path))
		}
		now := time.Now()
		return os.Chtimes(path, now, now)
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func ownedArtifactRecord(root, archiveName string) (artifactRecord, bool) {
	path := filepath.Join(root, artifactRecordName(archiveName))
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return artifactRecord{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return artifactRecord{}, false
	}
	var record artifactRecord
	if err := json.Unmarshal(data, &record); err != nil || !validArtifactRecord(record, archiveName) {
		return artifactRecord{}, false
	}
	return record, true
}

func artifactRecordName(archiveName string) string {
	return artifactRecordPrefix + strings.TrimSuffix(archiveName, ".zip") + artifactRecordSuffix
}

func validArtifactRecord(record artifactRecord, archiveName string) bool {
	if record.SchemaVersion != artifactRecordSchemaVersion || record.ArchiveName != archiveName || record.ArtifactID != strings.TrimSuffix(archiveName, ".zip") || record.Size < 0 || len(record.SHA256) != sha256.Size*2 {
		return false
	}
	if _, err := time.Parse(time.RFC3339, record.CreatedAt); err != nil {
		return false
	}
	for _, r := range record.SHA256 {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return isGeneratedArchiveName(archiveName)
}

func isGeneratedArchiveName(name string) bool {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".zip") {
		return false
	}
	id := strings.TrimSuffix(name, ".zip")
	separator := strings.LastIndexByte(id, '-')
	if separator <= 0 || len(id)-separator-1 != 16 {
		return false
	}
	for _, r := range id[separator+1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
