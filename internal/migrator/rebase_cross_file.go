package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/indexer"
)

// planRebaseCrossFileMoveConflict records enough evidence to resolve a
// cross-file move without applying one review action to two unrelated files.
// A project-owned new path can only win when the old target path has a
// transaction-owned shadow candidate with that exact semantic object removed.
// If the surrounding file decisions are not simple enough to prove that
// transformation, the conflict deliberately requires a new plan instead of
// offering a plausible but lossy resolution.
func planRebaseCrossFileMoveConflict(
	artifactRoot, transactionID string,
	indexes *rebaseSemanticIndexes,
	transaction *RebaseTransaction,
	projectRecord, targetRecord rebaseSemanticRecord,
	baseFile, projectFile, targetFile *SnapshotFile,
) (RebaseConflict, error) {
	conflict := newRebaseConflict(
		"semantic_cross_file_move_conflict", projectRecord.Path, projectRecord.ID,
		"project and target expose the same semantic object from different files; create a new plan after reconciling physical ownership",
		[]string{"edit_project"}, "edit_project", baseFile, projectFile, targetFile,
	)
	conflict.CounterpartPath = targetRecord.Path

	if transaction == nil || indexes == nil {
		return conflict, nil
	}
	sourceIndex, sourceOK := rebaseDecisionIndex(transaction.Files, projectRecord.Path)
	counterpartIndex, counterpartOK := rebaseDecisionIndex(transaction.Files, targetRecord.Path)
	if !sourceOK || !counterpartOK || sourceIndex == counterpartIndex {
		conflict.Message += "; planner could not locate both file decisions"
		return conflict, nil
	}
	sourceDecision := transaction.Files[sourceIndex]
	counterpartDecision := transaction.Files[counterpartIndex]
	if !rebaseCrossFileCounterpartCanBeShadowed(counterpartDecision, targetFile) {
		conflict.Message += "; target-side decision is not a pristine inherited file and cannot be safely shadowed"
		return conflict, nil
	}

	candidatePath, candidateState, err := storeRebaseCrossFileTargetRemovalCandidate(artifactRoot, transactionID, indexes, conflict, targetRecord, targetFile)
	if err != nil {
		// This is an ambiguity rather than a reason to abandon the whole
		// transaction.  Keep a reviewable, non-materializable blocker.
		conflict.Message += "; no verified target-removal candidate was produced: " + err.Error()
		return conflict, nil
	}
	conflict.TargetRemovalCandidatePath = candidatePath
	conflict.TargetRemovalCandidateSHA256 = candidateState.SHA256

	allowed := []string{"manual"}
	if rebaseCrossFileSourceCanKeep(sourceDecision, projectFile) {
		allowed = append([]string{"keep_project"}, allowed...)
	}
	if rebaseCrossFileSourceCanDelete(indexes, sourceDecision, projectRecord, projectFile) {
		allowed = append(allowed[:1], append([]string{"use_target"}, allowed[1:]...)...)
	}
	conflict.AllowedActions = allowed
	if rebaseCrossFileActionAllowed(allowed, "keep_project") {
		conflict.SuggestedAction = "keep_project"
		conflict.Message = "project and target expose the same semantic object from different files; keep_project makes " + projectRecord.Path + " the unique owner and applies the verified target-removal shadow to " + targetRecord.Path
	} else {
		conflict.SuggestedAction = "manual"
		conflict.Message = "project and target expose the same semantic object from different files; manual replaces only " + projectRecord.Path + " and the engine chooses one owner without copying that manual file to " + targetRecord.Path
	}
	return conflict, nil
}

func rebaseDecisionIndex(decisions []RebaseFileDecision, rel string) (int, bool) {
	for index := range decisions {
		if strings.EqualFold(decisions[index].Path, rel) {
			return index, true
		}
	}
	return 0, false
}

func rebaseCrossFileCounterpartCanBeShadowed(decision RebaseFileDecision, targetFile *SnapshotFile) bool {
	if targetFile == nil || !decision.Safe || len(decision.ConflictIDs) != 0 || !decision.Target.Exists || decision.Target.SHA256 != targetFile.SHA256 {
		return false
	}
	return decision.Action == "inherit_target" || decision.Action == "delete_project"
}

func rebaseCrossFileSourceCanKeep(decision RebaseFileDecision, projectFile *SnapshotFile) bool {
	return projectFile != nil && decision.Safe && len(decision.ConflictIDs) == 0 && decision.Action == "keep_project" && decision.Project.Exists && decision.Project.SHA256 == projectFile.SHA256
}

// use_target deletes the moved project file.  That is only safe when the file
// was project-added, has no target counterpart, and contains exactly the one
// relocated semantic object.  More complicated files require an explicit
// manual source edit and a new plan, rather than silently discarding siblings.
func rebaseCrossFileSourceCanDelete(indexes *rebaseSemanticIndexes, decision RebaseFileDecision, record rebaseSemanticRecord, projectFile *SnapshotFile) bool {
	if !rebaseCrossFileSourceCanKeep(decision, projectFile) || indexes == nil || indexes.baseFile(record.Path) != nil || indexes.targetFile(record.Path) != nil {
		return false
	}
	if indexes.project.parseErrors[strings.ToLower(record.Path)] != "" {
		return false
	}
	records := indexes.project.pathRecords(record.Path)
	return len(records) == 1 && strings.EqualFold(records[0].ID, record.ID)
}

func (indexes *rebaseSemanticIndexes) baseFile(rel string) *SnapshotFile {
	if indexes == nil {
		return nil
	}
	return rebaseSemanticLayerFile(&indexes.base, rel)
}

func (indexes *rebaseSemanticIndexes) targetFile(rel string) *SnapshotFile {
	if indexes == nil {
		return nil
	}
	return rebaseSemanticLayerFile(&indexes.target, rel)
}

func rebaseSemanticLayerFile(layer *rebaseSemanticLayer, rel string) *SnapshotFile {
	if layer == nil {
		return nil
	}
	for index := range layer.files {
		if strings.EqualFold(layer.files[index].Path, rel) {
			return &layer.files[index]
		}
	}
	return nil
}

func storeRebaseCrossFileTargetRemovalCandidate(root, id string, indexes *rebaseSemanticIndexes, conflict RebaseConflict, targetRecord rebaseSemanticRecord, targetFile *SnapshotFile) (string, RebaseFileState, error) {
	if targetFile == nil || !targetFile.Text {
		return "", RebaseFileState{}, fmt.Errorf("target counterpart is not a text file")
	}
	targetData, err := readSourceFile(indexes.target.root, targetRecord.Path)
	if err != nil {
		return "", RebaseFileState{}, err
	}
	if hashBytes(targetData) != targetFile.SHA256 {
		return "", RebaseFileState{}, fmt.Errorf("target counterpart changed while planning")
	}
	candidate, err := removeRebaseSemanticRecord(targetRecord.Path, string(targetData), targetRecord)
	if err != nil {
		return "", RebaseFileState{}, err
	}
	if count, err := rebaseSemanticIDOccurrences(targetRecord.Path, candidate, conflict.SemanticID); err != nil {
		return "", RebaseFileState{}, err
	} else if count != 0 {
		return "", RebaseFileState{}, fmt.Errorf("target-removal candidate still exposes %s", conflict.SemanticID)
	}
	ext := strings.ToLower(filepath.Ext(targetRecord.Path))
	if ext == "" {
		ext = ".txt"
	}
	artifactRel := filepath.ToSlash(filepath.Join("cross-file", conflict.ID+"-target-removal"+ext))
	var artifact RebaseFileDecision
	if err := storeRebaseCandidate(root, id, artifactRel, []byte(candidate), &artifact); err != nil {
		return "", RebaseFileState{}, err
	}
	return artifact.CandidatePath, artifact.Result, nil
}

func removeRebaseSemanticRecord(rel, text string, record rebaseSemanticRecord) (string, error) {
	start, end, ok := nodeRuneSpan(text, record.Node)
	if !ok {
		return "", fmt.Errorf("could not locate target semantic object span")
	}
	runes := []rune(text)
	if start < 0 || end < start || end > len(runes) || string(runes[start:end]) != record.Raw {
		return "", fmt.Errorf("target semantic object no longer matches its planned span")
	}
	result := string(append(append([]rune(nil), runes[:start]...), runes[end:]...))
	parsed := parseRebaseJomini(rel, result)
	if len(parsed.Errors) > 0 {
		return "", fmt.Errorf("target-removal candidate does not parse: %s", parsed.Errors[0].Message)
	}
	return result, nil
}

func rebaseSemanticIDOccurrences(rel, text, semanticID string) (int, error) {
	parsed := parseRebaseJomini(rel, text)
	if len(parsed.Errors) > 0 {
		return 0, fmt.Errorf("semantic candidate does not parse: %s", parsed.Errors[0].Message)
	}
	count := 0
	for _, node := range parsed.Nodes {
		if strings.EqualFold(semanticNodeID(rel, node), semanticID) {
			count++
		}
	}
	return count, nil
}

func rebaseCrossFileActionAllowed(allowed []string, action string) bool {
	for _, value := range allowed {
		if value == action {
			return true
		}
	}
	return false
}

// verifyRebaseCrossFileMoveUniqueness validates the effective overlay plus
// target stack for only the cross-file IDs recorded by a transaction.  It is
// intentionally narrow: pre-existing upstream duplicate diagnostics belong to
// the regular validation delta, while this check proves that a reviewed move
// did not recreate its own duplicate owner.
func verifyRebaseCrossFileMoveUniqueness(ctx context.Context, overlayRoot string, target indexer.Source, transaction RebaseTransaction) error {
	overlayFiles, _, err := collectFiles(overlayRoot)
	if err != nil {
		return fmt.Errorf("migration overlay inventory: %w", err)
	}
	targetFiles, _, err := collectFiles(target.Path)
	if err != nil {
		return fmt.Errorf("target inventory: %w", err)
	}
	overlay := rebaseSemanticLayer{root: overlayRoot, files: overlayFiles}
	if err := overlay.load(ctx); err != nil {
		return err
	}
	targetLayer := rebaseSemanticLayer{root: target.Path, files: targetFiles}
	if err := targetLayer.load(ctx); err != nil {
		return err
	}
	overlayPaths := make(map[string]bool, len(overlayFiles))
	for _, file := range overlayFiles {
		overlayPaths[strings.ToLower(file.Path)] = true
	}

	conflicts := append([]RebaseConflict(nil), transaction.Conflicts...)
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].ID < conflicts[j].ID })
	for _, conflict := range conflicts {
		if conflict.Code != "semantic_cross_file_move_conflict" {
			continue
		}
		if conflict.SemanticID == "" || conflict.Path == "" || conflict.CounterpartPath == "" {
			return fmt.Errorf("cross-file conflict %s lacks ownership evidence", conflict.ID)
		}
		for _, rel := range []string{conflict.Path, conflict.CounterpartPath} {
			key := strings.ToLower(rel)
			if overlayPaths[key] {
				if message := overlay.parseErrors[key]; message != "" {
					return fmt.Errorf("cross-file uniqueness check cannot parse overlay %s: %s", rel, message)
				}
			} else if message := targetLayer.parseErrors[key]; message != "" {
				return fmt.Errorf("cross-file uniqueness check cannot parse target %s: %s", rel, message)
			}
		}
		id := strings.ToLower(conflict.SemanticID)
		owners := append([]rebaseSemanticRecord(nil), overlay.recordsByID[id]...)
		for _, record := range targetLayer.recordsByID[id] {
			if !overlayPaths[strings.ToLower(record.Path)] {
				owners = append(owners, record)
			}
		}
		if len(owners) != 1 {
			paths := make([]string, 0, len(owners))
			for _, owner := range owners {
				paths = append(paths, owner.Path)
			}
			sort.Strings(paths)
			return fmt.Errorf("cross-file semantic ownership for %s has %d effective records (%s)", conflict.SemanticID, len(owners), strings.Join(paths, ", "))
		}
	}
	return nil
}

func readRebaseCrossFileCandidate(root, id string, conflict RebaseConflict) (string, RebaseFileState, error) {
	if strings.TrimSpace(conflict.CounterpartPath) == "" || strings.TrimSpace(conflict.TargetRemovalCandidatePath) == "" || !validSHA256(conflict.TargetRemovalCandidateSHA256) {
		return "", RebaseFileState{}, fmt.Errorf("cross-file conflict %s has no verified target-removal candidate", conflict.ID)
	}
	path, err := rebaseCandidateFile(root, id, conflict.TargetRemovalCandidatePath)
	if err != nil {
		return "", RebaseFileState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", RebaseFileState{}, err
	}
	if hashBytes(data) != conflict.TargetRemovalCandidateSHA256 {
		return "", RebaseFileState{}, fmt.Errorf("target-removal candidate hash changed")
	}
	if count, err := rebaseSemanticIDOccurrences(conflict.CounterpartPath, string(data), conflict.SemanticID); err != nil {
		return "", RebaseFileState{}, err
	} else if count != 0 {
		return "", RebaseFileState{}, fmt.Errorf("target-removal candidate still exposes %s", conflict.SemanticID)
	}
	return conflict.TargetRemovalCandidatePath, RebaseFileState{Exists: true, SHA256: hashBytes(data), Size: int64(len(data)), Text: isTextPath(conflict.CounterpartPath)}, nil
}
