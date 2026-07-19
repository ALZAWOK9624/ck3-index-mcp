package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/script"
)

// GUIQueryOptions selects a read-only view over GUI files already known to the
// main index. It intentionally reuses files/override state instead of building
// a parallel GUI index.
type GUIQueryOptions struct {
	Operation     string
	Path          string
	PathPrefix    string
	Symbol        string
	AllowProject  bool
	Limit         int
	Width         int
	Height        int
	Format        string
	HTMLMode      string
	Language      string
	Samples       []GUIScenarioSample
	ModelSamples  []GUIModelSampleCollection
	RuntimeFacts  []GUIRuntimeFactInput
	ActionEffects []GUIRuntimeActionEffectInput
}

type GUIQueryResult struct {
	Operation       string                `json:"operation"`
	Query           string                `json:"query,omitempty"`
	Files           int                   `json:"files"`
	ResolutionFiles int                   `json:"resolution_files,omitempty"`
	CacheHit        bool                  `json:"cache_hit,omitempty"`
	Found           bool                  `json:"found,omitempty"`
	File            *GUIFileModel         `json:"file,omitempty"`
	Type            *ResolvedGUIType      `json:"type,omitempty"`
	Template        *GUITemplate          `json:"template,omitempty"`
	Preview         *GUIPreviewResult     `json:"preview,omitempty"`
	Summary         *GUIResolutionSummary `json:"summary,omitempty"`
	Tree            *GUITreeStats         `json:"tree,omitempty"`
	Diagnostics     []GUIDiagnostic       `json:"diagnostics,omitempty"`
	Guidance        []string              `json:"guidance,omitempty"`
}

type GUITreeStats struct {
	TotalNodes    int  `json:"total_nodes"`
	ReturnedNodes int  `json:"returned_nodes"`
	NodeLimit     int  `json:"node_limit"`
	MaxDepth      int  `json:"max_depth"`
	Truncated     bool `json:"truncated"`
}

type GUIFileModel struct {
	Path   string   `json:"path"`
	Source string   `json:"source"`
	Rank   int      `json:"rank"`
	Model  GUIModel `json:"model"`
}

type activeGUIFile struct {
	path       string
	relPath    string
	sourceName string
	sourceRank int
	sha256     string
}

// QueryGUI parses and resolves active GUI files selected from the existing
// files table. Paths in the result stay source-root-relative; absolute paths
// are used only internally to read files already accepted by the scanner.
func (db *DB) QueryGUI(ctx context.Context, options GUIQueryOptions) (GUIQueryResult, error) {
	operation := strings.ToLower(strings.TrimSpace(options.Operation))
	if operation == "" {
		operation = "summary"
	}
	switch operation {
	case "summary", "file", "type", "template", "preview":
	default:
		return GUIQueryResult{Operation: operation}, fmt.Errorf("unknown GUI operation %q; expected summary, file, type, template, or preview", operation)
	}
	if (operation == "type" || operation == "template" || operation == "preview") && strings.TrimSpace(options.Symbol) == "" {
		return GUIQueryResult{Operation: operation}, fmt.Errorf("GUI %s query requires a symbol", operation)
	}
	if operation != "preview" && strings.TrimSpace(options.Format) != "" {
		return GUIQueryResult{Operation: operation}, fmt.Errorf("GUI format is only valid for operation=preview")
	}
	if operation != "preview" && strings.TrimSpace(options.HTMLMode) != "" {
		return GUIQueryResult{Operation: operation}, fmt.Errorf("GUI HTML mode is only valid for operation=preview")
	}
	if operation != "preview" && strings.TrimSpace(options.Language) != "" {
		return GUIQueryResult{Operation: operation}, fmt.Errorf("GUI language is only valid for operation=preview")
	}
	if operation != "preview" && (len(options.Samples) > 0 || len(options.ModelSamples) > 0 || len(options.RuntimeFacts) > 0 || len(options.ActionEffects) > 0) {
		return GUIQueryResult{Operation: operation}, fmt.Errorf("GUI scenario samples, model samples, runtime facts, and action effects are only valid for operation=preview")
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 500 {
		limit = 500
	}
	result := GUIQueryResult{Operation: operation}

	if operation == "file" {
		relPath, err := normalizeGUIQueryPath(options.Path, true)
		if err != nil {
			return result, err
		}
		if relPath == "" {
			return result, fmt.Errorf("GUI file query requires a source-root-relative path under gui/")
		}
		file, found, err := db.activeGUIFile(ctx, relPath, options.AllowProject)
		if err != nil {
			return result, err
		}
		result.Query = relPath
		if !found {
			return result, nil
		}
		data, err := os.ReadFile(file.path)
		if err != nil {
			return result, fmt.Errorf("read indexed GUI file %s: %w", relPath, err)
		}
		result.Files = 1
		result.Found = true
		model, tree := compactGUIModel(BuildGUIModel(string(data)), guiQueryNodeLimit(limit), defaultGUIQueryMaxDepth)
		result.File = &GUIFileModel{Path: file.relPath, Source: file.sourceName, Rank: file.sourceRank, Model: model}
		result.Tree = &tree
		appendGUITruncationGuidance(&result)
		return result, nil
	}

	prefix, err := normalizeGUIQueryPath(options.PathPrefix, false)
	if err != nil {
		return result, err
	}
	files, err := db.activeGUIFiles(ctx, prefix, options.AllowProject)
	if err != nil {
		return result, err
	}
	resolutionFiles := files
	resolutionPrefix := prefix
	if prefix != "" && operation != "summary" {
		// path_prefix scopes the requested symbol, but custom types and
		// templates are global CK3 GUI dependencies. Resolve the symbol
		// against the full active GUI set so a single-file scope does not
		// silently turn cross-file child controls into opaque placeholders.
		resolutionFiles, err = db.activeGUIFiles(ctx, "", options.AllowProject)
		if err != nil {
			return result, err
		}
		resolutionPrefix = ""
	}
	resolution, cacheHit, err := db.resolveActiveGUIFiles(ctx, resolutionFiles, resolutionPrefix, options.AllowProject)
	if err != nil {
		return result, err
	}
	result.Files = len(files)
	if len(resolutionFiles) != len(files) {
		result.ResolutionFiles = len(resolutionFiles)
	}
	result.CacheHit = cacheHit

	switch operation {
	case "summary":
		summary := resolution.Summary()
		result.Summary = &summary
		result.Found = len(files) > 0
		result.Guidance = []string{
			"GUI resolution reuses active file override state from ck3-index; it does not create a second index.",
			"Missing templates and unresolved external types are informational because some GUI primitives are engine-provided.",
		}
	case "type":
		result.Query = strings.TrimSpace(options.Symbol)
		for index := range resolution.Types {
			if resolution.Types[index].Name == result.Query && guiSourceMatchesPrefix(resolution.Types[index].Source, prefix) {
				value := resolution.Types[index]
				value.Element, result.Tree = compactGUIElementForQuery(value.Element, guiQueryNodeLimit(limit), defaultGUIQueryMaxDepth)
				result.Type = &value
				result.Found = true
				break
			}
		}
		result.Diagnostics = selectGUIDiagnostics(resolution.Diagnostics, result.Query, result.TypeSource(), limit)
	case "template":
		result.Query = strings.TrimSpace(options.Symbol)
		for index := range resolution.Templates {
			if resolution.Templates[index].Name == result.Query && guiSourceMatchesPrefix(resolution.Templates[index].Element.Source, prefix) {
				value := resolution.Templates[index]
				value.Element, result.Tree = compactGUIElementForQuery(value.Element, guiQueryNodeLimit(limit), defaultGUIQueryMaxDepth)
				result.Template = &value
				result.Found = true
				break
			}
		}
		source := ""
		if result.Template != nil {
			source = result.Template.Element.Source
		}
		result.Diagnostics = selectGUIDiagnostics(resolution.Diagnostics, result.Query, source, limit)
	case "preview":
		previewFormat := strings.ToLower(strings.TrimSpace(options.Format))
		if previewFormat == "" {
			previewFormat = "png"
		}
		if previewFormat != "png" && previewFormat != "html" && previewFormat != "both" {
			return result, fmt.Errorf("GUI preview format %q is invalid; expected png, html, or both", options.Format)
		}
		htmlMode := strings.ToLower(strings.TrimSpace(options.HTMLMode))
		if htmlMode == "" {
			htmlMode = GUIHTMLModeStatic
		}
		if htmlMode != GUIHTMLModeStatic && htmlMode != GUIHTMLModeInspector {
			return result, fmt.Errorf("GUI HTML mode %q is invalid; expected static or inspector", options.HTMLMode)
		}
		if previewFormat == "png" && strings.TrimSpace(options.HTMLMode) != "" {
			return result, fmt.Errorf("GUI HTML mode requires preview format html or both")
		}
		language, err := normalizeGUIPreviewLanguage(options.Language)
		if err != nil {
			return result, err
		}
		result.Query = strings.TrimSpace(options.Symbol)
		var element GUIElement
		source := ""
		symbolKind := ""
		for index := range resolution.Types {
			if resolution.Types[index].Name == result.Query && guiSourceMatchesPrefix(resolution.Types[index].Source, prefix) {
				element = resolution.Types[index].Element
				source = resolution.Types[index].Source
				symbolKind = "type"
				break
			}
		}
		if symbolKind == "" {
			for index := range resolution.Templates {
				if resolution.Templates[index].Name == result.Query && guiSourceMatchesPrefix(resolution.Templates[index].Element.Source, prefix) {
					element = resolution.Templates[index].Element
					source = element.Source
					symbolKind = "template"
					break
				}
			}
		}
		if symbolKind == "" {
			if root, found := findNamedGUIElementInScope(resolution.Roots, result.Query, prefix); found {
				element = root
				source = root.Source
				symbolKind = "element"
			}
		}
		if symbolKind != "" {
			preparedModelSamples, err := prepareGUIModelSamples(result.Query, &element, options.ModelSamples)
			if err != nil {
				return result, err
			}
			preview, err := RenderGUIPreview(result.Query, symbolKind, source, element, options.Width, options.Height, guiQueryNodeLimit(limit))
			if err != nil {
				return result, err
			}
			if err := db.bindGUIPreviewLocalization(ctx, &preview, language, options.AllowProject); err != nil {
				return result, err
			}
			if err := prepareGUIPreviewRuntimeWithActions(&preview, options.RuntimeFacts, options.ActionEffects); err != nil {
				return result, err
			}
			if err := applyGUIPreviewScenario(&preview, options.Samples); err != nil {
				return result, err
			}
			if err := applyGUIPreviewModelSamples(&preview, preparedModelSamples); err != nil {
				return result, err
			}
			if err := refreshGUIPreviewPNG(&preview); err != nil {
				return result, err
			}
			if err := db.bindGUIPreviewTextures(ctx, &preview, options.AllowProject); err != nil {
				return result, err
			}
			preview.Format = previewFormat
			if previewFormat == "html" || previewFormat == "both" {
				if err := embedGUIPreviewTextures(ctx, &preview); err != nil {
					return result, err
				}
				htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: htmlMode})
				if err != nil {
					return result, err
				}
				preview.HTML = &htmlPreview
			}
			if previewFormat == "html" {
				preview.PNG = nil
				preview.Bytes = 0
			}
			result.Preview = &preview
			result.Found = true
			result.Guidance = append(result.Guidance,
				"GUI preview is a deterministic diagnostic layout, not a pixel-perfect Jomini runtime simulation; inspect approximate and warnings before editing.",
			)
			if preview.HTML != nil {
				if preview.HTML.Mode == GUIHTMLModeInspector {
					result.Guidance = append(result.Guidance,
						"The HTML inspector is self-contained and uses one fixed CSP-hashed script for tree browsing, zoom, search, and controlled visual-state simulation.",
						"Resolved hbox, vbox, flowcontainer, scrollbox, fixedgridbox, and dynamicgridbox metadata drives bounded browser reflow. flowcontainer defaults horizontal; scrollbox is a clipped vertical viewport with wheel/range scrolling; ignoreinvisible removes known-hidden direct children before spacing, grid cells, and expanding policies are recomputed. Structural block wrappers are transparent to flow; model_samples may instantiate explicit provided rows, while unprovided virtualized rows and unresolved engine templates remain approximate.",
						"tooltipwidget descendants are hover-only overlays: they remain inspectable but do not inflate ordinary scene bounds or diagnostic PNG content. When no tooltipwidget exists, resolved text or bounded tooltip plans use a fixed text-only hover panel; neither path executes tooltip script.",
						"The inspector starts in Visual mode, which prioritizes embedded textures and resolved text, removes diagnostic chrome, alpha-masks allowlisted modify_texture blends to their nearest textured ancestor, propagates known-hidden parent state through the flattened preview subtree, and directly replays bounded click plans from the canvas while Replay clicks is enabled. Disable Replay clicks for selection-only inspection, or disable Visual to inspect approximate boxes and missing assets.",
						"Behavior simulation applies visible, enabled, down, selected, numeric min/max/value, text, state, and click consequences for review only. Progress values are normalized over their declared range; progressbar fill/no-progress textures and ordinary overlays stay separate. The evaluator supports And, Or, Not, and typed comparisons over explicit atomic facts, but never executes Jomini code or game effects.",
					)
				} else {
					result.Guidance = append(result.Guidance,
						"The static HTML preview is self-contained, script-free, and model-readable; runtime visibility, data context, localization, and effects remain unevaluated expressions.",
					)
				}
			}
			if preview.Localization.Resolved > 0 {
				result.Guidance = append(result.Guidance,
					"Indexed English and Simplified Chinese localization is attached to GUI text and tooltip keys. Static nested localization keys and macros are expanded from the same active index with bounded depth, while unresolved runtime localization expressions remain explicitly partial.",
				)
			}
			if preview.Scenario != nil {
				result.Guidance = append(result.Guidance,
					"GUI scenario values are caller-provided examples matched to exact expressions; they are labeled provided and are not observed runtime facts.",
				)
			}
			if preview.ModelSamples != nil {
				result.Guidance = append(result.Guidance,
					"GUI model_samples instantiate a bounded number of caller-provided rows from one exact grid item template. Row values are labeled provided, remain isolated by row id, and are not observed Jomini datamodel contents.",
				)
			}
			if preview.Runtime != nil && len(preview.Runtime.Plans) > 0 {
				result.Guidance = append(result.Guidance,
					"GUI runtime facts are caller-provided atomic values. Composed boolean expressions use three-valued logic, while direct numeric facts and literals can drive min/max/value bindings for range-normalized progresspie/progressbar fill; missing facts, invalid ranges, and unsupported syntax remain explicit.",
				)
			}
			if preview.Runtime != nil && len(preview.Runtime.TextPlans) > 0 {
				result.Guidance = append(result.Guidance,
					"Supported dynamic text and tooltip markers are compiled into deduplicated token plans and interpolate only explicit runtime facts. SelectLocalization, Select_CString, AddTextIf, and AddLocalizationIf use bounded lazy branches; missing selected-branch values remain <unknown>, while unsupported or dynamic localization branches remain explicit facts.",
				)
			}
			if preview.Runtime != nil && len(preview.Runtime.Actions) > 0 {
				result.Guidance = append(result.Guidance,
					"Repeated onclick properties stay in source order. Allowlisted GameView clicks update matching IsGameViewOpen facts, SetMapMode selects one IsMapMode fact exclusively, and static GetVariableSystem.Toggle/Clear/Set update bounded variable state. Literal Set updates both Exists and typed Get facts, Clear unsets the value, and HasValue is evaluated as Exists plus a typed equality check. Dynamic keys or values remain unsupported, and all other click expressions remain log-only.",
				)
			}
			if preview.Runtime != nil && preview.Runtime.Stats.ActionEffects > 0 {
				result.Guidance = append(result.Guidance,
					"Provided action_effects attach bounded typed postconditions only to normalized exact otherwise-unsupported onclick expressions. The expression itself is never executed; unmatched effects are reported and builtin action semantics cannot be overridden.",
				)
			}
		}
		if result.Preview != nil {
			result.Diagnostics = selectGUIPreviewDiagnostics(resolution.Diagnostics, result.Query, result.Preview.Nodes, limit)
		} else {
			result.Diagnostics = selectGUIDiagnostics(resolution.Diagnostics, result.Query, source, limit)
		}
	}
	appendGUITruncationGuidance(&result)
	return result, nil
}

// findNamedGUIElement lets the existing preview operation address ordinary
// named widgets as well as custom types/templates. Higher-priority inputs are
// appended later by QueryGUI, so search from the end and prefer root names
// before nested children with commonly repeated names.
func findNamedGUIElement(roots []GUIElement, name string) (GUIElement, bool) {
	return findNamedGUIElementInScope(roots, name, "")
}

func findNamedGUIElementInScope(roots []GUIElement, name, prefix string) (GUIElement, bool) {
	for index := len(roots) - 1; index >= 0; index-- {
		if roots[index].Name == name && guiSourceMatchesPrefix(roots[index].Source, prefix) {
			return roots[index], true
		}
	}
	var find func(GUIElement, int) (GUIElement, bool)
	find = func(element GUIElement, depth int) (GUIElement, bool) {
		if depth > guiPreviewMaxDepth {
			return GUIElement{}, false
		}
		for index := len(element.Children) - 1; index >= 0; index-- {
			child := element.Children[index]
			if child.Name == name && guiSourceMatchesPrefix(child.Source, prefix) {
				return child, true
			}
			if found, ok := find(child, depth+1); ok {
				return found, true
			}
		}
		for index := len(element.Linked) - 1; index >= 0; index-- {
			linked := element.Linked[index].Element
			if linked.Name == name && guiSourceMatchesPrefix(linked.Source, prefix) {
				return linked, true
			}
			if found, ok := find(linked, depth+1); ok {
				return found, true
			}
		}
		return GUIElement{}, false
	}
	for index := len(roots) - 1; index >= 0; index-- {
		if found, ok := find(roots[index], 1); ok {
			return found, true
		}
	}
	return GUIElement{}, false
}

func guiSourceMatchesPrefix(source, prefix string) bool {
	if prefix == "" {
		return true
	}
	source = strings.ToLower(filepath.ToSlash(strings.TrimSpace(source)))
	prefix = strings.ToLower(filepath.ToSlash(strings.TrimSpace(prefix)))
	return strings.HasPrefix(source, prefix)
}

func (result GUIQueryResult) TypeSource() string {
	if result.Type == nil {
		return ""
	}
	return result.Type.Source
}

func (db *DB) activeGUIFile(ctx context.Context, relPath string, allowProject bool) (activeGUIFile, bool, error) {
	query := `SELECT path,rel_path,source_name,source_rank,sha256 FROM files
		WHERE overridden=0 AND lower(rel_path)=lower(?) AND lower(rel_path) LIKE 'gui/%.gui'`
	args := []any{relPath}
	if !allowProject {
		query += ` AND source_rank>1`
	}
	query += ` ORDER BY source_rank ASC LIMIT 1`
	var file activeGUIFile
	err := db.sql.QueryRowContext(ctx, query, args...).Scan(&file.path, &file.relPath, &file.sourceName, &file.sourceRank, &file.sha256)
	if err != nil {
		if err == sql.ErrNoRows {
			return activeGUIFile{}, false, nil
		}
		return activeGUIFile{}, false, err
	}
	return file, true, nil
}

func (db *DB) activeGUIFiles(ctx context.Context, prefix string, allowProject bool) ([]activeGUIFile, error) {
	query := `SELECT path,rel_path,source_name,source_rank,sha256 FROM files
		WHERE overridden=0 AND lower(rel_path) LIKE 'gui/%.gui'`
	var args []any
	if prefix != "" {
		query += ` AND lower(rel_path) LIKE lower(?) ESCAPE '\'`
		args = append(args, escapeLike(prefix)+"%")
	}
	if !allowProject {
		query += ` AND source_rank>1`
	}
	query += ` ORDER BY source_rank DESC,lower(rel_path),rel_path,source_name`
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []activeGUIFile
	for rows.Next() {
		var file activeGUIFile
		if err := rows.Scan(&file.path, &file.relPath, &file.sourceName, &file.sourceRank, &file.sha256); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

const guiResolutionCacheLimit = 8

// resolveActiveGUIFiles keeps the expensive cross-file inheritance/template
// resolution warm for long-lived MCP sessions. The key is derived from the
// authoritative files-table hashes and selection rules, so a scan that changes
// any active GUI input naturally selects a new cache entry. There is no path-
// based fallback and no second on-disk GUI database.
func (db *DB) resolveActiveGUIFiles(ctx context.Context, files []activeGUIFile, prefix string, allowProject bool) (GUIResolution, bool, error) {
	key := guiResolutionCacheKey(files, prefix, allowProject)
	db.guiResolutionMu.Lock()
	if cached, ok := db.guiResolutionCache[key]; ok {
		db.guiResolutionMu.Unlock()
		return cached, true, nil
	}
	db.guiResolutionMu.Unlock()

	inputs := make([]GUIModelInput, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return GUIResolution{}, false, err
		}
		data, err := os.ReadFile(file.path)
		if err != nil {
			return GUIResolution{}, false, fmt.Errorf("read indexed GUI file %s: %w", file.relPath, err)
		}
		inputs = append(inputs, GUIModelInput{Path: file.relPath, Model: BuildGUIModel(string(data))})
	}
	resolution := ResolveGUIModels(inputs)

	db.guiResolutionMu.Lock()
	if db.guiResolutionCache == nil {
		db.guiResolutionCache = make(map[string]GUIResolution)
	}
	if _, exists := db.guiResolutionCache[key]; !exists {
		if len(db.guiResolutionOrder) >= guiResolutionCacheLimit {
			oldest := db.guiResolutionOrder[0]
			delete(db.guiResolutionCache, oldest)
			db.guiResolutionOrder = db.guiResolutionOrder[1:]
		}
		db.guiResolutionCache[key] = resolution
		db.guiResolutionOrder = append(db.guiResolutionOrder, key)
	}
	db.guiResolutionMu.Unlock()
	return resolution, false, nil
}

func guiResolutionCacheKey(files []activeGUIFile, prefix string, allowProject bool) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "v1\x00%s\x00%t\x00", prefix, allowProject)
	for _, file := range files {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%s\x00", file.relPath, file.sourceName, file.sourceRank, file.sha256)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func normalizeGUIQueryPath(value string, exact bool) (string, error) {
	value = strings.Trim(filepath.ToSlash(strings.TrimSpace(value)), "/")
	if value == "" {
		return "", nil
	}
	if filepath.IsAbs(filepath.FromSlash(value)) {
		return "", fmt.Errorf("GUI paths must be source-root-relative")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("GUI path %q contains an invalid segment", value)
		}
	}
	if !strings.EqualFold(strings.Split(value, "/")[0], "gui") {
		return "", fmt.Errorf("GUI path %q must be under gui/", value)
	}
	if exact && !strings.EqualFold(filepath.Ext(value), ".gui") {
		return "", fmt.Errorf("GUI file path %q must end in .gui", value)
	}
	return value, nil
}

func selectGUIDiagnostics(values []GUIDiagnostic, symbol, source string, limit int) []GUIDiagnostic {
	selected := make([]GUIDiagnostic, 0, limit)
	for _, diagnostic := range values {
		if diagnostic.Symbol != symbol && (source == "" || diagnostic.Source != source) {
			continue
		}
		selected = append(selected, diagnostic)
		if len(selected) >= limit {
			break
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Severity != selected[j].Severity {
			return selected[i].Severity < selected[j].Severity
		}
		if selected[i].Source != selected[j].Source {
			return selected[i].Source < selected[j].Source
		}
		return selected[i].Span.Line < selected[j].Span.Line
	})
	return selected
}

// selectGUIPreviewDiagnostics avoids flooding a focused preview with every
// unrelated informational diagnostic from a large GUI file. Diagnostics are
// retained when they name the selected symbol or overlap source spans that
// actually contributed a rendered node.
func selectGUIPreviewDiagnostics(values []GUIDiagnostic, symbol string, nodes []GUIPreviewNode, limit int) []GUIDiagnostic {
	type lineRange struct{ min, max int }
	ranges := map[string]lineRange{}
	for _, node := range nodes {
		if node.Source == "" || node.Line <= 0 {
			continue
		}
		value, ok := ranges[node.Source]
		if !ok || node.Line < value.min {
			value.min = node.Line
		}
		if !ok || node.Line > value.max {
			value.max = node.Line
		}
		ranges[node.Source] = value
	}
	selected := make([]GUIDiagnostic, 0, limit)
	for _, diagnostic := range values {
		include := diagnostic.Symbol == symbol
		if span, ok := ranges[diagnostic.Source]; ok {
			end := diagnostic.Span.EndLine
			if end <= 0 {
				end = diagnostic.Span.Line
			}
			include = include || (diagnostic.Span.Line <= span.max && end >= span.min)
		}
		if !include {
			continue
		}
		selected = append(selected, diagnostic)
		if len(selected) >= limit {
			break
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Severity != selected[j].Severity {
			return selected[i].Severity < selected[j].Severity
		}
		if selected[i].Source != selected[j].Source {
			return selected[i].Source < selected[j].Source
		}
		return selected[i].Span.Line < selected[j].Span.Line
	})
	return selected
}

const (
	defaultGUIQueryMaxDepth      = 10
	defaultGUIQueryPropertyLimit = 24
)

type guiTreeBudget struct {
	remaining int
	maxDepth  int
	returned  int
	truncated bool
}

func guiQueryNodeLimit(limit int) int {
	nodes := limit
	if nodes < 20 {
		nodes = 20
	}
	if nodes > 500 {
		nodes = 500
	}
	return nodes
}

func compactGUIElementForQuery(element GUIElement, nodeLimit, maxDepth int) (GUIElement, *GUITreeStats) {
	total := countGUIElementNodes(element)
	budget := guiTreeBudget{remaining: nodeLimit, maxDepth: maxDepth}
	compacted, ok := budget.take(element, 0)
	if !ok {
		compacted = GUIElement{}
	}
	stats := &GUITreeStats{
		TotalNodes: total, ReturnedNodes: budget.returned, NodeLimit: nodeLimit, MaxDepth: maxDepth,
		Truncated: budget.truncated || budget.returned < total,
	}
	return compacted, stats
}

func compactGUIModel(model GUIModel, nodeLimit, maxDepth int) (GUIModel, GUITreeStats) {
	total := 0
	for _, template := range model.Templates {
		total += countGUIElementNodes(template.Element)
	}
	for _, namespace := range model.Namespaces {
		for _, typeRule := range namespace.Types {
			total += countGUIElementNodes(typeRule.Element)
		}
	}
	for _, root := range model.Roots {
		total += countGUIElementNodes(root)
	}
	budget := guiTreeBudget{remaining: nodeLimit, maxDepth: maxDepth}
	compacted := GUIModel{ParseErrors: append([]script.ParseError(nil), model.ParseErrors...)}
	for _, template := range model.Templates {
		element, ok := budget.take(template.Element, 0)
		if !ok {
			break
		}
		template.Element = element
		compacted.Templates = append(compacted.Templates, template)
	}
	for _, namespace := range model.Namespaces {
		copyNamespace := GUINamespace{Name: namespace.Name, Span: namespace.Span}
		for _, typeRule := range namespace.Types {
			element, ok := budget.take(typeRule.Element, 0)
			if !ok {
				break
			}
			typeRule.Element = element
			copyNamespace.Types = append(copyNamespace.Types, typeRule)
		}
		if len(copyNamespace.Types) > 0 {
			compacted.Namespaces = append(compacted.Namespaces, copyNamespace)
		}
		if budget.remaining == 0 {
			break
		}
	}
	if budget.remaining > 0 {
		for _, root := range model.Roots {
			element, ok := budget.take(root, 0)
			if !ok {
				break
			}
			compacted.Roots = append(compacted.Roots, element)
		}
	}
	stats := GUITreeStats{
		TotalNodes: total, ReturnedNodes: budget.returned, NodeLimit: nodeLimit, MaxDepth: maxDepth,
		Truncated: budget.truncated || budget.returned < total,
	}
	return compacted, stats
}

func (budget *guiTreeBudget) take(element GUIElement, depth int) (GUIElement, bool) {
	if budget.remaining <= 0 {
		budget.truncated = true
		return GUIElement{}, false
	}
	budget.remaining--
	budget.returned++
	copy := element
	propertyCount := len(element.Properties)
	if propertyCount > defaultGUIQueryPropertyLimit {
		propertyCount = defaultGUIQueryPropertyLimit
		budget.truncated = true
	}
	copy.Properties = append([]GUIProperty(nil), element.Properties[:propertyCount]...)
	copy.Children = nil
	copy.Linked = nil
	if depth >= budget.maxDepth {
		if len(element.Children) > 0 {
			budget.truncated = true
		}
		return copy, true
	}
	for _, child := range element.Children {
		childCopy, ok := budget.take(child, depth+1)
		if !ok {
			break
		}
		copy.Children = append(copy.Children, childCopy)
	}
	for _, linked := range element.Linked {
		linkedCopy, ok := budget.take(linked.Element, depth+1)
		if !ok {
			break
		}
		copy.Linked = append(copy.Linked, GUILinkedElement{Property: linked.Property, Target: linked.Target, Element: linkedCopy})
	}
	return copy, true
}

func countGUIElementNodes(element GUIElement) int {
	count := 1
	for _, child := range element.Children {
		count += countGUIElementNodes(child)
	}
	for _, linked := range element.Linked {
		count += countGUIElementNodes(linked.Element)
	}
	return count
}

func appendGUITruncationGuidance(result *GUIQueryResult) {
	if result.Tree == nil || !result.Tree.Truncated {
		return
	}
	result.Guidance = append(result.Guidance,
		"The returned GUI tree is bounded for model safety; use path_prefix, a more specific symbol, or the low-level gui-model/gui-resolve CLI for full raw output.")
}
