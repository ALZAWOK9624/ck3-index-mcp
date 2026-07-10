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
	rules map[string]map[string][]string
}{rules: map[string]map[string][]string{}}

func ConfigureEngineRules(logs string) error {
	rules := map[string]map[string][]string{}
	if strings.TrimSpace(logs) == "" {
		engineScopeRegistry.Lock()
		engineScopeRegistry.rules = nil
		engineScopeRegistry.Unlock()
		return nil
	}
	for _, spec := range []struct{ name, kind string }{{"effects.log", "effect"}, {"triggers.log", "trigger"}, {"event_targets.log", "target"}, {"event_scopes.log", "scope"}} {
		blocks, err := readDocBlocks(filepath.Join(logs, spec.name))
		if err != nil {
			return err
		}
		for _, lines := range blocks {
			if len(lines) == 0 {
				continue
			}
			name := lines[0]
			if a, _, ok := strings.Cut(name, " - "); ok {
				name = strings.TrimSpace(a)
			}
			for _, line := range lines[1:] {
				prefix := ""
				if strings.HasPrefix(line, "Supported Scopes:") {
					prefix = "Supported Scopes:"
				} else if strings.HasPrefix(line, "Input Scopes:") {
					prefix = "Input Scopes:"
				}
				if prefix != "" {
					m := rules[strings.ToLower(name)]
					if m == nil {
						m = map[string][]string{}
						rules[strings.ToLower(name)] = m
					}
					m[spec.kind] = splitScopes(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
				}
			}
		}
	}
	engineScopeRegistry.Lock()
	engineScopeRegistry.rules = rules
	engineScopeRegistry.Unlock()
	return nil
}

func engineScopeConfirms(key, kind string, need TigerScope) bool {
	engineScopeRegistry.RLock()
	defer engineScopeRegistry.RUnlock()
	// Backward-compatible configs without engine_logs retain Tiger behavior.
	// Once engine_logs is configured, a live rule is mandatory for a hard mismatch.
	if engineScopeRegistry.rules == nil {
		return true
	}
	scopes := engineScopeRegistry.rules[strings.ToLower(key)][kind]
	if len(scopes) == 0 {
		return false
	}
	var live TigerScope
	for _, s := range scopes {
		if v, ok := engineScopeType(s); ok {
			live = scopeUnion(live, v)
		}
	}
	return isConcreteScope(live) && live.Intersects(need)
}

func engineScopeType(name string) (TigerScope, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if v, ok := tigerScopesByName[n]; ok {
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
	return TigerScope{}, false
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
	for _, spec := range []struct{ name, kind string }{
		{"event_scopes.log", "scope"}, {"event_targets.log", "target"}, {"effects.log", "effect"}, {"triggers.log", "trigger"},
	} {
		if err := ingestScopeLog(ctx, tx, filepath.Join(logs, spec.name), spec.kind); err != nil {
			return err
		}
	}
	return nil
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

func ingestScopeLog(ctx context.Context, tx *sql.Tx, path, kind string) error {
	blocks, err := readDocBlocks(path)
	if err != nil {
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
		input, output := "", ""
		var extra []string
		for _, line := range lines[1:] {
			switch {
			case strings.HasPrefix(line, "Supported Scopes:"):
				input = strings.TrimSpace(strings.TrimPrefix(line, "Supported Scopes:"))
			case strings.HasPrefix(line, "Input Scopes:"):
				input = strings.TrimSpace(strings.TrimPrefix(line, "Input Scopes:"))
			case strings.HasPrefix(line, "Output Scopes:"):
				output = strings.TrimSpace(strings.TrimPrefix(line, "Output Scopes:"))
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
		e.InputScopes = splitScopes(in)
		e.OutputScopes = splitScopes(outScopes)
		e.Confidence = "high"
		out = append(out, e)
	}
	return out, rows.Err()
}
func splitScopes(s string) []string {
	var out []string
	for _, v := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '|' }) {
		v = strings.TrimSpace(v)
		if v != "" && v != "none" {
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
	if _, err := tx.ExecContext(ctx, `CREATE VIRTUAL TABLE search_fts USING fts5(kind, name, text, source, path UNINDEXED, tokenize='unicode61 remove_diacritics 2')`); err != nil {
		return fmt.Errorf("FTS5 unavailable: %w", err)
	}
	stmts := []string{
		`INSERT INTO search_fts(kind,name,text,source,path) SELECT 'object',o.name,o.object_type||' '||o.name||' '||f.rel_path,o.source_name,f.rel_path FROM objects o JOIN files f ON f.id=o.file_id WHERE f.overridden=0`,
		`INSERT INTO search_fts(kind,name,text,source,path) SELECT 'resource',r.resource_path,r.kind||' '||r.resource_path,r.source_name,f.rel_path FROM resources r JOIN files f ON f.id=r.file_id WHERE f.overridden=0`,
		`INSERT INTO search_fts(kind,name,text,source,path) SELECT 'script_key',o.field,o.field||' '||o.object_name||' '||o.raw,o.source_name,f.rel_path FROM object_fields o JOIN files f ON f.id=o.file_id WHERE f.overridden=0`,
		`INSERT INTO search_fts(kind,name,text,source,path) SELECT 'localization',l.key,l.key||' '||l.value,l.source_name,f.rel_path FROM localization l JOIN files f ON f.id=l.file_id WHERE f.overridden=0 AND (lower(l.language) LIKE '%english%' OR lower(l.language) LIKE '%simp%')`,
		`INSERT INTO search_fts(kind,name,text,source,path) SELECT 'datatype',name,signature||' '||COALESCE(description,'')||' '||COALESCE(return_type,''),'engine_logs',source_path FROM engine_datatypes`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("FTS5 rebuild failed: %w", err)
		}
	}
	return nil
}
