package packager

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ck3-index/internal/indexer"
)

type IndexerValidator struct {
	DB *indexer.DB
}

func (v IndexerValidator) Validate(ctx context.Context, files []PreparedFile) (ValidationReport, error) {
	if v.DB == nil {
		return ValidationReport{}, fmt.Errorf("package validation requires an open ck3-index database")
	}
	patchFiles := make([]indexer.PatchFileInput, 0, len(files))
	var structured []Diagnostic
	for _, file := range files {
		if !indexedContentPath(file.Path) {
			continue
		}
		patchFiles = append(patchFiles, indexer.PatchFileInput{Path: file.Path, Content: string(file.Data)})
		analysis, err := indexer.AnalyzeVirtualFile(file.Path, "package", 1, string(file.Data))
		if err != nil {
			return ValidationReport{}, err
		}
		for _, diagnostic := range analysis.Diagnostics {
			structured = append(structured, Diagnostic{
				Severity: diagnostic.Severity, Code: diagnostic.Code, Message: diagnostic.Message,
				Path: diagnostic.Path, Line: diagnostic.Line, Column: diagnostic.Column,
				Confidence: diagnostic.Confidence,
			})
		}
	}
	if len(patchFiles) == 0 {
		return ValidationReport{Summary: "Package contains no indexable CK3 script or resource files."}, nil
	}
	preflight, err := v.DB.LLMPreflightPatch(ctx, patchFiles, indexer.LLMOptions{Limit: 20, AllowProject: true})
	if err != nil {
		return ValidationReport{}, err
	}
	report := ValidationReport{
		Summary: preflight.Summary, MissingLoc: append([]string(nil), preflight.MissingLocKeys...),
		MissingRes: append([]string(nil), preflight.MissingResources...), Counts: preflight.Counts,
		Diagnostics: structured,
	}
	appendPreflightDiagnostics(&report, preflight.Evidence)
	report.Blockers = preflight.Counts["blocking_risks"]
	report.Warnings = preflight.Counts["nonblocking_risks"]
	if len(report.MissingLoc) > 0 {
		report.Blockers += len(report.MissingLoc)
		report.Warnings -= min(len(report.MissingLoc), report.Warnings)
		for _, key := range report.MissingLoc {
			report.Diagnostics = append(report.Diagnostics, Diagnostic{
				Severity: "error", Code: "missing_localization", Message: "missing localization key " + key,
				Confidence: "high",
			})
		}
	}
	for _, resource := range report.MissingRes {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{
			Severity: "error", Code: "missing_resource", Message: "missing resource " + resource,
			Confidence: "high",
		})
	}
	for _, diagnostic := range structured {
		if strictDiagnostic(diagnostic) && diagnostic.Severity != "error" {
			report.Blockers++
			if report.Warnings > 0 {
				report.Warnings--
			}
		}
	}
	report.Blocked = report.Blockers > 0
	if report.Blocked {
		if !hasErrorDiagnostic(report.Diagnostics) {
			report.Diagnostics = append(report.Diagnostics, Diagnostic{
				Severity: "error", Code: "package_preflight_blocked", Message: preflight.Summary, Confidence: "high",
			})
		}
		report.Fixes = append(report.Fixes, "Resolve all release-blocking diagnostics before packaging again.")
		if len(report.MissingLoc) > 0 {
			report.Fixes = append(report.Fixes, "Add the missing localization keys to localization/<language>/*_l_<language>.yml files.")
		}
		if len(report.MissingRes) > 0 {
			report.Fixes = append(report.Fixes, "Add the missing resource files at the referenced gfx or sound paths.")
		}
	}
	sort.Slice(report.Diagnostics, func(i, j int) bool {
		if report.Diagnostics[i].Path != report.Diagnostics[j].Path {
			return report.Diagnostics[i].Path < report.Diagnostics[j].Path
		}
		if report.Diagnostics[i].Line != report.Diagnostics[j].Line {
			return report.Diagnostics[i].Line < report.Diagnostics[j].Line
		}
		return report.Diagnostics[i].Code < report.Diagnostics[j].Code
	})
	return report, nil
}

func appendPreflightDiagnostics(report *ValidationReport, evidence []indexer.LLMEvidence) {
	for _, item := range evidence {
		if item.Kind != "unresolved_patch_ref" {
			continue
		}
		refKind := strings.TrimSpace(strings.SplitN(item.Detail, " ", 2)[0])
		if refKind == "localization" || refKind == "resource" {
			continue
		}
		severity := "error"
		confidence := "high"
		code := "missing_object_reference"
		if refKind == "define" || refKind == "scope" {
			severity = "warning"
			confidence = "uncertain"
			code = "unresolved_" + refKind
		}
		report.Diagnostics = append(report.Diagnostics, Diagnostic{
			Severity: severity, Code: code, Message: item.Detail, Path: item.Path,
			Line: item.Line, Column: item.Column, Confidence: confidence,
		})
	}
}

func hasErrorDiagnostic(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if strings.EqualFold(diagnostic.Severity, "error") {
			return true
		}
	}
	return false
}

func indexedContentPath(rel string) bool {
	root := strings.ToLower(strings.SplitN(filepathSlash(rel), "/", 2)[0])
	return allowedRoots[root]
}

func strictDiagnostic(d Diagnostic) bool {
	if strings.EqualFold(d.Severity, "error") {
		return true
	}
	switch d.Code {
	case "scope_mismatch", "trigger_in_effect", "duplicate_object",
		"on_action_direct_override", "event_no_option", "gui_layout_misuse", "missing_event_loc",
		"lios_partial_override":
		return true
	default:
		return false
	}
}
