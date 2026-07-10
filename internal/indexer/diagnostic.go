package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type DiagnosticFilter struct {
	Code       string
	Source     string
	PathPrefix string
	Confidence string
}

func (db *DB) ExplainDiagnostic(ctx context.Context, code string) ([]Diagnostic, error) {
	return db.ExplainDiagnosticFiltered(ctx, DiagnosticFilter{Code: code})
}

func (db *DB) ExplainDiagnosticFiltered(ctx context.Context, f DiagnosticFilter) ([]Diagnostic, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT d.source,d.severity,d.code,d.message,COALESCE(d.path,''),COALESCE(d.line,0),COALESCE(d.col,0),COALESCE(fi.source_name,d.source_layer,''),d.confidence,d.fingerprint,d.occurrences
		FROM diagnostics d LEFT JOIN files fi ON fi.id=d.file_id
		WHERE (?='' OR d.code=?) AND (?='' OR fi.source_name=?) AND (?='' OR d.path LIKE ?) AND (?='' OR d.confidence=?)
		ORDER BY d.path,d.line LIMIT 5000`, f.Code, f.Code, f.Source, f.Source, f.PathPrefix, f.PathPrefix+"%", f.Confidence, f.Confidence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column, &d.SourceLayer, &d.Confidence, &d.Fingerprint, &d.Occurrences); err != nil {
			return nil, err
		}
		d.Suggestion, d.RuleSource = diagnosticHint(d.Code, d.Message)
		if d.Confidence == "medium" {
			d.Confidence = diagnosticConfidence(d.Code, d.Severity)
		}
		if d.Fingerprint == "" {
			d.Fingerprint = diagnosticFingerprint(d)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aggregateDiagnostics(out), nil
}

func diagnosticConfidence(code, severity string) string {
	if severity == "error" || code == "scope_mismatch" || code == "missing_object_reference" {
		return "high"
	}
	if code == "scope_uncertain" || code == "resource_resolution_uncertain" {
		return "low"
	}
	return "medium"
}

func resourceDiagnostic(name string) (string, string) {
	n := filepathSlash(strings.TrimSpace(name))
	// Only a source-root-qualified path is a deterministic missing resource.
	// Bare filenames and layer-relative fragments require owning-context resolution.
	if strings.Contains(n, "/") && filepath.Ext(n) != "" && (strings.HasPrefix(n, "gfx/") || strings.HasPrefix(n, "sound/") || strings.HasPrefix(n, "map_data/")) {
		return "missing_resource", "warning"
	}
	return "resource_resolution_uncertain", "info"
}

func diagnosticFingerprint(d Diagnostic) string {
	target := d.Message
	if a := strings.Index(target, "'"); a >= 0 {
		if b := strings.Index(target[a+1:], "'"); b >= 0 {
			target = target[a+1 : a+1+b]
		}
	}
	if d.Code == "missing_localization" || d.Code == "missing_resource" || d.Code == "resource_resolution_uncertain" {
		return d.Code + ":" + strings.ToLower(target)
	}
	return fmt.Sprintf("%s:%s:%d:%s", d.Code, filepathSlash(d.Path), d.Line, strings.ToLower(target))
}

func diagnosticRank(d Diagnostic) int {
	if d.Severity == "error" {
		return 0
	}
	if d.Confidence == "high" && (strings.HasPrefix(d.Code, "scope_") || strings.Contains(d.Code, "reference")) {
		return 1
	}
	if d.Code == "missing_resource" || d.Code == "resource_resolution_uncertain" {
		return 2
	}
	if strings.Contains(d.Code, "localization") || strings.Contains(d.Code, "_loc") {
		return 3
	}
	if d.Confidence == "low" || strings.Contains(d.Code, "uncertain") {
		return 5
	}
	return 4
}

func aggregateDiagnostics(in []Diagnostic) []Diagnostic {
	by := map[string]int{}
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		key := d.Fingerprint
		if key == "" {
			key = diagnosticFingerprint(d)
			d.Fingerprint = key
		}
		if i, ok := by[key]; ok {
			out[i].Occurrences += maxInt(1, d.Occurrences)
			continue
		}
		if d.Occurrences < 1 {
			d.Occurrences = 1
		}
		by[key] = len(out)
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := diagnosticRank(out[i]), diagnosticRank(out[j])
		if ri != rj {
			return ri < rj
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func diagnosticHint(code, message string) (string, string) {
	switch code {
	case "scope_mismatch":
		if strings.Contains(message, "can_recruit") && strings.Contains(message, "has_cultural") {
			return "In men_at_arms_type.can_recruit, wrap culture-scope triggers in culture = { ... }, or use valid_for_maa_trigger = { PARAMETER = unlock_maa_xxx }.", "ck3-index:maa_can_recruit_scope"
		}
		return "Move this trigger/effect into a block with the required scope, or use a scope transition/iterator that provides that scope before calling it.", "ck3-index:scope_rules"
	case "missing_localization":
		return "Add the referenced localization key under localization/<language>/, or change the script to reference an existing key. Do not use localization text as mechanism evidence.", "ck3-index:localization_refs"
	case "missing_resource":
		return "Add the referenced resource at the resolved CK3 path, or update the script to point at an existing indexed resource. For culture tradition layers, prefer layer-relative filenames such as 4 = icon.dds.", "ck3-index:resource_refs"
	case "resource_resolution_uncertain":
		return "This is a bare or context-relative filename. Resolve it against the owning gfx/script context before treating it as missing.", "ck3-index:resource_resolution"
	case "missing_object_reference":
		return "Define the referenced object in the current project or verify the ref kind/name against active upstream objects. If it is dynamic state, model it as flag/global_var rather than a normal object ref.", "ck3-index:object_refs"
	}
	return "", ""
}

func refHint(kind string) (string, string) {
	switch kind {
	case "localization":
		return "Add the localization key in the same patch or an indexed localization file, then run preflight_patch again before writing.", "ck3-index:localization_refs"
	case "resource":
		return "Add the resource file in the same patch or change the script to an existing indexed resource path.", "ck3-index:resource_refs"
	case "sound":
		return "Verify the event:/ sound name against known CK3 sound events or an existing project sound definition.", "ck3-index:sound_refs"
	case "flag", "global_var":
		return "Dynamic flags and variables normally do not require object definitions; if this appears unresolved, check extractor context before changing script.", "ck3-index:dynamic_state_refs"
	default:
		if isObjectRefKind(kind) {
			return "Define the referenced object or change the ref kind/name to an active indexed object; check query_object and find_refs before editing.", "ck3-index:object_refs"
		}
	}
	return "", ""
}
