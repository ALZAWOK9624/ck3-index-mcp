package indexer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"strings"

	"ck3-index/internal/script"
)

const StandaloneCheckVersion = "1"
const CheckMaxFiles = 64
const CheckMaxFileBytes = 32 << 20
const CheckMaxTotalBytes = 64 << 20

// CheckFile is virtual text. Path selects a CK3 grammar; it is never opened.
type CheckFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type CheckRequest struct {
	Files      []CheckFile `json:"files"`
	SyntaxOnly bool        `json:"syntax_only,omitempty"`
	Limit      int         `json:"limit,omitempty"`
}
type CheckCoverage struct {
	Syntax     string `json:"syntax"`
	Semantics  string `json:"semantics"`
	References string `json:"references"`
	Runtime    string `json:"runtime"`
}
type CheckFileResult struct {
	Limited       bool          `json:"limited"`
	Path          string        `json:"path"`
	ContentSHA256 string        `json:"content_sha256"`
	Dialect       string        `json:"dialect"`
	Coverage      CheckCoverage `json:"coverage"`
	Errors        int           `json:"errors"`
	Warnings      int           `json:"warnings"`
	Diagnostics   []Diagnostic  `json:"diagnostics"`
}
type CheckResult struct {
	Version         string            `json:"version"`
	Status          string            `json:"status"`
	Passed          bool              `json:"passed"`
	DatabaseUsed    bool              `json:"database_used"`
	RuleSource      string            `json:"rule_source"`
	RuleFingerprint string            `json:"rule_fingerprint,omitempty"`
	Errors          int               `json:"errors"`
	Warnings        int               `json:"warnings"`
	DiagnosticCount int               `json:"diagnostic_count"`
	Truncated       bool              `json:"truncated"`
	Files           []CheckFileResult `json:"files"`
	Guidance        string            `json:"guidance"`
}

// StandaloneChecker owns an immutable rule snapshot. It neither opens a DB nor
// publishes process-global rules, and is safe for concurrent independent bots.
type StandaloneChecker struct {
	rules       *EngineRuleSet
	fingerprint string
}

func NewStandaloneChecker(ctx context.Context, engineLogs string) (*StandaloneChecker, error) {
	bundle, err := LoadEngineBundle(ctx, engineLogs)
	if err != nil {
		return nil, err
	}
	c := &StandaloneChecker{rules: engineRuleSetFromBundle(bundle)}
	if c.rules != nil {
		c.fingerprint = bundle.Fingerprint
	}
	return c, nil
}

// CheckDialect is explicit: documentation, shaders, CSV, raster assets and
// arbitrary host paths cannot accidentally receive a successful script check.
func CheckDialect(rel string) string {
	p := strings.ToLower(rel)
	root, _, _ := strings.Cut(p, "/")
	ext := path.Ext(p)
	if root == "localization" && (ext == ".yml" || ext == ".yaml") {
		return "localization"
	}
	if ext == ".gui" && (root == "gui" || root == "common") {
		return "gui"
	}
	switch root {
	case "common", "events", "history", "gui", "gfx", "map_data", "sound", "music", "notifications":
		switch ext {
		case ".txt", ".asset", ".gfx", ".map", ".settings", ".sfx":
			return "pdx"
		}
	}
	return ""
}

func (c *StandaloneChecker) Check(ctx context.Context, req CheckRequest) (CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return CheckResult{}, err
	}
	if len(req.Files) == 0 || len(req.Files) > CheckMaxFiles {
		return CheckResult{}, fmt.Errorf("files must contain 1..%d complete virtual files", CheckMaxFiles)
	}
	limit := req.Limit
	if limit == 0 {
		limit = 200
	}
	if limit < 1 || limit > 5000 {
		return CheckResult{}, fmt.Errorf("limit must be 1..5000")
	}
	seen := map[string]bool{}
	total := 0
	paths := make([]string, len(req.Files))
	for i, f := range req.Files {
		rel, err := normalizePatchRelPath(f.Path)
		if err != nil {
			return CheckResult{}, err
		}
		if CheckDialect(rel) == "" {
			return CheckResult{}, fmt.Errorf("unsupported validation path %q", rel)
		}
		key := strings.ToLower(rel)
		if seen[key] {
			return CheckResult{}, fmt.Errorf("duplicate virtual path %q", rel)
		}
		seen[key] = true
		paths[i] = rel
		total += len(f.Content)
		if len(f.Content) > CheckMaxFileBytes || total > CheckMaxTotalBytes {
			return CheckResult{}, fmt.Errorf("check text exceeds the 32 MiB per-file or 64 MiB request limit")
		}
	}
	result := CheckResult{Version: StandaloneCheckVersion, Status: "checked", Passed: true,
		RuleSource: "compiled_ck3_contracts", Files: []CheckFileResult{},
		Guidance: "Match each content_sha256 to the final submitted text; recheck after any edit. Pass means no errors in the reported static coverage. Unknown commands, dynamic scopes, references, load order and game execution are not certified. Indexed review is for authorized local projects, not public QQ checks."}
	if c.rules != nil {
		result.RuleSource = "compiled_contracts_and_engine_logs"
		result.RuleFingerprint = c.fingerprint
	}
	for i, f := range req.Files {
		if err := ctx.Err(); err != nil {
			return CheckResult{}, err
		}
		checked, _ := c.checkText(paths[i], f.Content, req.SyntaxOnly, SourceRoleProject)
		if checked.Limited {
			result.Truncated = true
		}
		result.Errors += checked.Errors
		result.Warnings += checked.Warnings
		result.DiagnosticCount += len(checked.Diagnostics)
		if len(checked.Diagnostics) > limit {
			checked.Diagnostics = checked.Diagnostics[:limit]
			result.Truncated = true
		}
		limit -= len(checked.Diagnostics)
		result.Files = append(result.Files, checked)
	}
	if err := ctx.Err(); err != nil {
		return CheckResult{}, err
	}
	result.Passed = result.Errors == 0
	if !result.Passed {
		result.Status = "errors"
	} else if result.Warnings > 0 {
		result.Status = "warnings"
	}
	return result, nil
}

func (c *StandaloneChecker) checkText(rel, content string, syntaxOnly bool, role SourceRole) (CheckFileResult, script.File) {
	dialect := CheckDialect(rel)
	r := CheckFileResult{Path: rel, ContentSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content))), Dialect: dialect, Diagnostics: []Diagnostic{},
		Coverage: CheckCoverage{Syntax: "checked", Semantics: "not_requested", References: "not_checked", Runtime: "not_checked"}}
	var parsed script.File
	if token, invalid := script.InvalidTokenEncoding(content); invalid {
		r.Diagnostics = append(r.Diagnostics, Diagnostic{Path: rel, Source: "parser", Severity: "error", Code: "invalid_text_encoding", Message: token.Message, Line: token.Line, Column: token.Col})
	}
	if dialect == "localization" {
		r.Diagnostics = append(r.Diagnostics, ctxDiagnostics(rel, "parser", checkLocalizationDocument(rel, []byte(content)))...)
	} else {
		if dialect == "gui" {
			parsed = script.ParseGUI(content)
		} else {
			parsed = script.Parse(content)
		}
		for _, pe := range parsed.Errors {
			r.Diagnostics = append(r.Diagnostics, Diagnostic{Path: rel, Source: "parser", Severity: "error", Code: "parse_error", Message: pe.Message, Line: pe.Line, Column: pe.Col})
		}
		if parsed.Limited {
			r.Limited = true
			r.Coverage.Syntax = "limited"
		}
		if !syntaxOnly && len(r.Diagnostics) == 0 {
			r.Coverage.Semantics = "partial_static_contracts"
			if dialect == "gui" {
				r.Diagnostics = append(r.Diagnostics, ctxDiagnostics(rel, "compiler", checkGUISafety(parsed.Nodes, rel))...)
			} else {
				r.Diagnostics = append(r.Diagnostics, ctxDiagnostics(rel, "compiler", checkScriptContextWithRules(parsed.Nodes, rel, c.rules))...)
				r.Diagnostics = append(r.Diagnostics, ctxDiagnostics(rel, "compiler", checkScriptLint(parsed.Nodes, rel, role, nil))...)
				r.Diagnostics = append(r.Diagnostics, ctxDiagnostics(rel, "compiler", checkRuntimeContractsWithRules(parsed.Nodes, rel, c.rules))...)
				r.Diagnostics = append(r.Diagnostics, ctxDiagnostics(rel, "compiler", checkScopeTrackerWithRules(parsed.Nodes, rel, c.rules))...)
				if strings.HasPrefix(strings.ToLower(rel), "common/scripted_effects/") {
					for _, n := range parsed.Nodes {
						if n.Kind == "block" && n.Key != "" {
							r.Diagnostics = append(r.Diagnostics, ctxDiagnostics(rel, "compiler", checkScriptEffectRecursion(n.Children, rel, n.Key))...)
						}
					}
				}
			}
		} else if !syntaxOnly {
			r.Coverage.Semantics = "skipped_due_to_parse_errors"
		}
	}
	for _, d := range r.Diagnostics {
		if d.Severity == "error" {
			r.Errors++
		}
		if d.Severity == "warning" {
			r.Warnings++
		}
	}
	sortDiagnostics(r.Diagnostics)
	return r, parsed
}
