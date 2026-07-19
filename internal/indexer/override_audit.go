package indexer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/script"
)

// OverrideDriftAuditOptions selects a configured source layer and the lower
// precedence layer it should be compared against. It never accepts arbitrary
// filesystem roots: both names must come from Config.Sources.
type OverrideDriftAuditOptions struct {
	Source     string `json:"source,omitempty"`
	Base       string `json:"base,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// OverrideDriftAudit is a read-only, block-level explanation of CK3's
// file-replacement override model. It intentionally does not claim that a
// source-only or base-only definition is a bug, and it never writes a patch.
type OverrideDriftAudit struct {
	Intent     string              `json:"intent"`
	Status     string              `json:"status"`
	Source     string              `json:"source"`
	SourceRank int                 `json:"source_rank"`
	Base       string              `json:"base,omitempty"`
	BaseRank   int                 `json:"base_rank,omitempty"`
	PathPrefix string              `json:"path_prefix,omitempty"`
	Counts     map[string]int      `json:"counts"`
	Findings   []OverrideDriftFile `json:"findings,omitempty"`
	Truncated  bool                `json:"truncated,omitempty"`
	Guidance   []string            `json:"guidance"`
}

// OverrideDriftFile summarizes a single source file. Paths are source-root
// relative; physical source paths and raw script content are never exposed.
type OverrideDriftFile struct {
	Classification     string               `json:"classification"`
	Path               string               `json:"path"`
	Source             string               `json:"source"`
	Base               string               `json:"base,omitempty"`
	SourceEntries      int                  `json:"source_entries,omitempty"`
	BaseEntries        int                  `json:"base_entries,omitempty"`
	UnsupportedEntries int                  `json:"unsupported_entries,omitempty"`
	SourceParseErrors  int                  `json:"source_parse_errors,omitempty"`
	BaseParseErrors    int                  `json:"base_parse_errors,omitempty"`
	Changes            []OverrideDriftBlock `json:"changes,omitempty"`
	ChangesTruncated   bool                 `json:"changes_truncated,omitempty"`
}

// OverrideDriftBlock is a conservative pairing result for one unique,
// top-level key. Duplicate keys are reported as ambiguous rather than matched.
type OverrideDriftBlock struct {
	Key               string `json:"key"`
	Classification    string `json:"classification"`
	SourceLine        int    `json:"source_line,omitempty"`
	SourceEndLine     int    `json:"source_end_line,omitempty"`
	BaseLine          int    `json:"base_line,omitempty"`
	BaseEndLine       int    `json:"base_end_line,omitempty"`
	SourceOccurrences int    `json:"source_occurrences,omitempty"`
	BaseOccurrences   int    `json:"base_occurrences,omitempty"`
}

type overrideAuditFileInput struct {
	Source Source
	Path   string
	Rel    string
}

const overrideAuditBlockLimit = 100

// AuditOverrideDrift implements the safe/read-only part of cwtools' Vanilla
// Compare idea for CK3-index's source ordering. It compares project to the
// nearest lower-precedence configured source with the same logical path, then
// uses canonical AST hashes to separate structural change from formatting-only
// churn. P0 deliberately covers common/ and events/ .txt scripts only.
func AuditOverrideDrift(ctx context.Context, cfg Config, options OverrideDriftAuditOptions) (OverrideDriftAudit, error) {
	if err := validateSources(cfg.Sources); err != nil {
		return OverrideDriftAudit{}, err
	}
	source, err := overrideAuditSource(cfg, options.Source)
	if err != nil {
		return OverrideDriftAudit{}, err
	}
	pathPrefix := ""
	if strings.TrimSpace(options.PathPrefix) != "" {
		pathPrefix, err = normalizePatchRelPath(options.PathPrefix)
		if err != nil {
			return OverrideDriftAudit{}, fmt.Errorf("override audit path: %w", err)
		}
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	baseCandidates, err := overrideAuditBaseCandidates(cfg, source, options.Base)
	if err != nil {
		return OverrideDriftAudit{}, err
	}
	report := OverrideDriftAudit{
		Intent: "override_drift_audit",
		Status: "ok",
		Source: source.Name, SourceRank: source.Rank,
		PathPrefix: pathPrefix,
		Counts:     map[string]int{},
		Guidance: []string{
			"CK3 replaces files by path. Source-only and base-only definitions may be intentional; this is drift evidence, not an automatic bug report.",
			"Only unique top-level assignments in common/ and events/ .txt files are paired. Duplicate keys, anonymous entries, and unsupported syntax stay ambiguous or unsupported.",
			"semantic_changed ignores comments and whitespace through a canonical AST hash. This audit never writes, merges, or migrates files.",
		},
	}
	if len(baseCandidates) > 0 {
		report.Base, report.BaseRank = baseCandidates[0].Name, baseCandidates[0].Rank
	} else {
		report.Status = "unavailable"
		report.Guidance = append(report.Guidance, "No lower-precedence configured base source is available for the selected source.")
		return report, nil
	}

	files, err := collectOverrideAuditFiles(ctx, source, pathPrefix)
	if err != nil {
		return report, err
	}
	report.Counts["source_files"] = len(files)
	var all []OverrideDriftFile
	for _, input := range files {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		base, basePath := overrideAuditBaseFile(baseCandidates, input.Rel)
		if basePath == "" {
			report.Counts["base_missing_files"]++
			all = append(all, OverrideDriftFile{Classification: "base_missing", Path: input.Rel, Source: source.Name})
			continue
		}
		result, err := auditOverrideFile(ctx, input, base, basePath)
		if err != nil {
			return report, err
		}
		switch result.Classification {
		case "identical":
			report.Counts["identical_files"]++
		case "format_only":
			report.Counts["format_only_files"]++
			all = append(all, result)
		case "semantic_drift":
			report.Counts["semantic_drift_files"]++
			for _, change := range result.Changes {
				report.Counts[change.Classification]++
			}
			all = append(all, result)
		default:
			report.Counts["unsupported_files"]++
			all = append(all, result)
		}
	}

	sort.Slice(all, func(i, j int) bool {
		leftPriority := overrideDriftPriority(all[i].Classification)
		rightPriority := overrideDriftPriority(all[j].Classification)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return all[i].Path < all[j].Path
	})
	if len(all) > limit {
		report.Truncated = true
		all = all[:limit]
	}
	report.Findings = all
	return report, nil
}

func overrideAuditSource(cfg Config, requested string) (Source, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for _, source := range cfg.Sources {
			if strings.EqualFold(source.Name, requested) {
				return source, nil
			}
		}
		return Source{}, fmt.Errorf("override audit source %q is not configured", requested)
	}
	for _, source := range cfg.Sources {
		if strings.EqualFold(source.Name, "project") {
			return source, nil
		}
	}
	sources := append([]Source(nil), cfg.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Rank < sources[j].Rank })
	return sources[0], nil
}

func overrideAuditBaseCandidates(cfg Config, source Source, requested string) ([]Source, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for _, candidate := range cfg.Sources {
			if strings.EqualFold(candidate.Name, requested) {
				if candidate.Rank <= source.Rank {
					return nil, fmt.Errorf("override audit base %q must have lower precedence than source %q", requested, source.Name)
				}
				return []Source{candidate}, nil
			}
		}
		return nil, fmt.Errorf("override audit base %q is not configured", requested)
	}
	var out []Source
	for _, candidate := range cfg.Sources {
		if candidate.Rank > source.Rank {
			out = append(out, candidate)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out, nil
}

func collectOverrideAuditFiles(ctx context.Context, source Source, pathPrefix string) ([]overrideAuditFileInput, error) {
	var out []overrideAuditFileInput
	err := filepath.WalkDir(source.Path, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source.Path, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if shouldPruneSourceDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isOverrideAuditScriptPath(rel) || !overrideAuditPathMatches(rel, pathPrefix) {
			return nil
		}
		out = append(out, overrideAuditFileInput{Source: source, Path: path, Rel: rel})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

func isOverrideAuditScriptPath(rel string) bool {
	rel = filepathSlash(rel)
	if !strings.HasSuffix(rel, ".txt") || classifyRel(rel) != "script" {
		return false
	}
	return strings.HasPrefix(rel, "common/") || strings.HasPrefix(rel, "events/")
}

func overrideAuditPathMatches(rel, prefix string) bool {
	return prefix == "" || rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

func overrideAuditBaseFile(candidates []Source, rel string) (Source, string) {
	for _, source := range candidates {
		path := filepath.Join(source.Path, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return source, path
		}
	}
	return Source{}, ""
}

func auditOverrideFile(ctx context.Context, input overrideAuditFileInput, base Source, basePath string) (OverrideDriftFile, error) {
	sourceData, err := os.ReadFile(input.Path)
	if err != nil {
		return OverrideDriftFile{}, err
	}
	baseData, err := os.ReadFile(basePath)
	if err != nil {
		return OverrideDriftFile{}, err
	}
	result := OverrideDriftFile{Path: input.Rel, Source: input.Source.Name, Base: base.Name}
	if bytes.Equal(sourceData, baseData) {
		result.Classification = "identical"
		return result, nil
	}
	sourceParsed := script.Parse(string(sourceData))
	baseParsed := script.Parse(string(baseData))
	result.SourceParseErrors = len(sourceParsed.Errors)
	result.BaseParseErrors = len(baseParsed.Errors)
	if result.SourceParseErrors > 0 || result.BaseParseErrors > 0 {
		result.Classification = "unsupported"
		return result, nil
	}
	sourceEntries, sourceUnsupported := overrideAuditEntries(sourceParsed.Nodes)
	baseEntries, baseUnsupported := overrideAuditEntries(baseParsed.Nodes)
	result.SourceEntries = len(sourceEntries)
	result.BaseEntries = len(baseEntries)
	result.UnsupportedEntries = sourceUnsupported + baseUnsupported
	if len(sourceEntries) == 0 || len(baseEntries) == 0 {
		result.Classification = "unsupported"
		return result, nil
	}

	keys := map[string]bool{}
	for key := range sourceEntries {
		keys[key] = true
	}
	for key := range baseEntries {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	semanticChanges := 0
	for _, key := range ordered {
		if err := ctx.Err(); err != nil {
			return OverrideDriftFile{}, err
		}
		sourceNodes := sourceEntries[key]
		baseNodes := baseEntries[key]
		change := OverrideDriftBlock{Key: key, SourceOccurrences: len(sourceNodes), BaseOccurrences: len(baseNodes)}
		if len(sourceNodes) > 0 {
			change.SourceLine, change.SourceEndLine = sourceNodes[0].Line, sourceNodes[0].EndLine
		}
		if len(baseNodes) > 0 {
			change.BaseLine, change.BaseEndLine = baseNodes[0].Line, baseNodes[0].EndLine
		}
		switch {
		case len(sourceNodes) > 1 || len(baseNodes) > 1:
			change.Classification = "ambiguous_definition"
		case len(sourceNodes) == 0:
			change.Classification = "base_only_definition"
		case len(baseNodes) == 0:
			change.Classification = "source_only_definition"
		case canonicalOverrideNodeHash(sourceNodes[0]) != canonicalOverrideNodeHash(baseNodes[0]):
			change.Classification = "semantic_changed"
		default:
			continue
		}
		semanticChanges++
		if len(result.Changes) < overrideAuditBlockLimit {
			result.Changes = append(result.Changes, change)
		} else {
			result.ChangesTruncated = true
		}
	}
	if semanticChanges == 0 && result.UnsupportedEntries == 0 {
		result.Classification = "format_only"
		return result, nil
	}
	result.Classification = "semantic_drift"
	return result, nil
}

func overrideAuditEntries(nodes []*script.Node) (map[string][]*script.Node, int) {
	out := map[string][]*script.Node{}
	unsupported := 0
	for _, node := range nodes {
		if !isStableOverrideAuditEntry(node) {
			unsupported++
			continue
		}
		out[node.Key] = append(out[node.Key], node)
	}
	return out, unsupported
}

func isStableOverrideAuditEntry(node *script.Node) bool {
	if node == nil || node.Key == "" || node.Operator != "=" || (node.Kind != "block" && node.Kind != "atom") {
		return false
	}
	// Date blocks in history are ordered timeline statements, not unique
	// definitions. History is excluded in P0, but retain this guard if callers
	// reuse the pairing primitive later.
	parts := strings.Split(node.Key, ".")
	if len(parts) == 3 {
		allNumeric := true
		for _, part := range parts {
			if part == "" {
				allNumeric = false
				break
			}
			for _, r := range part {
				if r < '0' || r > '9' {
					allNumeric = false
					break
				}
			}
		}
		if allNumeric {
			return false
		}
	}
	return true
}

func canonicalOverrideNodeHash(node *script.Node) [32]byte {
	hash := sha256.New()
	writeCanonicalOverrideNode(hash, node)
	var out [32]byte
	copy(out[:], hash.Sum(nil))
	return out
}

func writeCanonicalOverrideNode(hash interface{ Write([]byte) (int, error) }, node *script.Node) {
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00", node.Kind, node.Key, node.Operator, node.Value)
	for _, child := range node.Children {
		writeCanonicalOverrideNode(hash, child)
	}
	_, _ = hash.Write([]byte{0xff})
}

func overrideDriftPriority(classification string) int {
	switch classification {
	case "semantic_drift":
		return 0
	case "unsupported":
		return 1
	case "base_missing":
		return 2
	case "format_only":
		return 3
	default:
		return 4
	}
}
