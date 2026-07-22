package indexer

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
	rules := map[string]map[string][]string{}
	ruleOutputs := map[string]map[string][]string{}
	targetOutputs := map[string][]string{}
	if strings.TrimSpace(logs) == "" {
		engineScopeRegistry.Lock()
		engineScopeRegistry.rules = nil
		engineScopeRegistry.ruleOutputs = nil
		engineScopeRegistry.targetOutputs = nil
		engineScopeRegistry.modifiers = nil
		engineScopeRegistry.Unlock()
		return nil
	}
	for _, spec := range engineScopeLogSpecs {
		blocks, err := readDocBlocks(filepath.Join(logs, spec.name))
		if err != nil {
			if spec.optional && os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, lines := range blocks {
			if len(lines) == 0 {
				continue
			}
			name := engineRuleName(lines[0])
			for _, line := range lines[1:] {
				if value, ok := inputScopeLine(line); ok {
					m := rules[strings.ToLower(name)]
					if m == nil {
						m = map[string][]string{}
						rules[strings.ToLower(name)] = m
					}
					m[spec.kind] = splitScopesWithNone(value, spec.kind == "on_action")
				}
			}
			for _, line := range lines[1:] {
				if value, ok := outputScopeLine(line); ok {
					m := ruleOutputs[strings.ToLower(name)]
					if m == nil {
						m = map[string][]string{}
						ruleOutputs[strings.ToLower(name)] = m
					}
					m[spec.kind] = splitScopes(value)
					if spec.kind == "target" {
						targetOutputs[strings.ToLower(name)] = m[spec.kind]
					}
					break
				}
			}
		}
	}
	modifierData, err := readEngineModifiers(filepath.Join(logs, "modifiers.log"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	engineScopeRegistry.Lock()
	engineScopeRegistry.rules = rules
	engineScopeRegistry.ruleOutputs = ruleOutputs
	engineScopeRegistry.targetOutputs = targetOutputs
	engineScopeRegistry.modifiers = modifierData
	engineScopeRegistry.Unlock()
	return nil
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM engine_datatypes; DELETE FROM engine_scope_rules`); err != nil {
		return err
	}
	if strings.TrimSpace(logs) == "" {
		return nil
	}
	if err := ingestDatatypes(ctx, tx, filepath.Join(logs, "data_types")); err != nil {
		return err
	}
	for _, spec := range engineScopeLogSpecs {
		if err := ingestScopeLog(ctx, tx, filepath.Join(logs, spec.name), spec.kind, spec.optional); err != nil {
			return err
		}
	}

	return nil
}

func readEngineModifiers(path string) (map[string]ModifierInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]ModifierInfo{}
	var tag string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Tag:"):
			tag = strings.TrimSpace(strings.TrimPrefix(line, "Tag:"))
		case tag != "" && strings.HasPrefix(line, "Use areas:"):
			areas := parseModifierUseAreas(strings.TrimSpace(strings.TrimPrefix(line, "Use areas:")))
			out[tag] = ModifierInfo{UseAreas: areas, Source: "engine_log"}
			tag = ""
		}
	}
	return out, nil
}

// parseModifierUseAreas understands the punctuation used in the current
// localized modifiers.log, including "character， province，以及 county".
func parseModifierUseAreas(raw string) []string {
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

func ingestDatatypes(ctx context.Context, tx *sql.Tx, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("engine data_types unavailable: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO engine_datatypes(name,signature,description,definition_type,return_type,category,source_path) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		blocks, err := readDocBlocks(path)
		if err != nil {
			return err
		}
		cat := strings.TrimSuffix(strings.TrimPrefix(ent.Name(), "data_types_"), filepath.Ext(ent.Name()))
		for _, lines := range blocks {
			if len(lines) == 0 {
				continue
			}
			sig := strings.TrimSpace(lines[0])
			if sig == "" {
				continue
			}
			name := sig
			if i := strings.Index(name, "("); i >= 0 {
				name = strings.TrimSpace(name[:i])
			}
			info := map[string]string{}
			var desc []string
			for _, line := range lines[1:] {
				if k, v, ok := strings.Cut(line, ":"); ok && (k == "Description" || k == "Definition type" || k == "Return type") {
					info[k] = strings.TrimSpace(v)
				} else if strings.TrimSpace(line) != "" {
					desc = append(desc, strings.TrimSpace(line))
				}
			}
			if info["Description"] == "" {
				info["Description"] = strings.Join(desc, " ")
			}
			if _, err := stmt.ExecContext(ctx, name, sig, info["Description"], info["Definition type"], info["Return type"], cat, filepathSlash(path)); err != nil {
				return err
			}
		}
	}
	return nil
}

func ingestScopeLog(ctx context.Context, tx *sql.Tx, path, kind string, optional bool) error {
	blocks, err := readDocBlocks(path)
	if err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("engine %s log unavailable: %w", kind, err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, lines := range blocks {
		if len(lines) == 0 {
			continue
		}
		head := strings.TrimSpace(lines[0])
		if head == "" || strings.HasSuffix(head, "Documentation:") {
			continue
		}
		name, desc := head, ""
		if a, b, ok := strings.Cut(head, " - "); ok {
			name, desc = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		name = strings.TrimSuffix(strings.TrimSpace(name), ":")
		input, output := "", ""
		var extra []string
		for _, line := range lines[1:] {
			switch {
			case strings.HasPrefix(line, "Supported Scopes:"), strings.HasPrefix(line, "Input Scopes:"), strings.HasPrefix(line, "Expected Scope:"):
				input, _ = inputScopeLine(line)
			case strings.HasPrefix(line, "Output Scopes:"), strings.HasPrefix(line, "Supported Targets:"):
				output, _ = outputScopeLine(line)
			default:
				if strings.TrimSpace(line) != "" && !strings.Contains(line, ": yes") && !strings.Contains(line, ": no") {
					extra = append(extra, strings.TrimSpace(line))
				}
			}
		}
		if desc == "" {
			desc = strings.Join(extra, " ")
		}
		if _, err := stmt.ExecContext(ctx, name, kind, input, output, desc, filepathSlash(path)); err != nil {
			return err
		}
	}
	return nil
}

func readDocBlocks(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]string
	var cur []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "----------------") {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		if line != "" {
			cur = append(cur, line)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out, s.Err()
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
