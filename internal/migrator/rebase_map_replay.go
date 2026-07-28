package migrator

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/indexer"
)

// rebaseProjectMapContext keeps the two exact RGB rewrites needed when the
// project map remains authoritative: old Base -> project map and new Target ->
// project map. The mapping is intentionally unavailable for target-authority
// and disabled map runs.
type rebaseProjectMapContext struct {
	baseToProject   *migrationPolicy
	targetToProject *migrationPolicy
	baseMapping     rebaseProvinceMapping
	targetMapping   rebaseProvinceMapping
	err             error
}

// rebaseTargetMapContext is the mirror of rebaseProjectMapContext for a
// target-authoritative core map.  It only permits a project customisation to
// survive when every referenced province can be proven identical by RGB and
// rewritten into the target map's ID space.
type rebaseTargetMapContext struct {
	baseToTarget    *migrationPolicy
	projectToTarget *migrationPolicy
	baseMapping     rebaseProvinceMapping
	projectMapping  rebaseProvinceMapping
	err             error
}

func prepareRebaseProjectMapContext(base, project, target indexer.Source, baseFiles, projectFiles, targetFiles map[string]*SnapshotFile) (*rebaseProjectMapContext, error) {
	const definition = "map_data/definition.csv"
	baseFile := baseFiles[definition]
	targetFile := targetFiles[definition]
	projectFile := projectFiles[definition]
	if baseFile == nil || targetFile == nil {
		return nil, fmt.Errorf("project map authority requires %s in both base and target", definition)
	}
	baseData, err := readSourceFile(base.Path, definition)
	if err != nil {
		return nil, err
	}
	targetData, err := readSourceFile(target.Path, definition)
	if err != nil {
		return nil, err
	}
	projectData := baseData
	if projectFile != nil {
		projectData, err = readSourceFile(project.Path, definition)
		if err != nil {
			return nil, err
		}
	}
	baseMapping, err := buildRebaseProvinceMapping(baseData, projectData, baseData)
	if err != nil {
		return nil, fmt.Errorf("build exact base-to-project province mapping: %w", err)
	}
	targetMapping, err := buildRebaseProvinceMapping(baseData, projectData, targetData)
	if err != nil {
		return nil, fmt.Errorf("build exact target-to-project province mapping: %w", err)
	}
	return &rebaseProjectMapContext{
		baseToProject:   rebaseExactProvinceMappingPolicy(baseMapping),
		targetToProject: rebaseExactProvinceMappingPolicy(targetMapping),
		baseMapping:     baseMapping,
		targetMapping:   targetMapping,
	}, nil
}

func prepareRebaseTargetMapContext(base, project, target indexer.Source, baseFiles, projectFiles, targetFiles map[string]*SnapshotFile) (*rebaseTargetMapContext, error) {
	const definition = "map_data/definition.csv"
	baseFile := baseFiles[definition]
	targetFile := targetFiles[definition]
	projectFile := projectFiles[definition]
	if baseFile == nil || targetFile == nil {
		return nil, fmt.Errorf("target map authority requires %s in both base and target", definition)
	}
	baseData, err := readSourceFile(base.Path, definition)
	if err != nil {
		return nil, err
	}
	targetData, err := readSourceFile(target.Path, definition)
	if err != nil {
		return nil, err
	}
	projectData := baseData
	if projectFile != nil {
		projectData, err = readSourceFile(project.Path, definition)
		if err != nil {
			return nil, err
		}
	}
	// buildRebaseProvinceMapping returns third-argument IDs rewritten into
	// second-argument IDs. Passing target as the middle argument therefore
	// gives Base -> Target and Project -> Target policies respectively.
	baseMapping, err := buildRebaseProvinceMapping(baseData, targetData, baseData)
	if err != nil {
		return nil, fmt.Errorf("build exact base-to-target province mapping: %w", err)
	}
	projectMapping, err := buildRebaseProvinceMapping(baseData, targetData, projectData)
	if err != nil {
		return nil, fmt.Errorf("build exact project-to-target province mapping: %w", err)
	}
	return &rebaseTargetMapContext{
		baseToTarget:    rebaseExactProvinceMappingPolicy(baseMapping),
		projectToTarget: rebaseExactProvinceMappingPolicy(projectMapping),
		baseMapping:     baseMapping,
		projectMapping:  projectMapping,
	}, nil
}

func rebaseExactProvinceMappingPolicy(mapping rebaseProvinceMapping) *migrationPolicy {
	policy := &migrationPolicy{
		decisions:   map[int]mapDecision{},
		resolutions: map[string]Resolution{},
		bySource:    map[int]Resolution{},
		sourceWater: map[int]bool{},
		targetWater: map[int]bool{},
		targetIDs:   map[int]bool{},
	}
	for source, target := range mapping.TargetToProject {
		policy.decisions[source] = mapDecision{
			Source: source, Targets: []int{target}, Kind: "exact_rgb",
			Coverage: 1, Confidence: 1, SafeScalar: true, SafeCollection: true,
		}
		policy.targetIDs[target] = true
	}
	return policy
}

func rebaseMapTransformFile(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	if !strings.HasSuffix(lower, ".txt") {
		return false
	}
	return strings.HasPrefix(lower, "history/provinces/") ||
		strings.HasPrefix(lower, "common/province_terrain/") ||
		strings.HasPrefix(lower, "common/landed_titles/")
}

func planRebaseProjectMapReference(
	artifactRoot, transactionID, rel string,
	project, base, target indexer.Source,
	baseFile, projectFile, targetFile *SnapshotFile,
	context *rebaseProjectMapContext,
) (RebaseFileDecision, []RebaseConflict, error) {
	decision := RebaseFileDecision{
		Path: rel, Classification: "map_reference_rebase", Adapter: "exact_rgb_province_mapping",
		Base: fileState(baseFile), Project: fileState(projectFile), Target: fileState(targetFile),
	}
	if context == nil || context.err != nil {
		message := "project map authority has no valid exact RGB province mapping"
		if context != nil && context.err != nil {
			message += ": " + context.err.Error()
		}
		return rebaseMapReferenceConflict(decision, "map_definition_mapping_error", rel, "", message, baseFile, projectFile, targetFile)
	}

	var baseData, projectData, targetData []byte
	var err error
	if baseFile != nil {
		baseData, err = readSourceFile(base.Path, rel)
		if err != nil {
			return decision, nil, err
		}
	}
	if projectFile != nil {
		projectData, err = readSourceFile(project.Path, rel)
		if err != nil {
			return decision, nil, err
		}
	}
	if targetFile != nil {
		targetData, err = readSourceFile(target.Path, rel)
		if err != nil {
			return decision, nil, err
		}
	}

	baseText, baseConflicts := rebaseRewriteMapText(rel, baseData, context.baseToProject, baseFile, projectFile, targetFile)
	targetText, targetConflicts := rebaseRewriteMapText(rel, targetData, context.targetToProject, baseFile, projectFile, targetFile)
	conflicts := append(baseConflicts, targetConflicts...)
	if len(conflicts) > 0 {
		decision.Action = "conflict"
		decision.Reason = "exact RGB map reference rewrite found unresolved province IDs"
		for _, conflict := range conflicts {
			decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
		}
		return decision, dedupeRebaseConflicts(conflicts), nil
	}

	if projectFile == nil {
		if targetFile == nil {
			if baseFile != nil {
				return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, baseText, nil, nil, "target deletion preserves the proven project-authority base map reference")
			}
			decision.Action, decision.Reason, decision.Safe = "inherit_target", "target deletion is inherited", true
			return decision, nil, nil
		}
		return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, targetText, targetData, targetFile, "target map references were replayed through exact RGB mapping")
	}

	projectUnchanged := baseFile != nil && (string(projectData) == string(baseData) || string(projectData) == targetOrBaseText(baseText, baseData))
	if targetFile == nil {
		if projectUnchanged {
			return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, baseText, nil, nil, "target deletion preserves the proven project-authority base map reference")
		}
		decision.Action, decision.Reason, decision.Safe, decision.Result = "keep_project", "project-only map reference override remains authoritative", true, fileState(projectFile)
		return decision, nil, nil
	}
	if baseFile == nil {
		candidate, ids, mergeConflicts := mergeRebaseMapReferenceJomini(rel, "", string(projectData), targetText, baseFile, projectFile, targetFile)
		decision.SemanticIDs = ids
		if len(mergeConflicts) > 0 {
			decision.Action, decision.Reason = "conflict", "both-added map reference file has overlapping semantic changes"
			for _, conflict := range mergeConflicts {
				decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
			}
			return decision, mergeConflicts, nil
		}
		return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, candidate, targetData, targetFile, "both-added map references merged after exact RGB mapping")
	}
	if projectUnchanged {
		return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, targetText, targetData, targetFile, "target map references were replayed through exact RGB mapping")
	}
	if targetText == baseText {
		decision.Action, decision.Reason, decision.Safe, decision.Result = "keep_project", "only project map-reference fields changed", true, fileState(projectFile)
		return decision, nil, nil
	}
	candidate, ids, mergeConflicts := mergeRebaseMapReferenceJomini(rel, baseText, string(projectData), targetText, baseFile, projectFile, targetFile)
	decision.SemanticIDs = ids
	if len(mergeConflicts) > 0 {
		decision.Action, decision.Reason = "conflict", "map reference merge found overlapping semantic changes"
		for _, conflict := range mergeConflicts {
			decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
		}
		return decision, mergeConflicts, nil
	}
	return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, candidate, targetData, targetFile, "map references merged after exact RGB mapping")
}

// planRebaseTargetMapReference replays project edits into a target-authority
// map only after rewriting the Base and project sides into target province-ID
// space through exact RGB identity.  It intentionally has no approximate
// geometry/name fallback: an unmapped numeric reference remains a conflict.
func planRebaseTargetMapReference(
	artifactRoot, transactionID, rel string,
	project, base, target indexer.Source,
	baseFile, projectFile, targetFile *SnapshotFile,
	context *rebaseTargetMapContext,
) (RebaseFileDecision, []RebaseConflict, error) {
	decision := RebaseFileDecision{
		Path: rel, Classification: "map_reference_rebase", Adapter: "exact_rgb_province_mapping",
		Base: fileState(baseFile), Project: fileState(projectFile), Target: fileState(targetFile),
	}
	if context == nil || context.err != nil {
		message := "target map authority has no valid exact RGB province mapping"
		if context != nil && context.err != nil {
			message += ": " + context.err.Error()
		}
		return rebaseMapReferenceConflict(decision, "map_definition_mapping_error", rel, "", message, baseFile, projectFile, targetFile)
	}

	var baseData, projectData, targetData []byte
	var err error
	if baseFile != nil {
		baseData, err = readSourceFile(base.Path, rel)
		if err != nil {
			return decision, nil, err
		}
	}
	if projectFile != nil {
		projectData, err = readSourceFile(project.Path, rel)
		if err != nil {
			return decision, nil, err
		}
	}
	if targetFile != nil {
		targetData, err = readSourceFile(target.Path, rel)
		if err != nil {
			return decision, nil, err
		}
	}

	baseText, baseConflicts := rebaseRewriteMapText(rel, baseData, context.baseToTarget, baseFile, projectFile, targetFile)
	projectText, projectConflicts := rebaseRewriteMapText(rel, projectData, context.projectToTarget, baseFile, projectFile, targetFile)
	conflicts := append(baseConflicts, projectConflicts...)
	if len(conflicts) > 0 {
		decision.Action = "conflict"
		decision.Reason = "exact RGB map reference rewrite found unresolved province IDs"
		for _, conflict := range conflicts {
			decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
		}
		return decision, dedupeRebaseConflicts(conflicts), nil
	}

	if projectFile == nil {
		if targetFile == nil {
			decision.Action, decision.Reason, decision.Safe = "inherit_target", "target deletion is inherited", true
			return decision, nil, nil
		}
		return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, string(targetData), targetData, targetFile, "target map authority inherits target references")
	}
	projectUnchanged := baseFile != nil && projectFile.SHA256 == baseFile.SHA256
	if projectUnchanged {
		if targetFile == nil {
			decision.Action, decision.Reason, decision.Safe = "delete_project", "target deletion is inherited after target map authority replay", true
			return decision, nil, nil
		}
		return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, string(targetData), targetData, targetFile, "target map authority inherits target references")
	}
	if baseFile == nil {
		if targetFile == nil {
			return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, projectText, nil, nil, "project-only map reference rewritten into target ID space")
		}
		candidate, ids, mergeConflicts := mergeRebaseMapReferenceJomini(rel, "", projectText, string(targetData), baseFile, projectFile, targetFile)
		decision.SemanticIDs = ids
		if len(mergeConflicts) > 0 {
			decision.Action, decision.Reason = "conflict", "both-added map reference file has overlapping semantic changes"
			for _, conflict := range mergeConflicts {
				decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
			}
			return decision, mergeConflicts, nil
		}
		return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, candidate, targetData, targetFile, "both-added map references merged after exact RGB target replay")
	}
	if targetFile == nil {
		conflict := newRebaseConflict(
			"map_reference_delete_modify_conflict", rel, "",
			"target deleted a map-reference file that the project modified; exact RGB proves IDs but not that resurrecting the file is intended",
			[]string{"use_target", "manual"}, "manual", baseFile, projectFile, targetFile,
		)
		decision.Action, decision.Reason, decision.ConflictIDs = "conflict", conflict.Message, []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	if string(targetData) == baseText {
		return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, projectText, targetData, targetFile, "only project map-reference fields changed and were rewritten into target ID space")
	}
	candidate, ids, mergeConflicts := mergeRebaseMapReferenceJomini(rel, baseText, projectText, string(targetData), baseFile, projectFile, targetFile)
	decision.SemanticIDs = ids
	if len(mergeConflicts) > 0 {
		decision.Action, decision.Reason = "conflict", "target map reference merge found overlapping semantic changes"
		for _, conflict := range mergeConflicts {
			decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
		}
		return decision, mergeConflicts, nil
	}
	return materializeRebaseMapCandidate(artifactRoot, transactionID, &decision, candidate, targetData, targetFile, "map references merged after exact RGB target replay")
}

func targetOrBaseText(transformed string, fallback []byte) string {
	if transformed != "" {
		return transformed
	}
	return string(fallback)
}

func materializeRebaseMapCandidate(artifactRoot, transactionID string, decision *RebaseFileDecision, candidate string, rawTarget []byte, targetFile *SnapshotFile, reason string) (RebaseFileDecision, []RebaseConflict, error) {
	if targetFile != nil && candidate == string(rawTarget) {
		if decision.Project.Exists {
			decision.Action, decision.Reason, decision.Safe = "delete_project", "target supplies the reviewed map reference result", true
		} else {
			decision.Action, decision.Reason, decision.Safe, decision.Result = "inherit_target", "target map reference needs no ID rewrite", true, fileState(targetFile)
		}
		return *decision, nil, nil
	}
	if decision.Project.Exists && candidate == "" {
		decision.Action, decision.Reason, decision.Safe, decision.Result = "keep_project", reason, true, decision.Project
		return *decision, nil, nil
	}
	if err := storeRebaseCandidate(artifactRoot, transactionID, decision.Path, []byte(candidate), decision); err != nil {
		return *decision, nil, err
	}
	decision.Action, decision.Reason, decision.Safe = "write_candidate", reason, true
	return *decision, nil, nil
}

func rebaseRewriteMapText(rel string, data []byte, policy *migrationPolicy, baseFile, projectFile, targetFile *SnapshotFile) (string, []RebaseConflict) {
	if len(data) == 0 {
		return "", nil
	}
	rewritten := rewriteSemantic(rel, data, policy)
	conflicts := make([]RebaseConflict, 0, len(rewritten.Conflicts))
	for _, legacy := range rewritten.Conflicts {
		semanticID := ""
		if legacy.SourceProvince > 0 {
			semanticID = fmt.Sprintf("province:%d", legacy.SourceProvince)
		}
		message := legacy.Message
		if legacy.Line > 0 {
			message = fmt.Sprintf("line %d: %s", legacy.Line, legacy.Message)
		}
		conflicts = append(conflicts, newRebaseConflict(
			"exact_rgb_"+legacy.Code, rel, semanticID, message,
			[]string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile,
		))
	}
	return rewritten.Content, dedupeRebaseConflicts(conflicts)
}

func mergeRebaseMapReferenceJomini(rel, baseText, projectText, targetText string, baseFile, projectFile, targetFile *SnapshotFile) (string, []string, []RebaseConflict) {
	semantic := &rebaseSemanticIndexes{
		base:    rebaseSemanticLayerFromText(rel, baseText),
		project: rebaseSemanticLayerFromText(rel, projectText),
		target:  rebaseSemanticLayerFromText(rel, targetText),
	}
	return mergeJominiThreeWay(rel, baseText, projectText, targetText, baseFile, projectFile, targetFile, semantic)
}

func rebaseSemanticLayerFromText(rel, text string) rebaseSemanticLayer {
	layer := rebaseSemanticLayer{
		recordsByID:   map[string][]rebaseSemanticRecord{},
		recordsByPath: map[string][]rebaseSemanticRecord{},
		parseErrors:   map[string]string{},
	}
	parsed := parseRebaseJomini(rel, text)
	pathKey := strings.ToLower(rel)
	if len(parsed.Errors) > 0 {
		layer.parseErrors[pathKey] = parsed.Errors[0].Message
		return layer
	}
	runes := []rune(text)
	for _, node := range parsed.Nodes {
		start, end, ok := nodeRuneSpan(text, node)
		if !ok {
			layer.parseErrors[pathKey] = "could not locate parsed object span"
			return layer
		}
		id, stable := semanticNodeID(rel, node)
		record := rebaseSemanticRecord{
			ID: id, Path: rel, Raw: string(runes[start:end]), Digest: semanticNodeDigest(node), Node: node, Unstable: !stable,
		}
		key := strings.ToLower(record.ID)
		layer.recordsByID[key] = append(layer.recordsByID[key], record)
		layer.recordsByPath[pathKey] = append(layer.recordsByPath[pathKey], record)
	}
	return layer
}

func rebaseMapReferenceConflict(decision RebaseFileDecision, code, rel, semanticID, message string, baseFile, projectFile, targetFile *SnapshotFile) (RebaseFileDecision, []RebaseConflict, error) {
	conflict := newRebaseConflict(code, rel, semanticID, message, []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile)
	decision.Action, decision.Reason, decision.ConflictIDs = "conflict", message, []string{conflict.ID}
	return decision, []RebaseConflict{conflict}, nil
}

func rebaseMapMappingCounts(context *rebaseProjectMapContext) map[string]int {
	if context == nil {
		return nil
	}
	return map[string]int{
		"map_exact_base_to_project":   len(context.baseMapping.TargetToProject),
		"map_exact_target_to_project": len(context.targetMapping.TargetToProject),
		"map_target_unmapped_ids":     len(context.targetMapping.UnmappedTargetIDs),
	}
}

func rebaseTargetMapMappingCounts(context *rebaseTargetMapContext) map[string]int {
	if context == nil {
		return nil
	}
	return map[string]int{
		"map_exact_base_to_target":    len(context.baseMapping.TargetToProject),
		"map_exact_project_to_target": len(context.projectMapping.TargetToProject),
		"map_project_unmapped_ids":    len(context.projectMapping.UnmappedTargetIDs),
	}
}

// Keep deterministic counts whenever this helper is called from a report or
// future detail endpoint; map iteration itself is not exposed.
func sortedRebaseMappingIDs(mapping rebaseProvinceMapping) []int {
	ids := make([]int, 0, len(mapping.TargetToProject))
	for id := range mapping.TargetToProject {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}
