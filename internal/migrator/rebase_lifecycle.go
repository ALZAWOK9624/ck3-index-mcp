package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ck3-index/internal/indexer"
)

// BuildRebase materializes the planned project overlay into its isolated
// migration-copy directory.  It deliberately never copies the target
// upstream: files that should inherit the target are absent from the overlay.
func BuildRebase(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions) (RebaseTransaction, error) {
	lock, err := lockRebaseLifecycle(cfg, id, "build", opts)
	if err != nil {
		return RebaseTransaction{}, err
	}
	defer func() { _ = lock.Release() }()
	root, transaction, project, base, target, err := loadRebaseLifecycle(cfg, id, opts)
	if err != nil {
		return transaction, err
	}
	resumingBuild := transaction.Status == RebaseStatusFailed && strings.HasPrefix(transaction.Progress.Phase, "build_failed")
	if transaction.Status != RebaseStatusReadyToBuild && transaction.Status != RebaseStatusNeedsReview && !resumingBuild {
		return transaction, fmt.Errorf("rebase transaction %s is %s; only ready_to_build or fully reviewed transactions can be built", id, transaction.Status)
	}
	decisions, err := resolveRebaseBuildDecisions(root, &transaction)
	if err != nil {
		return transaction, err
	}
	if err := rebaseRequireNoConflicts(decisions); err != nil {
		return transaction, err
	}
	transaction.Files = decisions
	if transaction.Status == RebaseStatusNeedsReview {
		transaction.Status = RebaseStatusReadyToBuild
		transaction.Progress = RebaseProgress{Phase: "reviewed", Message: "all conflicts have recorded materializable resolutions"}
		// Review resolutions rewrote the planned actions; republish the journal
		// so a later resume replays the reviewed decisions, not the raw plan.
		if err := persistRebaseDecisions(root, &transaction); err != nil {
			return transaction, err
		}
		if err := persistRebaseLifecycle(root, &transaction); err != nil {
			return transaction, err
		}
	}
	projectTreeFiles, err := rebaseVerifyInputs(cfg, transaction, project, base, target)
	if err != nil {
		return transaction, err
	}
	outputDir, err := rebaseOutputDir(transaction, cfg)
	if err != nil {
		return transaction, err
	}
	if _, err := os.Lstat(outputDir); err == nil {
		if resumingBuild || (transaction.Status == RebaseStatusReadyToBuild && transaction.Progress.Phase == "build") {
			copySHA, verifyErr := verifyRebaseOverlay(outputDir, decisions, projectTreeFiles, newRebaseOverlayPolicy(transaction.Profile))
			if verifyErr != nil {
				return transaction, fmt.Errorf("interrupted migration copy cannot be recovered: %w", verifyErr)
			}
			if verifyErr := verifyRebaseCrossFileMoveUniqueness(ctx, outputDir, target, transaction); verifyErr != nil {
				return transaction, fmt.Errorf("interrupted migration copy cross-file verification failed: %w", verifyErr)
			}
			transaction.Status = RebaseStatusBuilt
			transaction.Validation = RebaseValidation{Counts: map[string]int{}, DiagnosticsBase: map[string]int{}, DiagnosticsCopy: map[string]int{}, DiagnosticsNew: map[string]int{}, CopySHA256: copySHA}
			transaction.Progress = RebaseProgress{Phase: "built", Completed: len(projectTreeFiles) + len(decisions), Total: len(projectTreeFiles) + len(decisions), Message: "previously published migration overlay recovered"}
			if err := persistRebaseLifecycle(root, &transaction); err != nil {
				return transaction, err
			}
			return transaction, nil
		}
		return transaction, fmt.Errorf("migration copy output already exists: %s", outputDir)
	} else if !os.IsNotExist(err) {
		return transaction, err
	}

	if resumingBuild {
		transaction.Status = RebaseStatusReadyToBuild
	}
	transaction.Progress = RebaseProgress{Phase: "build", Total: len(projectTreeFiles) + len(transaction.Files), Message: "copying complete project tree"}
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}

	outputParent, err := ensureRebaseDirectory(filepath.Dir(outputDir))
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "build", err)
	}
	outputDir, err = safeRebaseContainedPath(outputParent, filepath.Join(outputParent, filepath.Base(outputDir)))
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "build", fmt.Errorf("unsafe migration copy output after creating parent: %w", err))
	}
	stageParent, err := os.MkdirTemp(outputParent, ".ck3-rebase-build-")
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "build", err)
	}
	defer os.RemoveAll(stageParent)
	stageRoot := filepath.Join(stageParent, filepath.Base(outputDir))
	if err := os.Mkdir(stageRoot, 0o755); err != nil {
		return rebaseLifecycleFailure(root, transaction, "build", err)
	}

	for index, file := range projectTreeFiles {
		if err := ctx.Err(); err != nil {
			return rebaseLifecycleFailure(root, transaction, "build", err)
		}
		if err := copyFile(filepath.Join(project.Path, filepath.FromSlash(file.Path)), filepath.Join(stageRoot, filepath.FromSlash(file.Path))); err != nil {
			return rebaseLifecycleFailure(root, transaction, "build", fmt.Errorf("copy project tree file %s: %w", file.Path, err))
		}
		transaction.Progress.Completed = index + 1
		transaction.Progress.CurrentPath = file.Path
		if err := rebaseCheckpoint(root, &transaction, index+1, len(projectTreeFiles)); err != nil {
			return transaction, err
		}
	}

	decisions = append([]RebaseFileDecision(nil), transaction.Files...)
	sort.Slice(decisions, func(i, j int) bool {
		return strings.ToLower(decisions[i].Path) < strings.ToLower(decisions[j].Path)
	})
	for index, decision := range decisions {
		if err := ctx.Err(); err != nil {
			return rebaseLifecycleFailure(root, transaction, "build", err)
		}
		if err := applyRebaseDecision(root, transaction.ID, stageRoot, decision); err != nil {
			return rebaseLifecycleFailure(root, transaction, "build", err)
		}
		transaction.Progress.Completed = len(projectTreeFiles) + index + 1
		transaction.Progress.CurrentPath = decision.Path
		if err := rebaseCheckpoint(root, &transaction, index+1, len(decisions)); err != nil {
			return transaction, err
		}
	}

	copySHA, err := verifyRebaseOverlay(stageRoot, decisions, projectTreeFiles, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "build", err)
	}
	if err := verifyRebaseCrossFileMoveUniqueness(ctx, stageRoot, target, transaction); err != nil {
		return rebaseLifecycleFailure(root, transaction, "build", err)
	}
	// On Windows os.Rename refuses to replace an existing directory.  The
	// preceding Lstat is also kept for POSIX callers, where that guarantee is
	// weaker for an empty directory.
	if _, err := os.Lstat(outputDir); err == nil {
		return rebaseLifecycleFailure(root, transaction, "build", fmt.Errorf("migration copy output appeared during build: %s", outputDir))
	} else if !os.IsNotExist(err) {
		return rebaseLifecycleFailure(root, transaction, "build", err)
	}
	if err := os.Rename(stageRoot, outputDir); err != nil {
		return rebaseLifecycleFailure(root, transaction, "build", fmt.Errorf("publish migration copy: %w", err))
	}
	transaction.Status = RebaseStatusBuilt
	transaction.Validation = RebaseValidation{
		Counts:          map[string]int{},
		DiagnosticsBase: map[string]int{}, DiagnosticsCopy: map[string]int{}, DiagnosticsNew: map[string]int{},
		CopySHA256: copySHA,
	}
	transaction.Progress = RebaseProgress{Phase: "built", Completed: len(projectTreeFiles) + len(decisions), Total: len(projectTreeFiles) + len(decisions), Message: "migration overlay built"}
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}
	return transaction, nil
}

// ValidateRebase runs the old and new load stacks independently.  The
// migration copy is always the highest-priority project source in the new
// stack, followed by the new upstream and the profile's validation sources.
func ValidateRebase(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions) (RebaseTransaction, error) {
	lock, err := lockRebaseLifecycle(cfg, id, "validate", opts)
	if err != nil {
		return RebaseTransaction{}, err
	}
	defer func() { _ = lock.Release() }()
	root, transaction, project, base, target, err := loadRebaseLifecycle(cfg, id, opts)
	if err != nil {
		return transaction, err
	}
	resumingValidation := transaction.Status == RebaseStatusFailed && strings.HasPrefix(transaction.Progress.Phase, "validate_failed")
	if transaction.Status != RebaseStatusBuilt && transaction.Status != RebaseStatusValidated && !resumingValidation {
		return transaction, fmt.Errorf("rebase transaction %s is %s; only built transactions can be validated", id, transaction.Status)
	}
	if _, err := rebaseVerifyInputs(cfg, transaction, project, base, target); err != nil {
		return transaction, err
	}
	outputDir, err := rebaseOutputDir(transaction, cfg)
	if err != nil {
		return transaction, err
	}
	copyFiles, _, err := collectRebaseOverlayFiles(outputDir, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return transaction, fmt.Errorf("migration copy inventory: %w", err)
	}
	copySHA := inventoryFingerprint(copyFiles)
	if transaction.Validation.CopySHA256 == "" || copySHA != transaction.Validation.CopySHA256 {
		return transaction, fmt.Errorf("migration copy changed after build; rebuild the transaction before validation")
	}

	if resumingValidation {
		transaction.Status = RebaseStatusBuilt
	}
	transaction.Progress = RebaseProgress{Phase: "validate", Total: 3, Message: "validating old, target-only, and migrated load stacks"}
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}
	dir, err := rebaseTransactionDir(root, transaction.ID)
	if err != nil {
		return transaction, err
	}
	validationRoot, err := os.MkdirTemp(dir, ".validation-")
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", err)
	}
	defer os.RemoveAll(validationRoot)

	oldStack, err := rebaseValidationStackConfig(cfg, transaction.Profile, project.Path, base, filepath.Join(validationRoot, "old.sqlite"), "rebase_old_project")
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", err)
	}
	oldResult, err := runRebaseValidationStack(ctx, oldStack, "rebase_old_project", base.Name)
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", fmt.Errorf("validate old stack: %w", err))
	}
	transaction.Progress.Completed = 1
	transaction.Progress.CurrentPath = "old load stack"
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}
	targetOnlyOverlay := filepath.Join(validationRoot, "target-only-overlay")
	if err := os.MkdirAll(targetOnlyOverlay, 0o755); err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", err)
	}
	targetOnlyStack, err := rebaseValidationStackConfig(cfg, transaction.Profile, targetOnlyOverlay, target, filepath.Join(validationRoot, "target-only.sqlite"), "rebase_target_only")
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", err)
	}
	targetOnlyResult, err := runRebaseValidationStack(ctx, targetOnlyStack, "rebase_target_only", target.Name)
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", fmt.Errorf("validate target-only stack: %w", err))
	}
	transaction.Progress.Completed = 2
	transaction.Progress.CurrentPath = "target-only load stack"
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}

	newStack, err := rebaseValidationStackConfig(cfg, transaction.Profile, outputDir, target, filepath.Join(validationRoot, "new.sqlite"), "rebase_migration_copy")
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", err)
	}
	newResult, err := runRebaseValidationStack(ctx, newStack, "rebase_migration_copy", target.Name)
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", fmt.Errorf("validate migration stack: %w", err))
	}

	validation := RebaseValidation{
		Status:           "ready",
		Counts:           map[string]int{},
		DiagnosticsBase:  cloneRebaseCounts(oldResult.Project.Counts),
		DiagnosticsCopy:  cloneRebaseCounts(newResult.Project.Counts),
		DiagnosticsNew:   cloneRebaseCounts(newResult.Upstream.Counts),
		MapAuditErrors:   newResult.MapErrors,
		MapAuditWarnings: newResult.MapWarnings,
		CopySHA256:       copySHA,
		SmokeApproved:    false,
		ValidatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	rebaseAddPrefixedCounts(validation.Counts, "baseline_stack_", oldResult.Full.Counts)
	rebaseAddPrefixedCounts(validation.Counts, "migration_stack_", newResult.Full.Counts)
	rebaseAddPrefixedCounts(validation.Counts, "target_only_stack_", targetOnlyResult.Full.Counts)
	rebaseAddPrefixedCounts(validation.Counts, "baseline_project_", oldResult.Project.Counts)
	rebaseAddPrefixedCounts(validation.Counts, "migration_copy_", newResult.Project.Counts)
	rebaseAddPrefixedCounts(validation.Counts, "new_upstream_", newResult.Upstream.Counts)
	validation.Counts["map_audit_baseline_errors"] = oldResult.MapErrors
	validation.Counts["map_audit_baseline_warnings"] = oldResult.MapWarnings
	validation.Counts["map_audit_new_errors"] = newResult.MapErrors
	validation.Counts["map_audit_new_warnings"] = newResult.MapWarnings

	newProjectErrors := rebaseNewDiagnostics(oldResult.Project, newResult.Project, "error")
	newStackDiagnostics := rebaseNewMigrationStackDiagnostics(oldResult.Full, targetOnlyResult.Full, newResult.Full)
	newMapFindings, err := rebaseNewMapAuditFindingsWithTarget(
		oldResult.MapAudit, targetOnlyResult.MapAudit, newResult.MapAudit,
		oldStack, targetOnlyStack, newStack,
	)
	if err != nil {
		return rebaseLifecycleFailure(root, transaction, "validate", fmt.Errorf("compare map audit findings: %w", err))
	}
	validation.Counts["new_project_error_fingerprints"] = len(newProjectErrors)
	validation.Counts["new_full_stack_gate_diagnostics"] = len(newStackDiagnostics)
	validation.Counts["new_map_audit_findings"] = len(newMapFindings)
	if len(newProjectErrors) > 0 || len(newStackDiagnostics) > 0 || len(newMapFindings) > 0 {
		validation.Blocked = true
		validation.Status = "blocked"
		validation.Counts["project_error_delta"] = len(newProjectErrors)
		validation.Counts["full_stack_diagnostic_delta"] = len(newStackDiagnostics)
		validation.Counts["map_audit_error_delta"] = len(newMapFindings)
	}
	transaction.Validation = validation
	transaction.Progress = RebaseProgress{Phase: "validated", Completed: 3, Total: 3, Message: "validation complete"}
	if validation.Blocked {
		transaction.Status = RebaseStatusFailed
	} else {
		transaction.Status = RebaseStatusValidated
	}
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}
	if validation.Blocked {
		return transaction, fmt.Errorf("rebase validation introduced project, full-stack, or map audit diagnostics")
	}
	return transaction, nil
}

// ApproveRebaseSmoke records the explicit human game-load approval required
// before a built-and-validated migration copy may replace the formal project.
func ApproveRebaseSmoke(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions) (RebaseTransaction, error) {
	if err := ctx.Err(); err != nil {
		return RebaseTransaction{}, err
	}
	lock, err := lockRebaseLifecycle(cfg, id, "approve-smoke", opts)
	if err != nil {
		return RebaseTransaction{}, err
	}
	defer func() { _ = lock.Release() }()
	root, transaction, project, base, target, err := loadRebaseLifecycle(cfg, id, opts)
	if err != nil {
		return transaction, err
	}
	if transaction.Status != RebaseStatusValidated || transaction.Validation.Blocked {
		return transaction, fmt.Errorf("rebase transaction %s must pass validation before smoke approval", id)
	}
	if _, err := rebaseVerifyInputs(cfg, transaction, project, base, target); err != nil {
		return transaction, err
	}
	outputDir, err := rebaseOutputDir(transaction, cfg)
	if err != nil {
		return transaction, err
	}
	copySHA, err := rebaseDirectoryFingerprint(outputDir, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return transaction, err
	}
	if copySHA != transaction.Validation.CopySHA256 {
		return transaction, fmt.Errorf("migration copy changed after validation; rerun build and validation")
	}
	transaction.Validation.SmokeApproved = true
	transaction.Progress = RebaseProgress{Phase: "smoke_approved", Message: "isolated game-load smoke test approved"}
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}
	return transaction, nil
}

// PromoteRebase performs the only operation that changes the formal project.
// It first moves the current project to a sibling backup, then atomically
// renames the reviewed migration copy into the formal project path.
func PromoteRebase(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions) (RebaseTransaction, error) {
	if err := ctx.Err(); err != nil {
		return RebaseTransaction{}, err
	}
	lock, err := lockRebaseLifecycle(cfg, id, "promote", opts)
	if err != nil {
		return RebaseTransaction{}, err
	}
	defer func() { _ = lock.Release() }()
	root, transaction, project, base, target, err := loadRebaseLifecycle(cfg, id, opts)
	if err != nil {
		return transaction, err
	}
	projectPath, err := resolveRebasePath(project.Path)
	if err != nil {
		return transaction, fmt.Errorf("formal project path: %w", err)
	}
	backupPath, err := rebaseBackupPath(projectPath, transaction.ID)
	if err != nil {
		return transaction, err
	}
	outputDir, err := rebaseOutputDir(transaction, cfg)
	if err != nil {
		return transaction, err
	}
	if transaction.Status == RebaseStatusPromoting {
		return resumeRebasePromotion(ctx, cfg, root, &transaction, projectPath, backupPath, outputDir, base, target)
	}
	if transaction.Status != RebaseStatusValidated || transaction.Validation.Blocked || !transaction.Validation.SmokeApproved {
		return transaction, fmt.Errorf("rebase transaction %s requires successful validation and explicit smoke approval before promotion", id)
	}
	if _, err := rebaseVerifyInputs(cfg, transaction, project, base, target); err != nil {
		return transaction, err
	}
	copySHA, err := rebaseDirectoryFingerprint(outputDir, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return transaction, fmt.Errorf("migration copy inventory: %w", err)
	}
	if copySHA != transaction.Validation.CopySHA256 {
		return transaction, fmt.Errorf("migration copy changed after validation; refusing promotion")
	}
	if _, err := os.Lstat(backupPath); err == nil {
		return transaction, fmt.Errorf("promotion backup already exists: %s", backupPath)
	} else if !os.IsNotExist(err) {
		return transaction, err
	}
	if err := rebaseRequireRegularDirectory(projectPath); err != nil {
		return transaction, fmt.Errorf("formal project: %w", err)
	}
	// Re-prove the rename domain immediately before the first rename. Planning
	// already checked it, but a remount between plan and promote would
	// otherwise be discovered only after the formal project had been moved
	// aside.
	if err := rebaseVerifyRenameDomain(outputDir, projectPath); err != nil {
		return transaction, err
	}

	// The index must be invalidated before the first rename, not after the last
	// one. Between the two renames the SQLite cache still describes the old
	// project while its recorded absolute paths already resolve to migrated
	// files, so a query would mix an old object graph with new file contents
	// under a cache key derived from the old hashes.
	if err := rebaseInvalidateIndex(ctx, cfg, opts, "project_tree_replaced", "full_refresh"); err != nil {
		return transaction, fmt.Errorf("invalidate main index before promotion: %w", err)
	}
	transaction.Promotion = &RebasePromotionReceipt{
		Stage: rebasePromotionIntentStage, FormalPath: projectPath, BackupPath: backupPath,
		PromotedSHA256: copySHA, PreviousSHA256: transaction.ProjectTreeFingerprint,
		IndexInvalidated: true,
		IndexRecovery:    "run a full ck3-index refresh before trusting any index-backed query",
	}
	transaction.Status = RebaseStatusPromoting
	transaction.Progress = RebaseProgress{Phase: "promote", Total: 2, Message: "durably recording promotion intent before moving the formal project"}
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}
	return resumeRebasePromotion(ctx, cfg, root, &transaction, projectPath, backupPath, outputDir, base, target)
}

// RollbackRebase reverses a completed promotion only while both the promoted
// formal project and its retained backup still match the recorded hashes.
func RollbackRebase(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions) (RebaseTransaction, error) {
	if err := ctx.Err(); err != nil {
		return RebaseTransaction{}, err
	}
	lock, err := lockRebaseLifecycle(cfg, id, "rollback", opts)
	if err != nil {
		return RebaseTransaction{}, err
	}
	defer func() { _ = lock.Release() }()
	root, transaction, project, _, _, err := loadRebaseLifecycle(cfg, id, opts)
	if err != nil {
		return transaction, err
	}
	projectPath, err := resolveRebasePath(project.Path)
	if err != nil {
		return transaction, err
	}
	backupPath, err := rebaseBackupPath(projectPath, transaction.ID)
	if err != nil {
		return transaction, err
	}
	outputDir, err := rebaseOutputDir(transaction, cfg)
	if err != nil {
		return transaction, err
	}
	if transaction.Status == RebaseStatusRollingBack {
		return resumeRebaseRollback(ctx, root, &transaction, projectPath, backupPath, outputDir)
	}
	if transaction.Status != RebaseStatusPromoted || transaction.Promotion == nil {
		return transaction, fmt.Errorf("rebase transaction %s has not been promoted", id)
	}
	if err := rebaseValidatePromotionReceipt(transaction, projectPath, backupPath); err != nil {
		return transaction, err
	}
	currentSHA, err := rebaseDirectoryFingerprint(projectPath, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return transaction, err
	}
	if currentSHA != transaction.Promotion.PromotedSHA256 {
		return transaction, fmt.Errorf("formal project changed after promotion; refusing rollback")
	}
	backupSHA, err := rebaseDirectoryFingerprint(backupPath, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return transaction, err
	}
	if backupSHA != transaction.Promotion.PreviousSHA256 {
		return transaction, fmt.Errorf("promotion backup changed after promotion; refusing rollback")
	}
	layout, err := rebaseRollbackLayout(projectPath, backupPath, outputDir, transaction.Promotion, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return transaction, err
	}
	if layout != rebaseRollbackIntentStage {
		return transaction, fmt.Errorf("rollback requires an intact promoted-project layout")
	}
	// Rollback replaces the project tree exactly as promotion did, so the index
	// is invalidated on this path too.
	if err := rebaseInvalidateIndex(ctx, cfg, opts, "project_tree_replaced", "full_refresh"); err != nil {
		return transaction, fmt.Errorf("invalidate main index before rollback: %w", err)
	}
	transaction.Promotion.IndexInvalidated = true
	transaction.Promotion.IndexRecovery = "run a full ck3-index refresh before trusting any index-backed query"
	transaction.Promotion.Stage = rebaseRollbackIntentStage
	transaction.Status = RebaseStatusRollingBack
	transaction.Progress = RebaseProgress{Phase: "rollback", Total: 2, Message: "durably recording rollback intent before moving the promoted project"}
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, err
	}
	return resumeRebaseRollback(ctx, root, &transaction, projectPath, backupPath, outputDir)
}

// lockRebaseLifecycle serializes every operation that can change a
// transaction. The status check and the publication that follows it sit at
// opposite ends of a long copy or rename, so without this two concurrent
// invocations can both pass the check and then write contradictory end states.
func lockRebaseLifecycle(cfg indexer.Config, id, operation string, opts RebaseOptions) (*RebaseTransactionLock, error) {
	root, err := rebaseArtifactRoot(cfg, opts)
	if err != nil {
		return nil, err
	}
	return acquireRebaseTransactionLock(root, id, operation)
}

func loadRebaseLifecycle(cfg indexer.Config, id string, opts RebaseOptions) (string, RebaseTransaction, indexer.Source, indexer.Source, indexer.Source, error) {
	root, err := rebaseArtifactRoot(cfg, opts)
	if err != nil {
		return "", RebaseTransaction{}, indexer.Source{}, indexer.Source{}, indexer.Source{}, err
	}
	transaction, err := loadRebaseTransactionFromRoot(root, id)
	if err != nil {
		return root, transaction, indexer.Source{}, indexer.Source{}, indexer.Source{}, err
	}
	if err := validateRebaseProfile(transaction.Profile); err != nil {
		return root, transaction, indexer.Source{}, indexer.Source{}, indexer.Source{}, fmt.Errorf("stored rebase profile: %w", err)
	}
	project, base, target, err := resolveRebaseSources(cfg, transaction.Profile)
	if err != nil {
		return root, transaction, project, base, target, err
	}
	return root, transaction, project, base, target, nil
}

// rebaseInvalidateIndex marks the configured main index stale so no reader can
// keep treating it as a ready description of the project tree that is about to
// be replaced. It fails closed: when a database is configured but cannot be
// marked, the caller must not proceed with the directory swap.
//
// A workspace with no configured database has no published index to mislead
// anyone, and a configured database file that does not exist yet has never
// published a generation; both skip cleanly.
func rebaseInvalidateIndex(ctx context.Context, cfg indexer.Config, opts RebaseOptions, reason, requiredAction string) error {
	if opts.DB != nil {
		return opts.DB.MarkIndexStale(ctx, reason, requiredAction)
	}
	path, err := indexer.ConfiguredDatabasePath(cfg)
	if err != nil || strings.TrimSpace(path) == "" {
		return nil
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return nil
	} else if statErr != nil {
		return statErr
	}
	db, err := indexer.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.MarkIndexStale(ctx, reason, requiredAction)
}

func rebaseRequireNoConflicts(decisions []RebaseFileDecision) error {
	for _, decision := range decisions {
		if !decision.Safe || decision.Action == "conflict" {
			return fmt.Errorf("rebase transaction has an unresolved unsafe decision at %s", decision.Path)
		}
	}
	return nil
}

// rebaseVerifyInputs hashes every configured migration input again just before
// an action that can publish output or replace the formal project.
func rebaseVerifyInputs(cfg indexer.Config, transaction RebaseTransaction, project, base, target indexer.Source) ([]SnapshotFile, error) {
	if err := rebaseVerifySourceIdentityFingerprints(transaction, project, base, target); err != nil {
		return nil, err
	}
	projectFiles, _, err := collectFiles(project.Path)
	if err != nil {
		return nil, fmt.Errorf("project inventory: %w", err)
	}
	if inventoryFingerprint(projectFiles) != transaction.ProjectFingerprint {
		return nil, fmt.Errorf("project input changed after planning; create a new rebase plan")
	}
	if !validSHA256(transaction.ProjectTreeFingerprint) {
		return nil, fmt.Errorf("rebase transaction has no valid full-tree project fingerprint; create a new rebase plan")
	}
	projectTreeFiles, _, err := collectRebaseOverlayFiles(project.Path, newRebaseOverlayPolicy(transaction.Profile))
	if err != nil {
		return nil, fmt.Errorf("project full-tree inventory: %w", err)
	}
	if inventoryFingerprint(projectTreeFiles) != transaction.ProjectTreeFingerprint {
		return nil, fmt.Errorf("project full tree changed after planning; create a new rebase plan")
	}
	baseFiles, _, err := collectFiles(base.Path)
	if err != nil {
		return nil, fmt.Errorf("base inventory: %w", err)
	}
	if inventoryFingerprint(baseFiles) != transaction.BaseFingerprint {
		return nil, fmt.Errorf("base input changed after planning; create a new rebase plan")
	}
	targetFiles, _, err := collectFiles(target.Path)
	if err != nil {
		return nil, fmt.Errorf("target inventory: %w", err)
	}
	if inventoryFingerprint(targetFiles) != transaction.TargetFingerprint {
		return nil, fmt.Errorf("target input changed after planning; create a new rebase plan")
	}
	if err := rebaseVerifyValidationSourceFingerprints(cfg, transaction); err != nil {
		return nil, err
	}
	return projectTreeFiles, nil
}

func rebaseSourceIdentityFingerprints(project, base, target indexer.Source) (map[string]string, error) {
	sources := map[string]indexer.Source{"project": project, "base": base, "target": target}
	identities := make(map[string]string, len(sources))
	for label, source := range sources {
		identity, err := rebaseSourceRootIdentity(source)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		identities[label] = identity
	}
	return identities, nil
}

func rebaseSourceRootIdentity(source indexer.Source) (string, error) {
	if strings.TrimSpace(source.Name) == "" {
		return "", fmt.Errorf("source has no configured name")
	}
	resolved, err := resolveRebasePath(source.Path)
	if err != nil {
		return "", fmt.Errorf("source %q has an unsafe path: %w", source.Name, err)
	}
	canonical := filepath.ToSlash(filepath.Clean(resolved))
	// CK3 deployments primarily run on Windows, where a path case-only
	// spelling change must not make a safe transaction unrecoverable.
	if filepath.VolumeName(resolved) != "" {
		canonical = strings.ToLower(canonical)
	}
	return hashBytes([]byte(strings.ToLower(strings.TrimSpace(source.Name)) + "\x00" + canonical)), nil
}

func rebaseVerifySourceIdentityFingerprints(transaction RebaseTransaction, project, base, target indexer.Source) error {
	if len(transaction.SourceIdentityFingerprints) != 3 {
		return fmt.Errorf("rebase transaction has no complete source-root identity manifest; create a new rebase plan")
	}
	actual, err := rebaseSourceIdentityFingerprints(project, base, target)
	if err != nil {
		return err
	}
	for _, label := range []string{"project", "base", "target"} {
		if !validSHA256(transaction.SourceIdentityFingerprints[label]) || transaction.SourceIdentityFingerprints[label] != actual[label] {
			return fmt.Errorf("configured %s source root changed after planning; create a new rebase plan", label)
		}
	}
	return nil
}

// rebaseValidationSourceFingerprints snapshots the CK3 load-root inventory
// of each auxiliary validation source.  The target source is already pinned
// by TargetFingerprint; the mutable project and historical base may never be
// smuggled into validation_sources.
func rebaseValidationSourceFingerprints(cfg indexer.Config, profile RebaseProfile) (map[string]string, error) {
	fingerprints := make(map[string]string)
	seen := make(map[string]struct{})
	for _, configuredName := range rebaseProfileValidationSourceNames(profile) {
		name := strings.TrimSpace(configuredName)
		if name == "" {
			return nil, fmt.Errorf("validation source name is empty")
		}
		if strings.EqualFold(name, profile.Project) || strings.EqualFold(name, profile.Base) {
			return nil, fmt.Errorf("validation source %q may not be the mutable project or old base", name)
		}
		// The target is included automatically in every validation stack and is
		// independently re-hashed as a migration input.
		if strings.EqualFold(name, profile.Target) {
			continue
		}
		key := strings.ToLower(name)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate validation source %q", name)
		}
		seen[key] = struct{}{}
		source, err := sourceByName(cfg, name)
		if err != nil {
			return nil, err
		}
		sourcePath, err := resolveRebasePath(source.Path)
		if err != nil {
			return nil, fmt.Errorf("validation source %q has an unsafe path: %w", source.Name, err)
		}
		files, _, err := collectFiles(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("validation source %q inventory: %w", source.Name, err)
		}
		fingerprints[source.Name] = inventoryFingerprint(files)
	}
	return fingerprints, nil
}

func rebaseVerifyValidationSourceFingerprints(cfg indexer.Config, transaction RebaseTransaction) error {
	expected, err := rebaseValidationSourceFingerprints(cfg, transaction.Profile)
	if err != nil {
		return err
	}
	if len(expected) != len(transaction.ValidationSourceFingerprints) {
		return fmt.Errorf("validation source set changed after planning; create a new rebase plan")
	}
	for name, hash := range expected {
		if transaction.ValidationSourceFingerprints[name] != hash {
			return fmt.Errorf("validation source %q changed after planning; create a new rebase plan", name)
		}
	}
	return nil
}

func rebaseOutputDir(transaction RebaseTransaction, cfg indexer.Config) (string, error) {
	value := strings.TrimSpace(transaction.OutputDir)
	if value == "" {
		return "", fmt.Errorf("rebase transaction has no migration copy output directory")
	}
	output, err := resolveRebasePath(value)
	if err != nil {
		return "", err
	}
	if err := ensureStorageOutsideSources(output, cfg.Sources); err != nil {
		return "", err
	}
	return output, nil
}

func applyRebaseDecision(root, transactionID, overlayRoot string, decision RebaseFileDecision) error {
	rel, err := normalizeRel(decision.Path)
	if err != nil {
		return fmt.Errorf("invalid planned path %q: %w", decision.Path, err)
	}
	if rel != decision.Path {
		return fmt.Errorf("planned path is not normalized: %s", decision.Path)
	}
	if !decision.Safe || decision.Action == "conflict" {
		return fmt.Errorf("refusing unresolved decision at %s", decision.Path)
	}
	full := filepath.Join(overlayRoot, filepath.FromSlash(rel))
	if !pathContains(overlayRoot, full) {
		return fmt.Errorf("planned path escapes migration copy: %s", decision.Path)
	}
	switch decision.Action {
	case "keep_project":
		return verifyRebasePlannedFile(full, decision.Result, decision.Path)
	case "write_candidate":
		candidate, err := rebaseCandidateFile(root, transactionID, decision.CandidatePath)
		if err != nil {
			return fmt.Errorf("candidate for %s: %w", decision.Path, err)
		}
		if err := replaceRebaseOverlayFile(candidate, full); err != nil {
			return fmt.Errorf("write candidate %s: %w", decision.Path, err)
		}
		return verifyRebasePlannedFile(full, decision.Result, decision.Path)
	case "delete_project", "inherit_target":
		if err := removeRebaseOverlayFile(full); err != nil {
			return fmt.Errorf("remove project shadow %s: %w", decision.Path, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported rebase decision action %q at %s", decision.Action, decision.Path)
	}
}

func rebaseCandidateFile(root, id, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("candidate path is required")
	}
	rel, err := normalizeRel(value)
	if err != nil || (!strings.HasPrefix(filepath.ToSlash(rel), "candidates/") && !strings.HasPrefix(filepath.ToSlash(rel), "manual/")) {
		return "", fmt.Errorf("candidate path is invalid")
	}
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if !pathContains(dir, full) {
		return "", fmt.Errorf("candidate path escapes transaction")
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("candidate is not a regular file")
	}
	return full, nil
}

func replaceRebaseOverlayFile(source, target string) error {
	if err := removeRebaseOverlayFile(target); err != nil {
		return err
	}
	return copyFile(source, target)
}

func removeRebaseOverlayFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("overlay entry is not a regular file")
	}
	return os.Remove(path)
}

func verifyRebasePlannedFile(path string, expected RebaseFileState, rel string) error {
	if !expected.Exists || !validSHA256(expected.SHA256) {
		return fmt.Errorf("planned result metadata is invalid for %s", rel)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("planned result is not a regular file")
	}
	hash, size, err := hashPath(path)
	if err != nil {
		return err
	}
	if hash != expected.SHA256 || size != expected.Size {
		return fmt.Errorf("planned result hash does not match for %s", rel)
	}
	return nil
}

// verifyRebaseOverlay proves both halves of a built overlay: planned CK3 load
// files have the reviewed result hashes, and every project pass-through file
// that is not governed by a decision survived byte-for-byte.
func verifyRebaseOverlay(root string, decisions []RebaseFileDecision, projectTreeFiles []SnapshotFile, policy rebaseOverlayPolicy) (string, error) {
	files, _, err := collectRebaseOverlayFiles(root, policy)
	if err != nil {
		return "", err
	}
	actual := fileMap(files)
	passThrough := fileMap(projectTreeFiles)
	expected := map[string]RebaseFileDecision{}
	for _, decision := range decisions {
		key := strings.ToLower(decision.Path)
		if _, exists := expected[key]; exists {
			return "", fmt.Errorf("duplicate planned path %s", decision.Path)
		}
		expected[key] = decision
		switch decision.Action {
		case "keep_project", "write_candidate":
			file := actual[key]
			if file == nil || !decision.Result.Exists || file.SHA256 != decision.Result.SHA256 || file.Size != decision.Result.Size {
				return "", fmt.Errorf("migration overlay result mismatch for %s", decision.Path)
			}
		case "delete_project", "inherit_target":
			if actual[key] != nil {
				return "", fmt.Errorf("migration overlay retained a deleted shadow at %s", decision.Path)
			}
		default:
			return "", fmt.Errorf("unsupported planned action %q", decision.Action)
		}
	}
	for _, source := range projectTreeFiles {
		key := strings.ToLower(source.Path)
		if _, planned := expected[key]; planned {
			continue
		}
		file := actual[key]
		if file == nil || file.SHA256 != source.SHA256 || file.Size != source.Size {
			return "", fmt.Errorf("migration overlay pass-through file changed or is missing: %s", source.Path)
		}
	}
	for key, file := range actual {
		if _, ok := expected[key]; ok {
			continue
		}
		passThroughFile := passThrough[key]
		if passThroughFile == nil {
			return "", fmt.Errorf("migration overlay contains an unplanned file: %s", file.Path)
		}
		if file.SHA256 != passThroughFile.SHA256 || file.Size != passThroughFile.Size {
			return "", fmt.Errorf("migration overlay pass-through hash mismatch: %s", file.Path)
		}
	}
	return inventoryFingerprint(files), nil
}

// rebaseCheckpoint records per-file progress. It deliberately does not rewrite
// the manifest: an interrupted build leaves no published output (the copy is
// staged in a temporary directory and removed), so per-file manifest durability
// bought nothing while making the build quadratic in the file count. Both the
// progress projection and the HTML report are refreshed at bounded intervals.
func rebaseCheckpoint(root string, transaction *RebaseTransaction, completed, total int) error {
	if err := checkpointRebaseProgress(root, *transaction, completed, total); err != nil {
		return err
	}
	if completed%100 != 0 && completed != total {
		return nil
	}
	return writeRebaseReport(root, *transaction)
}

func persistRebaseLifecycle(root string, transaction *RebaseTransaction) error {
	if err := writeRebaseTransaction(root, transaction); err != nil {
		return err
	}
	return writeRebaseReport(root, *transaction)
}

// persistRebaseDecisions republishes the decision journal. Only the stages
// that actually rewrite decisions (planning, and applying review resolutions
// before a build) call it, so the O(files) journal write happens a fixed number
// of times per transaction instead of once per file.
func persistRebaseDecisions(root string, transaction *RebaseTransaction) error {
	return writeRebaseDecisions(root, transaction.ID, transaction.Files)
}

func rebaseLifecycleFailure(root string, transaction RebaseTransaction, phase string, cause error) (RebaseTransaction, error) {
	transaction.Status = RebaseStatusFailed
	transaction.Progress.Phase = phase + "_failed"
	transaction.Progress.Message = cause.Error()
	transaction.Progress.CurrentPath = ""
	if err := persistRebaseLifecycle(root, &transaction); err != nil {
		return transaction, fmt.Errorf("%v; persist failure: %w", cause, err)
	}
	return transaction, cause
}

type rebaseStackValidation struct {
	Full        indexer.ValidationReport
	Project     indexer.ValidationReport
	Upstream    indexer.ValidationReport
	MapAudit    indexer.MapAssetAuditResult
	MapErrors   int
	MapWarnings int
}

// rebaseValidationStackConfig assembles the load order a validation scan uses.
//
// The migration overlay is always the highest-priority source. Everything else
// is placed relative to the new upstream by the profile: validation_sources is
// the shorthand for "these all load below the target", while validation_stack
// states the position of each source explicitly. Silently forcing every
// auxiliary source below the target would misreport any playset where a
// dependency actually overrides it.
func rebaseValidationStackConfig(baseCfg indexer.Config, profile RebaseProfile, overlayPath string, upstream indexer.Source, databasePath, overlayName string) (indexer.Config, error) {
	if strings.TrimSpace(overlayName) == "" || strings.EqualFold(overlayName, upstream.Name) {
		return indexer.Config{}, fmt.Errorf("invalid validation overlay source name")
	}
	entries := profile.ValidationStack
	if len(entries) == 0 {
		for _, sourceName := range profile.ValidationSources {
			entries = append(entries, RebaseValidationStackEntry{Source: sourceName, Position: RebaseValidationBelowTarget})
		}
	}
	used := map[string]bool{strings.ToLower(overlayName): true, strings.ToLower(upstream.Name): true}
	resolve := func(position string) ([]indexer.Source, error) {
		var out []indexer.Source
		for _, entry := range entries {
			if !strings.EqualFold(strings.TrimSpace(entry.Position), position) {
				continue
			}
			sourceName := entry.Source
			if strings.EqualFold(sourceName, profile.Project) || strings.EqualFold(sourceName, profile.Base) {
				return nil, fmt.Errorf("validation sources may not include the mutable project or old base source")
			}
			if strings.EqualFold(sourceName, upstream.Name) {
				continue
			}
			source, err := sourceByName(baseCfg, sourceName)
			if err != nil {
				return nil, err
			}
			if used[strings.ToLower(source.Name)] {
				return nil, fmt.Errorf("duplicate validation source %q", source.Name)
			}
			used[strings.ToLower(source.Name)] = true
			if source.Role == indexer.SourceRoleProject {
				source.Role = indexer.SourceRoleDependency
			}
			out = append(out, source)
		}
		return out, nil
	}
	above, err := resolve(RebaseValidationAboveTarget)
	if err != nil {
		return indexer.Config{}, err
	}
	below, err := resolve(RebaseValidationBelowTarget)
	if err != nil {
		return indexer.Config{}, err
	}

	// Lower rank wins in CK3 override precedence, so the stack is emitted in
	// priority order and ranks are assigned from the top down.
	sources := []indexer.Source{{Name: overlayName, Path: overlayPath, Rank: 0, Role: indexer.SourceRoleProject, Private: true}}
	sources = append(sources, above...)
	sources = append(sources, indexer.Source{Name: upstream.Name, Path: upstream.Path, Role: indexer.SourceRoleDependency, Private: upstream.Private, ResourceOnly: upstream.ResourceOnly})
	sources = append(sources, below...)
	for index := range sources {
		sources[index].Rank = index + 1
	}
	return indexer.Config{
		ConfigPath:   filepath.Join(filepath.Dir(databasePath), "ck3-index.toml"),
		Database:     databasePath,
		EngineLogs:   baseCfg.EngineLogs,
		ArtifactRoot: baseCfg.ArtifactRoot,
		Sources:      sources,
		ForceClean:   true,
	}, nil
}

func runRebaseValidationStack(ctx context.Context, cfg indexer.Config, projectName, upstreamName string) (rebaseStackValidation, error) {
	if _, err := indexer.Scan(ctx, cfg); err != nil {
		return rebaseStackValidation{}, err
	}
	database, err := indexer.ConfiguredDatabasePath(cfg)
	if err != nil {
		return rebaseStackValidation{}, err
	}
	db, err := indexer.Open(database)
	if err != nil {
		return rebaseStackValidation{}, err
	}
	defer db.Close()
	full, err := db.Validate(ctx)
	if err != nil {
		return rebaseStackValidation{}, err
	}
	project, err := db.CachedValidationForSource(ctx, projectName)
	if err != nil {
		return rebaseStackValidation{}, err
	}
	upstream, err := db.CachedValidationForSource(ctx, upstreamName)
	if err != nil {
		return rebaseStackValidation{}, err
	}
	audit, err := indexer.AuditMapAssets(ctx, cfg, "summary", 20)
	if err != nil {
		return rebaseStackValidation{}, err
	}
	return rebaseStackValidation{Full: full, Project: project, Upstream: upstream, MapAudit: audit, MapErrors: audit.Counts["error"], MapWarnings: audit.Counts["warning"]}, nil
}

// rebaseNewDiagnostics compares stable validation fingerprints rather than raw
// counts, so a disappearing baseline error cannot mask a new migration error.
func rebaseNewDiagnostics(baseline, migrated indexer.ValidationReport, severity string) []indexer.Diagnostic {
	known := make(map[string]int, len(baseline.Diagnostics))
	for _, diagnostic := range baseline.Diagnostics {
		if diagnostic.Severity == severity && diagnostic.Fingerprint != "" {
			known[diagnostic.Fingerprint] += rebaseDiagnosticOccurrences(diagnostic)
		}
	}
	var added []indexer.Diagnostic
	for _, diagnostic := range migrated.Diagnostics {
		if diagnostic.Severity != severity {
			continue
		}
		if diagnostic.Fingerprint == "" {
			added = append(added, diagnostic)
			continue
		}
		occurrences := rebaseDiagnosticOccurrences(diagnostic)
		if remaining := known[diagnostic.Fingerprint]; remaining >= occurrences {
			known[diagnostic.Fingerprint] = remaining - occurrences
			continue
		} else if remaining > 0 {
			diagnostic.Occurrences = occurrences - remaining
			known[diagnostic.Fingerprint] = 0
			added = append(added, diagnostic)
			continue
		}
		diagnostic.Occurrences = occurrences
		added = append(added, diagnostic)
	}
	return added
}

// rebaseNewMigrationStackDiagnostics gates diagnostics which appear only
// after the migration.  A target-only scan distinguishes an upstream problem
// from a problem introduced by replaying the project overlay; the old stack
// additionally preserves a stable pre-migration fingerprint as a baseline.
// Raw count deltas are intentionally never used here because one disappearing
// warning must not hide a new error.
func rebaseNewMigrationStackDiagnostics(oldStack, targetOnly, migrated indexer.ValidationReport) []indexer.Diagnostic {
	// Do not sum same-fingerprint occurrences across the old and target-only
	// stacks. They are separate baselines and can report the same inherited
	// upstream problem; a maximum is conservative and prevents a new overlay
	// reference from being hidden merely because both baselines saw it once.
	known := make(map[string]int, len(oldStack.Diagnostics)+len(targetOnly.Diagnostics))
	for _, report := range []indexer.ValidationReport{oldStack, targetOnly} {
		perReport := make(map[string]int, len(report.Diagnostics))
		for _, diagnostic := range report.Diagnostics {
			if rebaseMigrationGateDiagnostic(diagnostic) && diagnostic.Fingerprint != "" {
				perReport[diagnostic.Fingerprint] += rebaseDiagnosticOccurrences(diagnostic)
			}
		}
		for fingerprint, occurrences := range perReport {
			if occurrences > known[fingerprint] {
				known[fingerprint] = occurrences
			}
		}
	}
	var added []indexer.Diagnostic
	for _, diagnostic := range migrated.Diagnostics {
		if !rebaseMigrationGateDiagnostic(diagnostic) {
			continue
		}
		if diagnostic.Fingerprint == "" {
			// A gate diagnostic without a stable identity is unsafe to inherit.
			added = append(added, diagnostic)
			continue
		}
		occurrences := rebaseDiagnosticOccurrences(diagnostic)
		if remaining := known[diagnostic.Fingerprint]; remaining >= occurrences {
			known[diagnostic.Fingerprint] = remaining - occurrences
			continue
		} else if remaining > 0 {
			diagnostic.Occurrences = occurrences - remaining
			known[diagnostic.Fingerprint] = 0
			added = append(added, diagnostic)
			continue
		}
		diagnostic.Occurrences = occurrences
		added = append(added, diagnostic)
	}
	return added
}

func rebaseDiagnosticOccurrences(diagnostic indexer.Diagnostic) int {
	if diagnostic.Occurrences > 0 {
		return diagnostic.Occurrences
	}
	return 1
}

func rebaseMigrationGateDiagnostic(diagnostic indexer.Diagnostic) bool {
	if diagnostic.Severity == "error" {
		return true
	}
	switch diagnostic.Code {
	case "missing_localization", "missing_resource", "missing_object_reference", "scope_mismatch":
		return true
	}
	code := strings.ToLower(diagnostic.Code)
	return diagnostic.Confidence == "high" &&
		(strings.Contains(code, "reference") || strings.Contains(code, "parse"))
}

// rebaseNewMapAuditFindings accepts an inherited map finding only when the
// finding is identical and the active file which caused it is byte-identical.
// This keeps an old warning from becoming a blanket exemption after map work.
func rebaseNewMapAuditFindings(baseline, migrated indexer.MapAssetAuditResult, baselineCfg, migratedCfg indexer.Config) ([]indexer.MapAssetAuditFinding, error) {
	return rebaseNewMapAuditFindingsFromBaselines(migrated, migratedCfg, []rebaseMapAuditBaseline{{Audit: baseline, Config: baselineCfg}})
}

// rebaseNewMapAuditFindingsWithTarget accepts a map finding only if it is
// present against an unchanged active asset in either the old full stack or
// the target-only stack. The latter is essential: a defect already present in
// Theirs must not be attributed to replaying Ours into that same target.
func rebaseNewMapAuditFindingsWithTarget(baseline, targetOnly, migrated indexer.MapAssetAuditResult, baselineCfg, targetOnlyCfg, migratedCfg indexer.Config) ([]indexer.MapAssetAuditFinding, error) {
	return rebaseNewMapAuditFindingsFromBaselines(migrated, migratedCfg, []rebaseMapAuditBaseline{
		{Audit: baseline, Config: baselineCfg},
		{Audit: targetOnly, Config: targetOnlyCfg},
	})
}

type rebaseMapAuditBaseline struct {
	Audit  indexer.MapAssetAuditResult
	Config indexer.Config
}

func rebaseNewMapAuditFindingsFromBaselines(migrated indexer.MapAssetAuditResult, migratedCfg indexer.Config, baselines []rebaseMapAuditBaseline) ([]indexer.MapAssetAuditFinding, error) {
	type knownFinding struct {
		hash string
	}
	known := make(map[string][]knownFinding)
	for baselineIndex, baseline := range baselines {
		for _, finding := range baseline.Audit.Findings {
			hash, err := rebaseMapAuditFindingHash(baseline.Config, finding)
			if err != nil {
				return nil, fmt.Errorf("baseline %d %s: %w", baselineIndex+1, rebaseMapAuditFindingKey(finding), err)
			}
			key := rebaseMapAuditFindingKey(finding)
			known[key] = append(known[key], knownFinding{hash: hash})
		}
	}
	var added []indexer.MapAssetAuditFinding
	for _, finding := range migrated.Findings {
		hash, err := rebaseMapAuditFindingHash(migratedCfg, finding)
		if err != nil {
			return nil, fmt.Errorf("migration copy %s: %w", rebaseMapAuditFindingKey(finding), err)
		}
		inherited := false
		for _, prior := range known[rebaseMapAuditFindingKey(finding)] {
			if prior.hash == hash {
				inherited = true
				break
			}
		}
		if !inherited {
			added = append(added, finding)
		}
	}
	return added, nil
}

func rebaseMapAuditFindingKey(finding indexer.MapAssetAuditFinding) string {
	return strings.Join([]string{
		finding.Code,
		finding.Severity,
		filepath.ToSlash(finding.Path),
		finding.Message,
		fmt.Sprintf("%d", finding.Count),
		strings.Join(finding.Samples, "\x00"),
	}, "\x1f")
}

func rebaseMapAuditFindingHash(cfg indexer.Config, finding indexer.MapAssetAuditFinding) (string, error) {
	if strings.TrimSpace(finding.Source) == "" || strings.TrimSpace(finding.Path) == "" {
		return "", fmt.Errorf("finding has no source-backed path")
	}
	source, err := sourceByName(cfg, finding.Source)
	if err != nil {
		return "", err
	}
	data, err := readSourceFile(source.Path, finding.Path)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func cloneRebaseCounts(values map[string]int) map[string]int {
	out := map[string]int{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func rebaseAddPrefixedCounts(out map[string]int, prefix string, values map[string]int) {
	for key, value := range values {
		out[prefix+key] = value
	}
}

func rebaseDirectoryFingerprint(root string, policy rebaseOverlayPolicy) (string, error) {
	if err := rebaseRequireRegularDirectory(root); err != nil {
		return "", err
	}
	files, _, err := collectRebaseOverlayFiles(root, policy)
	if err != nil {
		return "", err
	}
	return inventoryFingerprint(files), nil
}

func rebaseRequireRegularDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("must be a regular directory")
	}
	return nil
}

func rebaseBackupPath(projectPath, id string) (string, error) {
	if err := validateRebaseTransactionID(id); err != nil {
		return "", err
	}
	projectPath = filepath.Clean(projectPath)
	parent := filepath.Dir(projectPath)
	base := filepath.Base(projectPath)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", fmt.Errorf("formal project path has no safe base name")
	}
	backup := filepath.Join(parent, base+".ck3-rebase-backup-"+id)
	if filepath.Dir(backup) != parent || !pathContains(parent, backup) {
		return "", fmt.Errorf("promotion backup path escapes formal project parent")
	}
	return backup, nil
}
