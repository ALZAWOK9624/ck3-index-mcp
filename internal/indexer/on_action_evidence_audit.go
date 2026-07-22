package indexer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultOnActionEvidenceAuditLimit = 50
	maxOnActionEvidenceAuditLimit     = 500
	maxOnActionEvidenceDocumentRoots  = 8
)

// OnActionEvidenceAudit is a bounded, read-only reconciliation view over the
// three deliberately separate on_action evidence layers. It is not a rule
// generator and must never feed lint or validation decisions.
type OnActionEvidenceAudit struct {
	Intent                         string                 `json:"intent"`
	Status                         string                 `json:"status"`
	IndexStatus                    string                 `json:"index_status"`
	EngineEvidenceAvailable        bool                   `json:"engine_evidence_available"`
	DocumentationEvidenceAvailable bool                   `json:"documentation_evidence_available"`
	DocumentationEvidenceStatus    string                 `json:"documentation_evidence_status"`
	SnapshotSourceVersion          string                 `json:"snapshot_source_version"`
	HookCount                      int                    `json:"hook_count"`
	Findings                       []OnActionEvidenceHook `json:"findings,omitempty"`
	Truncated                      bool                   `json:"truncated,omitempty"`
	Guidance                       []string               `json:"guidance"`
}

// OnActionEvidenceHook aggregates evidence for one hook. Status is a
// consolidated review result; the comparison fields retain each pairwise
// relationship so consumers never have to infer precedence from a label.
type OnActionEvidenceHook struct {
	Hook                            string                        `json:"hook"`
	Status                          string                        `json:"status"`
	Engine                          OnActionEvidenceRoot          `json:"engine"`
	Snapshot                        OnActionEvidenceSnapshot      `json:"snapshot"`
	Documentation                   OnActionEvidenceDocumentation `json:"documentation"`
	EngineSnapshotComparison        string                        `json:"engine_snapshot_comparison"`
	EngineDocumentationComparison   string                        `json:"engine_documentation_comparison"`
	SnapshotDocumentationComparison string                        `json:"snapshot_documentation_comparison"`
}

// OnActionEvidenceRoot is a normalized root-only projection. Type is a
// documented/static literal for non-engine layers and an engine input scope
// for the live layer; RuleSource makes that distinction explicit.
type OnActionEvidenceRoot struct {
	Status     string   `json:"status"`
	Type       string   `json:"type,omitempty"`
	Types      []string `json:"types,omitempty"`
	RuleSource string   `json:"rule_source"`
	Confidence string   `json:"confidence"`
}

// OnActionEvidenceSnapshot preserves static alias provenance without exposing
// the generated seed table as a validator-facing API.
type OnActionEvidenceSnapshot struct {
	Found         bool                 `json:"found"`
	Definition    string               `json:"definition,omitempty"`
	AliasPath     []string             `json:"alias_path,omitempty"`
	SourceVersion string               `json:"source_version"`
	Root          OnActionEvidenceRoot `json:"root"`
	Review        bool                 `json:"review,omitempty"`
}

// OnActionEvidenceDocumentation is a safe root-only projection of adjacent
// rank-three vanilla comments. It intentionally omits raw comment prose and
// configured source identities.
type OnActionEvidenceDocumentation struct {
	Status    string                              `json:"status"`
	Roots     []OnActionEvidenceDocumentationRoot `json:"roots,omitempty"`
	Truncated bool                                `json:"truncated,omitempty"`
}

type OnActionEvidenceDocumentationRoot struct {
	Root OnActionEvidenceRoot `json:"root"`
	Path string               `json:"path"`
	Line int                  `json:"line"`
}

// AuditOnActionEvidence aggregates published live engine roots, versioned
// generated CK3 1.19 snapshot roots, and parser-recognized adjacent vanilla
// comment roots by hook.
// The output is bounded and review-only: no result is persisted, and no
// comparison changes diagnostics or inferred effective scopes.
func (db *DB) AuditOnActionEvidence(ctx context.Context, cfg Config, limit int) (OnActionEvidenceAudit, error) {
	if limit <= 0 {
		limit = defaultOnActionEvidenceAuditLimit
	}
	if limit > maxOnActionEvidenceAuditLimit {
		limit = maxOnActionEvidenceAuditLimit
	}

	state, err := db.IndexState(ctx)
	if err != nil {
		return OnActionEvidenceAudit{}, err
	}
	report := OnActionEvidenceAudit{
		Intent:                      "on_action_unified_evidence_audit",
		Status:                      "ok",
		IndexStatus:                 state.Status,
		DocumentationEvidenceStatus: "unavailable",
		SnapshotSourceVersion:       engineOnActionSnapshotVersion,
		Guidance: []string{
			"Live engine evidence is authoritative only after a published ready index; the compiled CK3 1.19 snapshot and vanilla comments remain review evidence.",
			"Expected Scope: none describes the implicit root only. A documented named scope is not an implicit-root conflict by itself.",
			"This audit is read-only and has no diagnostic, validation, or inferred-scope effect.",
		},
	}

	engineRoots := map[string][]string{}
	if state.Ready() {
		fingerprint, fingerprintErr := db.metaValue(ctx, "engine_data_fingerprint")
		if fingerprintErr != nil {
			return report, fingerprintErr
		}
		live, liveErr := db.onActionEngineScopes(ctx)
		if liveErr != nil {
			return report, liveErr
		}
		// A bundle can contain other engine logs while omitting on_actions.log.
		// Do not turn those rows, or an empty cache, into counterfeit live proof.
		if fingerprint != "" && fingerprint != noEngineDataFingerprint && len(live) > 0 {
			report.EngineEvidenceAvailable = true
			engineRoots = live
		}
	}
	if !report.EngineEvidenceAvailable {
		report.Guidance = append(report.Guidance, "No published on_actions.log root evidence is available; engine comparisons remain evidence_unavailable.")
	}

	documentation := map[string][]OnActionScopeContract{}
	documentationStale := false
	if game, found := vanillaGameSource(cfg); found {
		canReadDocumentation := true
		// The engine layer is a published database snapshot while comments are
		// read directly from the configured source tree. Do not silently compare
		// the former with a source that has changed since that snapshot.
		if report.EngineEvidenceAvailable {
			freshness, freshnessErr := db.onActionDocumentationSnapshotFreshness(ctx, game)
			if freshnessErr != nil {
				return report, freshnessErr
			}
			switch freshness {
			case onActionDocumentationSnapshotStale:
				canReadDocumentation = false
				documentationStale = true
				report.Status = "stale"
				report.DocumentationEvidenceStatus = "stale"
				report.Guidance = append(report.Guidance, "Vanilla on_action files differ from the published index snapshot; run a full scan before comparing their comments with cached engine evidence.")
			case onActionDocumentationSnapshotUnavailable:
				canReadDocumentation = false
				report.Guidance = append(report.Guidance, "The configured rank-three game source has no readable common/on_action directory.")
			}
		}
		if canReadDocumentation {
			scan, scanErr := scanVanillaOnActionComments(ctx, game)
			switch {
			case scanErr == nil:
				report.DocumentationEvidenceAvailable = true
				report.DocumentationEvidenceStatus = "available"
				for _, contract := range scan.Contracts {
					key := strings.ToLower(strings.TrimSpace(contract.Name))
					if key != "" {
						documentation[key] = append(documentation[key], contract)
					}
				}
			case os.IsNotExist(scanErr):
				report.Guidance = append(report.Guidance, "The configured rank-three game source has no readable common/on_action directory.")
			default:
				return report, scanErr
			}
		}
		// A source may change while its comments are being scanned. Re-check the
		// relevant snapshot before exposing a mixed engine/comment comparison.
		if report.EngineEvidenceAvailable && report.DocumentationEvidenceAvailable {
			freshness, freshnessErr := db.onActionDocumentationSnapshotFreshness(ctx, game)
			if freshnessErr != nil {
				return report, freshnessErr
			}
			if freshness != onActionDocumentationSnapshotCurrent {
				documentation = map[string][]OnActionScopeContract{}
				report.DocumentationEvidenceAvailable = false
				if freshness == onActionDocumentationSnapshotStale {
					documentationStale = true
					report.Status = "stale"
					report.DocumentationEvidenceStatus = "stale"
					report.Guidance = append(report.Guidance, "Vanilla on_action files changed while this audit was reading them; comment comparisons were withheld.")
				} else {
					report.DocumentationEvidenceStatus = "unavailable"
					report.Guidance = append(report.Guidance, "The configured rank-three game source became unreadable while this audit was running; comment comparisons were withheld.")
				}
			}
		}
	} else {
		report.Guidance = append(report.Guidance, "No rank-three game source is configured, so adjacent vanilla comment roots are unavailable.")
	}
	if state.Ready() {
		finalState, finalStateErr := db.IndexState(ctx)
		if finalStateErr != nil {
			return report, finalStateErr
		}
		if !samePublishedIndexState(state, finalState) || !finalState.Ready() {
			report.Status = "stale"
			report.IndexStatus = finalState.Status
			report.EngineEvidenceAvailable = false
			report.DocumentationEvidenceAvailable = false
			report.DocumentationEvidenceStatus = "stale"
			report.Guidance = append(report.Guidance, "The published index changed while this audit was running; partial evidence was withheld. Retry after the scan settles.")
			return report, nil
		}
	}

	names := map[string]bool{}
	// A published 1.19 log is the canonical hook set. The generated snapshot
	// exists for offline operation only, so it must not flood a bounded audit
	// with irrelevant entries when a fixture or a future log contains a smaller
	// explicit live set.
	if !report.EngineEvidenceAvailable {
		for name := range engineOnActions {
			names[name] = true
		}
	}
	for name := range engineRoots {
		names[name] = true
	}
	for name := range documentation {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	report.HookCount = len(ordered)

	findings := make([]OnActionEvidenceHook, 0, len(ordered))
	for _, name := range ordered {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		engine := projectOnActionEvidenceEngineRoot(engineRoots[name], report.EngineEvidenceAvailable)
		snapshot := projectOnActionEvidenceSnapshot(name)
		docs := projectOnActionEvidenceDocumentation(documentation[name], report.DocumentationEvidenceAvailable)
		if documentationStale {
			docs = OnActionEvidenceDocumentation{Status: "stale"}
		}
		engineSnapshot := compareOnActionEvidenceRoots(engine, snapshot.Root, "snapshot")
		engineDocumentation := compareOnActionEvidenceRoots(engine, onActionEvidenceDocumentationComparableRoot(docs), "documented")
		snapshotDocumentation := compareOnActionSnapshotDocumentation(snapshot.Root, onActionEvidenceDocumentationComparableRoot(docs), docs.Status)
		findings = append(findings, OnActionEvidenceHook{
			Hook:                            name,
			Status:                          unifiedOnActionEvidenceStatus(engine, docs, engineSnapshot, engineDocumentation, snapshotDocumentation),
			Engine:                          engine,
			Snapshot:                        snapshot,
			Documentation:                   docs,
			EngineSnapshotComparison:        engineSnapshot,
			EngineDocumentationComparison:   engineDocumentation,
			SnapshotDocumentationComparison: snapshotDocumentation,
		})
	}
	sort.Slice(findings, func(i, j int) bool {
		left, right := onActionEvidenceStatusPriority(findings[i].Status), onActionEvidenceStatusPriority(findings[j].Status)
		if left != right {
			return left < right
		}
		return findings[i].Hook < findings[j].Hook
	})
	if len(findings) > limit {
		report.Truncated = true
		findings = findings[:limit]
	}
	report.Findings = findings
	return report, nil
}

func projectOnActionEvidenceEngineRoot(scopes []string, available bool) OnActionEvidenceRoot {
	if !available {
		return OnActionEvidenceRoot{Status: "unavailable", RuleSource: "engine_logs", Confidence: "none"}
	}
	types := normalizedOnActionEvidenceTypes(scopes)
	if len(types) == 0 {
		// Once an on_actions.log bundle has been published, an absent hook is
		// explicit negative evidence about that bundle rather than a missing
		// query result.
		return OnActionEvidenceRoot{Status: "not_found", RuleSource: "engine_logs", Confidence: "high"}
	}
	if len(types) > 1 {
		return OnActionEvidenceRoot{Status: "ambiguous", Types: types, RuleSource: "engine_logs", Confidence: "medium"}
	}
	status := "explicit"
	if types[0] == "none" {
		status = "none"
	}
	return OnActionEvidenceRoot{Status: status, Type: types[0], RuleSource: "engine_logs", Confidence: "high"}
}

func projectOnActionEvidenceSnapshot(name string) OnActionEvidenceSnapshot {
	contract, found := ResolveOnActionSnapshotContract(name)
	if !found {
		return OnActionEvidenceSnapshot{
			Found:         false,
			SourceVersion: engineOnActionSnapshotVersion,
			Root:          OnActionEvidenceRoot{Status: "not_found", RuleSource: "engine_1_19_snapshot", Confidence: "high"},
		}
	}
	root := OnActionEvidenceRoot{
		Type:       contract.Root.StaticType,
		RuleSource: "engine_1_19_snapshot",
		Confidence: "high",
	}
	switch contract.Root.ValueKind {
	case OnActionSnapshotValueKindScope:
		root.Status = "explicit"
	case OnActionSnapshotValueKindNone:
		root.Status = "none"
	default:
		root.Status = "unresolved"
	}
	return OnActionEvidenceSnapshot{
		Found:         true,
		Definition:    contract.Definition,
		AliasPath:     append([]string(nil), contract.AliasPath...),
		SourceVersion: contract.SourceVersion,
		Root:          root,
		Review:        contract.Review || contract.Root.Review,
	}
}

func projectOnActionEvidenceDocumentation(contracts []OnActionScopeContract, available bool) OnActionEvidenceDocumentation {
	if !available {
		return OnActionEvidenceDocumentation{Status: "unavailable"}
	}
	if len(contracts) == 0 {
		return OnActionEvidenceDocumentation{Status: "not_documented"}
	}
	ordered := append([]OnActionScopeContract(nil), contracts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		return ordered[i].Line < ordered[j].Line
	})
	result := OnActionEvidenceDocumentation{Status: "documented"}
	if len(ordered) > 1 {
		result.Status = "ambiguous"
	}
	for _, contract := range ordered {
		if len(result.Roots) >= maxOnActionEvidenceDocumentRoots {
			result.Truncated = true
			continue
		}
		roots := onActionRootBindings(contract.Bindings)
		root, found := contractRootBinding(contract.Bindings)
		projected := projectOnActionEvidenceDocumentRoot(root, found)
		// A single adjacent comment block can accidentally document root more
		// than once. Keep it as one source candidate, but never select its
		// first declaration as though it were an unambiguous contract.
		if len(roots) > 1 {
			result.Status = "ambiguous"
			projected = OnActionEvidenceRoot{Status: "ambiguous", RuleSource: "vanilla_adjacent_top_level_comments", Confidence: "low"}
		}
		result.Roots = append(result.Roots, OnActionEvidenceDocumentationRoot{
			Root: projected,
			Path: contract.Path,
			Line: contract.Line,
		})
	}
	return result
}

func projectOnActionEvidenceDocumentRoot(binding OnActionScopeContractBinding, found bool) OnActionEvidenceRoot {
	root := OnActionEvidenceRoot{RuleSource: "vanilla_adjacent_top_level_comments", Confidence: "low"}
	if !found {
		root.Status = "unresolved"
		return root
	}
	root.Confidence = binding.Confidence
	switch binding.Kind {
	case "scope":
		root.Status, root.Type = "explicit", binding.Scope
	case "none":
		root.Status, root.Type = "none", "none"
	default:
		root.Status = "unresolved"
	}
	return root
}

func onActionEvidenceDocumentationComparableRoot(documentation OnActionEvidenceDocumentation) OnActionEvidenceRoot {
	if documentation.Status == "ambiguous" {
		return OnActionEvidenceRoot{Status: "ambiguous", RuleSource: "vanilla_adjacent_top_level_comments", Confidence: "low"}
	}
	if len(documentation.Roots) != 1 {
		return OnActionEvidenceRoot{Status: documentation.Status, RuleSource: "vanilla_adjacent_top_level_comments", Confidence: "low"}
	}
	return documentation.Roots[0].Root
}

func compareOnActionEvidenceRoots(engine, other OnActionEvidenceRoot, otherKind string) string {
	if engine.Status == "ambiguous" || other.Status == "ambiguous" {
		return "ambiguous"
	}
	if engine.Status == "not_found" && onActionEvidenceComparable(other) {
		return "engine_missing_with_" + otherKind + "_root"
	}
	if !onActionEvidenceComparable(engine) || !onActionEvidenceComparable(other) {
		return "evidence_unavailable"
	}
	if engine.Status == other.Status && engine.Type == other.Type {
		return "match"
	}
	if engine.Status == "none" && other.Status == "explicit" {
		return "engine_none_with_" + otherKind + "_root"
	}
	return "engine_scope_conflicts_" + otherKind + "_root"
}

func compareOnActionSnapshotDocumentation(snapshot, documented OnActionEvidenceRoot, documentationStatus string) string {
	if documentationStatus == "ambiguous" || snapshot.Status == "ambiguous" {
		return "ambiguous"
	}
	if !onActionEvidenceComparable(snapshot) || !onActionEvidenceComparable(documented) {
		return "evidence_unavailable"
	}
	if snapshot.Status == documented.Status && snapshot.Type == documented.Type {
		return "match"
	}
	return "snapshot_scope_conflicts_documented_root"
}

func unifiedOnActionEvidenceStatus(engine OnActionEvidenceRoot, documentation OnActionEvidenceDocumentation, engineSnapshot, engineDocumentation, snapshotDocumentation string) string {
	// Preserve a known disagreement even when another pair happens to match.
	// In particular, engine=docs with a conflicting generated snapshot entry is
	// three-way
	// evidence drift, not a successful all-layer match.
	for _, comparison := range []string{engineSnapshot, engineDocumentation, snapshotDocumentation} {
		if onActionEvidenceComparisonConflicts(comparison) {
			return comparison
		}
	}
	if documentation.Status == "stale" {
		return "documentation_stale"
	}
	if documentation.Status == "ambiguous" || engine.Status == "ambiguous" {
		return "ambiguous"
	}
	if !onActionEvidenceComparable(engine) {
		return "evidence_unavailable"
	}
	for _, comparison := range []string{engineSnapshot, engineDocumentation, snapshotDocumentation} {
		if comparison == "match" {
			return "match"
		}
	}
	return "evidence_unavailable"
}

func onActionEvidenceComparisonConflicts(comparison string) bool {
	switch comparison {
	case "engine_none_with_snapshot_root", "engine_scope_conflicts_snapshot_root", "engine_missing_with_snapshot_root",
		"engine_none_with_documented_root", "engine_scope_conflicts_documented_root", "engine_missing_with_documented_root",
		"snapshot_scope_conflicts_documented_root":
		return true
	default:
		return false
	}
}

func onActionEvidenceComparable(root OnActionEvidenceRoot) bool {
	return root.Status == "explicit" || root.Status == "none"
}

func normalizedOnActionEvidenceTypes(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func onActionEvidenceStatusPriority(status string) int {
	switch status {
	case "engine_missing_with_snapshot_root":
		return 0
	case "engine_missing_with_documented_root":
		return 1
	case "engine_scope_conflicts_snapshot_root":
		return 2
	case "engine_none_with_snapshot_root":
		return 3
	case "engine_scope_conflicts_documented_root":
		return 4
	case "engine_none_with_documented_root":
		return 5
	case "snapshot_scope_conflicts_documented_root":
		return 6
	case "ambiguous":
		return 7
	case "documentation_stale":
		return 8
	case "evidence_unavailable":
		return 9
	case "match":
		return 10
	default:
		return 11
	}
}

type onActionDocumentationSnapshotFreshness string

const (
	onActionDocumentationSnapshotCurrent     onActionDocumentationSnapshotFreshness = "current"
	onActionDocumentationSnapshotStale       onActionDocumentationSnapshotFreshness = "stale"
	onActionDocumentationSnapshotUnavailable onActionDocumentationSnapshotFreshness = "unavailable"
)

// onActionDocumentationSnapshotFreshness proves that the comment files about
// to be read are the same source-root-relative files captured by the ready
// index. The audit intentionally compares only hashes and portable relative
// paths; physical source paths never leave this helper.
func (db *DB) onActionDocumentationSnapshotFreshness(ctx context.Context, source Source) (onActionDocumentationSnapshotFreshness, error) {
	indexed, indexedUnique, err := db.indexedOnActionDocumentationHashes(ctx, source)
	if err != nil {
		return onActionDocumentationSnapshotUnavailable, err
	}
	if !indexedUnique {
		return onActionDocumentationSnapshotStale, nil
	}
	current, available, err := currentOnActionDocumentationHashes(ctx, source)
	if err != nil {
		return onActionDocumentationSnapshotUnavailable, err
	}
	if !available {
		return onActionDocumentationSnapshotUnavailable, nil
	}
	if len(indexed) != len(current) {
		return onActionDocumentationSnapshotStale, nil
	}
	for path, hash := range indexed {
		if current[path] != hash {
			return onActionDocumentationSnapshotStale, nil
		}
	}
	return onActionDocumentationSnapshotCurrent, nil
}

func (db *DB) indexedOnActionDocumentationHashes(ctx context.Context, source Source) (map[string]string, bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT rel_path,sha256 FROM files
		WHERE source_name=? AND source_rank=? AND kind='script'
		AND lower(rel_path) LIKE 'common/on_action/%' AND lower(rel_path) LIKE '%.txt'`, source.Name, source.Rank)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	hashes := map[string]string{}
	for rows.Next() {
		var rel, hash string
		if err := rows.Scan(&rel, &hash); err != nil {
			return nil, false, err
		}
		rel = normalizedOnActionDocumentationPath(rel)
		if previous, exists := hashes[rel]; exists && previous != hash {
			return hashes, false, nil
		}
		hashes[rel] = hash
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return hashes, true, nil
}

func currentOnActionDocumentationHashes(ctx context.Context, source Source) (map[string]string, bool, error) {
	root := filepath.Join(source.Path, "common", "on_action")
	hashes := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".txt") {
			return nil
		}
		rel, err := filepath.Rel(source.Path, path)
		if err != nil {
			return err
		}
		hash, err := shaFile(path)
		if err != nil {
			return err
		}
		hashes[normalizedOnActionDocumentationPath(rel)] = hash
		return nil
	})
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return hashes, true, nil
}

func normalizedOnActionDocumentationPath(path string) string {
	return strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
}
