package migrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ck3-index/internal/indexer"
)

func BuildMigration(ctx context.Context, cfg indexer.Config, spec MigrationSpec, opts BuildOptions) (MigrationResult, error) {
	result := MigrationResult{Status: "blocked", SnapshotID: spec.SnapshotID, Target: spec.Target,
		Validation: Validation{PreflightCounts: map[string]int{}},
		Guidance:   []string{"Resolve every reported conflict and run the migration again; never globally replace bare province numbers."}}
	if err := validateSnapshotID(spec.SnapshotID); err != nil {
		return result, err
	}
	snapshotRoot := filepath.Join(cfg.MigrationSnapshotRoot, spec.SnapshotID)
	manifest, err := loadSnapshotManifest(snapshotRoot)
	if err != nil {
		return result, fmt.Errorf("load migration snapshot: %w", err)
	}
	project, err := sourceByName(cfg, manifest.Project)
	if err != nil {
		return result, err
	}
	target, err := sourceByName(cfg, spec.Target)
	if err != nil {
		return result, err
	}
	projectPath, _ := filepath.Abs(project.Path)
	targetPath, _ := filepath.Abs(target.Path)
	if strings.EqualFold(project.Name, target.Name) || strings.EqualFold(filepath.Clean(projectPath), filepath.Clean(targetPath)) {
		return result, fmt.Errorf("migration target must be a configured upstream source, not the project source")
	}
	if strings.TrimSpace(opts.ArtifactRoot) == "" {
		opts.ArtifactRoot = cfg.ArtifactRoot
	}
	if opts.Retention <= 0 {
		hours := cfg.ArtifactRetentionHours
		if hours <= 0 {
			hours = 24 * 7
		}
		opts.Retention = time.Duration(hours) * time.Hour
	}
	if opts.ArtifactRoot == "" {
		return result, fmt.Errorf("artifact root is not configured")
	}
	if err := ensureStorageOutsideSources(opts.ArtifactRoot, cfg.Sources); err != nil {
		return result, err
	}
	if opts.OutputDir != "" {
		if err := ensureStorageOutsideSources(opts.OutputDir, cfg.Sources); err != nil {
			return result, err
		}
	}
	if err := os.MkdirAll(opts.ArtifactRoot, 0o755); err != nil {
		return result, err
	}
	if err := cleanupMigrationArtifacts(opts.ArtifactRoot, opts.Retention); err != nil {
		return result, fmt.Errorf("clean expired migration artifacts: %w", err)
	}

	projectFiles, projectExcluded, err := collectFiles(project.Path)
	if err != nil {
		return result, fmt.Errorf("current project inventory: %w", err)
	}
	result.ExcludedFiles = projectExcluded
	if drift := inventoryDifference(manifest.ProjectFiles, projectFiles); drift != "" {
		result.Conflicts = append(result.Conflicts, generalConflict("project_changed_after_snapshot", "", drift, ""))
	}
	targetFiles, targetExcluded, err := collectFiles(target.Path)
	if err != nil {
		return result, fmt.Errorf("target inventory: %w", err)
	}
	result.ExcludedFiles = append(result.ExcludedFiles, targetExcluded...)
	sort.Strings(result.ExcludedFiles)

	mapping, err := mappingForMigration(ctx, snapshotRoot, target, spec.ControlPoints)
	if err != nil {
		return result, fmt.Errorf("province mapping: %w", err)
	}
	result.Mapping = mapping.Summary
	targetIDs, err := definitionIDs(target.Path)
	if err != nil {
		return result, fmt.Errorf("target definition: %w", err)
	}
	sourceWater, err := waterProvinceIDs(filepath.Join(snapshotRoot, "active_map"))
	if err != nil {
		return result, fmt.Errorf("snapshot water classification: %w", err)
	}
	targetWater, err := waterProvinceIDs(target.Path)
	if err != nil {
		return result, fmt.Errorf("target water classification: %w", err)
	}
	policy, err := buildPolicy(mapping, spec.Resolutions, sourceWater, targetWater, targetIDs)
	if err != nil {
		return result, err
	}
	resolutionByID := map[string]Resolution{}
	for _, resolution := range spec.Resolutions {
		if resolution.ConflictID != "" {
			resolutionByID[resolution.ConflictID] = resolution
		}
	}

	artifactID, err := newArtifactID()
	if err != nil {
		return result, err
	}
	artifactStage, err := os.MkdirTemp(opts.ArtifactRoot, ".ck3-migration-stage-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(artifactStage)
	outputName, err := normalizeOutputName(spec.OutputName, manifest.Project)
	if err != nil {
		return result, err
	}
	result.ArtifactID, result.OutputName = artifactID, outputName
	result.ExpiresAt = time.Now().UTC().Add(opts.Retention).Format(time.RFC3339)

	modStageParent := artifactStage
	if opts.OutputDir != "" {
		if err := ensureNewOutputDir(opts.OutputDir); err != nil {
			return result, err
		}
		modStageParent, err = os.MkdirTemp(filepath.Dir(opts.OutputDir), ".ck3-migration-stage-")
		if err != nil {
			return result, err
		}
		defer os.RemoveAll(modStageParent)
	}
	modRoot := filepath.Join(modStageParent, outputName)
	if err := os.MkdirAll(modRoot, 0o755); err != nil {
		return result, err
	}
	if err := copyInventory(target.Path, modRoot, targetFiles); err != nil {
		return result, err
	}

	files, rewriteConflicts, diagnostics, patchFiles, err := replayProject(ctx, snapshotRoot, manifest, project, target, projectFiles, targetFiles, modRoot, policy, resolutionByID)
	if err != nil {
		return result, err
	}
	result.Files = files
	result.Conflicts = append(result.Conflicts, rewriteConflicts...)
	result.Diagnostics = dedupeConflicts(diagnostics)
	for _, rel := range spec.DeletePaths {
		normalized, err := normalizeRel(rel)
		if err != nil {
			return result, fmt.Errorf("delete_paths: %w", err)
		}
		if err := os.Remove(filepath.Join(modRoot, filepath.FromSlash(normalized))); err != nil && !os.IsNotExist(err) {
			return result, err
		}
		result.Files = recordExplicitDelete(result.Files, normalized)
		patchFiles = append(patchFiles, indexer.PatchFileInput{Path: normalized, Op: "delete"})
	}
	result.Conflicts = dedupeConflicts(result.Conflicts)
	result.ConflictCount = len(result.Conflicts)
	for _, file := range result.Files {
		if isProjectReplay(file.Merge) {
			result.ChangedFiles++
		}
		result.ReplacementCount += file.Replacements
	}

	if len(result.Conflicts) == 0 {
		result.Validation = validateMigration(ctx, modRoot, targetIDs, patchFiles, opts.DB)
		result.Validation.SuspiciousNumeric = len(result.Diagnostics)
		if result.Validation.Blocked {
			result.Conflicts = append(result.Conflicts, generalConflict("validation_failed", "", "strict migration validation reported blockers", ""))
			result.ConflictCount = len(result.Conflicts)
		}
	} else {
		result.Validation.Blocked = true
		result.Validation.SuspiciousNumeric = len(result.Diagnostics)
	}
	if len(result.Conflicts) == 0 && !result.Validation.Blocked {
		result.Status = "ready"
		result.Guidance = []string{"This is a local test fork. Scan it again and launch it in an isolated playset before packaging or publishing."}
	} else {
		if err := os.RemoveAll(modRoot); err != nil {
			return result, fmt.Errorf("remove blocked migration fork: %w", err)
		}
	}

	if err := writeMigrationReports(artifactStage, &result, manifest, mapping); err != nil {
		return result, err
	}
	if result.Status == "ready" && opts.OutputDir != "" {
		if err := os.Rename(modRoot, opts.OutputDir); err != nil {
			return result, fmt.Errorf("publish output: %w", err)
		}
	}
	finalArtifact := filepath.Join(opts.ArtifactRoot, artifactID)
	if err := os.Rename(artifactStage, finalArtifact); err != nil {
		return result, fmt.Errorf("publish migration artifact: %w", err)
	}
	result.ArtifactRelPath = artifactID
	return result, nil
}

func isProjectReplay(merge string) bool {
	for _, prefix := range []string{"project_", "resolved_project", "three_way", "explicit_delete"} {
		if strings.HasPrefix(merge, prefix) {
			return true
		}
	}
	return strings.Contains(merge, "+dedupe")
}

func recordExplicitDelete(files []FileResult, rel string) []FileResult {
	for i := range files {
		if strings.EqualFold(files[i].Path, rel) {
			files[i].Origin = "project"
			files[i].Merge = "explicit_delete"
			files[i].SHA256 = ""
			files[i].Size = 0
			return files
		}
	}
	files = append(files, FileResult{Path: rel, Origin: "project", Merge: "explicit_delete"})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func replayProject(ctx context.Context, snapshotRoot string, manifest SnapshotManifest, project, target indexer.Source, projectFiles, targetFiles []SnapshotFile, modRoot string, policy *migrationPolicy, resolutions map[string]Resolution) ([]FileResult, []Conflict, []Conflict, []indexer.PatchFileInput, error) {
	oldMap, projectMap, targetMap := fileMap(manifest.BaseFiles), fileMap(projectFiles), fileMap(targetFiles)
	paths := make([]string, 0, len(projectFiles))
	for _, file := range projectFiles {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	var results []FileResult
	var conflicts []Conflict
	var diagnostics []Conflict
	var patches []indexer.PatchFileInput
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, nil, err
		}
		projectFile := projectMap[strings.ToLower(rel)]
		oldFile := oldMap[strings.ToLower(rel)]
		targetFile := targetMap[strings.ToLower(rel)]
		if targetGeometryAuthority(rel) {
			existing, err := resultForExisting(modRoot, rel, "target", "target_geometry", 0)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			results = append(results, existing)
			continue
		}
		projectData, err := os.ReadFile(filepath.Join(project.Path, filepath.FromSlash(rel)))
		if err != nil {
			return nil, nil, nil, nil, err
		}
		entry := FileResult{Path: rel, Origin: "project"}
		var final []byte
		switch {
		case oldFile == nil:
			if projectFile.Text {
				rewritten := rewriteSemantic(rel, projectData, policy)
				final, entry.Replacements = []byte(rewritten.Content), rewritten.Replacements
				conflicts = append(conflicts, rewritten.Conflicts...)
				diagnostics = append(diagnostics, rewritten.Diagnostics...)
			} else {
				final = projectData
			}
			entry.Merge = "project_added"
		case targetFile == nil && isRootMetadata(rel):
			final, entry.Merge = projectData, "project_metadata"
		case targetFile == nil:
			if projectFile.SHA256 == oldFile.SHA256 {
				entry.Merge = "target_deleted"
				results = append(results, entry)
				if err := os.Remove(filepath.Join(modRoot, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
					return nil, nil, nil, nil, fmt.Errorf("remove target-deleted file %s: %w", rel, err)
				}
				continue
			}
			c := generalConflict("delete_modify_conflict", rel, "target deleted a file that the project modified", "prefer_project")
			if resolved, useProject := resolveFileConflict(c, resolutions); resolved {
				if !useProject {
					results = append(results, FileResult{Path: rel, Origin: "target", Merge: "target_deleted"})
					continue
				}
				final, entry.Merge = projectData, "resolved_project"
			} else {
				conflicts = append(conflicts, c)
				continue
			}
		case isRootMetadata(rel):
			final, entry.Merge = projectData, "project_metadata"
		case projectFile.SHA256 == oldFile.SHA256:
			existing, err := resultForExisting(modRoot, rel, "target", "target_unchanged", 0)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			results = append(results, existing)
			continue
		case !projectFile.Text:
			if targetFile.SHA256 == oldFile.SHA256 || targetFile.SHA256 == projectFile.SHA256 {
				final, entry.Merge = projectData, "project_binary"
			} else {
				c := generalConflict("binary_merge_conflict", rel, "project and target both changed a binary file", "prefer_project")
				if resolved, useProject := resolveFileConflict(c, resolutions); resolved {
					if useProject {
						final, entry.Merge = projectData, "resolved_project"
					} else {
						existing, err := resultForExisting(modRoot, rel, "target", "resolved_target", 0)
						if err != nil {
							return nil, nil, nil, nil, err
						}
						results = append(results, existing)
						continue
					}
				} else {
					conflicts = append(conflicts, c)
					continue
				}
			}
		default:
			rewritten := rewriteSemantic(rel, projectData, policy)
			conflicts = append(conflicts, rewritten.Conflicts...)
			diagnostics = append(diagnostics, rewritten.Diagnostics...)
			entry.Replacements = rewritten.Replacements
			if targetFile.SHA256 == oldFile.SHA256 {
				final, entry.Merge = []byte(rewritten.Content), "project_replayed"
			} else {
				oldData, err := readSnapshotBaseline(snapshotRoot, *oldFile)
				if err != nil {
					return nil, nil, nil, nil, fmt.Errorf("read old baseline for %s: %w", rel, err)
				}
				oldRewritten := rewriteSemantic(rel, oldData, policy)
				targetData, err := os.ReadFile(filepath.Join(target.Path, filepath.FromSlash(rel)))
				if err != nil {
					return nil, nil, nil, nil, err
				}
				merged, conflict := mergeText(rel, oldRewritten.Content, rewritten.Content, string(targetData), resolutions)
				if conflict != nil {
					conflicts = append(conflicts, *conflict)
					continue
				}
				final, entry.Merge = []byte(merged), "three_way"
			}
		}
		if final != nil {
			full := filepath.Join(modRoot, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return nil, nil, nil, nil, err
			}
			if err := os.WriteFile(full, final, 0o644); err != nil {
				return nil, nil, nil, nil, err
			}
			entry.SHA256, entry.Size = hashBytes(final), int64(len(final))
			results = append(results, entry)
			if isTextPath(rel) {
				patches = append(patches, indexer.PatchFileInput{Path: rel, Content: string(final)})
			}
		}
	}
	for _, file := range targetFiles {
		if projectMap[strings.ToLower(file.Path)] != nil {
			continue
		}
		merge := "target_native"
		if targetGeometryAuthority(file.Path) {
			merge = "target_geometry"
		}
		existing, err := resultForExisting(modRoot, file.Path, "target", merge, 0)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		results = append(results, existing)
	}
	results, patches, err := consolidateIdenticalRecords(modRoot, results, patches)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, conflicts, diagnostics, patches, nil
}

func copyInventory(root, target string, files []SnapshotFile) error {
	for _, file := range files {
		if err := copyFile(filepath.Join(root, filepath.FromSlash(file.Path)), filepath.Join(target, filepath.FromSlash(file.Path))); err != nil {
			return err
		}
	}
	return nil
}

func resultForExisting(root, rel, origin, merge string, replacements int) (FileResult, error) {
	result := FileResult{Path: rel, Origin: origin, Merge: merge, Replacements: replacements}
	hash, size, err := hashPath(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return FileResult{}, fmt.Errorf("hash migration output %s: %w", rel, err)
	}
	result.SHA256, result.Size = hash, size
	return result, nil
}

func inventoryDifference(expected, actual []SnapshotFile) string {
	a, b := fileMap(expected), fileMap(actual)
	for key, expectedFile := range a {
		actualFile := b[key]
		if actualFile == nil {
			return "project file disappeared after the snapshot: " + expectedFile.Path
		}
		if actualFile.SHA256 != expectedFile.SHA256 {
			return "project file changed after the snapshot: " + expectedFile.Path
		}
	}
	for key, actualFile := range b {
		if a[key] == nil {
			return "project file was added after the snapshot: " + actualFile.Path
		}
	}
	return ""
}

func generalConflict(code, path, message, suggested string) Conflict {
	c := Conflict{Code: code, Path: path, Message: message, Severity: "error", SuggestedAction: suggested}
	c.ID = conflictID(c.Code, c.Path, 0, 0, c.Message)
	return c
}

func resolveFileConflict(conflict Conflict, resolutions map[string]Resolution) (resolved, useProject bool) {
	resolution, ok := resolutions[conflict.ID]
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(resolution.Action)) {
	case "prefer_project":
		return true, true
	case "prefer_target", "drop":
		return true, false
	default:
		return false, false
	}
}

func isRootMetadata(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	return !strings.Contains(lower, "/") && (lower == "descriptor.mod" || strings.HasSuffix(lower, ".mod") || lower == "thumbnail.png")
}

func validateSnapshotID(id string) error {
	if !strings.HasPrefix(id, "map-snapshot-") || strings.ContainsAny(id, `/\\:`) || len(id) != len("map-snapshot-")+16 {
		return fmt.Errorf("invalid snapshot_id")
	}
	return nil
}

func normalizeOutputName(raw, project string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		name = project + "-migrated"
	}
	if _, err := normalizeRel(name); err != nil || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("output_name must be a single safe directory name")
	}
	return name, nil
}

func ensureNewOutputDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if info, err := os.Stat(abs); err == nil {
		return fmt.Errorf("output directory already exists: %s", info.Name())
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(filepath.Dir(abs), 0o755)
}

func newArtifactID() (string, error) {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "ck3-map-migration-" + hex.EncodeToString(bytes[:]), nil
}

func cleanupMigrationArtifacts(root string, retention time.Duration) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-retention)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "ck3-map-migration-") && !strings.HasPrefix(entry.Name(), ".ck3-migration-stage-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", entry.Name(), err)
		}
		if info.ModTime().Before(cutoff) {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return fmt.Errorf("remove %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

func writeMigrationReports(stage string, result *MigrationResult, manifest SnapshotManifest, mapping indexer.MapProvinceMappingResult) error {
	report, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "migration-report.json"), append(report, '\n'), 0o600); err != nil {
		return err
	}
	template := make([]Resolution, 0, len(result.Conflicts))
	for _, conflict := range result.Conflicts {
		if conflict.SuggestedAction == "" {
			continue
		}
		template = append(template, Resolution{ConflictID: conflict.ID, SourceProvince: conflict.SourceProvince, Action: conflict.SuggestedAction})
	}
	data, err := json.MarshalIndent(map[string]any{"schema_version": SchemaVersion, "resolutions": template}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "resolution-template.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	manifestData := map[string]any{"schema_version": SchemaVersion, "snapshot_id": result.SnapshotID, "snapshot_map_sha256": manifest.ActiveMapSHA256,
		"target": result.Target, "mapping": mapping.Summary, "files": result.Files, "validation": result.Validation}
	data, err = json.MarshalIndent(manifestData, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, "migration-manifest.json"), append(data, '\n'), 0o600)
}
