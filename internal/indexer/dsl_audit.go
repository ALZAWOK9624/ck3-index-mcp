package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ck3-index/internal/script"
)

type DSLDiagnosticCount struct {
	Count    int          `json:"count"`
	Examples []Diagnostic `json:"examples"`
}
type DSLFieldObservation struct {
	Count       int            `json:"count"`
	Shapes      map[string]int `json:"shapes"`
	ExamplePath string         `json:"example_path"`
	ExampleLine int            `json:"example_line"`
}
type DSLFamily struct {
	ObjectType string                          `json:"indexed_object_type,omitempty"`
	Files      int                             `json:"files"`
	Nodes      int                             `json:"nodes"`
	Errors     int                             `json:"errors"`
	Fields     map[string]*DSLFieldObservation `json:"direct_definition_fields"`
}
type DSLAuditReport struct {
	CompiledRuleDrift   []DSLRuleDelta                 `json:"compiled_rule_drift"`
	Version             string                         `json:"version"`
	CorpusFingerprint   string                         `json:"corpus_fingerprint"`
	RuleFingerprint     string                         `json:"rule_fingerprint,omitempty"`
	Files               int                            `json:"files"`
	DocumentationFiles  int                            `json:"documentation_files"`
	DocumentationFields map[string][]string            `json:"documentation_fields"`
	CommandCounts       map[string]int                 `json:"command_counts"`
	Bytes               int64                          `json:"bytes"`
	Nodes               int                            `json:"nodes"`
	Errors              int                            `json:"errors"`
	Warnings            int                            `json:"warnings"`
	ElapsedSeconds      float64                        `json:"elapsed_seconds"`
	Families            map[string]*DSLFamily          `json:"families"`
	Diagnostics         map[string]*DSLDiagnosticCount `json:"diagnostics"`
	SyntaxFeatures      map[string]int                 `json:"syntax_features"`
	Limits              []string                       `json:"limits"`
}

// AuditDSL reads every supported text file in the administrator-selected root.
// It is a CLI/library operation, never a client-supplied MCP filesystem path.
// Observations are evidence, not an inferred closed-world allowlist.
func (c *StandaloneChecker) AuditDSL(ctx context.Context, root string) (DSLAuditReport, error) {
	start := time.Now()
	r := DSLAuditReport{Version: StandaloneCheckVersion, RuleFingerprint: c.fingerprint,
		Families: map[string]*DSLFamily{}, Diagnostics: map[string]*DSLDiagnosticCount{}, SyntaxFeatures: map[string]int{}, DocumentationFields: map[string][]string{}, CommandCounts: map[string]int{},
		Limits: []string{"Observed field shapes do not prove legal or exhaustive engine grammar.", "No reference resolution, load-order simulation, macro expansion or game execution.", "Documentation .info files are counted separately; they contain example placeholders, not executable definitions.", "Shaders, binary assets, CSV and unsupported file extensions are excluded explicitly."}}
	hash := sha256.New()
	r.CompiledRuleDrift = c.compiledRuleDrift()
	if c.rules == nil {
		r.CommandCounts["trigger"] = len(engineTriggerScopes)
		r.CommandCounts["effect"] = len(engineEffectScopes)
	} else {
		for _, kinds := range c.rules.rules {
			for kind := range kinds {
				r.CommandCounts[kind]++
			}
		}
	}
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && !strings.Contains(rel, "/") {
				switch rel {
				case "common", "events", "history", "gui", "localization", "gfx", "map_data", "sound", "music", "notifications":
				default:
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		isDocumentation := strings.HasSuffix(strings.ToLower(rel), ".info")
		if CheckDialect(rel) == "" && !isDocumentation {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > CheckMaxFileBytes {
			return fmt.Errorf("corpus file %s exceeds the 32 MiB text limit", rel)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%d:%s:%d:", len(rel), rel, len(data))
		hash.Write(data)
		if isDocumentation {
			r.DocumentationFiles++
			entries, err := parseSchemaBytes(rel, data)
			if err != nil {
				return err
			}
			fields := []string{}
			for _, entry := range entries {
				fields = append(fields, entry.field)
			}
			r.DocumentationFields[rel] = fields
			return nil
		}
		r.Files++
		r.Bytes += int64(len(data))
		checked, parsed := c.checkText(rel, string(data), false, SourceRoleGame)
		r.Errors += checked.Errors
		r.Warnings += checked.Warnings
		family := filepath.ToSlash(filepath.Dir(rel))
		// Event subdirectories carry namespaces and DLC labels, not new loaders.
		if strings.HasPrefix(family, "events/") {
			family = "events"
		}
		f := r.Families[family]
		if f == nil {
			f = &DSLFamily{ObjectType: objectTypeForPath(rel), Fields: map[string]*DSLFieldObservation{}}
			r.Families[family] = f
		}
		f.Files++
		f.Errors += checked.Errors
		for _, d := range checked.Diagnostics {
			count := r.Diagnostics[d.Code]
			if count == nil {
				count = &DSLDiagnosticCount{}
				r.Diagnostics[d.Code] = count
			}
			count.Count++
			if len(count.Examples) < 128 {
				count.Examples = append(count.Examples, d)
			}
		}
		var visit func([]*script.Node)
		visit = func(nodes []*script.Node) {
			for _, n := range nodes {
				r.Nodes++
				f.Nodes++
				r.SyntaxFeatures["kind:"+n.Kind]++
				if n.Operator != "" {
					r.SyntaxFeatures["operator:"+n.Operator]++
				}
				if strings.HasPrefix(n.Value, "@[") {
					r.SyntaxFeatures["arithmetic_expression"]++
				}
				if strings.Contains(n.Key, "$") || strings.Contains(n.Value, "$") {
					r.SyntaxFeatures["macro_parameter"]++
				}
				if n.Depth == 1 && n.Key != "" && !strings.HasPrefix(n.Key, "@") {
					field := f.Fields[n.Key]
					if field == nil {
						field = &DSLFieldObservation{Shapes: map[string]int{}, ExamplePath: rel, ExampleLine: n.Line}
						f.Fields[n.Key] = field
					}
					field.Count++
					field.Shapes[n.Kind]++
				}
				visit(n.Children)
			}
		}
		visit(parsed.Nodes)
		return nil
	})
	if err != nil {
		return r, err
	}
	if r.Files == 0 {
		return r, fmt.Errorf("no supported CK3 text files found")
	}
	r.CorpusFingerprint = hex.EncodeToString(hash.Sum(nil))
	r.ElapsedSeconds = time.Since(start).Seconds()
	return r, nil
}
