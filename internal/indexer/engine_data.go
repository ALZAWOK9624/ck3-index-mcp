package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var engineScopeRegistry = struct {
	sync.RWMutex
	rules         map[string]map[string][]string
	ruleOutputs   map[string]map[string][]string
	targetOutputs map[string][]string
	modifiers     map[string]ModifierInfo
}{
	rules:         map[string]map[string][]string{},
	ruleOutputs:   map[string]map[string][]string{},
	targetOutputs: map[string][]string{},
	modifiers:     map[string]ModifierInfo{},
}

type engineScopeLogSpec struct {
	name     string
	kind     string
	optional bool
}

var engineScopeLogSpecs = []engineScopeLogSpec{
	{name: "effects.log", kind: "effect"},
	{name: "triggers.log", kind: "trigger"},
	{name: "event_targets.log", kind: "target"},
	{name: "event_scopes.log", kind: "scope"},
	// on_actions.log is optional only for compatibility with older log bundles;
	// when present it is the live source of truth and supersedes the generated
	// CK3 1.19 snapshot.
	{name: "on_actions.log", kind: "on_action", optional: true},
}

func ConfigureEngineRules(logs string) error {
	bundle, err := LoadEngineBundle(context.Background(), logs)
	if err != nil {
		return err
	}
	ConfigureEngineRulesFromBundle(bundle)
	return nil
}

func ConfigureEngineRulesFromBundle(bundle *EngineBundle) {
	engineScopeRegistry.Lock()
	defer engineScopeRegistry.Unlock()
	if bundle == nil || bundle.Fingerprint == noEngineDataFingerprint {
		engineScopeRegistry.rules = nil
		engineScopeRegistry.ruleOutputs = nil
		engineScopeRegistry.targetOutputs = nil
		engineScopeRegistry.modifiers = nil
		return
	}
	engineScopeRegistry.rules = bundle.ScopeRules
	engineScopeRegistry.ruleOutputs = bundle.RuleOutputs
	engineScopeRegistry.targetOutputs = bundle.Targets
	engineScopeRegistry.modifiers = bundle.Modifiers
}

func engineRuleName(head string) string {
	name := strings.TrimSpace(head)
	if a, _, ok := strings.Cut(name, " - "); ok {
		name = strings.TrimSpace(a)
	}
	return strings.TrimSuffix(name, ":")
}

func inputScopeLine(line string) (string, bool) {
	for _, prefix := range []string{"Supported Scopes:", "Input Scopes:", "Expected Scope:"} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

func engineOnActionKnown(key string) bool {
	engineScopeRegistry.RLock()
	defer engineScopeRegistry.RUnlock()
	_, ok := engineScopeRegistry.rules[strings.ToLower(key)]["on_action"]
	return ok
}

func engineScopeConfirms(key, kind string, need EngineScope) bool {
	if !engineRulesConfigured() {
		// Without an engine-log bundle, the generated CK3 1.19 snapshot is the
		// only available source and retains its documented validation behavior.
		return true
	}
	live, found := engineRuleScope(key, kind)
	return found && isConcreteScope(live) && live.Intersects(need)
}

func outputScopeLine(line string) (string, bool) {
	for _, prefix := range []string{"Output Scopes:", "Supported Targets:"} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

func engineRulesConfigured() bool {
	engineScopeRegistry.RLock()
	defer engineScopeRegistry.RUnlock()
	return engineScopeRegistry.rules != nil
}

// engineRuleScope converts the current engine log's documented input scopes
// into the internal scope set. It returns false only when no live entry is
// configured for this key and kind; callers can then use the generated 1.19
// snapshot for offline operation.
func engineRuleScope(key, kind string) (EngineScope, bool) {
	engineScopeRegistry.RLock()
	defer engineScopeRegistry.RUnlock()
	if engineScopeRegistry.rules == nil {
		return EngineScope{}, false
	}
	scopes := engineScopeRegistry.rules[strings.ToLower(key)][kind]
	return engineScopesToMask(scopes)
}

// engineTargetOutputScope returns the output scope published by
// event_targets.log. It deliberately leaves targets without an explicit
// output as unknown rather than inventing a scope transition.
func engineTargetOutputScope(key string) (EngineScope, bool) {
	engineScopeRegistry.RLock()
	defer engineScopeRegistry.RUnlock()
	if engineScopeRegistry.targetOutputs == nil {
		return EngineScope{}, false
	}
	return engineScopesToMask(engineScopeRegistry.targetOutputs[strings.ToLower(key)])
}

// engineRuleOutputScope returns the documented child/target scope for a live
// trigger or effect. Iterator entries in the 1.19 logs use Supported Targets
// for this field, while event targets use Output Scopes.
func engineRuleOutputScope(key, kind string) (EngineScope, bool) {
	engineScopeRegistry.RLock()
	defer engineScopeRegistry.RUnlock()
	if engineScopeRegistry.ruleOutputs == nil {
		return EngineScope{}, false
	}
	scopes := engineScopeRegistry.ruleOutputs[strings.ToLower(key)][kind]
	return engineScopesToMask(scopes)
}

func engineModifier(key string) (ModifierInfo, bool) {
	engineScopeRegistry.RLock()
	defer engineScopeRegistry.RUnlock()
	if engineScopeRegistry.modifiers == nil {
		return ModifierInfo{}, false
	}
	info, ok := engineScopeRegistry.modifiers[key]
	return info, ok
}

func engineScopesToMask(scopes []string) (EngineScope, bool) {
	var out EngineScope
	found := false
	for _, name := range scopes {
		if scope, ok := engineScopeType(name); ok {
			out = scopeUnion(out, scope)
			found = true
		}
	}
	return out, found
}

func engineScopeType(name string) (EngineScope, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "ghw":
		n = "great_holy_war"
	case "story":
		n = "story_cycle"
	case "vassal_contract_obligation_level":
		n = "vassal_obligation_level"
	case "boolean", "bool", "flag", "none":
		return ScopeValue, true
	}
	if v, ok := engineScopesByName[n]; ok {
		return v, true
	}
	switch n {
	case "landed_title", "title_scope":
		return ScopeTitle, true
	case "county_title":
		return ScopeTitle, true
	case "province_scope":
		return ScopeProvince, true
	case "character_scope":
		return ScopeCharacter, true
	}
	return EngineScope{}, false
}

type DatatypeInfo struct {
	Name           string `json:"name"`
	Signature      string `json:"signature"`
	Description    string `json:"description,omitempty"`
	DefinitionType string `json:"definition_type,omitempty"`
	ReturnType     string `json:"return_type,omitempty"`
	Category       string `json:"category,omitempty"`
	Source         string `json:"source"`
}

type ScopeEvidence struct {
	Key          string   `json:"key"`
	RuleKind     string   `json:"rule_kind"`
	InputScopes  []string `json:"input_scopes,omitempty"`
	OutputScopes []string `json:"output_scopes,omitempty"`
	Description  string   `json:"description,omitempty"`
	RuleSource   string   `json:"rule_source"`
	Confidence   string   `json:"confidence"`
}

func rebuildEngineData(ctx context.Context, tx *sql.Tx, logs string) error {
	bundle, err := LoadEngineBundle(ctx, logs)
	if err != nil {
		return err
	}
	return rebuildEngineDataFromBundle(ctx, tx, bundle)
}

func rebuildEngineDataFromBundle(ctx context.Context, tx *sql.Tx, bundle *EngineBundle) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM engine_datatypes; DELETE FROM engine_scope_rules`); err != nil {
		return err
	}
	if bundle == nil || bundle.Fingerprint == noEngineDataFingerprint {
		return nil
	}
	datatypeStmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO engine_datatypes(name,signature,description,definition_type,return_type,category,source_path) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer datatypeStmt.Close()
	for _, datatype := range bundle.Datatypes {
		if _, err := datatypeStmt.ExecContext(ctx, datatype.Name, datatype.Signature, datatype.Description, datatype.DefinitionType, datatype.ReturnType, datatype.Category, datatype.Source); err != nil {
			return err
		}
	}
	scopeStmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer scopeStmt.Close()
	for _, row := range bundle.ScopeRows {
		if _, err := scopeStmt.ExecContext(ctx, row.Key, row.RuleKind, strings.Join(row.InputScopes, " "), strings.Join(row.OutputScopes, " "), row.Description, row.RuleSource); err != nil {
			return err
		}
	}
	return nil
}

// parseModifierUseAreas understands the punctuation used in the current
// localized modifiers.log, including "character， province，以及 county".
func parseModifierUseAreas(raw string) []string {
	// The current English modifiers.log uses both comma-separated lists and
	// the natural-language form "character and province". Normalize the
	// conjunction before splitting, otherwise a live log entry silently
	// overrides the generated area contract with one unusable area.
	raw = strings.ReplaceAll(raw, " and ", ",")
	raw = strings.ReplaceAll(raw, "，", ",")
	raw = strings.ReplaceAll(raw, "以及", ",")
	var out []string
	for _, area := range strings.Split(raw, ",") {
		area = strings.TrimSpace(area)
		if area == "" {
			continue
		}
		duplicate := false
		for _, existing := range out {
			if existing == area {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, area)
		}
	}
	return out
}

func (db *DB) LookupDatatype(ctx context.Context, query string, limit int) ([]DatatypeInfo, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT name,signature,COALESCE(description,''),COALESCE(definition_type,''),COALESCE(return_type,''),COALESCE(category,''),source_path FROM engine_datatypes WHERE name=? OR name LIKE ? ORDER BY CASE WHEN name=? THEN 0 ELSE 1 END,name LIMIT ?`, query, escapeLike(query)+"%", query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatatypeInfo
	for rows.Next() {
		var d DatatypeInfo
		if err := rows.Scan(&d.Name, &d.Signature, &d.Description, &d.DefinitionType, &d.ReturnType, &d.Category, &d.Source); err != nil {
			return nil, err
		}
		d.Source = logicalEngineEvidenceSource(d.Source)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (db *DB) LookupScopeEvidence(ctx context.Context, key string) ([]ScopeEvidence, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT name,rule_kind,COALESCE(input_scopes,''),COALESCE(output_scopes,''),COALESCE(description,''),source_path FROM engine_scope_rules WHERE name=? ORDER BY CASE rule_kind WHEN 'trigger' THEN 0 WHEN 'effect' THEN 1 WHEN 'target' THEN 2 ELSE 3 END`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopeEvidence
	for rows.Next() {
		var e ScopeEvidence
		var in, outScopes string
		if err := rows.Scan(&e.Key, &e.RuleKind, &in, &outScopes, &e.Description, &e.RuleSource); err != nil {
			return nil, err
		}
		e.RuleSource = logicalEngineEvidenceSource(e.RuleSource)
		e.InputScopes = splitScopesWithNone(in, e.RuleKind == "on_action")
		e.OutputScopes = splitScopes(outScopes)
		e.Confidence = "high"
		out = append(out, e)
	}
	return out, rows.Err()
}

// logicalEngineEvidenceSource strips machine-specific engine-log roots from
// query evidence. The database retains its physical source path for local
// maintenance, while CLI/MCP consumers get a portable logical provenance.
func logicalEngineEvidenceSource(sourcePath string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(sourcePath), func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return "engine_logs"
	}
	for index, part := range parts {
		if strings.EqualFold(part, "data_types") {
			return "engine_logs/" + strings.Join(parts[index:], "/")
		}
	}
	return "engine_logs/" + parts[len(parts)-1]
}

// LookupOnActionEvidence returns the live on_action contract exposed by the
// game logs. An empty result lets callers deliberately fall back to the
// generated CK3 1.19 snapshot rather than pretending the live rule was found.
func (db *DB) LookupOnActionEvidence(ctx context.Context, key string) ([]ScopeEvidence, error) {
	// Engine on_action ids are normalized to lower case at ingestion. Keep this
	// public lookup consistent with IsOnAction and documentation lookup rather
	// than treating a caller's casing as an engine-rule miss.
	rules, err := db.LookupScopeEvidence(ctx, strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		return nil, err
	}
	out := make([]ScopeEvidence, 0, len(rules))
	for _, rule := range rules {
		if rule.RuleKind == "on_action" {
			out = append(out, rule)
		}
	}
	return out, nil
}

func splitScopes(s string) []string {
	return splitScopesWithNone(s, false)
}

func splitScopesWithNone(s string, keepNone bool) []string {
	var out []string
	for _, v := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '|' }) {
		v = strings.TrimSpace(v)
		if v != "" && (keepNone || v != "none") {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func rebuildSearchFTS(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS search_fts`); err != nil {
		return fmt.Errorf("FTS5 unavailable: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE VIRTUAL TABLE search_fts USING fts5(kind, name, text, source, path UNINDEXED, file_id UNINDEXED, tokenize='unicode61 remove_diacritics 2')`); err != nil {
		return fmt.Errorf("FTS5 unavailable: %w", err)
	}
	stmts := []string{
		`INSERT INTO search_fts(kind,name,text,source,path,file_id) SELECT 'object',o.name,o.object_type||' '||o.name||' '||f.rel_path,o.source_name,f.rel_path,f.id FROM objects o JOIN files f ON f.id=o.file_id WHERE f.overridden=0`,
		`INSERT INTO search_fts(kind,name,text,source,path,file_id) SELECT 'resource',r.resource_path,r.kind||' '||r.resource_path,r.source_name,f.rel_path,f.id FROM resources r JOIN files f ON f.id=r.file_id WHERE f.overridden=0`,
		`INSERT INTO search_fts(kind,name,text,source,path,file_id) SELECT 'script_key',o.field,o.field||' '||o.object_name||' '||o.raw,o.source_name,f.rel_path,f.id FROM object_fields o JOIN files f ON f.id=o.file_id WHERE f.overridden=0`,
		`INSERT INTO search_fts(kind,name,text,source,path,file_id) SELECT 'localization',l.key,l.key||' '||l.value,l.source_name,f.rel_path,f.id FROM localization l JOIN files f ON f.id=l.file_id WHERE f.overridden=0 AND (lower(l.language) LIKE '%english%' OR lower(l.language) LIKE '%simp%')`,
		`INSERT INTO search_fts(kind,name,text,source,path,file_id) SELECT 'datatype',name,signature||' '||COALESCE(description,'')||' '||COALESCE(return_type,''),'engine_logs',source_path,0 FROM engine_datatypes`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("FTS5 rebuild failed: %w", err)
		}
	}
	return nil
}
