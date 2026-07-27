package migrator

import (
	"context"
	"fmt"
	"strings"

	"ck3-index/internal/indexer"
)

// ResumeRebase continues only a previously checkpointed stage. It never
// guesses a next lifecycle action: a normal ready transaction still requires
// the explicit build/validate/promote/rollback command chosen by the user.
func ResumeRebase(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions) (RebaseTransaction, error) {
	transaction, err := LoadRebaseTransaction(cfg, id, opts)
	if err != nil {
		return transaction, err
	}
	switch {
	case transaction.Status == RebaseStatusFailed && rebasePlanningPhase(transaction.Progress.Phase):
		if strings.TrimSpace(transaction.PlanProfilePath) == "" || strings.TrimSpace(transaction.OutputDir) == "" {
			return transaction, fmt.Errorf("rebase transaction %s cannot restart planning because its saved plan inputs are incomplete", id)
		}
		// Planning is read-only with respect to project/base/target, and a
		// failed pass publishes no migration copy. Restart under a fresh ID so
		// the immutable failed manifest remains useful forensic evidence while
		// every new input hash and classification is recomputed from scratch.
		return PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: transaction.PlanProfilePath, OutputDir: transaction.OutputDir}, opts)
	case transaction.Status == RebaseStatusFailed && strings.HasPrefix(transaction.Progress.Phase, "build_failed"):
		return BuildRebase(ctx, cfg, id, opts)
	case transaction.Status == RebaseStatusFailed && strings.HasPrefix(transaction.Progress.Phase, "validate_failed"):
		return ValidateRebase(ctx, cfg, id, opts)
	case transaction.Status == RebaseStatusPromoting:
		return PromoteRebase(ctx, cfg, id, opts)
	case transaction.Status == RebaseStatusRollingBack:
		return RollbackRebase(ctx, cfg, id, opts)
	default:
		return transaction, fmt.Errorf("rebase transaction %s has no resumable interrupted stage (status=%s phase=%s)", id, transaction.Status, transaction.Progress.Phase)
	}
}

func rebasePlanningPhase(phase string) bool {
	switch strings.TrimSpace(phase) {
	case "inventory", "classify", "planning", "plan_failed":
		return true
	default:
		return false
	}
}
