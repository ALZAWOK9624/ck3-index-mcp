package migrator

import (
	"time"

	"ck3-index/internal/indexer"
)

const (
	// RebaseSchemaVersion is deliberately independent from the legacy map
	// snapshot schema. Rebase transactions describe a project overlay rather
	// than a complete target-upstream fork.
	//
	// Version 3 splits the per-file decision list out of the manifest into a
	// separate journal and adds the monotonic Revision used for compare-and-set
	// writes, so an older manifest cannot be loaded as if it were current.
	RebaseSchemaVersion = 3

	// RebaseProfileSchemaVersion versions the user-authored migration profile.
	// It is deliberately independent of the transaction storage layout: moving
	// decisions into their own journal changed how a transaction is persisted,
	// not what a project's migration policy looks like.
	RebaseProfileSchemaVersion = 2

	RebaseStatusPlanning     = "planning"
	RebaseStatusNeedsReview  = "needs_review"
	RebaseStatusReadyToBuild = "ready_to_build"
	RebaseStatusBuilt        = "built"
	RebaseStatusValidated    = "validated"
	RebaseStatusPromoting    = "promoting"
	RebaseStatusPromoted     = "promoted"
	RebaseStatusRollingBack  = "rolling_back"
	RebaseStatusRolledBack   = "rolled_back"
	RebaseStatusFailed       = "failed"
)

// RebaseProfile is the persistent, project-specific policy used for every
// upstream migration. Source values are ck3-index configured source names;
// the profile never embeds machine-specific source paths in transaction
// reports.
type RebaseProfile struct {
	SchemaVersion     int      `toml:"schema_version" json:"schema_version"`
	Name              string   `toml:"name" json:"name"`
	Project           string   `toml:"project" json:"project"`
	Base              string   `toml:"base" json:"base"`
	Target            string   `toml:"target" json:"target"`
	MigrationMode     string   `toml:"migration_mode" json:"migration_mode"`
	BaseGameVersion   string   `toml:"base_game_version" json:"base_game_version"`
	TargetGameVersion string   `toml:"target_game_version" json:"target_game_version"`
	MapAuthority      string   `toml:"map_authority" json:"map_authority"`
	UnknownPolicy     string   `toml:"unknown_policy" json:"unknown_policy"`
	OwnedPrefixes     []string `toml:"owned_prefixes" json:"owned_prefixes,omitempty"`
	ValidationSources []string `toml:"validation_sources" json:"validation_sources,omitempty"`
	// ValidationStack describes the real ordered load stack when a dependency
	// does not sit below the new upstream. validation_sources is the shorthand
	// for the common case and places every named source below the target; a
	// dependency that actually overrides the target in the playset must be
	// declared here instead, or validation would measure a load order the game
	// never uses.
	ValidationStack []RebaseValidationStackEntry `toml:"validation_stack" json:"validation_stack,omitempty"`
	// PreservePaths names top-level project entries that are external state
	// rather than Mod content: VCS metadata, caches, and logs. They are left
	// out of the overlay inventory, the migration copy, and every transaction
	// fingerprint, and they stay with the project directory across promotion
	// and rollback instead of being copied. An omitted key preserves nothing,
	// which keeps existing profiles behaving exactly as before.
	PreservePaths []string `toml:"preserve_paths" json:"preserve_paths,omitempty"`
}

// RebaseValidationStackEntry places one auxiliary source relative to the new
// upstream in the validation load order.
type RebaseValidationStackEntry struct {
	Source string `toml:"source" json:"source"`
	// Position is "above_target" (the source overrides the new upstream) or
	// "below_target" (the new upstream overrides the source).
	Position string `toml:"position" json:"position"`
}

const (
	RebaseValidationAboveTarget = "above_target"
	RebaseValidationBelowTarget = "below_target"
)

// RebaseGameVersionGate is resolved before any semantic layer is loaded.
// File hashes and exact raster coordinates remain version-neutral, while
// Jomini object replay is allowed only when this transaction has a compatible
// CK3 semantic rule bundle.
type RebaseGameVersionGate struct {
	Mode                   string            `json:"mode"`
	BaseVersion            string            `json:"base_version"`
	TargetVersion          string            `json:"target_version"`
	BaseFamily             string            `json:"base_family"`
	TargetFamily           string            `json:"target_family"`
	SemanticRuleFamily     string            `json:"semantic_rule_family"`
	CompatibilityAdapter   string            `json:"compatibility_adapter,omitempty"`
	DescriptorVersions     map[string]string `json:"descriptor_versions,omitempty"`
	Status                 string            `json:"status"`
	SemanticMergeAllowed   bool              `json:"semantic_merge_allowed"`
	CoordinateDeltaAllowed bool              `json:"coordinate_delta_allowed"`
	BlockingCode           string            `json:"blocking_code,omitempty"`
	Reason                 string            `json:"reason"`
}

// RebasePlanSpec contains run-specific values. The profile is kept separate
// so ownership and map-authority policy can be reviewed once and reused.
type RebasePlanSpec struct {
	ProfilePath string `json:"profile_path"`
	OutputDir   string `json:"output_dir"`
}

type RebaseOptions struct {
	ArtifactRoot string
	DB           *indexer.DB
}

type RebaseFileState struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Text   bool   `json:"text,omitempty"`
}

// RebaseMapDelta describes the transparent Base-to-Ours patch retained by the
// transaction. The patch is never used as a lossy alpha blend: non-transparent
// pixels identify exact coordinates whose full project pixel replaces the
// corresponding target pixel in the separately encoded result candidate.
type RebaseMapDelta struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	OriginX       int    `json:"origin_x"`
	OriginY       int    `json:"origin_y"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	ChangedPixels int    `json:"changed_pixels"`
}

// RebaseFileDecision records both the evidence and the exact operation that
// will be replayed into the migration copy.
type RebaseFileDecision struct {
	Path           string          `json:"path"`
	Classification string          `json:"classification"`
	Adapter        string          `json:"adapter"`
	Action         string          `json:"action"`
	Reason         string          `json:"reason"`
	Safe           bool            `json:"safe"`
	Base           RebaseFileState `json:"base"`
	Project        RebaseFileState `json:"project"`
	Target         RebaseFileState `json:"target"`
	Result         RebaseFileState `json:"result"`
	CandidatePath  string          `json:"candidate_path,omitempty"`
	MapDelta       *RebaseMapDelta `json:"map_delta,omitempty"`
	SemanticIDs    []string        `json:"semantic_ids,omitempty"`
	ConflictIDs    []string        `json:"conflict_ids,omitempty"`
}

type RebaseConflict struct {
	ID         string `json:"conflict_id"`
	Code       string `json:"code"`
	Path       string `json:"path,omitempty"`
	SemanticID string `json:"semantic_id,omitempty"`
	// CounterpartPath identifies the other physical file involved in a
	// cross-file semantic conflict.  It deliberately stays relative to the
	// source root so a transaction is portable and reviewable.
	CounterpartPath string `json:"counterpart_path,omitempty"`
	// TargetRemovalCandidatePath is a transaction-owned, hash-pinned shadow
	// candidate whose content removes SemanticID from CounterpartPath.  It is
	// only populated when the planner could prove that a paired resolution is
	// safe to materialize.
	TargetRemovalCandidatePath   string   `json:"target_removal_candidate_path,omitempty"`
	TargetRemovalCandidateSHA256 string   `json:"target_removal_candidate_sha256,omitempty"`
	Message                      string   `json:"message"`
	Severity                     string   `json:"severity"`
	AllowedActions               []string `json:"allowed_actions"`
	SuggestedAction              string   `json:"suggested_action,omitempty"`
}

type RebaseResolution struct {
	ConflictID string `json:"conflict_id"`
	Action     string `json:"action"`
	ManualPath string `json:"manual_path,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

type RebaseProgress struct {
	Phase       string `json:"phase"`
	Completed   int    `json:"completed"`
	Total       int    `json:"total"`
	CurrentPath string `json:"current_path,omitempty"`
	Message     string `json:"message,omitempty"`
	UpdatedAt   string `json:"updated_at"`
}

type RebaseValidation struct {
	Status           string         `json:"status,omitempty"`
	Blocked          bool           `json:"blocked"`
	Counts           map[string]int `json:"counts,omitempty"`
	DiagnosticsBase  map[string]int `json:"diagnostics_base,omitempty"`
	DiagnosticsCopy  map[string]int `json:"diagnostics_copy,omitempty"`
	DiagnosticsNew   map[string]int `json:"diagnostics_new,omitempty"`
	MapAuditErrors   int            `json:"map_audit_errors"`
	MapAuditWarnings int            `json:"map_audit_warnings"`
	CopySHA256       string         `json:"copy_sha256,omitempty"`
	SmokeApproved    bool           `json:"smoke_approved"`
	ValidatedAt      string         `json:"validated_at,omitempty"`
}

type RebasePromotionReceipt struct {
	// Stage is a durable directory-rename intent checkpoint. It is written
	// before the first rename so a later promote/rollback invocation can
	// reconcile a process interruption without guessing about the formal path.
	Stage          string `json:"stage,omitempty"`
	FormalPath     string `json:"formal_path,omitempty"`
	BackupPath     string `json:"backup_path,omitempty"`
	PromotedSHA256 string `json:"promoted_sha256,omitempty"`
	PreviousSHA256 string `json:"previous_sha256,omitempty"`
	PromotedAt     string `json:"promoted_at,omitempty"`
	RolledBackAt   string `json:"rolled_back_at,omitempty"`
	// IndexInvalidated records that the main ck3-index cache was marked stale
	// before the first directory rename. Without this the index would keep
	// reporting a ready generation describing the replaced project.
	IndexInvalidated bool   `json:"index_invalidated,omitempty"`
	IndexRecovery    string `json:"index_recovery,omitempty"`
}

type RebaseTransaction struct {
	SchemaVersion int `json:"schema_version"`
	// Revision is a monotonic manifest counter. Every manifest write requires
	// the on-disk revision to still equal the one this in-memory copy was
	// loaded with, so a second process that raced past the transaction lock
	// cannot silently overwrite a newer state with its own stale copy.
	Revision      int64                 `json:"revision"`
	ID            string                `json:"transaction_id"`
	Status        string                `json:"status"`
	Profile       RebaseProfile         `json:"profile"`
	ProfileSHA256 string                `json:"profile_sha256"`
	GameVersion   RebaseGameVersionGate `json:"game_version"`
	// PlanProfilePath is the canonical local policy file used for a safe
	// restart of an interrupted planning pass. It is only stored in the local
	// transaction manifest; source paths themselves remain represented by
	// fingerprints in the report fields.
	PlanProfilePath    string `json:"plan_profile_path,omitempty"`
	OutputDir          string `json:"output_dir"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	BaseFingerprint    string `json:"base_fingerprint"`
	ProjectFingerprint string `json:"project_fingerprint"`
	// ProjectTreeFingerprint covers every regular project file that will be
	// carried through into a promoted overlay, including non-load-root assets
	// such as tools, README files, source material, and VCS metadata.  The
	// narrower ProjectFingerprint remains the semantic CK3 load inventory.
	ProjectTreeFingerprint string `json:"project_tree_fingerprint"`
	TargetFingerprint      string `json:"target_fingerprint"`
	// SourceIdentityFingerprints pin the configured source-root identities in
	// addition to their content inventories. Content-only checks cannot tell
	// whether a config edit retargeted a source name at a byte-identical but
	// unrelated directory just before promotion.
	SourceIdentityFingerprints map[string]string `json:"source_identity_fingerprints,omitempty"`
	// ValidationSourceFingerprints pins every configured auxiliary source in
	// the real validation load stack (normally the game and required
	// dependencies).  They are deliberately separate from target so a target
	// source can continue to be checked as the migration input itself.
	ValidationSourceFingerprints map[string]string       `json:"validation_source_fingerprints,omitempty"`
	ProjectReplacePaths          []string                `json:"project_replace_paths,omitempty"`
	Counts                       map[string]int          `json:"counts"`
	Files                        []RebaseFileDecision    `json:"files"`
	Conflicts                    []RebaseConflict        `json:"conflicts,omitempty"`
	Resolutions                  []RebaseResolution      `json:"resolutions,omitempty"`
	Progress                     RebaseProgress          `json:"progress"`
	Validation                   RebaseValidation        `json:"validation"`
	Promotion                    *RebasePromotionReceipt `json:"promotion,omitempty"`
}

func (transaction *RebaseTransaction) touch(now time.Time) {
	transaction.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	transaction.Progress.UpdatedAt = transaction.UpdatedAt
}
