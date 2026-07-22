package indexer

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// OnActionScopeContractBinding is documentation evidence immediately adjacent
// to a vanilla on_action declaration. It is intentionally not a validator
// rule: comments describe useful context, but they do not reliably model the
// engine's implicit root scope.
type OnActionScopeContractBinding struct {
	Target      string `json:"target"`
	Kind        string `json:"kind"`
	Scope       string `json:"scope,omitempty"`
	Description string `json:"description,omitempty"`
	Line        int    `json:"line"`
	Confidence  string `json:"confidence"`
}

// OnActionScopeContract combines one top-level vanilla on_action declaration
// with the comment bindings directly above it. Path is always source-root
// relative so CLI output remains portable and safe to share.
type OnActionScopeContract struct {
	Name              string                         `json:"name"`
	Source            string                         `json:"source"`
	Path              string                         `json:"path"`
	Line              int                            `json:"line"`
	Bindings          []OnActionScopeContractBinding `json:"bindings"`
	EngineInputScopes []string                       `json:"engine_input_scopes,omitempty"`
	RootComparison    string                         `json:"root_comparison"`
	Confidence        string                         `json:"confidence"`
}

// OnActionScopeContractFinding is review evidence only. Neither this audit nor
// its findings change scope checking or diagnostics.
type OnActionScopeContractFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

// OnActionScopeContractAudit reports the overlap between vanilla documentation
// comments and the cached on_actions.log root-scope evidence. It is a bounded,
// read-only evidence report, not a source of automatic validation rules.
type OnActionScopeContractAudit struct {
	Intent                  string                         `json:"intent"`
	Status                  string                         `json:"status"`
	CommentSource           string                         `json:"comment_source,omitempty"`
	EngineEvidenceAvailable bool                           `json:"engine_evidence_available"`
	EngineRuleFingerprint   string                         `json:"engine_rule_fingerprint,omitempty"`
	FilesScanned            int                            `json:"files_scanned"`
	OnActionsFound          int                            `json:"on_actions_found"`
	DocumentedOnActions     int                            `json:"documented_on_actions"`
	DocumentedBindings      int                            `json:"documented_bindings"`
	ComparableRootBindings  int                            `json:"comparable_root_bindings"`
	RootMatches             int                            `json:"root_matches"`
	ReviewCount             int                            `json:"review_count"`
	Contracts               []OnActionScopeContract        `json:"contracts,omitempty"`
	Findings                []OnActionScopeContractFinding `json:"findings,omitempty"`
	Truncated               bool                           `json:"truncated,omitempty"`
	Guidance                []string                       `json:"guidance"`
}

// OnActionDocumentationContract is the bounded MCP-safe projection of the
// adjacent vanilla comments for one on_action. It keeps comment evidence
// separate from live engine rules: no field here is a generated scope rule or
// a diagnostic input.
type OnActionDocumentationContract struct {
	Status                  string                           `json:"status"`
	EvidenceKind            string                           `json:"evidence_kind"`
	ReviewOnly              bool                             `json:"review_only"`
	Selection               string                           `json:"selection"`
	EngineEvidenceAvailable bool                             `json:"engine_evidence_available"`
	Candidates              []OnActionDocumentationCandidate `json:"candidates,omitempty"`
	Truncated               bool                             `json:"truncated,omitempty"`
	Guidance                []string                         `json:"guidance"`
}

// OnActionDocumentationCandidate intentionally omits the comment prose and
// configured source identity. The source reference is only source-root-
// relative vanilla location evidence.
type OnActionDocumentationCandidate struct {
	SourceRef           *OnActionDocumentationSourceRef `json:"source_ref,omitempty"`
	Root                OnActionDocumentedRoot          `json:"root"`
	Bindings            []OnActionDocumentedBinding     `json:"bindings,omitempty"`
	DynamicBindingCount int                             `json:"dynamic_binding_count,omitempty"`
	BindingsTruncated   bool                            `json:"bindings_truncated,omitempty"`
	EngineRuleFound     bool                            `json:"engine_rule_found"`
	ReviewStatus        string                          `json:"review_status"`
	Confidence          string                          `json:"confidence"`
}

type OnActionDocumentationSourceRef struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// OnActionDocumentedRoot records only a parser-recognized literal type. It
// never represents an inferred or effective engine scope.
type OnActionDocumentedRoot struct {
	Status         string `json:"status"`
	DocumentedType string `json:"documented_type,omitempty"`
	Confidence     string `json:"confidence"`
}

// OnActionDocumentedBinding is deliberately narrower than the internal
// comment binding: prose, raw dynamic names, and inferred scope names are not
// exposed through MCP.
type OnActionDocumentedBinding struct {
	Name           string `json:"name"`
	ValueKind      string `json:"value_kind"`
	DocumentedType string `json:"documented_type,omitempty"`
	Confidence     string `json:"confidence"`
}

type vanillaOnActionCommentScan struct {
	Files     int
	OnActions int
	Contracts []OnActionScopeContract
}

type onActionCommentLine struct {
	Text string
	Line int
}

var topLevelOnActionDefinition = regexp.MustCompile(`^\s*([A-Za-z0-9_]+)\s*=\s*\{`)

const (
	defaultOnActionDocumentationCandidates = 8
	maxOnActionDocumentationCandidates     = 8
	maxOnActionDocumentationBindings       = 32
)

// LookupOnActionDocumentationContract returns adjacent vanilla on_action
// comments as review-only evidence. A ready index locates the exact vanilla
// file first; an unpublished index deliberately falls back to a source scan
// instead of reading partially published rows.
func (db *DB) LookupOnActionDocumentationContract(ctx context.Context, cfg Config, key string, limit int) (OnActionDocumentationContract, error) {
	result := OnActionDocumentationContract{
		Status:       "unavailable",
		EvidenceKind: "vanilla_adjacent_top_level_comments",
		ReviewOnly:   true,
		Selection:    "none",
		Guidance: []string{
			"Vanilla comment evidence is review-only and never creates scope diagnostics.",
			"Live engine on_action rules remain authoritative; documented types are not inferred effective scopes.",
			"Only parser-recognized adjacent top-level comments are included; prose and dynamic binding names are omitted.",
			"engine_evidence_available describes the published engine-log layer; each candidate separately states whether that layer has a rule for this key.",
		},
	}
	game, ok := GameSource(cfg)
	if !ok {
		result.Guidance = append(result.Guidance, "A configured game source is not available, so documentation evidence is unavailable.")
		return result, nil
	}
	limit = boundedOnActionDocumentationLimit(limit)

	state, stateErr := db.IndexState(ctx)
	indexReady := stateErr == nil && state.Ready()
	engineScopes := []string(nil)
	if indexReady {
		engineAvailable, err := db.hasOnActionEngineEvidence(ctx)
		if err == nil && engineAvailable {
			live, lookupErr := db.LookupOnActionEvidence(ctx, key)
			if lookupErr == nil {
				result.EngineEvidenceAvailable = true
				engineScopes = onActionEvidenceInputScopes(live)
			}
		}
	}

	var contracts []OnActionScopeContract
	locatedWithReadyIndex := false
	if indexReady {
		paths, err := db.onActionDocumentationPaths(ctx, game, key)
		if err == nil {
			// An indexed locator miss is already a bounded answer: do not walk
			// every vanilla on_action file for repeatedly queried unknown or
			// undocumented keys. Fall back only when the live index has not been
			// published or its locator itself failed.
			contracts = make([]OnActionScopeContract, 0)
			if len(paths) > 0 {
				contracts, err = scanOnActionDocumentationPaths(ctx, game, key, paths)
			}
			if err == nil {
				locatedWithReadyIndex = true
			} else if ctx.Err() != nil {
				return result, err
			}
		}
	}
	if !locatedWithReadyIndex {
		scan, err := scanVanillaOnActionComments(ctx, game)
		if err != nil {
			if ctx.Err() != nil {
				return result, err
			}
			result.Guidance = append(result.Guidance, "Vanilla on_action comments could not be read for this query.")
			return result, nil
		}
		contracts = onActionContractsNamed(scan.Contracts, key)
	}

	sort.Slice(contracts, func(i, j int) bool {
		if contracts[i].Path != contracts[j].Path {
			return contracts[i].Path < contracts[j].Path
		}
		return contracts[i].Line < contracts[j].Line
	})
	if len(contracts) == 0 {
		result.Status = "not_documented"
		result.Guidance = append(result.Guidance, "No parser-recognized adjacent vanilla comment contract was found for this on_action.")
		return result, nil
	}
	if len(contracts) == 1 {
		result.Status = "documented"
		result.Selection = "unique"
	} else {
		result.Status = "ambiguous"
		result.Selection = "multiple"
	}
	if len(contracts) > limit {
		result.Truncated = true
		contracts = contracts[:limit]
	}
	result.Candidates = make([]OnActionDocumentationCandidate, 0, len(contracts))
	for _, contract := range contracts {
		result.Candidates = append(result.Candidates, projectOnActionDocumentationCandidate(contract, engineScopes, result.EngineEvidenceAvailable))
	}
	return result, nil
}

func boundedOnActionDocumentationLimit(limit int) int {
	if limit <= 0 {
		return defaultOnActionDocumentationCandidates
	}
	if limit > maxOnActionDocumentationCandidates {
		return maxOnActionDocumentationCandidates
	}
	return limit
}

func (db *DB) hasOnActionEngineEvidence(ctx context.Context) (bool, error) {
	var exists int
	err := db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM engine_scope_rules WHERE rule_kind='on_action')`).Scan(&exists)
	return exists != 0, err
}

// onActionDocumentationPaths uses indexed, source-root-relative definition
// paths only after the index has published. It is a fast locator, not a
// second source of documentation truth.
func (db *DB) onActionDocumentationPaths(ctx context.Context, game Source, key string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT DISTINCT path FROM objects
		WHERE object_type='on_action' AND lower(name)=lower(?)
		AND source_rank=? AND lower(source_name)=lower(?)
		AND lower(path) LIKE 'common/on_action/%'
		ORDER BY path`, key, game.Rank, game.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if _, rel, ok := resolveVanillaOnActionCommentPath(game, path); ok {
			paths = append(paths, rel)
		}
	}
	return paths, rows.Err()
}

func scanOnActionDocumentationPaths(ctx context.Context, game Source, key string, paths []string) ([]OnActionScopeContract, error) {
	var contracts []OnActionScopeContract
	for _, path := range paths {
		physical, rel, ok := resolveVanillaOnActionCommentPath(game, path)
		if !ok {
			continue
		}
		parsed, _, err := scanOnActionCommentFile(ctx, physical, rel, game.Name)
		if err != nil {
			return nil, err
		}
		contracts = append(contracts, onActionContractsNamed(parsed, key)...)
	}
	return contracts, nil
}

func resolveVanillaOnActionCommentPath(source Source, raw string) (string, string, bool) {
	rel := filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw)))
	if rel == "." || filepath.IsAbs(rel) {
		return "", "", false
	}
	logical := filepath.ToSlash(rel)
	lower := strings.ToLower(logical)
	if strings.HasPrefix(logical, "../") || !strings.HasPrefix(lower, "common/on_action/") || !strings.HasSuffix(lower, ".txt") {
		return "", "", false
	}
	root, err := filepath.Abs(source.Path)
	if err != nil {
		return "", "", false
	}
	physical, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		return "", "", false
	}
	contained, err := filepath.Rel(root, physical)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return physical, logical, true
}

func onActionContractsNamed(contracts []OnActionScopeContract, key string) []OnActionScopeContract {
	var out []OnActionScopeContract
	for _, contract := range contracts {
		if strings.EqualFold(contract.Name, strings.TrimSpace(key)) {
			out = append(out, contract)
		}
	}
	return out
}

func onActionEvidenceInputScopes(rules []ScopeEvidence) []string {
	seen := map[string]bool{}
	for _, rule := range rules {
		for _, scope := range rule.InputScopes {
			seen[scope] = true
		}
	}
	out := make([]string, 0, len(seen))
	for scope := range seen {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

func projectOnActionDocumentationCandidate(contract OnActionScopeContract, engineScopes []string, engineAvailable bool) OnActionDocumentationCandidate {
	result := OnActionDocumentationCandidate{
		SourceRef:       &OnActionDocumentationSourceRef{Path: contract.Path, Line: contract.Line},
		Root:            OnActionDocumentedRoot{Status: "not_documented", Confidence: "low"},
		EngineRuleFound: engineAvailable && len(engineScopes) > 0,
		Confidence:      "low",
	}
	roots := onActionRootBindings(contract.Bindings)
	switch len(roots) {
	case 0:
		result.ReviewStatus = "not_documented"
	case 1:
		root := roots[0]
		switch root.Kind {
		case "scope":
			result.Root = OnActionDocumentedRoot{Status: "explicit", DocumentedType: root.Scope, Confidence: root.Confidence}
		case "none":
			result.Root = OnActionDocumentedRoot{Status: "none", Confidence: root.Confidence}
		default:
			result.Root = OnActionDocumentedRoot{Status: "unresolved", Confidence: "low"}
		}
		result.ReviewStatus, result.Confidence = compareDocumentedOnActionRoot(root, true, engineScopes, engineAvailable)
	default:
		result.Root = OnActionDocumentedRoot{Status: "ambiguous", Confidence: "low"}
		result.ReviewStatus = "ambiguous_documented_root"
	}
	for _, binding := range contract.Bindings {
		if binding.Target == "root" {
			continue
		}
		if strings.ContainsAny(binding.Target, "<>") {
			result.DynamicBindingCount++
			continue
		}
		projected := projectOnActionDocumentedBinding(binding)
		if len(result.Bindings) >= maxOnActionDocumentationBindings {
			result.BindingsTruncated = true
			continue
		}
		result.Bindings = append(result.Bindings, projected)
	}
	return result
}

func onActionRootBindings(bindings []OnActionScopeContractBinding) []OnActionScopeContractBinding {
	var roots []OnActionScopeContractBinding
	for _, binding := range bindings {
		if binding.Target == "root" {
			roots = append(roots, binding)
		}
	}
	return roots
}

func projectOnActionDocumentedBinding(binding OnActionScopeContractBinding) OnActionDocumentedBinding {
	projected := OnActionDocumentedBinding{Name: binding.Target, Confidence: binding.Confidence}
	switch binding.Kind {
	case "scope":
		projected.ValueKind = "documented_type"
		projected.DocumentedType = binding.Scope
	case "none":
		projected.ValueKind = "none"
	case "unknown":
		projected.ValueKind = "unresolved"
	default:
		projected.ValueKind = binding.Kind
	}
	return projected
}

// AuditOnActionScopeContracts compares only comment blocks directly adjacent
// to top-level vanilla on_action declarations. It deliberately leaves prose
// such as "root is the owner" unresolved rather than guessing a scope type.
func (db *DB) AuditOnActionScopeContracts(ctx context.Context, cfg Config, limit int) (OnActionScopeContractAudit, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	report := OnActionScopeContractAudit{
		Intent: "on_action_scope_contract_audit",
		Status: "ok",
		Guidance: []string{
			"Vanilla comments are documentation evidence only; this audit does not add scope diagnostics or mutate rules.",
			"Expected Scope: none describes the implicit root only. Named scopes documented beside an on_action remain useful and are not contradicted by none.",
			"Only direct values such as root = character are comparable automatically. Narrative prose and flag/list/value bindings stay unresolved for human review.",
		},
	}
	state, err := db.IndexState(ctx)
	if err != nil {
		return report, err
	}
	if !state.Ready() {
		return report, fmt.Errorf("on_action scope contract audit requires a ready published index; current scan status is %q", state.Status)
	}

	game, ok := GameSource(cfg)
	if !ok {
		report.Status = "unavailable"
		report.Guidance = append(report.Guidance, "No game source is configured, so vanilla on_action comments cannot be inspected.")
		return report, nil
	}
	report.CommentSource = game.Name
	scan, err := scanVanillaOnActionComments(ctx, game)
	if err != nil {
		if os.IsNotExist(err) {
			report.Status = "unavailable"
			report.Guidance = append(report.Guidance, "The configured game source has no common/on_action directory.")
			return report, nil
		}
		return report, err
	}
	report.FilesScanned = scan.Files
	report.OnActionsFound = scan.OnActions
	report.DocumentedOnActions = len(scan.Contracts)

	fingerprint, err := db.metaValue(ctx, "engine_data_fingerprint")
	if err != nil {
		return report, err
	}
	engineScopes, err := db.onActionEngineScopes(ctx)
	if err != nil {
		return report, err
	}
	report.EngineRuleFingerprint = fingerprint
	// A bundle may have a valid fingerprint even when optional on_actions.log
	// was absent. Only actual cached on_action rows are live comparison evidence.
	report.EngineEvidenceAvailable = fingerprint != "" && fingerprint != noEngineDataFingerprint && len(engineScopes) > 0

	all := make([]OnActionScopeContract, 0, len(scan.Contracts))
	for _, contract := range scan.Contracts {
		report.DocumentedBindings += len(contract.Bindings)
		root, hasRoot := contractRootBinding(contract.Bindings)
		if report.EngineEvidenceAvailable {
			contract.EngineInputScopes = engineScopes[strings.ToLower(contract.Name)]
		}
		contract.RootComparison, contract.Confidence = compareDocumentedOnActionRoot(root, hasRoot, contract.EngineInputScopes, report.EngineEvidenceAvailable)
		if hasRoot && (root.Kind == "scope" || root.Kind == "none") {
			report.ComparableRootBindings++
		}
		if contract.RootComparison == "match" {
			report.RootMatches++
		}
		if _, ok := onActionContractReviewFinding(contract, root, hasRoot); ok {
			report.ReviewCount++
		}
		all = append(all, contract)
	}

	sort.Slice(all, func(i, j int) bool {
		leftReview := isOnActionContractReview(all[i].RootComparison)
		rightReview := isOnActionContractReview(all[j].RootComparison)
		if leftReview != rightReview {
			return leftReview
		}
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].Path < all[j].Path
	})
	if len(all) > limit {
		report.Truncated = true
		all = all[:limit]
	}
	report.Contracts = all
	for _, contract := range all {
		root, hasRoot := contractRootBinding(contract.Bindings)
		if finding, ok := onActionContractReviewFinding(contract, root, hasRoot); ok {
			report.Findings = append(report.Findings, finding)
		}
	}
	return report, nil
}

func (db *DB) onActionEngineScopes(ctx context.Context) (map[string][]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT name,COALESCE(input_scopes,'') FROM engine_scope_rules WHERE rule_kind='on_action' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var name, scopes string
		if err := rows.Scan(&name, &scopes); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = splitScopesWithNone(scopes, true)
	}
	return out, rows.Err()
}

func scanVanillaOnActionComments(ctx context.Context, source Source) (vanillaOnActionCommentScan, error) {
	if err := validateSourceRoots([]Source{source}); err != nil {
		return vanillaOnActionCommentScan{}, err
	}
	root := filepath.Join(source.Path, "common", "on_action")
	result := vanillaOnActionCommentScan{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("on_action documentation contains symbolic link %s", filepath.Base(path))
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".txt") {
			return nil
		}
		rel, err := filepath.Rel(source.Path, path)
		if err != nil {
			return err
		}
		contracts, count, err := scanOnActionCommentFile(ctx, path, filepath.ToSlash(rel), source.Name)
		if err != nil {
			return err
		}
		result.Files++
		result.OnActions += count
		result.Contracts = append(result.Contracts, contracts...)
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func scanOnActionCommentFile(ctx context.Context, path, rel, source string) ([]OnActionScopeContract, int, error) {
	if _, err := sourceRegularFileInfo(path); err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	depth := 0
	lineNumber := 0
	onActions := 0
	var pending []onActionCommentLine
	var contracts []OnActionScopeContract
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, onActions, err
		}
		lineNumber++
		code, comment := splitPDXComment(scanner.Text())
		trimmedCode := strings.TrimSpace(code)
		trimmedComment := strings.TrimSpace(comment)
		if depth == 0 {
			switch {
			case trimmedCode == "" && trimmedComment != "":
				pending = append(pending, onActionCommentLine{Text: trimmedComment, Line: lineNumber})
			case trimmedCode != "":
				if match := topLevelOnActionDefinition.FindStringSubmatch(code); len(match) == 2 {
					onActions++
					bindings := parseOnActionCommentBindings(pending)
					if len(bindings) > 0 {
						contracts = append(contracts, OnActionScopeContract{
							Name: match[1], Source: source, Path: rel, Line: lineNumber, Bindings: bindings,
						})
					}
				}
				pending = nil
			}
		}
		depth += pdxBraceDelta(code)
		if depth < 0 {
			depth = 0
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, onActions, err
	}
	return contracts, onActions, nil
}

func splitPDXComment(line string) (string, string) {
	inQuote := false
	escaped := false
	for index, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inQuote {
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if r == '#' && !inQuote {
			return line[:index], line[index+1:]
		}
	}
	return line, ""
}

func pdxBraceDelta(code string) int {
	delta := 0
	inQuote := false
	escaped := false
	for _, r := range code {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inQuote {
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		switch r {
		case '{':
			delta++
		case '}':
			delta--
		}
	}
	return delta
}

func parseOnActionCommentBindings(lines []onActionCommentLine) []OnActionScopeContractBinding {
	bindings := make([]OnActionScopeContractBinding, 0, len(lines))
	for _, line := range lines {
		if binding, ok := parseOnActionCommentBinding(line); ok {
			bindings = append(bindings, binding)
		}
	}
	return bindings
}

func parseOnActionCommentBinding(line onActionCommentLine) (OnActionScopeContractBinding, bool) {
	text := strings.TrimSpace(line.Text)
	lower := strings.ToLower(text)
	target := ""
	body := ""
	switch {
	case strings.HasPrefix(lower, "root") && isBindingBoundary(lower, len("root")):
		target = "root"
		body = strings.TrimSpace(text[len("root"):])
	case strings.HasPrefix(lower, "scope:"):
		rest := text[len("scope:"):]
		nameEnd := 0
		for nameEnd < len(rest) && isOnActionScopeNameByte(rest[nameEnd]) {
			nameEnd++
		}
		if nameEnd == 0 || (nameEnd < len(rest) && rest[nameEnd] == '.') {
			return OnActionScopeContractBinding{}, false
		}
		target = strings.TrimSpace(rest[:nameEnd])
		body = strings.TrimSpace(rest[nameEnd:])
	default:
		return OnActionScopeContractBinding{}, false
	}
	body = strings.TrimSpace(strings.TrimLeft(body, "=:-"))
	if strings.HasPrefix(strings.ToLower(body), "is ") {
		body = strings.TrimSpace(body[len("is "):])
	}
	kind, scope, confidence := classifyOnActionCommentValue(body)
	return OnActionScopeContractBinding{
		Target: target, Kind: kind, Scope: scope, Description: body, Line: line.Line, Confidence: confidence,
	}, true
}

func isBindingBoundary(value string, offset int) bool {
	if len(value) == offset {
		return true
	}
	switch value[offset] {
	case ' ', '\t', ':', '=', '-':
		return true
	default:
		return false
	}
}

func isOnActionScopeNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_' || value == '<' || value == '>'
}

func classifyOnActionCommentValue(value string) (kind, scope, confidence string) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.Trim(normalized, " .,:;()")
	if normalized == "none" || strings.Contains(normalized, "does not exist") {
		return "none", "none", "high"
	}
	if strings.Contains(normalized, "flag:") {
		return "flag", "", "high"
	}
	if strings.Contains(normalized, "<") || strings.Contains(normalized, ">") {
		return "dynamic", "", "medium"
	}
	if normalized == "list" || strings.HasPrefix(normalized, "list of ") || strings.HasPrefix(normalized, "a list of ") {
		return "list", "", "medium"
	}
	if normalized == "true" || normalized == "false" {
		return "boolean", "", "high"
	}
	if normalized == "value" || normalized == "int" || normalized == "float" || normalized == "string" {
		return "value", "", "high"
	}
	for _, prefix := range []string{"the ", "a ", "an "} {
		normalized = strings.TrimPrefix(normalized, prefix)
	}
	if knownOnActionCommentScopes[normalized] {
		return "scope", normalized, "high"
	}
	return "unknown", "", "low"
}

var knownOnActionCommentScopes = map[string]bool{
	"activity": true, "army": true, "artifact": true, "barony": true, "character": true,
	"character_memory": true, "combat": true, "county": true, "county_title": true,
	"culture": true, "dynasty": true, "faith": true, "faction": true, "government": true,
	"house": true, "landed_title": true, "province": true, "realm": true, "regiment": true,
	"scheme": true, "secret": true, "situation": true, "story_cycle": true, "struggle": true,
	"title": true, "travel_plan": true, "war": true,
}

func contractRootBinding(bindings []OnActionScopeContractBinding) (OnActionScopeContractBinding, bool) {
	for _, binding := range bindings {
		if binding.Target == "root" {
			return binding, true
		}
	}
	return OnActionScopeContractBinding{}, false
}

func compareDocumentedOnActionRoot(root OnActionScopeContractBinding, documented bool, engineScopes []string, engineAvailable bool) (string, string) {
	if !documented {
		return "not_documented", "low"
	}
	if root.Kind != "scope" && root.Kind != "none" {
		return "unresolved_documentation", "low"
	}
	if !engineAvailable {
		return "engine_evidence_unavailable", "medium"
	}
	if len(engineScopes) == 0 {
		return "engine_rule_missing", "medium"
	}
	for _, engineScope := range engineScopes {
		if engineScope == root.Scope {
			return "match", "high"
		}
	}
	if containsOnActionScope(engineScopes, "none") && root.Kind == "scope" {
		return "engine_none_with_documented_root", "medium"
	}
	return "engine_scope_conflicts_documented_root", "medium"
}

func containsOnActionScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func isOnActionContractReview(comparison string) bool {
	return comparison == "engine_none_with_documented_root" || comparison == "engine_scope_conflicts_documented_root"
}

func onActionContractReviewFinding(contract OnActionScopeContract, root OnActionScopeContractBinding, documented bool) (OnActionScopeContractFinding, bool) {
	if !documented || !isOnActionContractReview(contract.RootComparison) {
		return OnActionScopeContractFinding{}, false
	}
	message := fmt.Sprintf("vanilla comment documents root as %q while cached engine root evidence is %s; review manually before changing any scope rule", root.Scope, strings.Join(contract.EngineInputScopes, ", "))
	return OnActionScopeContractFinding{
		Code: contract.RootComparison, Severity: "review", Name: contract.Name, Path: contract.Path, Line: root.Line, Message: message,
	}, true
}
