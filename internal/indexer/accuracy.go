package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AccuracyReport struct {
	Dir      string               `json:"dir"`
	Passed   int                  `json:"passed"`
	Failed   int                  `json:"failed"`
	Cases    []AccuracyCaseResult `json:"cases"`
	Failures []string             `json:"failures,omitempty"`
}

type AccuracyCaseResult struct {
	Name   string   `json:"name"`
	Pass   bool     `json:"pass"`
	Errors []string `json:"errors,omitempty"`
}

type accuracyCase struct {
	Name               string                       `json:"name"`
	Files              []accuracyFile               `json:"files"`
	ExpectDiagnostics  []accuracyDiagnosticExpect   `json:"expect_diagnostics,omitempty"`
	RejectDiagnostics  []accuracyDiagnosticExpect   `json:"reject_diagnostics,omitempty"`
	ExpectRefs         []accuracyRefExpect          `json:"expect_refs,omitempty"`
	RejectRefs         []accuracyRefExpect          `json:"reject_refs,omitempty"`
	ExpectPreflight    []accuracyPreflightExpect    `json:"expect_preflight,omitempty"`
	ExpectLocalization []accuracyLocalizationExpect `json:"expect_localization,omitempty"`
}

type accuracyFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type accuracyDiagnosticExpect struct {
	Code     string `json:"code"`
	Contains string `json:"contains,omitempty"`
}

type accuracyRefExpect struct {
	ID        string `json:"id"`
	Direction string `json:"direction,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Resolved  *bool  `json:"resolved,omitempty"`
}

type accuracyPreflightExpect struct {
	ID       string         `json:"id"`
	Counts   map[string]int `json:"counts,omitempty"`
	Contains []string       `json:"contains,omitempty"`
}

type accuracyLocalizationExpect struct {
	Key      string `json:"key"`
	Contains string `json:"contains,omitempty"`
}

func RunAccuracy(ctx context.Context, dir string) (AccuracyReport, error) {
	if dir == "" {
		dir = filepath.Join("testdata", "accuracy")
	}
	report := AccuracyReport{Dir: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return report, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		res := AccuracyCaseResult{Name: strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))}
		c, err := readAccuracyCase(path)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			report.Failed++
			report.Cases = append(report.Cases, res)
			report.Failures = append(report.Failures, res.Name)
			continue
		}
		if c.Name != "" {
			res.Name = c.Name
		}
		res.Errors = runAccuracyCase(ctx, c)
		res.Pass = len(res.Errors) == 0
		if res.Pass {
			report.Passed++
		} else {
			report.Failed++
			report.Failures = append(report.Failures, res.Name)
		}
		report.Cases = append(report.Cases, res)
	}
	return report, nil
}

func readAccuracyCase(path string) (accuracyCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return accuracyCase{}, err
	}
	var c accuracyCase
	if err := json.Unmarshal(data, &c); err != nil {
		return accuracyCase{}, err
	}
	if c.Name == "" {
		c.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if len(c.Files) == 0 {
		return accuracyCase{}, fmt.Errorf("accuracy case %s has no files", c.Name)
	}
	return c, nil
}

func runAccuracyCase(ctx context.Context, c accuracyCase) []string {
	tmp, err := os.MkdirTemp("", "ck3-index-accuracy-*")
	if err != nil {
		return []string{err.Error()}
	}
	defer os.RemoveAll(tmp)
	for _, f := range c.Files {
		rel, err := normalizePatchRelPath(f.Path)
		if err != nil {
			return []string{fmt.Sprintf("%s: %v", f.Path, err)}
		}
		full := filepath.Join(tmp, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return []string{err.Error()}
		}
		if err := os.WriteFile(full, []byte(f.Content), 0644); err != nil {
			return []string{err.Error()}
		}
	}
	cfgPath := filepath.Join(tmp, "ck3-index.toml")
	cfgText := `database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
`
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0644); err != nil {
		return []string{err.Error()}
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return []string{err.Error()}
	}
	if _, err := Scan(ctx, cfg); err != nil {
		return []string{err.Error()}
	}
	db, err := Open(filepath.Join(tmp, "cache", "test.sqlite"))
	if err != nil {
		return []string{err.Error()}
	}
	defer db.Close()
	var errs []string
	for _, ex := range c.ExpectDiagnostics {
		if !accuracyDiagnosticFound(ctx, db, ex) {
			errs = append(errs, fmt.Sprintf("expected diagnostic %s containing %q", ex.Code, ex.Contains))
		}
	}
	for _, ex := range c.RejectDiagnostics {
		if accuracyDiagnosticFound(ctx, db, ex) {
			errs = append(errs, fmt.Sprintf("rejected diagnostic %s containing %q was present", ex.Code, ex.Contains))
		}
	}
	for _, ex := range c.ExpectRefs {
		if !accuracyRefFound(ctx, db, ex) {
			errs = append(errs, fmt.Sprintf("expected ref id=%s direction=%s kind=%s name=%s", ex.ID, ex.Direction, ex.Kind, ex.Name))
		}
	}
	for _, ex := range c.RejectRefs {
		if accuracyRefFound(ctx, db, ex) {
			errs = append(errs, fmt.Sprintf("rejected ref id=%s direction=%s kind=%s name=%s was present", ex.ID, ex.Direction, ex.Kind, ex.Name))
		}
	}
	for _, ex := range c.ExpectPreflight {
		errs = append(errs, accuracyCheckPreflight(ctx, db, ex)...)
	}
	for _, ex := range c.ExpectLocalization {
		q, err := db.QueryLocalization(ctx, ex.Key)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		found := false
		for _, h := range q.Values {
			if ex.Contains == "" || strings.Contains(h.Value, ex.Contains) {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("expected localization %s containing %q", ex.Key, ex.Contains))
		}
	}
	return errs
}

func accuracyDiagnosticFound(ctx context.Context, db *DB, ex accuracyDiagnosticExpect) bool {
	diags, err := db.ExplainDiagnostic(ctx, ex.Code)
	if err != nil {
		return false
	}
	for _, d := range diags {
		text := d.Message + " " + d.Path
		if ex.Contains == "" || strings.Contains(text, ex.Contains) {
			return true
		}
	}
	return false
}

func accuracyRefFound(ctx context.Context, db *DB, ex accuracyRefExpect) bool {
	q, err := db.QueryRefs(ctx, ex.ID)
	if err != nil {
		return false
	}
	check := func(h RefHit) bool {
		if ex.Kind != "" && h.Kind != ex.Kind {
			return false
		}
		if ex.Name != "" && h.Name != ex.Name {
			return false
		}
		if ex.Resolved != nil && h.Resolved != *ex.Resolved {
			return false
		}
		return true
	}
	if ex.Direction == "" || ex.Direction == "incoming" {
		for _, h := range q.Incoming {
			if check(h) {
				return true
			}
		}
	}
	if ex.Direction == "" || ex.Direction == "outgoing" {
		for _, h := range q.Outgoing {
			if check(h) {
				return true
			}
		}
	}
	return false
}

func accuracyCheckPreflight(ctx context.Context, db *DB, ex accuracyPreflightExpect) []string {
	r, err := db.LLMPreflight(ctx, ex.ID, LLMOptions{AllowProject: true, Limit: 20})
	if err != nil {
		return []string{err.Error()}
	}
	var errs []string
	for key, want := range ex.Counts {
		if got := r.Counts[key]; got != want {
			errs = append(errs, fmt.Sprintf("preflight %s count %s: got %d want %d", ex.ID, key, got, want))
		}
	}
	blob := r.Summary
	for _, ev := range r.Evidence {
		blob += "\n" + ev.Detail + "\n" + ev.Name + "\n" + ev.Suggestion
	}
	for _, want := range ex.Contains {
		if !strings.Contains(blob, want) {
			errs = append(errs, fmt.Sprintf("preflight %s missing %q", ex.ID, want))
		}
	}
	return errs
}
