package migrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ck3-index/internal/indexer"
)

const (
	rebasePromotionIntentStage       = "promote_intent"
	rebasePromotionBackupMovedStage  = "backup_moved"
	rebasePromotionCompleteStage     = "promoted"
	rebaseRollbackIntentStage        = "rollback_intent"
	rebaseRollbackPromotedMovedStage = "promoted_moved"
	rebaseRollbackCompleteStage      = "rolled_back"
)

type rebaseDirectoryState struct {
	Exists bool
	SHA256 string
}

func rebaseDirectoryStateAt(path string) (rebaseDirectoryState, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return rebaseDirectoryState{}, nil
	} else if err != nil {
		return rebaseDirectoryState{}, err
	}
	hash, err := rebaseDirectoryFingerprint(path)
	if err != nil {
		return rebaseDirectoryState{}, err
	}
	return rebaseDirectoryState{Exists: true, SHA256: hash}, nil
}

func rebaseVerifyUpstreamFingerprints(cfg indexer.Config, transaction RebaseTransaction, base, target indexer.Source) error {
	if len(transaction.SourceIdentityFingerprints) != 3 ||
		!validSHA256(transaction.SourceIdentityFingerprints["base"]) ||
		!validSHA256(transaction.SourceIdentityFingerprints["target"]) {
		return fmt.Errorf("rebase transaction has no complete source-root identity manifest; create a new rebase plan")
	}
	baseIdentity, err := rebaseSourceRootIdentity(base)
	if err != nil {
		return err
	}
	targetIdentity, err := rebaseSourceRootIdentity(target)
	if err != nil {
		return err
	}
	if transaction.SourceIdentityFingerprints["base"] != baseIdentity || transaction.SourceIdentityFingerprints["target"] != targetIdentity {
		return fmt.Errorf("configured base or target source root changed after planning; create a new rebase plan")
	}
	baseFiles, _, err := collectFiles(base.Path)
	if err != nil {
		return fmt.Errorf("base inventory: %w", err)
	}
	if inventoryFingerprint(baseFiles) != transaction.BaseFingerprint {
		return fmt.Errorf("base input changed after planning; create a new rebase plan")
	}
	targetFiles, _, err := collectFiles(target.Path)
	if err != nil {
		return fmt.Errorf("target inventory: %w", err)
	}
	if inventoryFingerprint(targetFiles) != transaction.TargetFingerprint {
		return fmt.Errorf("target input changed after planning; create a new rebase plan")
	}
	return rebaseVerifyValidationSourceFingerprints(cfg, transaction)
}

func rebaseValidatePromotionReceipt(transaction RebaseTransaction, projectPath, backupPath string) error {
	if transaction.Promotion == nil {
		return fmt.Errorf("rebase transaction has no durable promotion intent")
	}
	receipt := transaction.Promotion
	if filepath.Clean(receipt.FormalPath) != filepath.Clean(projectPath) || filepath.Clean(receipt.BackupPath) != filepath.Clean(backupPath) {
		return fmt.Errorf("promotion intent paths do not match the configured formal project")
	}
	if !validSHA256(receipt.PreviousSHA256) || !validSHA256(receipt.PromotedSHA256) {
		return fmt.Errorf("promotion intent hashes are invalid")
	}
	return nil
}

func rebasePromotionLayout(projectPath, backupPath, outputPath string, receipt *RebasePromotionReceipt) (string, error) {
	project, err := rebaseDirectoryStateAt(projectPath)
	if err != nil {
		return "", fmt.Errorf("formal project state: %w", err)
	}
	backup, err := rebaseDirectoryStateAt(backupPath)
	if err != nil {
		return "", fmt.Errorf("promotion backup state: %w", err)
	}
	output, err := rebaseDirectoryStateAt(outputPath)
	if err != nil {
		return "", fmt.Errorf("migration copy state: %w", err)
	}
	previous, promoted := receipt.PreviousSHA256, receipt.PromotedSHA256
	switch {
	case project.Exists && project.SHA256 == previous && !backup.Exists && output.Exists && output.SHA256 == promoted:
		return rebasePromotionIntentStage, nil
	case !project.Exists && backup.Exists && backup.SHA256 == previous && output.Exists && output.SHA256 == promoted:
		return rebasePromotionBackupMovedStage, nil
	case project.Exists && project.SHA256 == promoted && backup.Exists && backup.SHA256 == previous && !output.Exists:
		return rebasePromotionCompleteStage, nil
	default:
		return "", fmt.Errorf("promotion intent directory layout is unsafe (project=%t:%s backup=%t:%s output=%t:%s)", project.Exists, project.SHA256, backup.Exists, backup.SHA256, output.Exists, output.SHA256)
	}
}

func rebaseRollbackLayout(projectPath, backupPath, outputPath string, receipt *RebasePromotionReceipt) (string, error) {
	project, err := rebaseDirectoryStateAt(projectPath)
	if err != nil {
		return "", fmt.Errorf("formal project state: %w", err)
	}
	backup, err := rebaseDirectoryStateAt(backupPath)
	if err != nil {
		return "", fmt.Errorf("promotion backup state: %w", err)
	}
	output, err := rebaseDirectoryStateAt(outputPath)
	if err != nil {
		return "", fmt.Errorf("migration copy state: %w", err)
	}
	previous, promoted := receipt.PreviousSHA256, receipt.PromotedSHA256
	switch {
	case project.Exists && project.SHA256 == promoted && backup.Exists && backup.SHA256 == previous && !output.Exists:
		return rebaseRollbackIntentStage, nil
	case !project.Exists && backup.Exists && backup.SHA256 == previous && output.Exists && output.SHA256 == promoted:
		return rebaseRollbackPromotedMovedStage, nil
	case project.Exists && project.SHA256 == previous && !backup.Exists && output.Exists && output.SHA256 == promoted:
		return rebaseRollbackCompleteStage, nil
	default:
		return "", fmt.Errorf("rollback intent directory layout is unsafe (project=%t:%s backup=%t:%s output=%t:%s)", project.Exists, project.SHA256, backup.Exists, backup.SHA256, output.Exists, output.SHA256)
	}
}

// resumeRebasePromotion makes a persisted promotion intent converge from any
// of the three safe filesystem layouts.  A process crash between two renames
// therefore leaves a reviewable, resumable transaction rather than a missing
// formal project path.
func resumeRebasePromotion(ctx context.Context, cfg indexer.Config, root string, transaction *RebaseTransaction, projectPath, backupPath, outputPath string, base, target indexer.Source) (RebaseTransaction, error) {
	if transaction == nil {
		return RebaseTransaction{}, fmt.Errorf("rebase transaction is nil")
	}
	if err := ctx.Err(); err != nil {
		return *transaction, err
	}
	if err := rebaseVerifyUpstreamFingerprints(cfg, *transaction, base, target); err != nil {
		return *transaction, err
	}
	if err := rebaseValidatePromotionReceipt(*transaction, projectPath, backupPath); err != nil {
		return *transaction, err
	}
	for {
		layout, err := rebasePromotionLayout(projectPath, backupPath, outputPath, transaction.Promotion)
		if err != nil {
			return *transaction, err
		}
		switch layout {
		case rebasePromotionIntentStage:
			if err := os.Rename(projectPath, backupPath); err != nil {
				return *transaction, fmt.Errorf("backup formal project: %w", err)
			}
			transaction.Promotion.Stage = rebasePromotionBackupMovedStage
			transaction.Progress = RebaseProgress{Phase: "promote", Completed: 1, Total: 2, CurrentPath: filepath.Base(backupPath), Message: "formal project moved to recoverable backup"}
			if err := persistRebaseLifecycle(root, transaction); err != nil {
				if restoreErr := os.Rename(backupPath, projectPath); restoreErr != nil {
					return *transaction, fmt.Errorf("persist backup-moved checkpoint: %v; recovery required because formal project restore failed: %w", err, restoreErr)
				}
				return *transaction, fmt.Errorf("persist backup-moved checkpoint: %w; formal project restored", err)
			}
		case rebasePromotionBackupMovedStage:
			if err := os.Rename(outputPath, projectPath); err != nil {
				return *transaction, fmt.Errorf("promote migration copy: %w", err)
			}
			transaction.Promotion.Stage = rebasePromotionCompleteStage
			transaction.Promotion.PromotedAt = time.Now().UTC().Format(time.RFC3339Nano)
			transaction.Status = RebaseStatusPromoted
			transaction.Progress = RebaseProgress{Phase: "promoted", Completed: 2, Total: 2, Message: "migration copy promoted; previous formal project retained as sibling backup"}
			if err := persistRebaseLifecycle(root, transaction); err != nil {
				if restoreErr := rebaseRestorePromoteStart(projectPath, backupPath, outputPath); restoreErr != nil {
					return *transaction, fmt.Errorf("persist promoted checkpoint: %v; recovery required because reverse promotion failed: %w", err, restoreErr)
				}
				return *transaction, fmt.Errorf("persist promoted checkpoint: %w; promotion was reversed", err)
			}
			return *transaction, nil
		case rebasePromotionCompleteStage:
			transaction.Promotion.Stage = rebasePromotionCompleteStage
			if transaction.Promotion.PromotedAt == "" {
				transaction.Promotion.PromotedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			transaction.Status = RebaseStatusPromoted
			transaction.Progress = RebaseProgress{Phase: "promoted", Completed: 2, Total: 2, Message: "migration copy promotion recovered and finalized"}
			if err := persistRebaseLifecycle(root, transaction); err != nil {
				return *transaction, fmt.Errorf("persist recovered promotion receipt: %w", err)
			}
			return *transaction, nil
		}
	}
}

func rebaseRestorePromoteStart(projectPath, backupPath, outputPath string) error {
	if err := os.Rename(projectPath, outputPath); err != nil {
		return fmt.Errorf("move promoted project back to migration copy: %w", err)
	}
	if err := os.Rename(backupPath, projectPath); err != nil {
		if restoreErr := os.Rename(outputPath, projectPath); restoreErr != nil {
			return fmt.Errorf("restore formal project: %v; restore promoted project: %w", err, restoreErr)
		}
		return fmt.Errorf("restore formal project: %w", err)
	}
	return nil
}

func resumeRebaseRollback(ctx context.Context, root string, transaction *RebaseTransaction, projectPath, backupPath, outputPath string) (RebaseTransaction, error) {
	if transaction == nil {
		return RebaseTransaction{}, fmt.Errorf("rebase transaction is nil")
	}
	if err := ctx.Err(); err != nil {
		return *transaction, err
	}
	if err := rebaseValidatePromotionReceipt(*transaction, projectPath, backupPath); err != nil {
		return *transaction, err
	}
	for {
		layout, err := rebaseRollbackLayout(projectPath, backupPath, outputPath, transaction.Promotion)
		if err != nil {
			return *transaction, err
		}
		switch layout {
		case rebaseRollbackIntentStage:
			if err := os.Rename(projectPath, outputPath); err != nil {
				return *transaction, fmt.Errorf("move promoted project aside: %w", err)
			}
			transaction.Promotion.Stage = rebaseRollbackPromotedMovedStage
			transaction.Progress = RebaseProgress{Phase: "rollback", Completed: 1, Total: 2, CurrentPath: filepath.Base(outputPath), Message: "promoted project moved to migration-copy recovery path"}
			if err := persistRebaseLifecycle(root, transaction); err != nil {
				if restoreErr := os.Rename(outputPath, projectPath); restoreErr != nil {
					return *transaction, fmt.Errorf("persist rollback checkpoint: %v; recovery required because promoted project restore failed: %w", err, restoreErr)
				}
				return *transaction, fmt.Errorf("persist rollback checkpoint: %w; promoted project restored", err)
			}
		case rebaseRollbackPromotedMovedStage:
			if err := os.Rename(backupPath, projectPath); err != nil {
				return *transaction, fmt.Errorf("restore formal project: %w", err)
			}
			transaction.Promotion.Stage = rebaseRollbackCompleteStage
			transaction.Promotion.RolledBackAt = time.Now().UTC().Format(time.RFC3339Nano)
			transaction.Status = RebaseStatusRolledBack
			transaction.Progress = RebaseProgress{Phase: "rolled_back", Completed: 2, Total: 2, Message: "formal project restored; promoted copy retained at its migration-copy path"}
			if err := persistRebaseLifecycle(root, transaction); err != nil {
				if restoreErr := rebaseRestoreRollbackStart(projectPath, backupPath, outputPath); restoreErr != nil {
					return *transaction, fmt.Errorf("persist rolled-back checkpoint: %v; recovery required because reverse rollback failed: %w", err, restoreErr)
				}
				return *transaction, fmt.Errorf("persist rolled-back checkpoint: %w; rollback was reversed", err)
			}
			return *transaction, nil
		case rebaseRollbackCompleteStage:
			transaction.Promotion.Stage = rebaseRollbackCompleteStage
			if transaction.Promotion.RolledBackAt == "" {
				transaction.Promotion.RolledBackAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			transaction.Status = RebaseStatusRolledBack
			transaction.Progress = RebaseProgress{Phase: "rolled_back", Completed: 2, Total: 2, Message: "rollback recovered and finalized"}
			if err := persistRebaseLifecycle(root, transaction); err != nil {
				return *transaction, fmt.Errorf("persist recovered rollback receipt: %w", err)
			}
			return *transaction, nil
		}
	}
}

func rebaseRestoreRollbackStart(projectPath, backupPath, outputPath string) error {
	if err := os.Rename(projectPath, backupPath); err != nil {
		return fmt.Errorf("move restored project back to backup: %w", err)
	}
	if err := os.Rename(outputPath, projectPath); err != nil {
		if restoreErr := os.Rename(backupPath, projectPath); restoreErr != nil {
			return fmt.Errorf("restore promoted project: %v; restore original project: %w", err, restoreErr)
		}
		return fmt.Errorf("restore promoted project: %w", err)
	}
	return nil
}
