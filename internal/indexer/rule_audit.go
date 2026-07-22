package indexer

import (
	"context"
	"fmt"
	"sort"
)

// OnActionRuleAudit is a read-only comparison between the compact generated
// CK3 1.19 snapshot and the live on_actions.log evidence captured in the cache. It
// deliberately reports drift for review; it never mutates validation rules.
type OnActionRuleAudit struct {
	Operation             string              `json:"operation"`
	LiveEvidenceAvailable bool                `json:"live_evidence_available"`
	EngineRuleFingerprint string              `json:"engine_rule_fingerprint,omitempty"`
	LiveCount             int                 `json:"live_count"`
	SnapshotCount         int                 `json:"snapshot_count"`
	EngineOnlyCount       int                 `json:"engine_only_count"`
	SnapshotOnlyCount     int                 `json:"snapshot_only_count"`
	EngineOnly            []OnActionRuleDelta `json:"engine_only,omitempty"`
	SnapshotOnly          []OnActionRuleDelta `json:"snapshot_only,omitempty"`
	Truncated             bool                `json:"truncated,omitempty"`
	Guidance              []string            `json:"guidance"`
}

type OnActionRuleDelta struct {
	Name           string   `json:"name"`
	Classification string   `json:"classification"`
	InputScopes    []string `json:"input_scopes,omitempty"`
	RuleSource     string   `json:"rule_source"`
	Confidence     string   `json:"confidence"`
}

// AuditOnActionRules returns bounded evidence for the largest source of
// script-rule drift: a configured log can differ from the generated CK3 1.19
// snapshot. A missing engine log cache is reported as unavailable rather than
// turning every snapshot entry into a false discrepancy.
func (db *DB) AuditOnActionRules(ctx context.Context, limit int) (OnActionRuleAudit, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	state, err := db.IndexState(ctx)
	if err != nil {
		return OnActionRuleAudit{}, err
	}
	if !state.Ready() {
		return OnActionRuleAudit{}, fmt.Errorf("rule audit requires a ready published index; current scan status is %q", state.Status)
	}
	fingerprint, err := db.metaValue(ctx, "engine_data_fingerprint")
	if err != nil {
		return OnActionRuleAudit{}, err
	}
	report := OnActionRuleAudit{
		Operation:             "on_actions",
		EngineRuleFingerprint: fingerprint,
		SnapshotCount:         len(engineOnActions),
		Guidance: []string{
			"Engine-only and snapshot-only entries are review evidence, not automatic validation changes.",
			"Expected Scope: none means the hook has no implicit root scope; do not normalize it to any scope.",
		},
	}
	live, err := db.onActionEngineScopes(ctx)
	if err != nil {
		return OnActionRuleAudit{}, err
	}
	// on_actions.log is optional in an engine-log bundle. A fingerprint for the
	// other logs is not proof that this particular live rule source exists.
	report.LiveEvidenceAvailable = fingerprint != "" && fingerprint != noEngineDataFingerprint && len(live) > 0
	if !report.LiveEvidenceAvailable {
		report.Guidance = append(report.Guidance, "No published on_actions.log evidence is available. Configure engine_logs and run a full scan before treating drift counts as meaningful.")
		return report, nil
	}
	report.LiveCount = len(live)
	engineOnlyNames := make([]string, 0)
	for name := range live {
		if _, ok := engineOnActions[name]; !ok {
			engineOnlyNames = append(engineOnlyNames, name)
		}
	}
	snapshotOnlyNames := make([]string, 0)
	for name := range engineOnActions {
		if _, ok := live[name]; !ok {
			snapshotOnlyNames = append(snapshotOnlyNames, name)
		}
	}
	sort.Strings(engineOnlyNames)
	sort.Strings(snapshotOnlyNames)
	report.EngineOnlyCount = len(engineOnlyNames)
	report.SnapshotOnlyCount = len(snapshotOnlyNames)
	for _, name := range engineOnlyNames {
		if len(report.EngineOnly) >= limit {
			report.Truncated = true
			break
		}
		report.EngineOnly = append(report.EngineOnly, OnActionRuleDelta{
			Name: name, Classification: "engine_only", InputScopes: live[name], RuleSource: "engine_logs", Confidence: "high",
		})
	}
	for _, name := range snapshotOnlyNames {
		if len(report.SnapshotOnly) >= limit {
			report.Truncated = true
			break
		}
		report.SnapshotOnly = append(report.SnapshotOnly, OnActionRuleDelta{
			Name: name, Classification: "snapshot_only", RuleSource: "engine_1_19_snapshot", Confidence: "high",
		})
	}
	return report, nil
}
