package indexer

import (
	"crypto/sha256"
	"fmt"
	"html"
	"math"
	"path"
	"strconv"
	"strings"
)

const (
	GUIHTMLSchemaVersion = "ck3-gui-html/v1"
	GUIHTMLMaxBytes      = 1 << 20
	GUIHTMLModeStatic    = "static"
	GUIHTMLModeInspector = "inspector"
)

type GUIHTMLRenderOptions struct {
	Mode string
}

type GUIHTMLBehaviorStats struct {
	VisibleExpressions   int `json:"visible_expressions"`
	EnabledExpressions   int `json:"enabled_expressions"`
	DownExpressions      int `json:"down_expressions"`
	SelectedExpressions  int `json:"selected_expressions"`
	AlphaExpressions     int `json:"alpha_expressions"`
	ValueExpressions     int `json:"value_expressions"`
	ColorExpressions     int `json:"color_expressions"`
	DynamicTexts         int `json:"dynamic_texts"`
	ClickActions         int `json:"click_actions"`
	States               int `json:"states"`
	TooltipOverlays      int `json:"tooltip_overlays,omitempty"`
	ScrollViewports      int `json:"scroll_viewports,omitempty"`
	ModelRows            int `json:"model_rows,omitempty"`
	RuntimePlans         int `json:"runtime_plans,omitempty"`
	RuntimeFacts         int `json:"runtime_facts,omitempty"`
	RuntimeEvaluated     int `json:"runtime_evaluated,omitempty"`
	RuntimeUnknown       int `json:"runtime_unknown,omitempty"`
	RuntimeActions       int `json:"runtime_actions,omitempty"`
	RuntimeActionEffects int `json:"runtime_action_effects,omitempty"`
	RuntimeUnusedEffects int `json:"runtime_unused_action_effects,omitempty"`
	RuntimeTextPlans     int `json:"runtime_text_plans,omitempty"`
	RuntimeTextReady     int `json:"runtime_text_ready,omitempty"`
	RuntimeTextPartial   int `json:"runtime_text_partial,omitempty"`
}

// GUIHTMLPreview is a deterministic, self-contained browser representation of
// the same bounded scene used by the diagnostic PNG. Static mode is script-free;
// inspector mode permits one fixed CSP-hashed generator script. Neither mode
// accepts local paths or network-capable resources from GUI input.
type GUIHTMLPreview struct {
	SchemaVersion    string               `json:"schema_version"`
	Mode             string               `json:"mode"`
	Document         string               `json:"document,omitempty"`
	Bytes            int                  `json:"bytes"`
	SHA256           string               `json:"sha256"`
	NodeCount        int                  `json:"node_count"`
	Scripts          bool                 `json:"scripts"`
	ScriptPolicy     string               `json:"script_policy"`
	ScriptSHA256     string               `json:"script_sha256,omitempty"`
	ExternalRequests bool                 `json:"external_requests"`
	ModelReadable    bool                 `json:"model_readable"`
	Behaviors        GUIHTMLBehaviorStats `json:"behaviors"`
}

type guiHTMLTextureAssets struct {
	classes map[string]string
	values  []string
}

// RenderGUIHTMLPreview converts an already-resolved and laid-out preview to the
// backwards-compatible script-free standalone HTML document. All CSS geometry
// is derived from numeric preview bounds; GUI strings cannot become markup or
// CSS.
func RenderGUIHTMLPreview(preview GUIPreviewResult) (GUIHTMLPreview, error) {
	return RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeStatic})
}

func RenderGUIHTMLPreviewWithOptions(preview GUIPreviewResult, options GUIHTMLRenderOptions) (GUIHTMLPreview, error) {
	mode := strings.ToLower(strings.TrimSpace(options.Mode))
	if mode == "" {
		mode = GUIHTMLModeStatic
	}
	switch mode {
	case GUIHTMLModeStatic:
		return renderGUIHTMLStaticPreview(preview)
	case GUIHTMLModeInspector:
		return renderGUIHTMLInspectorPreview(preview)
	default:
		return GUIHTMLPreview{}, fmt.Errorf("GUI HTML mode %q is invalid; expected static or inspector", options.Mode)
	}
}

func renderGUIHTMLStaticPreview(preview GUIPreviewResult) (GUIHTMLPreview, error) {
	displayNodes, _, _, _, _ := fitGUIPreviewNodes(preview.Nodes, preview.Width, preview.Height)
	bindGUIHTMLTextureModifierMasks(displayNodes)
	textureAssets := collectGUIHTMLTextureAssets(displayNodes)
	var output strings.Builder
	output.Grow(16*1024 + len(displayNodes)*512)
	output.WriteString("<!doctype html>\n<html lang=\"und\">\n<head>\n<meta charset=\"utf-8\">\n")
	output.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'\">\n")
	output.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n<title>")
	output.WriteString(html.EscapeString(preview.Symbol))
	output.WriteString(" · CK3 GUI preview</title>\n<style>\n")
	output.WriteString(guiHTMLStyles(preview.Width, preview.Height))
	writeGUIHTMLTextureAssetStyles(&output, textureAssets)
	output.WriteString("</style>\n</head>\n<body>\n<main id=\"ck3-gui-preview\" class=\"ck3-preview\" data-ck3-schema=\"")
	output.WriteString(GUIHTMLSchemaVersion)
	output.WriteString("\" data-ck3-symbol=\"")
	output.WriteString(html.EscapeString(preview.Symbol))
	output.WriteString("\" data-ck3-symbol-kind=\"")
	output.WriteString(html.EscapeString(preview.SymbolKind))
	output.WriteString("\" data-ck3-language=\"")
	language, _ := normalizeGUIPreviewLanguage(preview.Language)
	output.WriteString(html.EscapeString(language))
	output.WriteString("\" aria-label=\"CK3 GUI diagnostic preview: ")
	output.WriteString(html.EscapeString(preview.Symbol))
	output.WriteString("\">\n<section class=\"ck3-canvas\" role=\"img\" aria-label=\"Resolved CK3 GUI layout\">\n")
	for _, node := range displayNodes {
		if node.BehaviorOnly || node.Overlay != nil {
			continue
		}
		writeGUIHTMLNode(&output, node, textureAssets, preview.Language)
	}
	output.WriteString("</section>\n<ol class=\"ck3-semantic-tree\" aria-label=\"GUI semantic nodes\">\n")
	for _, node := range displayNodes {
		output.WriteString("<li>")
		output.WriteString(html.EscapeString(guiHTMLNodeDescription(node)))
		output.WriteString("</li>\n")
	}
	output.WriteString("</ol>\n</main>\n</body>\n</html>\n")
	document := output.String()
	if len(document) > GUIHTMLMaxBytes {
		return GUIHTMLPreview{}, fmt.Errorf("GUI HTML preview exceeds %d bytes", GUIHTMLMaxBytes)
	}
	digest := sha256.Sum256([]byte(document))
	behaviors := summarizeGUIHTMLBehaviors(displayNodes)
	applyGUIHTMLRuntimeStats(&behaviors, preview.Runtime)
	return GUIHTMLPreview{
		SchemaVersion: GUIHTMLSchemaVersion, Mode: GUIHTMLModeStatic, Document: document, Bytes: len(document), SHA256: fmt.Sprintf("%x", digest),
		NodeCount: len(displayNodes), Scripts: false, ScriptPolicy: "none", ExternalRequests: false, ModelReadable: true,
		Behaviors: behaviors,
	}, nil
}

func applyGUIHTMLRuntimeStats(stats *GUIHTMLBehaviorStats, runtime *GUIPreviewRuntime) {
	if stats == nil || runtime == nil {
		return
	}
	stats.RuntimePlans = len(runtime.Plans)
	stats.RuntimeFacts = len(runtime.Facts)
	stats.RuntimeEvaluated = runtime.Stats.Evaluated
	stats.RuntimeUnknown = runtime.Stats.Unknown
	stats.RuntimeActions = runtime.Stats.Actions
	stats.RuntimeActionEffects = runtime.Stats.ActionEffects
	stats.RuntimeUnusedEffects = runtime.Stats.UnusedEffects
	stats.RuntimeTextPlans = runtime.Stats.TextPlans
	stats.RuntimeTextReady = runtime.Stats.TextReady
	stats.RuntimeTextPartial = runtime.Stats.TextPartial
}

func summarizeGUIHTMLBehaviors(nodes []GUIPreviewNode) GUIHTMLBehaviorStats {
	var result GUIHTMLBehaviorStats
	modelRows := map[string]bool{}
	for _, node := range nodes {
		if node.ModelRow != nil {
			modelRows[fmt.Sprintf("%d:%d", node.ModelRow.Collection, node.ModelRow.Index)] = true
		}
		if node.Overlay != nil && node.Overlay.Role == "tooltip_root" {
			result.TooltipOverlays++
		}
		if node.Layout != nil && node.Layout.ScrollViewport {
			result.ScrollViewports++
		}
		if node.StateDefinition != nil {
			result.States++
		}
		if node.Semantics == nil {
			if strings.Contains(node.Text, "[") || (node.TextLocalization != nil && node.TextLocalization.Partial) {
				result.DynamicTexts++
			}
			continue
		}
		if node.Semantics.Visible != "" {
			result.VisibleExpressions++
		}
		if node.Semantics.Enabled != "" {
			result.EnabledExpressions++
		}
		if node.Semantics.Down != "" {
			result.DownExpressions++
		}
		if node.Semantics.Selected != "" {
			result.SelectedExpressions++
		}
		if node.Semantics.Alpha != "" {
			result.AlphaExpressions++
		}
		if node.Semantics.Value != "" {
			result.ValueExpressions++
		}
		if node.Semantics.TintColor != "" {
			result.ColorExpressions++
		}
		if node.Semantics.FontTintColor != "" {
			result.ColorExpressions++
		}
		if strings.Contains(node.Text, "[") || node.Semantics.RawText != "" || (node.TextLocalization != nil && node.TextLocalization.Partial) {
			result.DynamicTexts++
		}
		if len(node.Semantics.OnClicks) > 0 {
			result.ClickActions += len(node.Semantics.OnClicks)
		} else if node.Semantics.OnClick != "" {
			result.ClickActions++
		}
		if node.Semantics.State != "" && node.StateDefinition == nil {
			result.States++
		}
	}
	result.ModelRows = len(modelRows)
	return result
}

func guiHTMLStyles(width, height int) string {
	return fmt.Sprintf(`:root{color-scheme:dark;--ck3-bg:#10151d;--ck3-grid:#293240;--ck3-text:#e7edf5;--ck3-muted:#9eabb9;--ck3-container:#2e78ad80;--ck3-container-border:#58a6e6;--ck3-button:#a865248f;--ck3-button-border:#eba149;--ck3-text-node:#26764f70;--ck3-text-border:#5ccc8e;--ck3-image:#68459580;--ck3-image-border:#b17ae5;--ck3-generic:#54667a70;--ck3-generic-border:#8ba8c7;--ck3-approx:#e0bc5c}
*{box-sizing:border-box}
html,body{margin:0;padding:0;width:%dpx;height:%dpx;overflow:hidden;background:var(--ck3-bg);font-family:"Noto Sans CJK SC","Microsoft YaHei UI","Segoe UI",sans-serif;color:var(--ck3-text)}
.ck3-preview,.ck3-canvas{position:relative;width:%dpx;height:%dpx;overflow:hidden}
.ck3-canvas{background-color:var(--ck3-bg);background-image:linear-gradient(var(--ck3-grid) 1px,transparent 1px),linear-gradient(90deg,var(--ck3-grid) 1px,transparent 1px);background-size:64px 64px}
.ck3-node{position:absolute;overflow:hidden;border:1px solid var(--ck3-generic-border);background:var(--ck3-generic);color:var(--ck3-text);font-size:12px;line-height:1.25;pointer-events:none;opacity:calc(var(--ck3-alpha,1)*var(--ck3-state-alpha,1))}
.ck3-container{background:var(--ck3-container);border-color:var(--ck3-container-border)}
.ck3-button{display:flex;align-items:center;justify-content:center;background:linear-gradient(180deg,#bc7b34aa,#754315aa);border-color:var(--ck3-button-border);font-weight:600;text-align:center}
.ck3-text{display:flex;align-items:center;background:var(--ck3-text-node);border-color:var(--ck3-text-border);padding:2px 4px}
.ck3-image{display:flex;align-items:flex-end;background:repeating-linear-gradient(135deg,#68459588 0,#68459588 8px,#50356f88 8px,#50356f88 16px);border-color:var(--ck3-image-border);padding:2px 4px}
.ck3-expand{background:transparent;border-color:var(--ck3-muted)}
.ck3-texture-modifier{background:transparent;border-color:transparent}.ck3-blend-screen{mix-blend-mode:screen}.ck3-blend-multiply{mix-blend-mode:multiply}.ck3-blend-overlay{mix-blend-mode:overlay}.ck3-blend-color-dodge{mix-blend-mode:color-dodge}
.ck3-approximate{border-color:var(--ck3-approx);border-style:dashed}
.ck3-scenario-hidden{opacity:.08;filter:grayscale(1)}.ck3-scenario-disabled{filter:grayscale(1);border-color:#d96b6b}
.ck3-node.is-sim-down{transform:translateY(1px);box-shadow:inset 0 2px 5px #000b;filter:saturate(.82)}.ck3-node.is-sim-selected-state{border-color:#f0cf72;box-shadow:inset 0 0 0 1px #f0cf72aa,0 0 7px #f0cf7255}
.ck3-texture{position:absolute;inset:0;width:100%%;height:100%%;background-position:center;background-repeat:no-repeat;background-size:100%% 100%%;background-color:var(--ck3-tint-color,transparent);background-blend-mode:multiply;pointer-events:none;user-select:none;transform-origin:center}.ck3-image>.ck3-texture,.ck3-button>.ck3-texture{background-size:contain}.ck3-texture.ck3-framed{background-position:var(--ck3-frame-up-x) var(--ck3-frame-up-y)}.ck3-node:hover>.ck3-texture.ck3-framed{background-position:var(--ck3-frame-over-x) var(--ck3-frame-over-y)}.ck3-node:active>.ck3-texture.ck3-framed,.ck3-node.is-sim-down>.ck3-texture.ck3-framed{background-position:var(--ck3-frame-down-x) var(--ck3-frame-down-y)}.ck3-node:disabled>.ck3-texture.ck3-framed,.ck3-node.is-sim-disabled>.ck3-texture.ck3-framed{background-position:var(--ck3-frame-disabled-x) var(--ck3-frame-disabled-y)}.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-up-image)}.ck3-node:hover>.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-over-image)}.ck3-node:active>.ck3-texture.ck3-framed-images,.ck3-node.is-sim-down>.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-down-image)}.ck3-node:disabled>.ck3-texture.ck3-framed-images,.ck3-node.is-sim-disabled>.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-disabled-image)}.ck3-texture.ck3-framed-images:not(.ck3-nine-slice){background-image:var(--ck3-active-frame-image)!important;background-size:100%% 100%%}.ck3-texture.ck3-nine-slice{background-image:none!important;border-style:solid;border-width:var(--ck3-slice-y) var(--ck3-slice-x);border-image-source:var(--ck3-active-frame-image,var(--ck3-texture-image));border-image-slice:var(--ck3-source-slice-y) var(--ck3-source-slice-x) fill;border-image-width:var(--ck3-slice-y) var(--ck3-slice-x);border-image-repeat:stretch}.ck3-texture.ck3-nine-slice-tiled{border-image-repeat:round}.ck3-texture.ck3-mirror-horizontal{transform:scaleX(-1)}.ck3-texture.ck3-mirror-vertical{transform:scaleY(-1)}.ck3-texture.ck3-mirror-both{transform:scale(-1,-1)}
.ck3-progresspie>.ck3-texture{-webkit-mask-image:conic-gradient(from -90deg,#000 0deg var(--ck3-progress-angle,360deg),transparent var(--ck3-progress-angle,360deg) 360deg);mask-image:conic-gradient(from -90deg,#000 0deg var(--ck3-progress-angle,360deg),transparent var(--ck3-progress-angle,360deg) 360deg)}.ck3-progressbar>.ck3-progress-fill{clip-path:inset(0 var(--ck3-progress-inverse,0%%) 0 0)}
.ck3-caption{position:relative;z-index:1;display:block;max-width:100%%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;text-shadow:0 1px 2px #000}
.ck3-container>.ck3-caption{position:absolute;left:2px;top:1px;color:var(--ck3-muted);font-size:10px}
.ck3-semantic-tree{position:absolute!important;width:1px!important;height:1px!important;padding:0!important;margin:-1px!important;overflow:hidden!important;clip:rect(0,0,0,0)!important;white-space:nowrap!important;border:0!important}
`, width, height, width, height)
}

func writeGUIHTMLNode(output *strings.Builder, node GUIPreviewNode, textureAssets guiHTMLTextureAssets, previewLanguage string) {
	classes := []string{"ck3-node", guiHTMLKindClass(node.Kind)}
	if blendClass := guiHTMLTextureBlendClass(node); blendClass != "" {
		classes = append(classes, "ck3-texture-modifier", blendClass)
	}
	if node.Approximate {
		classes = append(classes, "ck3-approximate")
	}
	if value, known := guiNodeEffectiveVisible(node); known && !value {
		classes = append(classes, "ck3-scenario-hidden")
	}
	if value, known := guiNodeEffectiveEnabled(node); known && !value {
		classes = append(classes, "ck3-scenario-disabled")
	}
	if value, known := guiNodeEffectiveDown(node); known && value {
		classes = append(classes, "is-sim-down")
	}
	if value, known := guiNodeEffectiveSelected(node); known && value {
		classes = append(classes, "is-sim-selected-state")
	}
	output.WriteString("<div id=\"ck3-node-")
	output.WriteString(strconv.Itoa(node.Index))
	output.WriteString("\" class=\"")
	output.WriteString(strings.Join(classes, " "))
	output.WriteString("\" data-ck3-index=\"")
	output.WriteString(strconv.Itoa(node.Index))
	output.WriteString("\" data-ck3-parent=\"")
	output.WriteString(strconv.Itoa(node.Parent))
	output.WriteString("\" data-ck3-kind=\"")
	output.WriteString(html.EscapeString(node.Kind))
	output.WriteString("\"")
	writeGUIHTMLData(output, "name", node.Name)
	writeGUIHTMLData(output, "type-chain", strings.Join(node.TypeChain, ","))
	writeGUIHTMLData(output, "source", node.Source)
	writeGUIHTMLData(output, "mirror", node.Mirror)
	writeGUIHTMLData(output, "texture-blend-mode", node.TextureBlendMode)
	writeGUIHTMLTextureFrameData(output, node.TextureFrames)
	writeGUIHTMLTextureSliceData(output, node.TextureSlice)
	if node.Line > 0 {
		writeGUIHTMLData(output, "line", strconv.Itoa(node.Line))
	}
	writeGUIHTMLSemantics(output, node.Semantics)
	writeGUIHTMLLocalizationData(output, "text", node.TextLocalization)
	writeGUIHTMLLocalizationData(output, "tooltip", node.TooltipLocalization)
	writeGUIHTMLModelRowData(output, node.ModelRow)
	writeGUIHTMLScenarioData(output, node.Scenario)
	writeGUIHTMLRuntimeData(output, node, previewLanguage)
	fmt.Fprintf(output, " style=\"left:%dpx;top:%dpx;width:%dpx;height:%dpx;z-index:%d", node.Bounds.X, node.Bounds.Y, node.Bounds.Width, node.Bounds.Height, node.Index+1)
	if clipStyle := guiHTMLInitialClipStyle(node); clipStyle != "" {
		output.WriteString(";")
		output.WriteString(clipStyle)
	}
	if progressStyle := guiHTMLProgressStyle(node); progressStyle != "" {
		output.WriteString(";")
		output.WriteString(progressStyle)
	}
	if colorStyle := guiHTMLColorStyle(node); colorStyle != "" {
		output.WriteString(";")
		output.WriteString(colorStyle)
	}
	if alphaStyle := guiHTMLAlphaStyle(node); alphaStyle != "" {
		output.WriteString(";")
		output.WriteString(alphaStyle)
	}
	output.WriteString("\"")
	output.WriteString(">")
	writeGUIHTMLTextureImage(output, node, textureAssets)
	output.WriteString("<span class=\"ck3-caption\">")
	output.WriteString(html.EscapeString(guiHTMLStageLabel(node)))
	output.WriteString("</span></div>\n")
}

func guiHTMLInitialClipStyle(node GUIPreviewNode) string {
	if node.ClipBounds == nil {
		return ""
	}
	visible := guiPreviewRectIntersection(node.Bounds, *node.ClipBounds)
	if visible.Width <= 0 || visible.Height <= 0 {
		return "display:none;"
	}
	top := maxInt(0, visible.Y-node.Bounds.Y)
	right := maxInt(0, node.Bounds.X+node.Bounds.Width-(visible.X+visible.Width))
	bottom := maxInt(0, node.Bounds.Y+node.Bounds.Height-(visible.Y+visible.Height))
	left := maxInt(0, visible.X-node.Bounds.X)
	if top == 0 && right == 0 && bottom == 0 && left == 0 {
		return ""
	}
	return fmt.Sprintf("clip-path:inset(%dpx %dpx %dpx %dpx);", top, right, bottom, left)
}

func guiNodeEffectiveVisible(node GUIPreviewNode) (bool, bool) {
	if node.Scenario != nil && node.Scenario.Visible != nil {
		return *node.Scenario.Visible, true
	}
	if node.Runtime != nil && node.Runtime.Visible != nil && node.Runtime.Visible.Result != nil {
		return *node.Runtime.Visible.Result, true
	}
	return false, false
}

func guiNodeEffectiveEnabled(node GUIPreviewNode) (bool, bool) {
	if node.Scenario != nil && node.Scenario.Enabled != nil {
		return *node.Scenario.Enabled, true
	}
	if node.Runtime != nil && node.Runtime.Enabled != nil && node.Runtime.Enabled.Result != nil {
		return *node.Runtime.Enabled.Result, true
	}
	return false, false
}

func guiNodeEffectiveDown(node GUIPreviewNode) (bool, bool) {
	if node.Runtime != nil && node.Runtime.Down != nil && node.Runtime.Down.Result != nil {
		return *node.Runtime.Down.Result, true
	}
	return false, false
}

func guiNodeEffectiveSelected(node GUIPreviewNode) (bool, bool) {
	if node.Runtime != nil && node.Runtime.Selected != nil && node.Runtime.Selected.Result != nil {
		return *node.Runtime.Selected.Result, true
	}
	return false, false
}

func guiNodeEffectiveAlpha(node GUIPreviewNode) (float64, bool) {
	if node.Runtime != nil && node.Runtime.Alpha != nil && node.Runtime.Alpha.Result != nil {
		return *node.Runtime.Alpha.Result, true
	}
	if node.Semantics != nil {
		if literal, ok := parseGUIRuntimeLiteral(node.Semantics.Alpha); ok && literal.kind == guiRuntimeKindNumber {
			return literal.number, true
		}
	}
	return 0, false
}

func guiHTMLAlphaStyle(node GUIPreviewNode) string {
	alpha, known := guiNodeEffectiveAlpha(node)
	if !known || math.IsNaN(alpha) || math.IsInf(alpha, 0) {
		return ""
	}
	return "--ck3-alpha:" + guiHTMLCSSNumber(math.Max(0, math.Min(1, alpha)))
}

func guiNodeEffectiveProgress(node GUIPreviewNode) (float64, bool) {
	if node.Runtime != nil && node.Runtime.Value != nil && node.Runtime.Value.Result != nil {
		return *node.Runtime.Value.Result, true
	}
	return 0, false
}

func guiNodeEffectiveColor(binding *GUIRuntimeColorBinding) (string, bool) {
	if binding != nil && binding.Result != nil {
		return *binding.Result, true
	}
	return "", false
}

func guiHTMLColorStyle(node GUIPreviewNode) string {
	if node.Runtime == nil {
		return ""
	}
	styles := make([]string, 0, 3)
	if color, known := guiNodeEffectiveColor(node.Runtime.TintColor); known {
		styles = append(styles, "--ck3-tint-color:"+color)
	}
	if color, known := guiNodeEffectiveColor(node.Runtime.FontTintColor); known {
		styles = append(styles, "--ck3-font-tint-color:"+color, "color:var(--ck3-font-tint-color)")
	}
	return strings.Join(styles, ";")
}

func guiNodeEffectiveProgressBound(binding *GUIRuntimeNumberBinding, fallback float64, required bool) (float64, bool) {
	if binding != nil {
		if binding.Result == nil {
			return 0, false
		}
		return *binding.Result, true
	}
	return fallback, !required
}

func guiNodeEffectiveProgressRange(node GUIPreviewNode) (float64, float64, bool) {
	minRequired := node.Semantics != nil && strings.TrimSpace(node.Semantics.Min) != ""
	maxRequired := node.Semantics != nil && strings.TrimSpace(node.Semantics.Max) != ""
	var minBinding, maxBinding *GUIRuntimeNumberBinding
	if node.Runtime != nil {
		minBinding = node.Runtime.Min
		maxBinding = node.Runtime.Max
	}
	minimum, minKnown := guiNodeEffectiveProgressBound(minBinding, 0, minRequired)
	maximum, maxKnown := guiNodeEffectiveProgressBound(maxBinding, 1, maxRequired)
	return minimum, maximum, minKnown && maxKnown && maximum > minimum
}

func guiHTMLProgressStyle(node GUIPreviewNode) string {
	kind := strings.ToLower(strings.TrimSpace(node.Kind))
	if kind != "progresspie" && !strings.Contains(kind, "progressbar") {
		return ""
	}
	value, known := guiNodeEffectiveProgress(node)
	if !known {
		return ""
	}
	minimum, maximum, rangeKnown := guiNodeEffectiveProgressRange(node)
	if !rangeKnown {
		return ""
	}
	progress := math.Max(0, math.Min(1, (value-minimum)/(maximum-minimum)))
	return fmt.Sprintf("--ck3-progress:%s;--ck3-progress-angle:%sdeg;--ck3-progress-inverse:%s%%",
		guiHTMLCSSNumber(progress), guiHTMLCSSNumber(progress*360), guiHTMLCSSNumber((1-progress)*100))
}

func guiNodeEffectivePressed(node GUIPreviewNode) (bool, bool) {
	semanticCount, knownCount := 0, 0
	pressed := false
	if node.Semantics != nil && node.Semantics.Down != "" {
		semanticCount++
		if value, known := guiNodeEffectiveDown(node); known {
			knownCount++
			pressed = pressed || value
		}
	}
	if node.Semantics != nil && node.Semantics.Selected != "" {
		semanticCount++
		if value, known := guiNodeEffectiveSelected(node); known {
			knownCount++
			pressed = pressed || value
		}
	}
	if pressed {
		return true, true
	}
	return false, semanticCount > 0 && knownCount == semanticCount
}

func writeGUIHTMLRuntimeData(output *strings.Builder, node GUIPreviewNode, previewLanguage string) {
	if node.Runtime == nil {
		return
	}
	if binding := node.Runtime.Visible; binding != nil {
		writeGUIHTMLData(output, "visible-plan", strconv.Itoa(binding.PlanID))
		writeGUIHTMLData(output, "visible-runtime-status", binding.Status)
		if node.Scenario == nil || node.Scenario.Visible == nil {
			if binding.Result != nil {
				value := strconv.FormatBool(*binding.Result)
				writeGUIHTMLRawData(output, "sim-visible", value)
				writeGUIHTMLData(output, "initial-sim-visible", value)
			}
		} else {
			writeGUIHTMLData(output, "visible-override", "scenario")
		}
	}
	if binding := node.Runtime.Enabled; binding != nil {
		writeGUIHTMLData(output, "enabled-plan", strconv.Itoa(binding.PlanID))
		writeGUIHTMLData(output, "enabled-runtime-status", binding.Status)
		if node.Scenario == nil || node.Scenario.Enabled == nil {
			if binding.Result != nil {
				value := strconv.FormatBool(*binding.Result)
				writeGUIHTMLRawData(output, "sim-enabled", value)
				writeGUIHTMLData(output, "initial-sim-enabled", value)
			}
		} else {
			writeGUIHTMLData(output, "enabled-override", "scenario")
		}
	}
	if binding := node.Runtime.Down; binding != nil {
		writeGUIHTMLData(output, "down-plan", strconv.Itoa(binding.PlanID))
		writeGUIHTMLData(output, "down-runtime-status", binding.Status)
		if binding.Result != nil {
			value := strconv.FormatBool(*binding.Result)
			writeGUIHTMLRawData(output, "sim-down", value)
			writeGUIHTMLData(output, "initial-sim-down", value)
		}
	}
	if binding := node.Runtime.Selected; binding != nil {
		writeGUIHTMLData(output, "selected-plan", strconv.Itoa(binding.PlanID))
		writeGUIHTMLData(output, "selected-runtime-status", binding.Status)
		if binding.Result != nil {
			value := strconv.FormatBool(*binding.Result)
			writeGUIHTMLRawData(output, "sim-selected", value)
			writeGUIHTMLData(output, "initial-sim-selected", value)
		}
	}
	writeGUIHTMLRuntimeNumberData(output, "min", node.Runtime.Min)
	writeGUIHTMLRuntimeNumberData(output, "max", node.Runtime.Max)
	writeGUIHTMLRuntimeNumberData(output, "value", node.Runtime.Value)
	writeGUIHTMLRuntimeNumberData(output, "alpha", node.Runtime.Alpha)
	writeGUIHTMLRuntimeColorData(output, "tint-color", node.Runtime.TintColor)
	writeGUIHTMLRuntimeColorData(output, "font-tint-color", node.Runtime.FontTintColor)
	if binding := node.Runtime.Action; binding != nil {
		writeGUIHTMLData(output, "action-plan", strconv.Itoa(binding.PlanID))
		writeGUIHTMLData(output, "action-runtime-status", binding.Status)
	}
	if len(node.Runtime.Actions) > 0 {
		plans := make([]string, 0, len(node.Runtime.Actions))
		for _, binding := range node.Runtime.Actions {
			plans = append(plans, strconv.Itoa(binding.PlanID))
		}
		writeGUIHTMLData(output, "action-plans", strings.Join(plans, ","))
	}
	writeGUIHTMLRuntimeTextSet(output, "text", node.Runtime.Text)
	writeGUIHTMLRuntimeTextSet(output, "tooltip", node.Runtime.Tooltip)
	if node.Runtime.Text != nil {
		if node.Scenario != nil && node.Scenario.Text != nil {
			writeGUIHTMLData(output, "text-override", "scenario")
		} else {
			language := previewLanguage
			if language == "" {
				language = GUIPreviewLanguageRaw
			}
			if node.TextLocalization != nil && node.TextLocalization.SelectedLanguage != "" {
				language = node.TextLocalization.SelectedLanguage
			}
			if value, ok := resolvedGUIRuntimeText(node.Runtime.Text, language); ok {
				writeGUIHTMLRawData(output, "sim-text", value)
				writeGUIHTMLData(output, "initial-sim-text", value)
			}
		}
	}
	if node.Runtime.Tooltip != nil {
		language := previewLanguage
		if language == "" {
			language = GUIPreviewLanguageRaw
		}
		if node.TooltipLocalization != nil && node.TooltipLocalization.SelectedLanguage != "" {
			language = node.TooltipLocalization.SelectedLanguage
		}
		if value, ok := resolvedGUIRuntimeText(node.Runtime.Tooltip, language); ok {
			writeGUIHTMLRawData(output, "sim-tooltip", value)
		}
	}
}

func writeGUIHTMLRuntimeNumberData(output *strings.Builder, name string, binding *GUIRuntimeNumberBinding) {
	if binding == nil {
		return
	}
	writeGUIHTMLData(output, name+"-plan", strconv.Itoa(binding.PlanID))
	writeGUIHTMLData(output, name+"-runtime-status", binding.Status)
	if binding.Result != nil {
		value := strconv.FormatFloat(*binding.Result, 'g', -1, 64)
		writeGUIHTMLRawData(output, "sim-"+name, value)
		writeGUIHTMLData(output, "initial-sim-"+name, value)
	}
}

func writeGUIHTMLRuntimeColorData(output *strings.Builder, name string, binding *GUIRuntimeColorBinding) {
	if binding == nil {
		return
	}
	writeGUIHTMLData(output, name+"-plan", strconv.Itoa(binding.PlanID))
	writeGUIHTMLData(output, name+"-runtime-status", binding.Status)
	if binding.Result != nil {
		writeGUIHTMLRawData(output, "sim-"+name, *binding.Result)
		writeGUIHTMLData(output, "initial-sim-"+name, *binding.Result)
	}
}

func writeGUIHTMLRuntimeTextSet(output *strings.Builder, prefix string, set *GUIRuntimeTextBindingSet) {
	if set == nil {
		return
	}
	for _, item := range []struct {
		name    string
		binding *GUIRuntimeTextBinding
	}{{"raw", set.Raw}, {"english", set.English}, {"simp-chinese", set.SimpChinese}} {
		if item.binding == nil {
			continue
		}
		writeGUIHTMLData(output, prefix+"-plan-"+item.name, strconv.Itoa(item.binding.PlanID))
		writeGUIHTMLData(output, prefix+"-runtime-status-"+item.name, item.binding.Status)
	}
}

func writeGUIHTMLScenarioData(output *strings.Builder, scenario *GUINodeScenario) {
	if scenario == nil {
		return
	}
	writeGUIHTMLData(output, "scenario-source", scenario.Source)
	if scenario.Text != nil {
		writeGUIHTMLRawData(output, "sim-text", *scenario.Text)
		writeGUIHTMLData(output, "initial-sim-text", *scenario.Text)
	}
	if scenario.Texture != nil {
		writeGUIHTMLData(output, "scenario-texture", *scenario.Texture)
	}
	if scenario.Visible != nil {
		writeGUIHTMLRawData(output, "sim-visible", strconv.FormatBool(*scenario.Visible))
		writeGUIHTMLData(output, "initial-sim-visible", strconv.FormatBool(*scenario.Visible))
	}
	if scenario.Enabled != nil {
		writeGUIHTMLRawData(output, "sim-enabled", strconv.FormatBool(*scenario.Enabled))
		writeGUIHTMLData(output, "initial-sim-enabled", strconv.FormatBool(*scenario.Enabled))
	}
}

func writeGUIHTMLModelRowData(output *strings.Builder, row *GUIPreviewModelRow) {
	if row == nil {
		return
	}
	writeGUIHTMLData(output, "model-row-source", row.Source)
	writeGUIHTMLData(output, "model-row-collection", strconv.Itoa(row.Collection))
	writeGUIHTMLData(output, "model-row-id", row.ID)
	writeGUIHTMLData(output, "model-row-index", strconv.Itoa(row.Index))
	writeGUIHTMLData(output, "model-row-target", row.Target)
	writeGUIHTMLData(output, "model-row-datamodel", row.DataModel)
}

func writeGUIHTMLLocalizationData(output *strings.Builder, prefix string, binding *GUILocalizedText) {
	if binding == nil {
		return
	}
	writeGUIHTMLData(output, prefix+"-key", binding.Key)
	writeGUIHTMLData(output, prefix+"-selected-language", binding.SelectedLanguage)
	writeGUIHTMLData(output, prefix+"-selected", binding.SelectedText)
	if binding.English != nil {
		writeGUIHTMLData(output, prefix+"-english", binding.English.DisplayText)
		writeGUIHTMLData(output, prefix+"-english-source", binding.English.Source)
	}
	if binding.SimpChinese != nil {
		writeGUIHTMLData(output, prefix+"-simp-chinese", binding.SimpChinese.DisplayText)
		writeGUIHTMLData(output, prefix+"-simp-chinese-source", binding.SimpChinese.Source)
	}
	writeGUIHTMLData(output, prefix+"-bilingual", selectGUIPreviewLocalizedText(binding, GUIPreviewLanguageBilingual))
	if binding.Partial {
		writeGUIHTMLData(output, prefix+"-partial", "true")
	}
}

func collectGUIHTMLTextureAssets(nodes []GUIPreviewNode) guiHTMLTextureAssets {
	assets := guiHTMLTextureAssets{classes: map[string]string{}}
	add := func(dataURI string) {
		if dataURI == "" {
			return
		}
		if _, exists := assets.classes[dataURI]; exists {
			return
		}
		className := "ck3-texture-a" + strconv.Itoa(len(assets.values))
		assets.classes[dataURI] = className
		assets.values = append(assets.values, dataURI)
	}
	for _, node := range nodes {
		for _, ref := range guiNodeTextureRefs(&node) {
			if ref == nil || !ref.Embedded || ref.dataURI == "" {
				continue
			}
			add(ref.dataURI)
			for _, dataURI := range ref.frameDataURIs {
				add(dataURI)
			}
		}
	}
	return assets
}

func writeGUIHTMLTextureAssetStyles(output *strings.Builder, assets guiHTMLTextureAssets) {
	for index, dataURI := range assets.values {
		variable := "--ck3-texture-image-a" + strconv.Itoa(index)
		output.WriteString(":root{")
		output.WriteString(variable)
		output.WriteString(":url(\"")
		output.WriteString(dataURI)
		output.WriteString("\")}\n")
		output.WriteString(".ck3-texture-a")
		output.WriteString(strconv.Itoa(index))
		output.WriteString("{--ck3-texture-image:var(")
		output.WriteString(variable)
		output.WriteString(");background-image:var(--ck3-texture-image)}\n")
	}
}

func writeGUIHTMLTextureImage(output *strings.Builder, node GUIPreviewNode, assets guiHTMLTextureAssets) {
	if node.NoProgressTextureRef != nil {
		background := node
		background.TextureRef = node.NoProgressTextureRef
		background.TextureFrames = nil
		writeGUIHTMLTextureImageRef(output, background, assets, "ck3-no-progress")
	}
	extraClass := ""
	if strings.Contains(strings.ToLower(strings.TrimSpace(node.Kind)), "progressbar") {
		extraClass = "ck3-progress-fill"
	}
	writeGUIHTMLTextureImageRef(output, node, assets, extraClass)
}

func writeGUIHTMLTextureImageRef(output *strings.Builder, node GUIPreviewNode, assets guiHTMLTextureAssets, extraClass string) {
	if node.TextureRef == nil || !node.TextureRef.Embedded || node.TextureRef.dataURI == "" {
		return
	}
	className := assets.classes[node.TextureRef.dataURI]
	if className == "" {
		return
	}
	output.WriteString("<span class=\"ck3-texture ")
	output.WriteString(className)
	if guiHTMLTextureHasFrameImages(node) {
		output.WriteString(" ck3-framed-images")
	} else if guiHTMLTextureIsFramed(node) {
		output.WriteString(" ck3-framed")
	}
	if guiHTMLTextureIsNineSlice(node) {
		output.WriteString(" ck3-nine-slice")
		if strings.Contains(strings.ToLower(node.TextureSlice.SpriteType), "tiled") {
			output.WriteString(" ck3-nine-slice-tiled")
		}
	}
	if mirrorClass := guiHTMLMirrorClass(node.Mirror); mirrorClass != "" {
		output.WriteString(" ")
		output.WriteString(mirrorClass)
	}
	if extraClass != "" {
		output.WriteString(" ")
		output.WriteString(extraClass)
	}
	output.WriteString("\"")
	styleParts := []string{
		guiHTMLTextureFrameStyle(node),
		guiHTMLTextureFrameImageStyle(node, assets),
		guiHTMLTextureSliceStyle(node),
		guiHTMLTextureMaskStyle(node, assets),
	}
	filteredStyleParts := styleParts[:0]
	for _, part := range styleParts {
		if part = strings.Trim(part, "; "); part != "" {
			filteredStyleParts = append(filteredStyleParts, part)
		}
	}
	style := strings.Join(filteredStyleParts, ";")
	if style != "" {
		output.WriteString(" style=\"")
		output.WriteString(style)
		output.WriteString("\"")
	}
	output.WriteString(" aria-hidden=\"true\"></span>")
}

func bindGUIHTMLTextureModifierMasks(nodes []GUIPreviewNode) {
	for index := range nodes {
		if !strings.EqualFold(strings.TrimSpace(nodes[index].Kind), "modify_texture") {
			continue
		}
		parent := nodes[index].Parent
		for parent >= 0 && parent < len(nodes) {
			candidate := nodes[parent]
			if candidate.TextureRef != nil && candidate.TextureRef.Embedded && candidate.TextureRef.dataURI != "" {
				dataURI := candidate.TextureRef.dataURI
				if guiHTMLTextureHasFrameImages(candidate) {
					up, _, _, _ := guiHTMLTextureFrameIndices(candidate, len(candidate.TextureRef.frameDataURIs))
					dataURI = candidate.TextureRef.frameDataURIs[up]
				}
				nodes[index].textureMaskDataURI = dataURI
				break
			}
			parent = candidate.Parent
		}
	}
}

func guiHTMLTextureMaskStyle(node GUIPreviewNode, assets guiHTMLTextureAssets) string {
	if node.textureMaskDataURI == "" {
		return ""
	}
	className := assets.classes[node.textureMaskDataURI]
	suffix := strings.TrimPrefix(className, "ck3-texture-")
	if className == "" || suffix == className {
		return ""
	}
	image := "var(--ck3-texture-image-" + suffix + ")"
	return "-webkit-mask-image:" + image + ";mask-image:" + image +
		";-webkit-mask-position:center;mask-position:center;-webkit-mask-repeat:no-repeat;mask-repeat:no-repeat" +
		";-webkit-mask-size:contain;mask-size:contain;mask-mode:alpha"
}

func guiHTMLTextureIsNineSlice(node GUIPreviewNode) bool {
	return node.TextureSlice != nil && node.TextureRef != nil && node.TextureRef.Embedded &&
		(node.TextureRef.FrameCols*node.TextureRef.FrameRows <= 1 || guiHTMLTextureHasFrameImages(node))
}

func guiHTMLTextureSliceStyle(node GUIPreviewNode) string {
	if !guiHTMLTextureIsNineSlice(node) {
		return ""
	}
	density := node.TextureSlice.TextureDensity
	if density <= 0 {
		density = 1
	}
	sourceW, sourceH := node.TextureRef.SourceW, node.TextureRef.SourceH
	embeddedW, embeddedH := node.TextureRef.Width, node.TextureRef.Height
	if guiHTMLTextureHasFrameImages(node) && node.TextureFrames != nil {
		sourceW, sourceH = node.TextureFrames.Width, node.TextureFrames.Height
		embeddedW, embeddedH = node.TextureRef.FrameW, node.TextureRef.FrameH
	}
	scaleX, scaleY := 1.0, 1.0
	if sourceW > 0 {
		scaleX = float64(embeddedW) / float64(sourceW)
	}
	if sourceH > 0 {
		scaleY = float64(embeddedH) / float64(sourceH)
	}
	sourceX := math.Max(0, float64(node.TextureSlice.BorderX)*scaleX)
	sourceY := math.Max(0, float64(node.TextureSlice.BorderY)*scaleY)
	displayX := math.Max(0, float64(node.TextureSlice.BorderX)/density)
	displayY := math.Max(0, float64(node.TextureSlice.BorderY)/density)
	return fmt.Sprintf("--ck3-source-slice-x:%s;--ck3-source-slice-y:%s;--ck3-slice-x:%spx;--ck3-slice-y:%spx",
		guiHTMLCSSNumber(sourceX), guiHTMLCSSNumber(sourceY), guiHTMLCSSNumber(displayX), guiHTMLCSSNumber(displayY))
}

func guiHTMLCSSNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func writeGUIHTMLTextureFrameData(output *strings.Builder, frames *GUITextureFrames) {
	if frames == nil {
		return
	}
	writeGUIHTMLData(output, "frame-size", fmt.Sprintf("%dx%d", frames.Width, frames.Height))
	if frames.Frame != nil {
		writeGUIHTMLData(output, "frame", strconv.Itoa(*frames.Frame))
	}
	if frames.UpFrame != nil {
		writeGUIHTMLData(output, "up-frame", strconv.Itoa(*frames.UpFrame))
	}
	if frames.OverFrame != nil {
		writeGUIHTMLData(output, "over-frame", strconv.Itoa(*frames.OverFrame))
	}
	if frames.DownFrame != nil {
		writeGUIHTMLData(output, "down-frame", strconv.Itoa(*frames.DownFrame))
	}
	if frames.DisabledFrame != nil {
		writeGUIHTMLData(output, "disabled-frame", strconv.Itoa(*frames.DisabledFrame))
	}
}

func writeGUIHTMLTextureSliceData(output *strings.Builder, slice *GUITextureSlice) {
	if slice == nil {
		return
	}
	writeGUIHTMLData(output, "sprite-type", slice.SpriteType)
	writeGUIHTMLData(output, "sprite-border", fmt.Sprintf("%dx%d", slice.BorderX, slice.BorderY))
	if slice.TextureDensity > 0 {
		writeGUIHTMLData(output, "texture-density", guiHTMLCSSNumber(slice.TextureDensity))
	}
}

func guiHTMLTextureIsFramed(node GUIPreviewNode) bool {
	return node.TextureFrames != nil && node.TextureRef != nil && node.TextureRef.FrameCols*node.TextureRef.FrameRows > 1
}

func guiHTMLTextureHasFrameImages(node GUIPreviewNode) bool {
	if !guiHTMLTextureIsFramed(node) {
		return false
	}
	total := node.TextureRef.FrameCols * node.TextureRef.FrameRows
	return total > 1 && node.TextureRef.FrameImages == total && len(node.TextureRef.frameDataURIs) == total
}

func guiHTMLTextureFrameStyle(node GUIPreviewNode) string {
	if !guiHTMLTextureIsFramed(node) || guiHTMLTextureHasFrameImages(node) {
		return ""
	}
	columns := maxInt(1, node.TextureRef.FrameCols)
	rows := maxInt(1, node.TextureRef.FrameRows)
	total := columns * rows
	up, over, down, disabled := guiHTMLTextureFrameIndices(node, total)
	upX, upY := guiHTMLTextureFramePosition(up, columns, rows)
	overX, overY := guiHTMLTextureFramePosition(over, columns, rows)
	downX, downY := guiHTMLTextureFramePosition(down, columns, rows)
	disabledX, disabledY := guiHTMLTextureFramePosition(disabled, columns, rows)
	return fmt.Sprintf(
		"background-size:%d%% %d%%;--ck3-frame-up-x:%s;--ck3-frame-up-y:%s;--ck3-frame-over-x:%s;--ck3-frame-over-y:%s;--ck3-frame-down-x:%s;--ck3-frame-down-y:%s;--ck3-frame-disabled-x:%s;--ck3-frame-disabled-y:%s",
		columns*100, rows*100, upX, upY, overX, overY, downX, downY, disabledX, disabledY,
	)
}

func guiHTMLTextureFrameImageStyle(node GUIPreviewNode, assets guiHTMLTextureAssets) string {
	if !guiHTMLTextureHasFrameImages(node) {
		return ""
	}
	total := len(node.TextureRef.frameDataURIs)
	up, over, down, disabled := guiHTMLTextureFrameIndices(node, total)
	variable := func(index int) string {
		className := assets.classes[node.TextureRef.frameDataURIs[index]]
		suffix := strings.TrimPrefix(className, "ck3-texture-")
		if className == "" || suffix == className {
			return ""
		}
		return "var(--ck3-texture-image-" + suffix + ")"
	}
	return fmt.Sprintf("--ck3-frame-up-image:%s;--ck3-frame-over-image:%s;--ck3-frame-down-image:%s;--ck3-frame-disabled-image:%s",
		variable(up), variable(over), variable(down), variable(disabled))
}

func guiHTMLTextureFrameIndices(node GUIPreviewNode, total int) (int, int, int, int) {
	initial := guiHTMLTextureFrameIndex(node.TextureFrames.Frame, true, 0, total)
	up := guiHTMLTextureFrameIndex(node.TextureFrames.UpFrame, false, initial, total)
	over := guiHTMLTextureFrameIndex(node.TextureFrames.OverFrame, false, up, total)
	down := guiHTMLTextureFrameIndex(node.TextureFrames.DownFrame, false, over, total)
	disabled := guiHTMLTextureFrameIndex(node.TextureFrames.DisabledFrame, false, up, total)
	return up, over, down, disabled
}

func guiHTMLTextureFrameIndex(raw *int, zeroBased bool, fallback, total int) int {
	if raw == nil {
		return minInt(maxInt(0, fallback), maxInt(0, total-1))
	}
	index := *raw
	if !zeroBased {
		index--
	}
	return minInt(maxInt(0, index), maxInt(0, total-1))
}

func guiHTMLTextureFramePosition(index, columns, rows int) (string, string) {
	column := index % columns
	row := index / columns
	position := func(offset, count int) string {
		if count <= 1 {
			return "0%"
		}
		value := float64(offset) * 100 / float64(count-1)
		return strconv.FormatFloat(value, 'f', 3, 64) + "%"
	}
	return position(column, columns), position(row, rows)
}

func guiHTMLMirrorClass(mirror string) string {
	switch strings.ToLower(strings.TrimSpace(mirror)) {
	case "horizontal":
		return "ck3-mirror-horizontal"
	case "vertical":
		return "ck3-mirror-vertical"
	case "horizontal|vertical", "vertical|horizontal":
		return "ck3-mirror-both"
	default:
		return ""
	}
}

func writeGUIHTMLSemantics(output *strings.Builder, semantics *GUISemantics) {
	if semantics == nil {
		return
	}
	writeGUIHTMLData(output, "visible", semantics.Visible)
	writeGUIHTMLData(output, "enabled", semantics.Enabled)
	writeGUIHTMLData(output, "down", semantics.Down)
	writeGUIHTMLData(output, "selected", semantics.Selected)
	writeGUIHTMLData(output, "alpha", semantics.Alpha)
	writeGUIHTMLData(output, "min", semantics.Min)
	writeGUIHTMLData(output, "max", semantics.Max)
	writeGUIHTMLData(output, "value", semantics.Value)
	writeGUIHTMLData(output, "tint-color", semantics.TintColor)
	writeGUIHTMLData(output, "font-tint-color", semantics.FontTintColor)
	writeGUIHTMLData(output, "data-context", semantics.DataContext)
	writeGUIHTMLData(output, "data-model", semantics.DataModel)
	writeGUIHTMLData(output, "on-click", semantics.OnClick)
	if len(semantics.OnClicks) > 0 {
		writeGUIHTMLData(output, "on-clicks", strings.Join(semantics.OnClicks, "\n"))
		writeGUIHTMLData(output, "on-click-count", strconv.Itoa(len(semantics.OnClicks)))
	} else if semantics.OnClick != "" {
		writeGUIHTMLData(output, "on-click-count", "1")
	}
	writeGUIHTMLData(output, "tooltip", semantics.Tooltip)
	writeGUIHTMLData(output, "raw-text", semantics.RawText)
	writeGUIHTMLData(output, "raw-texture", semantics.RawTexture)
	writeGUIHTMLData(output, "no-progress-texture", semantics.NoProgressTexture)
	writeGUIHTMLData(output, "state", semantics.State)
}

func writeGUIHTMLData(output *strings.Builder, name, value string) {
	writeGUIHTMLRawData(output, "ck3-"+name, value)
}

func writeGUIHTMLRawData(output *strings.Builder, name, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	output.WriteString(" data-")
	output.WriteString(name)
	output.WriteString("=\"")
	output.WriteString(html.EscapeString(value))
	output.WriteString("\"")
}

func guiHTMLKindClass(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch {
	case kind == "hbox" || kind == "vbox" || kind == "widget" || strings.Contains(kind, "container"):
		return "ck3-container"
	case kind == "progresspie":
		return "ck3-image ck3-progresspie"
	case strings.Contains(kind, "progressbar"):
		return "ck3-image ck3-progressbar"
	case strings.Contains(kind, "button"):
		return "ck3-button"
	case kind == "modify_texture":
		return "ck3-image"
	case strings.Contains(kind, "text"):
		return "ck3-text"
	case strings.Contains(kind, "icon") || kind == "background":
		return "ck3-image"
	case kind == "expand":
		return "ck3-expand"
	default:
		return "ck3-generic"
	}
}

func guiHTMLTextureBlendClass(node GUIPreviewNode) string {
	switch node.TextureBlendMode {
	case "add", "screen":
		return "ck3-blend-screen"
	case "multiply", "alphamultiply":
		return "ck3-blend-multiply"
	case "overlay":
		return "ck3-blend-overlay"
	case "colordodge":
		return "ck3-blend-color-dodge"
	default:
		return ""
	}
}

func guiHTMLNodeLabel(node GUIPreviewNode) string {
	if text := strings.Trim(strings.TrimSpace(guiPreviewNodeDisplayText(node)), "\""); text != "" {
		return text
	}
	if node.Semantics != nil {
		if text := strings.Trim(strings.TrimSpace(node.Semantics.RawText), "\""); text != "" {
			return text
		}
	}
	if node.StateDefinition != nil {
		if node.StateDefinition.Name != "" {
			return node.StateDefinition.Name
		}
	}
	if node.Name != "" {
		return node.Name
	}
	if node.Texture != "" {
		texture := strings.Trim(strings.TrimSpace(node.Texture), "\"")
		if strings.ContainsAny(texture, "[]") {
			return texture
		}
		return path.Base(strings.ReplaceAll(texture, "\\", "/"))
	}
	return node.Kind
}

func guiHTMLStageLabel(node GUIPreviewNode) string {
	if text := strings.Trim(strings.TrimSpace(guiPreviewNodeDisplayText(node)), "\""); text != "" {
		return text
	}
	if node.Semantics != nil {
		if text := strings.Trim(strings.TrimSpace(node.Semantics.RawText), "\""); text != "" {
			return text
		}
	}
	if node.TextureRef != nil && node.TextureRef.Embedded {
		return ""
	}
	return guiHTMLNodeLabel(node)
}

func guiHTMLHasContentLabel(node GUIPreviewNode) bool {
	if strings.TrimSpace(guiPreviewNodeDisplayText(node)) != "" || node.TextLocalization != nil {
		return true
	}
	return node.Semantics != nil && strings.TrimSpace(node.Semantics.RawText) != ""
}

func guiHTMLRawStageLabel(node GUIPreviewNode) string {
	textLocalization := node.TextLocalization
	scenario := node.Scenario
	runtime := node.Runtime
	node.TextLocalization = nil
	node.Scenario = nil
	node.Runtime = nil
	label := guiHTMLStageLabel(node)
	node.TextLocalization = textLocalization
	node.Scenario = scenario
	node.Runtime = runtime
	return label
}

func guiHTMLLocalizedStageLabel(node GUIPreviewNode, language string) string {
	if node.Runtime != nil {
		if value, ok := resolvedGUIRuntimeText(node.Runtime.Text, language); ok && value != "" {
			return value
		}
	}
	if node.TextLocalization == nil {
		return guiHTMLRawStageLabel(node)
	}
	if value := selectGUIPreviewLocalizedText(node.TextLocalization, language); value != "" {
		return value
	}
	return guiHTMLRawStageLabel(node)
}

func guiHTMLNodeDescription(node GUIPreviewNode) string {
	parts := []string{
		fmt.Sprintf("node %d", node.Index), fmt.Sprintf("parent %d", node.Parent), node.Kind,
		fmt.Sprintf("bounds %d,%d %dx%d", node.Bounds.X, node.Bounds.Y, node.Bounds.Width, node.Bounds.Height),
	}
	if node.Name != "" {
		parts = append(parts, "name "+node.Name)
	}
	if label := guiHTMLNodeLabel(node); label != "" && label != node.Name && label != node.Kind {
		parts = append(parts, "label "+label)
	}
	if node.Semantics != nil {
		if node.Semantics.DataContext != "" {
			parts = append(parts, "datacontext "+node.Semantics.DataContext)
		}
		if node.Semantics.Visible != "" {
			parts = append(parts, "visible "+node.Semantics.Visible)
		}
		if node.Semantics.Down != "" {
			parts = append(parts, "down "+node.Semantics.Down)
		}
		if node.Semantics.Selected != "" {
			parts = append(parts, "selected "+node.Semantics.Selected)
		}
		if node.Semantics.Min != "" {
			parts = append(parts, "min "+node.Semantics.Min)
		}
		if node.Semantics.Max != "" {
			parts = append(parts, "max "+node.Semantics.Max)
		}
		if node.Semantics.Value != "" {
			parts = append(parts, "value "+node.Semantics.Value)
		}
		if node.Semantics.TintColor != "" {
			parts = append(parts, "tintcolor "+node.Semantics.TintColor)
		}
		if node.Semantics.FontTintColor != "" {
			parts = append(parts, "fonttintcolor "+node.Semantics.FontTintColor)
		}
		if len(node.Semantics.OnClicks) > 0 {
			for _, expression := range node.Semantics.OnClicks {
				parts = append(parts, "onclick "+expression)
			}
		} else if node.Semantics.OnClick != "" {
			parts = append(parts, "onclick "+node.Semantics.OnClick)
		}
	}
	if node.StateDefinition != nil {
		parts = append(parts, "state "+node.StateDefinition.Name)
		if node.StateDefinition.Alpha != "" {
			parts = append(parts, "alpha "+node.StateDefinition.Alpha)
		}
		if node.StateDefinition.Duration != "" {
			parts = append(parts, "duration "+node.StateDefinition.Duration)
		}
	}
	if node.Approximate {
		parts = append(parts, "approximate")
	}
	return strings.Join(parts, "; ")
}
