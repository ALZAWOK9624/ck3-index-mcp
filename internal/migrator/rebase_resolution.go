package migrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveRebaseBuildDecisions converts review choices into the exact overlay
// operations that BuildRebase will execute. Conflicts stay in the transaction
// as audit evidence; the resolved file decisions are what makes a reviewed
// transaction reproducible on a later resume.
func resolveRebaseBuildDecisions(root string, transaction *RebaseTransaction) ([]RebaseFileDecision, error) {
	if transaction == nil {
		return nil, fmt.Errorf("rebase transaction is nil")
	}
	resolutions, err := loadRebaseResolutions(root, transaction.ID)
	if err != nil {
		return nil, err
	}
	if len(transaction.Conflicts) == 0 {
		if len(resolutions) != 0 {
			return nil, fmt.Errorf("rebase transaction has resolutions but no conflicts")
		}
		return append([]RebaseFileDecision(nil), transaction.Files...), nil
	}
	validated, err := validateRebaseReviewResolutions(*transaction, resolutions)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]RebaseResolution, len(validated))
	for _, resolution := range validated {
		byID[resolution.ConflictID] = resolution
	}
	for _, conflict := range transaction.Conflicts {
		if _, exists := byID[conflict.ID]; !exists {
			return nil, fmt.Errorf("rebase transaction %s has unresolved conflict %s", transaction.ID, conflict.ID)
		}
	}

	resolved := append([]RebaseFileDecision(nil), transaction.Files...)
	handled := make(map[string]bool, len(transaction.Conflicts))
	// A cross-file move names two physical decisions but has one semantic
	// resolution. It must be handled as a pair: applying the same generic
	// action to both paths would either fail for an absent project file or
	// write one manual object twice.
	special := make(map[string]bool)
	for _, conflict := range transaction.Conflicts {
		if conflict.Code != "semantic_cross_file_move_conflict" {
			continue
		}
		resolution := byID[conflict.ID]
		if err := applyRebaseCrossFileMoveResolution(root, transaction.ID, resolved, conflict, resolution); err != nil {
			return nil, err
		}
		handled[conflict.ID] = true
		special[conflict.ID] = true
	}
	for index := range resolved {
		decision := &resolved[index]
		if decision.Action != "conflict" && len(decision.ConflictIDs) == 0 {
			continue
		}
		var selected *RebaseResolution
		for _, conflictID := range decision.ConflictIDs {
			if special[conflictID] {
				continue
			}
			resolution, exists := byID[conflictID]
			if !exists {
				return nil, fmt.Errorf("decision %s has unresolved conflict %s", decision.Path, conflictID)
			}
			if selected != nil && (selected.Action != resolution.Action || selected.ManualPath != resolution.ManualPath || selected.SHA256 != resolution.SHA256) {
				return nil, fmt.Errorf("decision %s has incompatible resolutions; split it manually before build", decision.Path)
			}
			value := resolution
			selected = &value
			handled[conflictID] = true
		}
		if selected == nil {
			if len(decision.ConflictIDs) > 0 {
				// Every conflict on this decision was handled by a paired
				// resolver above. Its action has already been made concrete.
				continue
			}
			return nil, fmt.Errorf("decision %s is marked conflicted without conflict ids", decision.Path)
		}
		if err := applyRebaseResolution(root, transaction.ID, decision, *selected); err != nil {
			return nil, err
		}
	}
	for _, conflict := range transaction.Conflicts {
		if !handled[conflict.ID] {
			return nil, fmt.Errorf("conflict %s is a transaction-wide blocker and requires a new plan", conflict.ID)
		}
	}
	transaction.Resolutions = validated
	return resolved, nil
}

func applyRebaseCrossFileMoveResolution(root, id string, decisions []RebaseFileDecision, conflict RebaseConflict, resolution RebaseResolution) error {
	if !rebaseCrossFileActionAllowed(conflict.AllowedActions, resolution.Action) {
		return fmt.Errorf("cross-file conflict %s does not allow %q", conflict.ID, resolution.Action)
	}
	sourceIndex, sourceOK := rebaseDecisionIndex(decisions, conflict.Path)
	counterpartIndex, counterpartOK := rebaseDecisionIndex(decisions, conflict.CounterpartPath)
	if !sourceOK || !counterpartOK || sourceIndex == counterpartIndex {
		return fmt.Errorf("cross-file conflict %s does not identify two planned paths", conflict.ID)
	}
	source := &decisions[sourceIndex]
	counterpart := &decisions[counterpartIndex]
	if !rebaseDecisionHasConflict(*source, conflict.ID) || !rebaseDecisionHasConflict(*counterpart, conflict.ID) {
		return fmt.Errorf("cross-file conflict %s is not attached to both planned paths", conflict.ID)
	}
	shadowPath, shadowState, err := readRebaseCrossFileCandidate(root, id, conflict)
	if err != nil {
		return err
	}
	applyShadow := func() {
		counterpart.Action = "write_candidate"
		counterpart.Reason = "review resolution: remove moved semantic object from target counterpart"
		counterpart.Safe = true
		counterpart.CandidatePath = shadowPath
		counterpart.Result = shadowState
	}
	switch resolution.Action {
	case "keep_project":
		if !source.Project.Exists || !validSHA256(source.Project.SHA256) {
			return fmt.Errorf("cross-file keep_project has no project source at %s", source.Path)
		}
		source.Action = "keep_project"
		source.Reason = "review resolution: project path is the unique semantic owner"
		source.Safe = true
		source.Result = source.Project
		source.CandidatePath = ""
		applyShadow()
	case "use_target":
		source.Action = "delete_project"
		source.Reason = "review resolution: target path is the unique semantic owner"
		source.Safe = true
		source.Result = RebaseFileState{}
		source.CandidatePath = ""
		counterpart.Action = "inherit_target"
		counterpart.Reason = "review resolution: retain target counterpart"
		counterpart.Safe = true
		counterpart.Result = counterpart.Target
		counterpart.CandidatePath = ""
	case "manual":
		manualPath, manualState, err := rebaseManualResolutionFile(root, id, resolution, source.Path)
		if err != nil {
			return err
		}
		manualFile, err := rebaseCandidateFile(root, id, manualPath)
		if err != nil {
			return err
		}
		manualData, err := os.ReadFile(manualFile)
		if err != nil {
			return err
		}
		count, err := rebaseSemanticIDOccurrences(source.Path, string(manualData), conflict.SemanticID)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("manual cross-file candidate must expose exactly one %s at %s", conflict.SemanticID, source.Path)
		}
		source.Action = "write_candidate"
		source.Reason = "review resolution: manual source path is the unique semantic owner"
		source.Safe = true
		source.CandidatePath = manualPath
		source.Result = manualState
		applyShadow()
	default:
		return fmt.Errorf("cross-file conflict %s has non-materializable action %q", conflict.ID, resolution.Action)
	}
	return nil
}

func rebaseDecisionHasConflict(decision RebaseFileDecision, id string) bool {
	for _, value := range decision.ConflictIDs {
		if value == id {
			return true
		}
	}
	return false
}

func applyRebaseResolution(root, id string, decision *RebaseFileDecision, resolution RebaseResolution) error {
	switch resolution.Action {
	case "keep_project":
		if !decision.Project.Exists || !validSHA256(decision.Project.SHA256) {
			return fmt.Errorf("keep_project is not available for absent project file %s", decision.Path)
		}
		decision.Action = "keep_project"
		decision.Reason = "review resolution: keep project version"
		decision.Safe = true
		decision.Result = decision.Project
		decision.CandidatePath = ""
		return nil
	case "use_target", "drop":
		decision.Action = "delete_project"
		decision.Reason = "review resolution: " + resolution.Action
		decision.Safe = true
		decision.Result = RebaseFileState{}
		decision.CandidatePath = ""
		return nil
	case "manual":
		path, state, err := rebaseManualResolutionFile(root, id, resolution, decision.Path)
		if err != nil {
			return err
		}
		decision.Action = "write_candidate"
		decision.Reason = "review resolution: approved manual candidate"
		decision.Safe = true
		decision.CandidatePath = path
		decision.Result = state
		return nil
	default:
		return fmt.Errorf("resolution %s uses a non-materializable action %q", resolution.ConflictID, resolution.Action)
	}
}

func rebaseManualResolutionFile(root, id string, resolution RebaseResolution, decisionPath string) (string, RebaseFileState, error) {
	if strings.TrimSpace(resolution.ManualPath) == "" || strings.TrimSpace(resolution.SHA256) == "" {
		return "", RebaseFileState{}, fmt.Errorf("manual resolution for %s requires manual_path and sha256", resolution.ConflictID)
	}
	rel, err := normalizeRel(resolution.ManualPath)
	if err != nil || !strings.HasPrefix(filepath.ToSlash(rel), "manual/") {
		return "", RebaseFileState{}, fmt.Errorf("manual resolution for %s must reference a file under manual/", resolution.ConflictID)
	}
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return "", RebaseFileState{}, err
	}
	full, err := safeRebaseContainedPath(dir, filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", RebaseFileState{}, fmt.Errorf("manual resolution path escapes transaction: %w", err)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", RebaseFileState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", RebaseFileState{}, fmt.Errorf("manual resolution file is not regular")
	}
	hash, size, err := hashPath(full)
	if err != nil {
		return "", RebaseFileState{}, err
	}
	if !strings.EqualFold(hash, resolution.SHA256) {
		return "", RebaseFileState{}, fmt.Errorf("manual resolution SHA-256 does not match for %s", resolution.ConflictID)
	}
	return rel, RebaseFileState{Exists: true, SHA256: hash, Size: size, Text: isTextPath(decisionPath)}, nil
}
