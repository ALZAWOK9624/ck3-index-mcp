package migrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ck3-index/internal/indexer"
	"ck3-index/internal/script"
)

var rebaseCoreMapPaths = []string{
	"map_data/definition.csv",
	"map_data/provinces.png",
	"map_data/provinces.bmp",
	"map_data/default.map",
}

func PlanRebase(ctx context.Context, cfg indexer.Config, spec RebasePlanSpec, opts RebaseOptions) (RebaseTransaction, error) {
	var err error
	cfg, err = indexer.NormalizeConfig(cfg)
	if err != nil {
		return RebaseTransaction{}, fmt.Errorf("normalize rebase configuration: %w", err)
	}
	profilePath, err := resolveRebasePath(spec.ProfilePath)
	if err != nil {
		return RebaseTransaction{}, fmt.Errorf("unsafe rebase profile path: %w", err)
	}
	profile, profileHash, err := loadRebaseProfile(profilePath)
	if err != nil {
		return RebaseTransaction{}, err
	}
	project, base, target, err := resolveRebaseSources(cfg, profile)
	if err != nil {
		return RebaseTransaction{}, err
	}
	gameVersion, err := resolveRebaseGameVersionGate(profile, project, base, target)
	if err != nil {
		return RebaseTransaction{}, fmt.Errorf("game version gate: %w", err)
	}
	root, err := rebaseArtifactRoot(cfg, opts)
	if err != nil {
		return RebaseTransaction{}, err
	}
	if strings.TrimSpace(spec.OutputDir) == "" {
		return RebaseTransaction{}, fmt.Errorf("output_dir is required")
	}
	outputDir, err := resolveRebasePath(spec.OutputDir)
	if err != nil {
		return RebaseTransaction{}, fmt.Errorf("unsafe migration copy output: %w", err)
	}
	if _, statErr := os.Lstat(outputDir); statErr == nil {
		return RebaseTransaction{}, fmt.Errorf("migration copy output already exists: %s", spec.OutputDir)
	} else if !os.IsNotExist(statErr) {
		return RebaseTransaction{}, statErr
	}
	if err := ensureStorageOutsideSources(outputDir, cfg.Sources); err != nil {
		return RebaseTransaction{}, err
	}
	if err := rebaseVerifyRenameDomain(outputDir, project.Path); err != nil {
		return RebaseTransaction{}, err
	}

	id, err := newRebaseTransactionID()
	if err != nil {
		return RebaseTransaction{}, err
	}
	lock, err := acquireRebaseTransactionLock(root, id, "plan")
	if err != nil {
		return RebaseTransaction{}, err
	}
	defer func() { _ = lock.Release() }()
	now := time.Now().UTC()
	transaction := RebaseTransaction{
		SchemaVersion:   RebaseSchemaVersion,
		ID:              id,
		Status:          RebaseStatusPlanning,
		Profile:         profile,
		ProfileSHA256:   profileHash,
		GameVersion:     gameVersion,
		PlanProfilePath: profilePath,
		OutputDir:       outputDir,
		CreatedAt:       now.Format(time.RFC3339Nano),
		UpdatedAt:       now.Format(time.RFC3339Nano),
		Counts:          map[string]int{},
		Validation:      RebaseValidation{Counts: map[string]int{}, DiagnosticsBase: map[string]int{}, DiagnosticsCopy: map[string]int{}, DiagnosticsNew: map[string]int{}},
		Progress:        RebaseProgress{Phase: "inventory", Message: "collecting base inventory"},
	}
	if conflict := rebaseGameVersionConflict(gameVersion); conflict != nil {
		transaction.Conflicts = append(transaction.Conflicts, *conflict)
	}
	if gameVersion.SemanticMergeAllowed {
		transaction.Counts["game_version_semantic_merge_allowed"] = 1
	} else {
		transaction.Counts["game_version_semantic_merge_blocked"] = 1
	}
	transaction.SourceIdentityFingerprints, err = rebaseSourceIdentityFingerprints(project, base, target)
	if err != nil {
		return transaction, fmt.Errorf("migration source identities: %w", err)
	}
	if err := writeRebaseTransaction(root, &transaction); err != nil {
		return transaction, err
	}
	fail := func(cause error) (RebaseTransaction, error) {
		transaction.Status = RebaseStatusFailed
		transaction.Progress.Message = cause.Error()
		_ = writeRebaseTransaction(root, &transaction)
		_ = writeRebaseReport(root, transaction)
		return transaction, cause
	}

	baseFiles, baseExcluded, err := collectFiles(base.Path)
	if err != nil {
		return fail(fmt.Errorf("base inventory: %w", err))
	}
	transaction.BaseFingerprint = inventoryFingerprint(baseFiles)
	transaction.Counts["base_files"] = len(baseFiles)
	transaction.Counts["base_excluded"] = len(baseExcluded)
	transaction.Progress.Message = "collecting project inventory"
	if err := writeRebaseTransaction(root, &transaction); err != nil {
		return transaction, err
	}
	projectFiles, projectExcluded, err := collectFiles(project.Path)
	if err != nil {
		return fail(fmt.Errorf("project inventory: %w", err))
	}
	transaction.ProjectFingerprint = inventoryFingerprint(projectFiles)
	transaction.Counts["project_files"] = len(projectFiles)
	transaction.Counts["project_excluded"] = len(projectExcluded)
	projectTreeFiles, projectTreeExcluded, err := collectRebaseOverlayFiles(project.Path, newRebaseOverlayPolicy(profile))
	if err != nil {
		return fail(fmt.Errorf("project full-tree inventory: %w", err))
	}
	transaction.ProjectTreeFingerprint = inventoryFingerprint(projectTreeFiles)
	transaction.Counts["project_tree_files"] = len(projectTreeFiles)
	transaction.Counts["project_tree_excluded"] = len(projectTreeExcluded)
	transaction.Progress.Message = "collecting target inventory"
	if err := writeRebaseTransaction(root, &transaction); err != nil {
		return transaction, err
	}
	targetFiles, targetExcluded, err := collectFiles(target.Path)
	if err != nil {
		return fail(fmt.Errorf("target inventory: %w", err))
	}
	transaction.TargetFingerprint = inventoryFingerprint(targetFiles)
	transaction.Counts["target_files"] = len(targetFiles)
	transaction.Counts["target_excluded"] = len(targetExcluded)
	validationFingerprints, err := rebaseValidationSourceFingerprints(cfg, profile)
	if err != nil {
		return fail(fmt.Errorf("validation source inventory: %w", err))
	}
	transaction.ValidationSourceFingerprints = validationFingerprints
	transaction.Counts["validation_sources"] = len(validationFingerprints)

	replacePaths, err := projectReplacePaths(project.Path)
	if err != nil {
		return fail(err)
	}
	transaction.ProjectReplacePaths = replacePaths
	baseMap, projectMap, targetMap := fileMap(baseFiles), fileMap(projectFiles), fileMap(targetFiles)
	mapAuthorityChanged := coreMapChanged(baseMap, projectMap, targetMap)
	var projectMapContext *rebaseProjectMapContext
	var targetMapContext *rebaseTargetMapContext
	if profile.MapAuthority == "project" {
		projectMapContext, err = prepareRebaseProjectMapContext(base, project, target, baseMap, projectMap, targetMap)
		if err != nil {
			projectMapContext = &rebaseProjectMapContext{err: err}
			transaction.Conflicts = append(transaction.Conflicts, newRebaseConflict(
				"map_definition_mapping_error", "map_data/definition.csv", "",
				"project map authority could not establish exact RGB province mapping: "+err.Error(),
				[]string{"edit_profile"}, "edit_profile", baseMap["map_data/definition.csv"], projectMap["map_data/definition.csv"], targetMap["map_data/definition.csv"],
			))
		} else {
			for key, value := range rebaseMapMappingCounts(projectMapContext) {
				transaction.Counts[key] = value
			}
		}
	} else if profile.MapAuthority == "target" {
		targetMapContext, err = prepareRebaseTargetMapContext(base, project, target, baseMap, projectMap, targetMap)
		if err != nil {
			targetMapContext = &rebaseTargetMapContext{err: err}
			transaction.Conflicts = append(transaction.Conflicts, newRebaseConflict(
				"map_definition_mapping_error", "map_data/definition.csv", "",
				"target map authority could not establish exact RGB province mapping: "+err.Error(),
				[]string{"edit_profile"}, "edit_profile", baseMap["map_data/definition.csv"], projectMap["map_data/definition.csv"], targetMap["map_data/definition.csv"],
			))
		} else {
			for key, value := range rebaseTargetMapMappingCounts(targetMapContext) {
				transaction.Counts[key] = value
			}
		}
	}

	if len(mapAuthorityChanged) > 0 && profile.MapAuthority == "" {
		conflict := newRebaseConflict(
			"map_authority_required", "map_data", "",
			"core map hashes differ; set map_authority to project, target, or disabled in the migration profile",
			[]string{"edit_profile"}, "edit_profile", nil, nil, nil,
		)
		conflict.Message += ": " + strings.Join(mapAuthorityChanged, ", ")
		transaction.Conflicts = append(transaction.Conflicts, conflict)
	}

	transaction.Progress = RebaseProgress{Phase: "classify", Total: len(unionInventoryPaths(baseFiles, projectFiles, targetFiles)), Message: "classifying three-way hashes"}
	if err := writeRebaseTransaction(root, &transaction); err != nil {
		return transaction, err
	}

	var semantic *rebaseSemanticIndexes
	if gameVersion.SemanticMergeAllowed {
		semantic, err = buildRebaseSemanticIndexes(ctx, base, project, target, baseFiles, projectFiles, targetFiles)
		if err != nil {
			return fail(fmt.Errorf("semantic inventory: %w", err))
		}
	}
	paths := unionInventoryPaths(baseFiles, projectFiles, targetFiles)
	for index, rel := range paths {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		decision, conflicts, err := planRebasePath(
			ctx, root, transaction.ID, rel, project, base, target,
			baseMap[strings.ToLower(rel)], projectMap[strings.ToLower(rel)], targetMap[strings.ToLower(rel)],
			profile, gameVersion, replacePaths, semantic, projectMapContext, targetMapContext, len(mapAuthorityChanged) > 0,
		)
		if err != nil {
			return fail(err)
		}
		transaction.Files = append(transaction.Files, decision)
		transaction.Conflicts = append(transaction.Conflicts, conflicts...)
		transaction.Counts[decision.Classification]++
		transaction.Counts["action_"+decision.Action]++
		if decision.MapDelta != nil {
			transaction.Counts["map_coordinate_delta_files"]++
			transaction.Counts["map_coordinate_delta_pixels"] += decision.MapDelta.ChangedPixels
		}
		transaction.Progress.Completed = index + 1
		transaction.Progress.CurrentPath = rel
		// Only the small progress projection moves per file. Rewriting the whole
		// manifest here made planning quadratic in the file count, and it bought
		// nothing: an interrupted plan publishes no output and is restarted under
		// a fresh transaction id rather than resumed mid-classification.
		if err := checkpointRebaseProgress(root, transaction, index+1, len(paths)); err != nil {
			return transaction, err
		}
	}
	if gameVersion.SemanticMergeAllowed {
		if err := applyRebaseCrossFileSemanticSafety(ctx, root, transaction.ID, semantic, &transaction, baseMap, projectMap, targetMap); err != nil {
			return fail(fmt.Errorf("cross-file semantic safety: %w", err))
		}
	}
	recountRebaseDecisionActions(&transaction)
	transaction.Conflicts = dedupeRebaseConflicts(transaction.Conflicts)
	transaction.Counts["conflicts"] = len(transaction.Conflicts)
	if len(transaction.Conflicts) > 0 {
		transaction.Status = RebaseStatusNeedsReview
		transaction.Progress.Message = "plan complete; resolve blocking conflicts"
	} else {
		transaction.Status = RebaseStatusReadyToBuild
		transaction.Progress.Message = "plan complete; migration copy can be built"
	}
	transaction.Progress.Phase = "planned"
	transaction.Progress.CurrentPath = ""
	if err := writeRebaseResolutions(root, transaction.ID, nil); err != nil {
		return fail(err)
	}
	// The decision journal is published once, after cross-file safety has had
	// its chance to convert individual decisions into conflicts.
	if err := writeRebaseDecisions(root, transaction.ID, transaction.Files); err != nil {
		return fail(err)
	}
	if err := writeRebaseTransaction(root, &transaction); err != nil {
		return transaction, err
	}
	if err := writeRebaseReport(root, transaction); err != nil {
		return fail(err)
	}
	return transaction, nil
}

func recountRebaseDecisionActions(transaction *RebaseTransaction) {
	for key := range transaction.Counts {
		if strings.HasPrefix(key, "action_") {
			delete(transaction.Counts, key)
		}
	}
	for _, decision := range transaction.Files {
		transaction.Counts["action_"+decision.Action]++
	}
}

func unionInventoryPaths(inventories ...[]SnapshotFile) []string {
	canonical := map[string]string{}
	for _, inventory := range inventories {
		for _, file := range inventory {
			key := strings.ToLower(file.Path)
			if _, exists := canonical[key]; !exists {
				canonical[key] = file.Path
			}
		}
	}
	out := make([]string, 0, len(canonical))
	for _, rel := range canonical {
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func projectReplacePaths(root string) ([]string, error) {
	var descriptors []string
	primary := filepath.Join(root, "descriptor.mod")
	if _, err := os.Stat(primary); err == nil {
		descriptors = append(descriptors, primary)
	}
	if len(descriptors) == 0 {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".mod") {
				descriptors = append(descriptors, filepath.Join(root, entry.Name()))
			}
		}
	}
	seen := map[string]bool{}
	for _, descriptor := range descriptors {
		data, err := os.ReadFile(descriptor)
		if err != nil {
			return nil, err
		}
		parsed := script.Parse(string(data))
		if len(parsed.Errors) > 0 {
			return nil, fmt.Errorf("parse project descriptor %s: %s", filepath.Base(descriptor), parsed.Errors[0].Message)
		}
		for _, node := range parsed.Nodes {
			if !strings.EqualFold(node.Key, "replace_path") || node.Kind != "atom" {
				continue
			}
			value := strings.Trim(strings.TrimSpace(node.Value), `"`)
			value = strings.TrimSuffix(filepath.ToSlash(value), "/")
			if value == "" {
				continue
			}
			if _, err := normalizeRel(value + "/placeholder"); err != nil {
				return nil, fmt.Errorf("invalid project replace_path %q: %w", value, err)
			}
			seen[strings.ToLower(value)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func underReplacePath(rel string, replacePaths []string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	for _, prefix := range replacePaths {
		if lower == prefix || strings.HasPrefix(lower, prefix+"/") {
			return true
		}
	}
	return false
}

func coreMapChanged(base, project, target map[string]*SnapshotFile) []string {
	paths := map[string]string{}
	for _, layer := range []map[string]*SnapshotFile{base, project, target} {
		for key, file := range layer {
			if file != nil && isCoreMapPath(file.Path) {
				paths[key] = file.Path
			}
		}
	}
	var out []string
	for key, rel := range paths {
		baseFile := base[key]
		projectFile := project[key]
		targetFile := target[key]
		effective := baseFile
		if projectFile != nil {
			effective = projectFile
		}
		if !sameSnapshotFile(effective, targetFile) {
			if effective != nil || targetFile != nil {
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out
}

func sameSnapshotFile(left, right *SnapshotFile) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.SHA256 == right.SHA256
}

func planRebasePath(
	ctx context.Context,
	artifactRoot, transactionID, rel string,
	project, base, target indexer.Source,
	baseFile, projectFile, targetFile *SnapshotFile,
	profile RebaseProfile,
	gameVersion RebaseGameVersionGate,
	replacePaths []string,
	semantic *rebaseSemanticIndexes,
	projectMapContext *rebaseProjectMapContext,
	targetMapContext *rebaseTargetMapContext,
	mapAuthorityChanged bool,
) (RebaseFileDecision, []RebaseConflict, error) {
	decision := RebaseFileDecision{
		Path: rel, Adapter: rebaseAdapterForPath(rel),
		Base: fileState(baseFile), Project: fileState(projectFile), Target: fileState(targetFile),
	}
	set := func(classification, action, reason string, safe bool, result *SnapshotFile) (RebaseFileDecision, []RebaseConflict, error) {
		decision.Classification, decision.Action, decision.Reason, decision.Safe = classification, action, reason, safe
		decision.Result = fileState(result)
		return decision, nil, nil
	}

	if isRootMetadata(rel) && projectFile != nil {
		return set("project_metadata", "keep_project", "root Mod metadata belongs to the project overlay", true, projectFile)
	}
	if profile.MapAuthority == "project" && rebaseCoordinateMapRasterPath(rel) {
		return planRebaseProjectCoordinateMapRaster(
			artifactRoot, transactionID, rel, project, base, target,
			baseFile, projectFile, targetFile, decision,
		)
	}
	if isCoreMapPath(rel) && profile.MapAuthority == "disabled" && !sameSnapshotFile(effectiveRebaseMapFile(baseFile, projectFile), targetFile) {
		conflict := newRebaseConflict(
			"map_migration_disabled", rel, "", "core map differs while map_authority is disabled",
			[]string{"keep_project", "use_target", "edit_profile"}, "edit_profile", baseFile, projectFile, targetFile,
		)
		decision.Classification, decision.Action, decision.Reason = "map_migration_disabled", "conflict", conflict.Message
		decision.ConflictIDs = []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	if isCoreMapPath(rel) && profile.MapAuthority != "" && profile.MapAuthority != "disabled" {
		switch profile.MapAuthority {
		case "project":
			if projectFile != nil {
				return set("map_project_authority", "keep_project", "project map authority preserves the project core map", true, projectFile)
			}
			if baseFile != nil {
				candidate, err := readSourceFile(base.Path, rel)
				if err != nil {
					return decision, nil, err
				}
				if err := storeRebaseCandidate(artifactRoot, transactionID, rel, candidate, &decision); err != nil {
					return decision, nil, err
				}
				decision.Classification, decision.Action, decision.Reason, decision.Safe = "map_project_authority", "write_candidate", "project map authority materializes the old effective core map", true
				return decision, nil, nil
			}
			conflict := newRebaseConflict(
				"map_project_asset_unproven", rel, "",
				"project map authority cannot safely inherit a target-only core map asset without a project or base counterpart",
				[]string{"edit_profile"}, "edit_profile", baseFile, projectFile, targetFile,
			)
			decision.Classification, decision.Action, decision.Reason = "map_project_asset_unproven", "conflict", conflict.Message
			decision.ConflictIDs = []string{conflict.ID}
			return decision, []RebaseConflict{conflict}, nil
		case "target":
			return set("map_target_authority", "delete_project", "target map authority removes any project core-map shadow", true, nil)
		}
	}
	if profile.MapAuthority == "project" && rebaseMapTransformFile(rel) {
		return planRebaseProjectMapReference(artifactRoot, transactionID, rel, project, base, target, baseFile, projectFile, targetFile, projectMapContext)
	}
	if profile.MapAuthority == "target" && mapAuthorityChanged && rebaseMapTransformFile(rel) && rebaseTargetAuthorityReferenceNeedsRewrite(baseFile, projectFile) {
		return planRebaseTargetMapReference(artifactRoot, transactionID, rel, project, base, target, baseFile, projectFile, targetFile, targetMapContext)
	}
	var titleReferenceSource indexer.Source
	var titleReferenceFile *SnapshotFile
	switch {
	case profile.MapAuthority == "project":
		// Target title history would be replayed into the project map, but V1
		// has no proven title-history province-field adapter.
		titleReferenceSource, titleReferenceFile = target, targetFile
	case profile.MapAuthority == "target" && mapAuthorityChanged && rebaseTargetAuthorityReferenceNeedsRewrite(baseFile, projectFile):
		// Conversely, a project customisation would be replayed into the target
		// map. Inspect that side rather than letting generic Jomini merge retain
		// a potentially old numeric province reference.
		titleReferenceSource, titleReferenceFile = project, projectFile
	}
	if titleReferenceFile != nil {
		titleNeedsReview, err := rebaseTitleHistoryNeedsProvinceReview(rel, titleReferenceSource, titleReferenceFile)
		if err != nil {
			return decision, nil, err
		}
		if titleNeedsReview {
			allowedActions := []string{"keep_project", "use_target", "manual"}
			if profile.MapAuthority == "target" {
				allowedActions = []string{"use_target", "manual"}
			}
			conflict := newRebaseConflict(
				"title_history_province_reference_unproven", rel, "",
				"title history contains a possible numeric province reference but no exact field adapter is available",
				allowedActions, "manual", baseFile, projectFile, targetFile,
			)
			decision.Classification, decision.Action, decision.Reason = "title_history_province_reference_unproven", "conflict", conflict.Message
			decision.ConflictIDs = []string{conflict.ID}
			return decision, []RebaseConflict{conflict}, nil
		}
	}

	replaced := underReplacePath(rel, replacePaths)
	owned := rebaseOwnedPath(rel, profile.OwnedPrefixes)
	if projectFile == nil {
		if replaced {
			switch {
			case baseFile == nil && targetFile != nil:
				conflict := newRebaseConflict(
					"replace_path_target_addition", rel, "",
					"target added a file under a project replace_path; adoption must be explicit",
					[]string{"use_target", "drop"}, "use_target", baseFile, projectFile, targetFile,
				)
				decision.Classification, decision.Action, decision.Reason = "replace_path_target_addition", "conflict", conflict.Message
				decision.ConflictIDs = []string{conflict.ID}
				return decision, []RebaseConflict{conflict}, nil
			default:
				return set("project_deleted_under_replace_path", "delete_project", "project replace_path intentionally keeps this path absent", true, nil)
			}
		}
		switch {
		case baseFile == nil && targetFile != nil:
			return set("upstream_added", "inherit_target", "new upstream file is inherited without a project copy", true, targetFile)
		case baseFile != nil && targetFile == nil:
			return set("upstream_deleted", "inherit_target", "upstream deletion is inherited because the project has no override", true, nil)
		case sameSnapshotFile(baseFile, targetFile):
			return set("unchanged_inherited", "inherit_target", "project has no same-path override", true, targetFile)
		default:
			return set("upstream_only", "inherit_target", "only upstream changed; project inherits the target", true, targetFile)
		}
	}

	if baseFile == nil {
		switch {
		case targetFile == nil:
			return set("project_added", "keep_project", "project-owned file has no upstream ancestor", true, projectFile)
		case projectFile.SHA256 == targetFile.SHA256 && !owned:
			return set("both_added_same", "delete_project", "target now supplies the same file; remove the redundant project shadow", true, nil)
		default:
			return planBothChangedPath(ctx, artifactRoot, transactionID, rel, project, base, target, baseFile, projectFile, targetFile, decision, semantic, gameVersion)
		}
	}

	if projectFile.SHA256 == baseFile.SHA256 {
		if owned {
			return set("owned_upstream_copy", "keep_project", "owned-prefix policy keeps this project file", true, projectFile)
		}
		return set("shadow_copy", "delete_project", "project file is byte-identical to the old upstream and must not shadow the target", true, nil)
	}
	if targetFile == nil {
		conflict := newRebaseConflict(
			"delete_modify_conflict", rel, "",
			"target deleted a file that the project modified",
			[]string{"keep_project", "drop"}, "keep_project", baseFile, projectFile, targetFile,
		)
		decision.Classification, decision.Action, decision.Reason = "delete_modify_conflict", "conflict", conflict.Message
		decision.ConflictIDs = []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	if targetFile.SHA256 == baseFile.SHA256 {
		return set("project_only", "keep_project", "only the project changed", true, projectFile)
	}
	if projectFile.SHA256 == targetFile.SHA256 {
		if owned {
			return set("both_changed_same", "keep_project", "project-owned file matches the target", true, projectFile)
		}
		return set("both_changed_same", "delete_project", "target independently reached the project result; remove the redundant shadow", true, nil)
	}
	return planBothChangedPath(ctx, artifactRoot, transactionID, rel, project, base, target, baseFile, projectFile, targetFile, decision, semantic, gameVersion)
}

func effectiveRebaseMapFile(baseFile, projectFile *SnapshotFile) *SnapshotFile {
	if projectFile != nil {
		return projectFile
	}
	return baseFile
}

func rebaseTitleHistoryNeedsProvinceReview(rel string, target indexer.Source, targetFile *SnapshotFile) (bool, error) {
	lower := strings.ToLower(filepath.ToSlash(rel))
	if targetFile == nil || !strings.HasPrefix(lower, "history/titles/") || !strings.HasSuffix(lower, ".txt") {
		return false, nil
	}
	data, err := readSourceFile(target.Path, rel)
	if err != nil {
		return false, err
	}
	text := string(data)
	return provinceAtom.MatchString(text) || typedProvince.MatchString(text) || uncertainProvinceScalar.MatchString(text), nil
}

func planBothChangedPath(
	ctx context.Context,
	artifactRoot, transactionID, rel string,
	project, base, target indexer.Source,
	baseFile, projectFile, targetFile *SnapshotFile,
	decision RebaseFileDecision,
	semantic *rebaseSemanticIndexes,
	gameVersion RebaseGameVersionGate,
) (RebaseFileDecision, []RebaseConflict, error) {
	decision.Classification = "both_changed"
	adapter := decision.Adapter
	// Pixel three-way merge has no defensible blank-base interpretation. Two
	// independently-added rasters may look compatible while actually encoding
	// unrelated maps, masks, or atlas slots, so require an explicit decision.
	if baseFile == nil && (adapter == "png_pixels" || adapter == "tga_pixels") {
		conflict := newRebaseConflict(
			"raster_add_conflict", rel, "", "project and target both added a raster without a shared pixel baseline",
			[]string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile,
		)
		decision.Action, decision.Reason, decision.ConflictIDs = "conflict", conflict.Message, []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	if rebaseSemanticAdapterRequiresVersionGate(adapter) && !gameVersion.SemanticMergeAllowed {
		conflict := newRebaseConflict(
			"semantic_game_version_blocked", rel, "",
			"Jomini semantic replay is unavailable for this game-version gate: "+gameVersion.Reason,
			[]string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile,
		)
		decision.Action, decision.Reason, decision.ConflictIDs = "conflict", conflict.Message, []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	if rebaseSemanticAdapterRequiresVersionGate(adapter) && !rebaseSemanticMergeAllowedForPath(rel) {
		conflict := newRebaseConflict(
			"semantic_domain_not_allowlisted", rel, "",
			"automatic Jomini object merge is only enabled for CK3 file families with a proven object identity; this path is not one of them",
			[]string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile,
		)
		decision.Action, decision.Reason, decision.ConflictIDs = "conflict", conflict.Message, []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	if adapter == "unknown" || adapter == "binary" {
		code := "unsupported_both_changed"
		if adapter == "binary" {
			code = "binary_merge_conflict"
		}
		conflict := newRebaseConflict(
			code, rel, "", "project and target both changed a file without a proven zero-conflict adapter",
			[]string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile,
		)
		decision.Action, decision.Reason, decision.ConflictIDs = "conflict", conflict.Message, []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	candidate, semanticIDs, conflicts, err := buildRebaseCandidate(ctx, rel, project, base, target, baseFile, projectFile, targetFile, adapter, semantic)
	if err != nil {
		return decision, nil, err
	}
	decision.SemanticIDs = semanticIDs
	if len(conflicts) > 0 {
		decision.Action, decision.Reason = "conflict", "adapter found overlapping semantic changes"
		for _, conflict := range conflicts {
			decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
		}
		if candidate != nil {
			if err := storeRebaseCandidate(artifactRoot, transactionID, rel, candidate, &decision); err != nil {
				return decision, nil, err
			}
		}
		return decision, conflicts, nil
	}
	if targetFile != nil && hashBytes(candidate) == targetFile.SHA256 {
		decision.Action, decision.Reason, decision.Safe = "delete_project", "adapter result is byte-identical to the target; remove the redundant project shadow", true
		decision.Result = RebaseFileState{}
		return decision, nil, nil
	}
	if err := storeRebaseCandidate(artifactRoot, transactionID, rel, candidate, &decision); err != nil {
		return decision, nil, err
	}
	decision.Action, decision.Reason, decision.Safe = "write_candidate", "adapter produced a zero-conflict semantic three-way merge", true
	return decision, nil, nil
}

func rebaseOwnedPath(rel string, prefixes []string) bool {
	base := strings.ToLower(path.Base(filepath.ToSlash(rel)))
	for _, prefix := range prefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func isCoreMapPath(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	if strings.HasPrefix(lower, "map_data/") || strings.HasPrefix(lower, "gfx/map/") {
		return true
	}
	for _, candidate := range rebaseCoreMapPaths {
		if lower == candidate {
			return true
		}
	}
	return false
}

func rebaseTargetAuthorityReferenceNeedsRewrite(baseFile, projectFile *SnapshotFile) bool {
	if projectFile == nil {
		return false
	}
	return baseFile == nil || projectFile.SHA256 != baseFile.SHA256
}

// rebaseSemanticMergeDomains is the explicit allowlist of CK3 file families
// whose top-level object identity this migrator can prove.
//
// Opening automatic Jomini merge to every .txt/.gui/.asset/.settings/.mod file
// was too wide: those formats routinely repeat one generic top-level key
// (widget, object, locator) and carry their real identity in a child field, so
// a generic key-based merge can rewrite the wrong object or append a duplicate.
// A path outside this list is not refused - it becomes a review conflict with
// keep_project / use_target / manual, which is the same treatment any other
// unsupported both-changed file gets.
var rebaseSemanticMergeDomains = []string{
	"events/",
	"common/scripted_effects/",
	"common/scripted_triggers/",
	"common/scripted_modifiers/",
	"common/culture/cultures/",
	"common/culture/traditions/",
	"common/religion/religions/",
	"common/religion/faiths/",
	"common/landed_titles/",
	"history/characters/",
	"history/provinces/",
	"history/titles/",
}

func rebaseSemanticMergeAllowedForPath(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	for _, prefix := range rebaseSemanticMergeDomains {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func rebaseAdapterForPath(rel string) string {
	lower := strings.ToLower(filepath.ToSlash(rel))
	ext := strings.ToLower(filepath.Ext(lower))
	switch {
	case isCoreMapPath(lower):
		return "map_core"
	case ext == ".yml" || ext == ".yaml":
		return "localization"
	case ext == ".png":
		return "png_pixels"
	case ext == ".tga":
		return "tga_pixels"
	case ext == ".txt" || ext == ".gui" || ext == ".map" || ext == ".asset" || ext == ".settings" || ext == ".mod":
		if strings.Contains(lower, "locator") || strings.Contains(lower, "map_object_data") {
			return "locator_objects"
		}
		return "jomini_objects"
	case ext == ".dds" || ext == ".mesh" || ext == ".anim" || ext == ".bank" || ext == ".wav" || ext == ".ogg" || ext == ".dat" || ext == ".heightmap":
		return "binary"
	default:
		return "unknown"
	}
}

func readSourceFile(root, rel string) ([]byte, error) {
	normalized, err := normalizeRel(rel)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := resolveRebasePath(root)
	if err != nil {
		return nil, fmt.Errorf("unsafe source root: %w", err)
	}
	full, err := safeRebaseContainedPath(resolvedRoot, filepath.Join(resolvedRoot, filepath.FromSlash(normalized)))
	if err != nil {
		return nil, fmt.Errorf("source path escapes its configured root: %w", err)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source file is not a regular file: %s", normalized)
	}
	return os.ReadFile(full)
}

func storeRebaseCandidate(root, id, rel string, data []byte, decision *RebaseFileDecision) error {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return err
	}
	normalized, err := normalizeRel(rel)
	if err != nil {
		return err
	}
	candidateRel := filepath.ToSlash(filepath.Join("candidates", filepath.FromSlash(normalized)))
	full, err := safeRebaseContainedPath(dir, filepath.Join(dir, filepath.FromSlash(candidateRel)))
	if err != nil {
		return fmt.Errorf("candidate path escapes transaction root: %w", err)
	}
	parent, err := ensureRebaseDirectory(filepath.Dir(full))
	if err != nil {
		return err
	}
	full, err = safeRebaseContainedPath(dir, filepath.Join(parent, filepath.Base(full)))
	if err != nil {
		return fmt.Errorf("candidate path escapes transaction root after directory creation: %w", err)
	}
	file, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	decision.CandidatePath = candidateRel
	decision.Result = RebaseFileState{Exists: true, SHA256: hashBytes(data), Size: int64(len(data)), Text: isTextPath(rel)}
	return nil
}

func newRebaseConflict(code, path, semanticID, message string, allowed []string, suggested string, base, project, target *SnapshotFile) RebaseConflict {
	payload := strings.Join([]string{
		code, strings.ToLower(filepath.ToSlash(path)), semanticID, message,
		fileHash(base), fileHash(project), fileHash(target),
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return RebaseConflict{
		ID:   rebaseTransactionPrefix + "conflict-" + hex.EncodeToString(sum[:8]),
		Code: code, Path: path, SemanticID: semanticID, Message: message, Severity: "error",
		AllowedActions: append([]string(nil), allowed...), SuggestedAction: suggested,
	}
}

func fileHash(file *SnapshotFile) string {
	if file == nil {
		return "-"
	}
	return file.SHA256
}

func dedupeRebaseConflicts(conflicts []RebaseConflict) []RebaseConflict {
	seen := map[string]bool{}
	out := conflicts[:0]
	for _, conflict := range conflicts {
		if seen[conflict.ID] {
			continue
		}
		seen[conflict.ID] = true
		out = append(out, conflict)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
