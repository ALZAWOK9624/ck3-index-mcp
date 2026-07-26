package indexer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
)

func renderGUIHTMLInspectorPreview(preview GUIPreviewResult) (GUIHTMLPreview, error) {
	displayNodes, _, _, _, _ := fitGUIPreviewNodes(preview.Nodes, preview.Width, preview.Height)
	bindGUIHTMLTextureModifierMasks(displayNodes)
	textureAssets := collectGUIHTMLTextureAssets(displayNodes)
	behaviors := summarizeGUIHTMLBehaviors(displayNodes)
	applyGUIHTMLRuntimeStats(&behaviors, preview.Runtime)
	language, err := normalizeGUIPreviewLanguage(preview.Language)
	if err != nil {
		return GUIHTMLPreview{}, err
	}
	scriptDigest := sha256.Sum256([]byte(guiHTMLInspectorScript))
	scriptPolicy := "sha256-" + base64.StdEncoding.EncodeToString(scriptDigest[:])

	var output strings.Builder
	output.Grow(36*1024 + len(displayNodes)*1100)
	output.WriteString("<!doctype html>\n<html lang=\"und\">\n<head>\n<meta charset=\"utf-8\">\n")
	output.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'; script-src '")
	output.WriteString(scriptPolicy)
	output.WriteString("'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'\">\n")
	output.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n<title>")
	output.WriteString(html.EscapeString(preview.Symbol))
	output.WriteString(" · CK3 GUI inspector</title>\n<style>\n")
	output.WriteString(guiHTMLInspectorStyles)
	writeGUIHTMLTextureAssetStyles(&output, textureAssets)
	output.WriteString("</style>\n</head>\n<body>\n<main id=\"ck3-gui-inspector\" class=\"ck3-inspector\" data-ck3-schema=\"")
	output.WriteString(GUIHTMLSchemaVersion)
	output.WriteString("\" data-ck3-symbol=\"")
	output.WriteString(html.EscapeString(preview.Symbol))
	output.WriteString("\" data-ck3-symbol-kind=\"")
	output.WriteString(html.EscapeString(preview.SymbolKind))
	output.WriteString("\" data-ck3-width=\"")
	output.WriteString(strconv.Itoa(preview.Width))
	output.WriteString("\" data-ck3-height=\"")
	output.WriteString(strconv.Itoa(preview.Height))
	output.WriteString("\" data-ck3-language=\"")
	output.WriteString(html.EscapeString(language))
	output.WriteString("\">\n")
	writeGUIHTMLInspectorToolbar(&output, preview, behaviors)
	output.WriteString("<div class=\"ck3-workspace\">\n<aside class=\"ck3-panel ck3-tree-panel\" aria-label=\"GUI element tree\">\n<div class=\"ck3-panel-heading\"><strong>Elements</strong><span>")
	output.WriteString(strconv.Itoa(len(displayNodes)))
	output.WriteString(" nodes</span></div>\n<div id=\"ck3-tree\" class=\"ck3-tree\">\n")
	for _, node := range displayNodes {
		writeGUIHTMLInspectorTreeItem(&output, node)
	}
	output.WriteString("</div>\n</aside>\n<section id=\"ck3-viewport\" class=\"ck3-viewport\" aria-label=\"GUI preview canvas\">\n<div id=\"ck3-stage-shell\" class=\"ck3-stage-shell\"><div id=\"ck3-canvas\" class=\"ck3-canvas\" style=\"width:")
	output.WriteString(strconv.Itoa(preview.Width))
	output.WriteString("px;height:")
	output.WriteString(strconv.Itoa(preview.Height))
	output.WriteString("px\">\n")
	for _, node := range displayNodes {
		writeGUIHTMLInspectorNode(&output, node, textureAssets, preview.Language)
	}
	for _, node := range displayNodes {
		if node.Layout != nil && node.Layout.ScrollViewport {
			writeGUIHTMLInspectorScrollControl(&output, node)
		}
	}
	output.WriteString("<div id=\"ck3-runtime-tooltip\" class=\"ck3-runtime-tooltip\" role=\"tooltip\" aria-hidden=\"true\"></div>\n")
	output.WriteString("</div></div>\n</section>\n")
	writeGUIHTMLInspectorDetails(&output, preview.Runtime)
	output.WriteString("</div>\n")
	writeGUIHTMLRuntimePlans(&output, preview.Runtime)
	output.WriteString("<div id=\"ck3-status\" class=\"ck3-status\" role=\"status\">Select a node to inspect its resolved layout and simulate runtime state.</div>\n</main>\n<script>")
	output.WriteString(guiHTMLInspectorScript)
	output.WriteString("</script>\n</body>\n</html>\n")

	document := output.String()
	if len(document) > GUIHTMLMaxBytes {
		return GUIHTMLPreview{}, fmt.Errorf("GUI HTML inspector exceeds %d bytes", GUIHTMLMaxBytes)
	}
	digest := sha256.Sum256([]byte(document))
	return GUIHTMLPreview{
		SchemaVersion: GUIHTMLSchemaVersion, Mode: GUIHTMLModeInspector, Document: document,
		Bytes: len(document), SHA256: fmt.Sprintf("%x", digest), NodeCount: len(displayNodes),
		Scripts: true, ScriptPolicy: "fixed-generator-script", ScriptSHA256: scriptPolicy,
		ExternalRequests: false, ModelReadable: true, Behaviors: behaviors,
	}, nil
}

func writeGUIHTMLInspectorToolbar(output *strings.Builder, preview GUIPreviewResult, behaviors GUIHTMLBehaviorStats) {
	output.WriteString("<header class=\"ck3-toolbar\">\n<div class=\"ck3-identity\"><strong>")
	output.WriteString(html.EscapeString(preview.Symbol))
	output.WriteString("</strong><span>")
	output.WriteString(html.EscapeString(preview.SymbolKind))
	output.WriteString("</span>")
	if preview.Approximate {
		output.WriteString("<span class=\"ck3-badge ck3-warning\">approximate</span>")
	}
	output.WriteString("</div>\n<label class=\"ck3-field ck3-search\"><span>Search</span><input id=\"ck3-search\" type=\"search\" placeholder=\"name, kind, source\" autocomplete=\"off\"></label>\n")
	output.WriteString("<label class=\"ck3-field ck3-zoom\"><span>Zoom</span><input id=\"ck3-zoom\" type=\"range\" min=\"25\" max=\"200\" step=\"5\" value=\"100\"><output id=\"ck3-zoom-value\" for=\"ck3-zoom\">100%</output></label>\n")
	output.WriteString("<button id=\"ck3-fit\" class=\"ck3-control\" type=\"button\">Fit</button><label class=\"ck3-check\"><input id=\"ck3-visual-mode\" type=\"checkbox\" checked><span>Visual</span></label><label class=\"ck3-check\"><input id=\"ck3-replay-clicks\" type=\"checkbox\" checked><span>Replay clicks</span></label><label class=\"ck3-check\"><input id=\"ck3-show-approx\" type=\"checkbox\" checked><span>Approximate</span></label><label class=\"ck3-check\"><input id=\"ck3-show-labels\" type=\"checkbox\"><span>Node labels</span></label><button id=\"ck3-reset-all\" class=\"ck3-control\" type=\"button\">Reset simulation</button>\n")
	output.WriteString("<label class=\"ck3-field ck3-language\"><span>Language</span><select id=\"ck3-language\"><option value=\"raw\">Raw keys</option><option value=\"english\">English</option><option value=\"simp_chinese\">简体中文</option><option value=\"bilingual\">双语</option></select></label>\n")
	output.WriteString("<div class=\"ck3-behavior-counts\" aria-label=\"Simulatable behaviors\"><span>V ")
	output.WriteString(strconv.Itoa(behaviors.VisibleExpressions))
	output.WriteString("</span><span>E ")
	output.WriteString(strconv.Itoa(behaviors.EnabledExpressions))
	output.WriteString("</span><span>D ")
	output.WriteString(strconv.Itoa(behaviors.DownExpressions))
	output.WriteString("</span><span>S ")
	output.WriteString(strconv.Itoa(behaviors.SelectedExpressions))
	output.WriteString("</span><span>CK ")
	output.WriteString(strconv.Itoa(behaviors.CheckedExpressions))
	output.WriteString("</span><span>TW ")
	output.WriteString(strconv.Itoa(behaviors.StateTriggers))
	output.WriteString("</span><span>A ")
	output.WriteString(strconv.Itoa(behaviors.AlphaExpressions))
	output.WriteString("</span><span>SC ")
	output.WriteString(strconv.Itoa(behaviors.ScaleExpressions))
	output.WriteString("</span><span>UV ")
	output.WriteString(strconv.Itoa(behaviors.RotateUVExpressions))
	output.WriteString("</span><span>P ")
	output.WriteString(strconv.Itoa(behaviors.ValueExpressions))
	output.WriteString("</span><span>T ")
	output.WriteString(strconv.Itoa(behaviors.DynamicTexts))
	output.WriteString("</span><span>C ")
	output.WriteString(strconv.Itoa(behaviors.ClickActions))
	output.WriteString("</span><span>RC ")
	output.WriteString(strconv.Itoa(behaviors.RightClickActions))
	output.WriteString("</span><span>O ")
	output.WriteString(strconv.Itoa(behaviors.TooltipOverlays))
	output.WriteString("</span><span>R ")
	output.WriteString(strconv.Itoa(behaviors.ScrollViewports))
	output.WriteString("</span><span>M ")
	output.WriteString(strconv.Itoa(behaviors.ModelRows))
	output.WriteString("</span></div>\n</header>\n")
}

func writeGUIHTMLInspectorTreeItem(output *strings.Builder, node GUIPreviewNode) {
	output.WriteString("<button type=\"button\" class=\"ck3-tree-item\" data-ck3-tree-index=\"")
	output.WriteString(strconv.Itoa(node.Index))
	output.WriteString("\" data-ck3-search=\"")
	output.WriteString(html.EscapeString(strings.ToLower(strings.Join([]string{
		node.Kind, strings.Join(node.TypeChain, " "), node.Name, node.Source, node.Align, guiHTMLRawStageLabel(node), guiHTMLNodeDescription(node),
		guiHTMLLocalizedStageLabel(node, GUIPreviewLanguageEnglish),
		guiHTMLLocalizedStageLabel(node, GUIPreviewLanguageSimpChinese), guiHTMLModelRowSearch(node.ModelRow),
	}, " "))))
	output.WriteString("\" style=\"--ck3-depth:")
	output.WriteString(strconv.Itoa(node.Depth))
	output.WriteString("\"><span class=\"ck3-tree-kind\">")
	output.WriteString(html.EscapeString(node.Kind))
	output.WriteString("</span><span class=\"ck3-tree-name\">")
	label := node.Name
	if label == "" {
		label = guiHTMLNodeLabel(node)
	}
	output.WriteString(html.EscapeString(label))
	if node.ModelRow != nil {
		output.WriteString(" <span class=\"ck3-tree-row\">row ")
		output.WriteString(html.EscapeString(node.ModelRow.ID))
		output.WriteString("</span>")
	}
	output.WriteString("</span><span class=\"ck3-tree-index\">#")
	output.WriteString(strconv.Itoa(node.Index))
	output.WriteString("</span></button>\n")
}

func writeGUIHTMLInspectorNode(output *strings.Builder, node GUIPreviewNode, textureAssets guiHTMLTextureAssets, previewLanguage string) {
	classes := []string{"ck3-node", guiHTMLKindClass(node.Kind)}
	checkable := guiHTMLNodeIsCheckable(node)
	if checkable {
		classes = append(classes, "ck3-checkable")
	}
	if blendClass := guiHTMLTextureBlendClass(node); blendClass != "" {
		classes = append(classes, "ck3-texture-modifier", blendClass)
	}
	if node.BehaviorOnly {
		classes = append(classes, "ck3-behavior-node")
	}
	if node.Overlay != nil {
		if node.Overlay.Role == "tooltip_root" {
			classes = append(classes, "ck3-tooltip-root")
		} else {
			classes = append(classes, "ck3-tooltip-content")
		}
	}
	if node.TextureRef != nil && node.TextureRef.Embedded {
		classes = append(classes, "ck3-has-texture")
	}
	if node.Approximate {
		classes = append(classes, "ck3-approximate")
	}
	initialClipStyle := guiHTMLInitialClipStyle(node)
	if initialClipStyle == "display:none;" {
		classes = append(classes, "is-scroll-clipped")
		initialClipStyle = ""
	}
	if value, known := guiNodeEffectiveVisible(node); known && !value {
		classes = append(classes, "is-sim-hidden")
	}
	if value, known := guiNodeEffectiveEnabled(node); known && !value {
		classes = append(classes, "is-sim-disabled")
	}
	if value, known := guiNodeEffectiveDown(node); known && value {
		classes = append(classes, "is-sim-down")
	}
	if value, known := guiNodeEffectiveSelected(node); known && value {
		classes = append(classes, "is-sim-selected-state")
	}
	if value, known := guiNodeEffectiveChecked(node); known && value && checkable {
		classes = append(classes, "is-sim-checked")
	}
	stageLabel := guiHTMLStageLabel(node)
	rawLabel := guiHTMLRawStageLabel(node)
	output.WriteString("<button type=\"button\" id=\"ck3-node-")
	output.WriteString(strconv.Itoa(node.Index))
	output.WriteString("\" class=\"")
	output.WriteString(strings.Join(classes, " "))
	output.WriteString("\" data-ck3-stage-node=\"true\" data-ck3-index=\"")
	output.WriteString(strconv.Itoa(node.Index))
	output.WriteString("\" data-ck3-parent=\"")
	output.WriteString(strconv.Itoa(node.Parent))
	output.WriteString("\" data-ck3-kind=\"")
	output.WriteString(html.EscapeString(node.Kind))
	output.WriteString("\" data-ck3-original-label=\"")
	output.WriteString(html.EscapeString(rawLabel))
	output.WriteString("\" data-ck3-bounds=\"")
	fmt.Fprintf(output, "%d,%d %dx%d", node.Bounds.X, node.Bounds.Y, node.Bounds.Width, node.Bounds.Height)
	output.WriteString("\"")
	if checkable {
		writeGUIHTMLData(output, "checkable", "true")
		if node.Runtime == nil || node.Runtime.Checked == nil || node.Runtime.Checked.Result == nil {
			if value, known := guiNodeEffectiveChecked(node); known {
				encoded := strconv.FormatBool(value)
				writeGUIHTMLRawData(output, "sim-checked", encoded)
				writeGUIHTMLData(output, "initial-sim-checked", encoded)
			}
		}
	}
	writeGUIHTMLData(output, "name", node.Name)
	writeGUIHTMLData(output, "type-chain", strings.Join(node.TypeChain, ","))
	writeGUIHTMLData(output, "source", node.Source)
	writeGUIHTMLData(output, "texture", node.Texture)
	writeGUIHTMLData(output, "background-texture", node.BackgroundTexture)
	writeGUIHTMLData(output, "mask", node.MaskTexture)
	writeGUIHTMLData(output, "coat-of-arms-mask-resource", node.CoatOfArmsMask)
	writeGUIHTMLLineGeometryData(output, node.LineGeometry)
	writeGUIHTMLVectorData(output, "coat-of-arms-offset-literal", node.CoatOfArmsOffset)
	writeGUIHTMLVectorData(output, "coat-of-arms-scale-literal", node.CoatOfArmsScale)
	writeGUIHTMLData(output, "mirror", node.Mirror)
	writeGUIHTMLData(output, "fit-type", node.FitType)
	writeGUIHTMLData(output, "align", node.Align)
	writeGUIHTMLData(output, "texture-blend-mode", node.TextureBlendMode)
	writeGUIHTMLTextureFrameData(output, node.TextureFrames)
	writeGUIHTMLTextureSliceData(output, node.TextureSlice)
	if node.Line > 0 {
		writeGUIHTMLData(output, "line", strconv.Itoa(node.Line))
	}
	if node.Approximate {
		writeGUIHTMLData(output, "approximate", "true")
	}
	if node.Overlay != nil {
		writeGUIHTMLData(output, "overlay-role", node.Overlay.Role)
		writeGUIHTMLData(output, "overlay-owner", strconv.Itoa(node.Overlay.Owner))
	}
	if node.ClipBounds != nil {
		writeGUIHTMLData(output, "clip-bounds", fmt.Sprintf("%d,%d %dx%d", node.ClipBounds.X, node.ClipBounds.Y, node.ClipBounds.Width, node.ClipBounds.Height))
	}
	writeGUIHTMLInspectorLayoutData(output, node)
	if node.TextureRef != nil {
		writeGUIHTMLData(output, "texture-resolved", strconv.FormatBool(node.TextureRef.Resolved))
		writeGUIHTMLData(output, "texture-source", node.TextureRef.Source)
		writeGUIHTMLData(output, "texture-kind", node.TextureRef.Kind)
		writeGUIHTMLData(output, "texture-embedded", strconv.FormatBool(node.TextureRef.Embedded))
		writeGUIHTMLData(output, "texture-format", node.TextureRef.Format)
		if node.TextureRef.Width > 0 && node.TextureRef.Height > 0 {
			writeGUIHTMLData(output, "texture-size", fmt.Sprintf("%dx%d", node.TextureRef.Width, node.TextureRef.Height))
		}
		if node.TextureRef.SourceW > 0 && node.TextureRef.SourceH > 0 {
			writeGUIHTMLData(output, "texture-source-size", fmt.Sprintf("%dx%d", node.TextureRef.SourceW, node.TextureRef.SourceH))
		}
		if node.TextureRef.Resized {
			writeGUIHTMLData(output, "texture-resized", "true")
		}
		if node.TextureRef.FrameW > 0 && node.TextureRef.FrameH > 0 {
			writeGUIHTMLData(output, "texture-frame-size", fmt.Sprintf("%dx%d", node.TextureRef.FrameW, node.TextureRef.FrameH))
		}
		if node.TextureRef.FrameCols > 0 && node.TextureRef.FrameRows > 0 {
			writeGUIHTMLData(output, "texture-frame-grid", fmt.Sprintf("%dx%d", node.TextureRef.FrameCols, node.TextureRef.FrameRows))
		}
		if node.TextureRef.FrameImages > 0 {
			writeGUIHTMLData(output, "texture-frame-images", strconv.Itoa(node.TextureRef.FrameImages))
		}
	}
	if node.NoProgressTextureRef != nil {
		writeGUIHTMLData(output, "no-progress-texture-resolved", strconv.FormatBool(node.NoProgressTextureRef.Resolved))
		writeGUIHTMLData(output, "no-progress-texture-source", node.NoProgressTextureRef.Source)
		writeGUIHTMLData(output, "no-progress-texture-kind", node.NoProgressTextureRef.Kind)
		writeGUIHTMLData(output, "no-progress-texture-embedded", strconv.FormatBool(node.NoProgressTextureRef.Embedded))
		writeGUIHTMLData(output, "no-progress-texture-format", node.NoProgressTextureRef.Format)
		if node.NoProgressTextureRef.Width > 0 && node.NoProgressTextureRef.Height > 0 {
			writeGUIHTMLData(output, "no-progress-texture-size", fmt.Sprintf("%dx%d", node.NoProgressTextureRef.Width, node.NoProgressTextureRef.Height))
		}
	}
	if node.BackgroundTextureRef != nil {
		writeGUIHTMLData(output, "background-texture-resolved", strconv.FormatBool(node.BackgroundTextureRef.Resolved))
		writeGUIHTMLData(output, "background-texture-source", node.BackgroundTextureRef.Source)
		writeGUIHTMLData(output, "background-texture-kind", node.BackgroundTextureRef.Kind)
		writeGUIHTMLData(output, "background-texture-embedded", strconv.FormatBool(node.BackgroundTextureRef.Embedded))
		writeGUIHTMLData(output, "background-texture-format", node.BackgroundTextureRef.Format)
		if node.BackgroundTextureRef.Width > 0 && node.BackgroundTextureRef.Height > 0 {
			writeGUIHTMLData(output, "background-texture-size", fmt.Sprintf("%dx%d", node.BackgroundTextureRef.Width, node.BackgroundTextureRef.Height))
		}
	}
	if node.MaskTextureRef != nil {
		writeGUIHTMLData(output, "mask-resolved", strconv.FormatBool(node.MaskTextureRef.Resolved))
		writeGUIHTMLData(output, "mask-source", node.MaskTextureRef.Source)
		writeGUIHTMLData(output, "mask-kind", node.MaskTextureRef.Kind)
		writeGUIHTMLData(output, "mask-embedded", strconv.FormatBool(node.MaskTextureRef.Embedded))
		writeGUIHTMLData(output, "mask-format", node.MaskTextureRef.Format)
		if node.MaskTextureRef.Width > 0 && node.MaskTextureRef.Height > 0 {
			writeGUIHTMLData(output, "mask-size", fmt.Sprintf("%dx%d", node.MaskTextureRef.Width, node.MaskTextureRef.Height))
		}
	}
	if node.CoatOfArmsMaskRef != nil {
		writeGUIHTMLData(output, "coat-of-arms-mask-resource-resolved", strconv.FormatBool(node.CoatOfArmsMaskRef.Resolved))
		writeGUIHTMLData(output, "coat-of-arms-mask-resource-source", node.CoatOfArmsMaskRef.Source)
		writeGUIHTMLData(output, "coat-of-arms-mask-resource-kind", node.CoatOfArmsMaskRef.Kind)
		writeGUIHTMLData(output, "coat-of-arms-mask-resource-embedded", strconv.FormatBool(node.CoatOfArmsMaskRef.Embedded))
		writeGUIHTMLData(output, "coat-of-arms-mask-resource-format", node.CoatOfArmsMaskRef.Format)
		if node.CoatOfArmsMaskRef.Width > 0 && node.CoatOfArmsMaskRef.Height > 0 {
			writeGUIHTMLData(output, "coat-of-arms-mask-resource-size", fmt.Sprintf("%dx%d", node.CoatOfArmsMaskRef.Width, node.CoatOfArmsMaskRef.Height))
		}
	}
	if node.StateDefinition != nil {
		writeGUIHTMLData(output, "state-name", node.StateDefinition.Name)
		writeGUIHTMLData(output, "state-alpha", node.StateDefinition.Alpha)
		writeGUIHTMLData(output, "state-scale", node.StateDefinition.Scale)
		writeGUIHTMLData(output, "state-position-x", node.StateDefinition.PositionX)
		writeGUIHTMLData(output, "state-position-y", node.StateDefinition.PositionY)
		writeGUIHTMLData(output, "state-duration", node.StateDefinition.Duration)
		writeGUIHTMLData(output, "state-trigger", node.StateDefinition.TriggerWhen)
	}
	writeGUIHTMLSemantics(output, node.Semantics)
	writeGUIHTMLLocalizationData(output, "text", node.TextLocalization)
	writeGUIHTMLLocalizationData(output, "tooltip", node.TooltipLocalization)
	writeGUIHTMLLocalizationData(output, "tooltip-when-disabled", node.TooltipWhenDisabledLocalization)
	writeGUIHTMLModelRowData(output, node.ModelRow)
	writeGUIHTMLScenarioData(output, node.Scenario)
	writeGUIHTMLRuntimeData(output, node, previewLanguage)
	writeGUIHTMLData(output, "label-raw", rawLabel)
	writeGUIHTMLData(output, "label-english", guiHTMLLocalizedStageLabel(node, GUIPreviewLanguageEnglish))
	writeGUIHTMLData(output, "label-simp-chinese", guiHTMLLocalizedStageLabel(node, GUIPreviewLanguageSimpChinese))
	writeGUIHTMLData(output, "label-bilingual", guiHTMLLocalizedStageLabel(node, GUIPreviewLanguageBilingual))
	if strings.Contains(node.Text, "[") || (node.Semantics != nil && node.Semantics.RawText != "") || (node.TextLocalization != nil && node.TextLocalization.Partial) || (node.Runtime != nil && node.Runtime.Text != nil) {
		writeGUIHTMLData(output, "dynamic-text", "true")
	}
	fmt.Fprintf(output, " style=\"left:%dpx;top:%dpx;width:%dpx;height:%dpx;z-index:%d", node.Bounds.X, node.Bounds.Y, node.Bounds.Width, node.Bounds.Height, node.Index+1)
	if initialClipStyle != "" {
		output.WriteString(";")
		output.WriteString(initialClipStyle)
	}
	if progressStyle := guiHTMLProgressStyle(node); progressStyle != "" {
		output.WriteString(";")
		output.WriteString(progressStyle)
	}
	if colorStyle := guiHTMLColorStyle(node); colorStyle != "" {
		output.WriteString(";")
		output.WriteString(colorStyle)
	}
	if grayscaleStyle := guiHTMLGrayscaleStyle(node); grayscaleStyle != "" {
		output.WriteString(";")
		output.WriteString(grayscaleStyle)
	}
	if alphaStyle := guiHTMLAlphaStyle(node); alphaStyle != "" {
		output.WriteString(";")
		output.WriteString(alphaStyle)
	}
	if scaleStyle := guiHTMLScaleStyle(node); scaleStyle != "" {
		output.WriteString(";")
		output.WriteString(scaleStyle)
	}
	if rotateUVStyle := guiHTMLRotateUVStyle(node); rotateUVStyle != "" {
		output.WriteString(";")
		output.WriteString(rotateUVStyle)
	}
	if alignStyle := guiHTMLAlignStyle(node); alignStyle != "" {
		output.WriteString(";")
		output.WriteString(alignStyle)
	}
	output.WriteString("\"")
	output.WriteString(" aria-label=\"")
	output.WriteString(html.EscapeString(guiHTMLNodeDescription(node)))
	output.WriteString("\"")
	if value, known := guiNodeEffectiveVisible(node); known && !value {
		output.WriteString(" aria-hidden=\"true\"")
	}
	if value, known := guiNodeEffectiveEnabled(node); known && !value {
		output.WriteString(" disabled aria-disabled=\"true\"")
	}
	if value, known := guiNodeEffectivePressed(node); known {
		output.WriteString(" aria-pressed=\"")
		output.WriteString(strconv.FormatBool(value))
		output.WriteString("\"")
	}
	output.WriteString(">")
	if guiPreviewLineGeometryReady(node.LineGeometry) {
		writeGUIHTMLLineStroke(output, node, textureAssets)
	} else {
		writeGUIHTMLTextureImage(output, node, textureAssets)
	}
	writeGUIHTMLCheckMark(output, node)
	output.WriteString("<span class=\"ck3-caption")
	if !guiHTMLHasContentLabel(node) {
		output.WriteString(" ck3-diagnostic-caption")
	}
	output.WriteString("\">")
	output.WriteString(html.EscapeString(stageLabel))
	output.WriteString("</span></button>\n")
}

func guiHTMLModelRowSearch(row *GUIPreviewModelRow) string {
	if row == nil {
		return ""
	}
	return strings.Join([]string{row.ID, row.Target, row.DataModel, "row", strconv.Itoa(row.Index)}, " ")
}

func writeGUIHTMLInspectorLayoutData(output *strings.Builder, node GUIPreviewNode) {
	writeGUIHTMLData(output, "base-left", strconv.Itoa(node.Bounds.X))
	writeGUIHTMLData(output, "base-top", strconv.Itoa(node.Bounds.Y))
	writeGUIHTMLData(output, "base-width", strconv.Itoa(node.Bounds.Width))
	writeGUIHTMLData(output, "base-height", strconv.Itoa(node.Bounds.Height))
	if node.Layout == nil {
		return
	}
	writeGUIHTMLData(output, "flow-direction", node.Layout.FlowDirection)
	if node.Layout.IgnoreInvisible {
		writeGUIHTMLData(output, "ignore-invisible", "true")
	}
	if node.Layout.FlowDirection != "" {
		writeGUIHTMLData(output, "flow-spacing", strconv.Itoa(node.Layout.Spacing))
	}
	if node.Layout.FlowItem {
		writeGUIHTMLData(output, "flow-item", "true")
	}
	if node.Layout.FillParent {
		writeGUIHTMLData(output, "fill-parent", "true")
	}
	writeGUIHTMLData(output, "margin-left", strconv.Itoa(node.Layout.MarginLeft))
	writeGUIHTMLData(output, "margin-right", strconv.Itoa(node.Layout.MarginRight))
	writeGUIHTMLData(output, "margin-top", strconv.Itoa(node.Layout.MarginTop))
	writeGUIHTMLData(output, "margin-bottom", strconv.Itoa(node.Layout.MarginBottom))
	if node.Layout.ExpandHorizontal {
		writeGUIHTMLData(output, "expand-horizontal", "true")
	}
	if node.Layout.ExpandVertical {
		writeGUIHTMLData(output, "expand-vertical", "true")
	}
	if node.Layout.StretchHorizontal > 0 {
		writeGUIHTMLData(output, "stretch-horizontal", strconv.Itoa(node.Layout.StretchHorizontal))
	}
	if node.Layout.StretchVertical > 0 {
		writeGUIHTMLData(output, "stretch-vertical", strconv.Itoa(node.Layout.StretchVertical))
	}
	if node.Layout.AllowOutside || node.Layout.AllowOutsideExpr != "" {
		value := node.Layout.AllowOutsideExpr
		if value == "" {
			value = "true"
		}
		writeGUIHTMLData(output, "allow-outside", value)
	}
	if node.Layout.AutoResize {
		writeGUIHTMLData(output, "auto-resize", "true")
	}
	if node.Layout.Multiline {
		writeGUIHTMLData(output, "multiline", "true")
	}
	if node.Layout.MinWidth > 0 {
		writeGUIHTMLData(output, "min-width", strconv.Itoa(node.Layout.MinWidth))
	}
	if node.Layout.MaxWidth > 0 {
		writeGUIHTMLData(output, "max-width", strconv.Itoa(node.Layout.MaxWidth))
	}
	if node.Layout.MinHeight > 0 {
		writeGUIHTMLData(output, "min-height", strconv.Itoa(node.Layout.MinHeight))
	}
	if node.Layout.MaxHeight > 0 {
		writeGUIHTMLData(output, "max-height", strconv.Itoa(node.Layout.MaxHeight))
	}
	if node.Layout.GridColumns > 0 {
		writeGUIHTMLData(output, "grid-columns", strconv.Itoa(node.Layout.GridColumns))
		writeGUIHTMLData(output, "grid-column-step", strconv.Itoa(node.Layout.GridColumnStep))
		writeGUIHTMLData(output, "grid-row-step", strconv.Itoa(node.Layout.GridRowStep))
	}
	if node.Layout.GridFlip {
		writeGUIHTMLData(output, "grid-flip", "true")
	}
	if node.Layout.GridLayoutAnchor != "" {
		writeGUIHTMLData(output, "grid-layout-anchor", node.Layout.GridLayoutAnchor)
	}
	if node.Layout.GridItem {
		writeGUIHTMLData(output, "grid-item", "true")
		writeGUIHTMLData(output, "grid-row", strconv.Itoa(node.Layout.GridRow))
		writeGUIHTMLData(output, "grid-column", strconv.Itoa(node.Layout.GridColumn))
	}
	if node.Layout.ScrollViewport {
		writeGUIHTMLData(output, "scroll-viewport", "true")
		writeGUIHTMLData(output, "scroll-direction", node.Layout.ScrollDirection)
		writeGUIHTMLData(output, "scroll-content-width", strconv.Itoa(node.Layout.ScrollContentW))
		writeGUIHTMLData(output, "scroll-content-height", strconv.Itoa(node.Layout.ScrollContentH))
		writeGUIHTMLData(output, "scroll-step", strconv.Itoa(node.Layout.ScrollStep))
	}
}

func writeGUIHTMLInspectorScrollControl(output *strings.Builder, node GUIPreviewNode) {
	if node.Layout == nil || !node.Layout.ScrollViewport {
		return
	}
	maximum := maxInt(0, node.Layout.ScrollContentH-node.Bounds.Height)
	step := maxInt(1, node.Layout.ScrollStep)
	left := node.Bounds.X + maxInt(0, node.Bounds.Width-12)
	top := node.Bounds.Y + 2
	controlHeight := maxInt(12, node.Bounds.Height-4)
	output.WriteString("<label class=\"ck3-scrollbar")
	if maximum == 0 {
		output.WriteString(" is-scrollbar-idle")
	}
	output.WriteString("\" data-ck3-scroll-control=\"")
	output.WriteString(strconv.Itoa(node.Index))
	fmt.Fprintf(output, "\" style=\"left:%dpx;top:%dpx;height:%dpx;z-index:%d\"", left, top, controlHeight, 100000+node.Index)
	output.WriteString("><span class=\"ck3-visually-hidden\">Scroll ")
	output.WriteString(html.EscapeString(node.Name))
	output.WriteString("</span><input type=\"range\" min=\"0\" max=\"")
	output.WriteString(strconv.Itoa(maximum))
	output.WriteString("\" step=\"")
	output.WriteString(strconv.Itoa(step))
	output.WriteString("\" value=\"0\" aria-label=\"Scroll viewport ")
	output.WriteString(strconv.Itoa(node.Index))
	output.WriteString("\"></label>\n")
}

func writeGUIHTMLInspectorDetails(output *strings.Builder, runtime *GUIPreviewRuntime) {
	output.WriteString(`<aside id="ck3-detail-panel" class="ck3-panel ck3-detail-panel" aria-label="Selected GUI node inspector">
<div class="ck3-panel-heading"><strong>Inspector</strong><span id="ck3-selected-index">none</span></div>
<dl class="ck3-properties">
<div><dt>Click effects</dt><dd id="ck3-detail-clicks"></dd></div>
<div><dt>Kind</dt><dd id="ck3-detail-kind">—</dd></div>
<div><dt>Name</dt><dd id="ck3-detail-name">—</dd></div>
<div><dt>Parent</dt><dd id="ck3-detail-parent">—</dd></div>
<div><dt>Bounds</dt><dd id="ck3-detail-bounds">—</dd></div>
<div><dt>Source</dt><dd id="ck3-detail-source">—</dd></div>
<div><dt>Texture</dt><dd id="ck3-detail-texture">—</dd></div>
<div><dt>Data context</dt><dd id="ck3-detail-context">—</dd></div>
<div><dt>Localized text</dt><dd id="ck3-detail-localized">—</dd></div>
<div><dt>Tooltip</dt><dd id="ck3-detail-tooltip">—</dd></div>
<div><dt>Model row</dt><dd id="ck3-detail-model-row">—</dd></div>
<div><dt>Scenario</dt><dd id="ck3-detail-scenario">—</dd></div>
<div><dt>State definition</dt><dd id="ck3-detail-state">—</dd></div>
</dl>
<section class="ck3-simulation" aria-label="Runtime behavior simulation">
<h2>Behavior simulation</h2>
<p class="ck3-note">Controls replay visual consequences only; Jomini expressions are preserved, not executed.</p>
<label class="ck3-check ck3-sim-row"><input id="ck3-sim-down" type="checkbox"><span>Down</span><code id="ck3-down-expression"></code></label>
<label class="ck3-check ck3-sim-row"><input id="ck3-sim-selected-state" type="checkbox"><span>Selected</span><code id="ck3-selected-expression"></code></label>
<label class="ck3-check ck3-sim-row"><input id="ck3-sim-checked" type="checkbox"><span>Checked</span><code id="ck3-checked-expression"></code></label>
<label class="ck3-check ck3-sim-row"><input id="ck3-sim-visible" type="checkbox"><span>Visible</span><code id="ck3-visible-expression">—</code></label>
<label class="ck3-check ck3-sim-row"><input id="ck3-sim-enabled" type="checkbox"><span>Enabled</span><code id="ck3-enabled-expression">—</code></label>
<label class="ck3-field ck3-sim-field"><span>Dynamic text</span><input id="ck3-sim-text" type="text" autocomplete="off"><code id="ck3-text-expression">—</code></label>
<label class="ck3-field ck3-sim-field"><span>State</span><input id="ck3-sim-state" type="text" autocomplete="off"><code id="ck3-state-expression">—</code></label>
<div class="ck3-action-row"><button id="ck3-apply-state" class="ck3-control" type="button">Apply state</button><button id="ck3-sim-click" class="ck3-control ck3-primary" type="button">Simulate click</button><button id="ck3-sim-right-click" class="ck3-control" type="button">Simulate right-click</button><button id="ck3-reset-node" class="ck3-control" type="button">Reset node</button></div>
<output id="ck3-action-log" class="ck3-action-log">No simulated action.</output>
</section>
`)
	writeGUIHTMLRuntimeFacts(output, runtime)
	output.WriteString("</aside>\n")
}

func writeGUIHTMLRuntimeFacts(output *strings.Builder, runtime *GUIPreviewRuntime) {
	if runtime == nil || len(runtime.Facts) == 0 {
		return
	}
	output.WriteString(`<section class="ck3-simulation ck3-runtime-facts" aria-label="Bounded expression facts">
<h2>Expression facts</h2>
<p class="ck3-note">Atomic values below drive a bounded three-valued evaluator. Blank means unknown; no Jomini code is executed.</p>
<div id="ck3-runtime-facts">`)
	for _, fact := range runtime.Facts {
		output.WriteString("<label class=\"ck3-runtime-fact\"><span>")
		output.WriteString(html.EscapeString(fact.Expression))
		output.WriteString("</span>")
		initial := ""
		if fact.Provided {
			initial = fmt.Sprint(fact.Value)
		}
		switch fact.Kind {
		case guiRuntimeKindBoolean:
			output.WriteString("<select data-ck3-runtime-fact=\"")
			output.WriteString(strconv.Itoa(fact.Index))
			output.WriteString("\" data-ck3-runtime-expression=\"")
			output.WriteString(html.EscapeString(fact.Expression))
			output.WriteString("\" data-ck3-runtime-kind=\"boolean\" data-ck3-runtime-initial=\"")
			output.WriteString(html.EscapeString(strings.ToLower(initial)))
			output.WriteString("\"><option value=\"\"")
			if !fact.Provided {
				output.WriteString(" selected")
			}
			output.WriteString(">unknown</option><option value=\"true\"")
			if fact.Provided && initial == "true" {
				output.WriteString(" selected")
			}
			output.WriteString(">true</option><option value=\"false\"")
			if fact.Provided && initial == "false" {
				output.WriteString(" selected")
			}
			output.WriteString(">false</option></select>")
		default:
			inputType := "text"
			if fact.Kind == guiRuntimeKindNumber {
				inputType = "number"
			}
			output.WriteString("<input type=\"")
			output.WriteString(inputType)
			output.WriteString("\" data-ck3-runtime-fact=\"")
			output.WriteString(strconv.Itoa(fact.Index))
			output.WriteString("\" data-ck3-runtime-expression=\"")
			output.WriteString(html.EscapeString(fact.Expression))
			output.WriteString("\" data-ck3-runtime-kind=\"")
			output.WriteString(html.EscapeString(fact.Kind))
			output.WriteString("\" data-ck3-runtime-initial=\"")
			output.WriteString(html.EscapeString(initial))
			output.WriteString("\" value=\"")
			output.WriteString(html.EscapeString(initial))
			output.WriteString("\" autocomplete=\"off\">")
		}
		output.WriteString("<small>")
		if fact.Provided {
			output.WriteString("provided ")
		} else {
			output.WriteString("unknown ")
		}
		output.WriteString(strconv.Itoa(fact.References))
		output.WriteString(" ref</small></label>")
	}
	output.WriteString(`</div><output id="ck3-runtime-status" class="ck3-action-log">Facts ready.</output></section>`)
}

func writeGUIHTMLRuntimePlans(output *strings.Builder, runtime *GUIPreviewRuntime) {
	if runtime == nil || (len(runtime.Plans) == 0 && len(runtime.TextPlans) == 0 && len(runtime.Actions) == 0) {
		return
	}
	output.WriteString("<div id=\"ck3-runtime-plans\" hidden>")
	for _, plan := range runtime.Plans {
		data, err := json.Marshal(plan.Tokens)
		if err != nil {
			continue
		}
		output.WriteString("<span data-ck3-runtime-plan=\"")
		output.WriteString(strconv.Itoa(plan.ID))
		output.WriteString("\" data-ck3-runtime-supported=\"")
		output.WriteString(strconv.FormatBool(plan.Supported))
		output.WriteString("\" data-ck3-runtime-tokens=\"")
		output.WriteString(base64.StdEncoding.EncodeToString(data))
		output.WriteString("\"></span>")
	}
	for _, plan := range runtime.TextPlans {
		data, err := json.Marshal(plan.Tokens)
		if err != nil {
			continue
		}
		output.WriteString("<span data-ck3-runtime-text-plan=\"")
		output.WriteString(strconv.Itoa(plan.ID))
		output.WriteString("\" data-ck3-runtime-supported=\"")
		output.WriteString(strconv.FormatBool(plan.Supported))
		output.WriteString("\" data-ck3-runtime-tokens=\"")
		output.WriteString(base64.StdEncoding.EncodeToString(data))
		output.WriteString("\"></span>")
	}
	for _, action := range runtime.Actions {
		updates := ""
		if len(action.Updates) > 0 {
			if data, err := json.Marshal(action.Updates); err == nil {
				updates = base64.StdEncoding.EncodeToString(data)
			}
		}
		output.WriteString("<span data-ck3-runtime-action=\"")
		output.WriteString(strconv.Itoa(action.ID))
		output.WriteString("\" data-ck3-runtime-operation=\"")
		output.WriteString(html.EscapeString(action.Operation))
		output.WriteString("\" data-ck3-runtime-fact-index=\"")
		output.WriteString(strconv.Itoa(action.Fact))
		output.WriteString("\" data-ck3-runtime-argument=\"")
		output.WriteString(html.EscapeString(action.Argument))
		output.WriteString("\" data-ck3-runtime-data-expression=\"")
		output.WriteString(html.EscapeString(action.DataExpression))
		output.WriteString("\" data-ck3-runtime-source=\"")
		output.WriteString(html.EscapeString(action.Source))
		output.WriteString("\" data-ck3-runtime-updates=\"")
		output.WriteString(updates)
		output.WriteString("\"></span>")
	}
	output.WriteString("</div>\n")
}

const guiHTMLInspectorStyles = `:root{color-scheme:dark;--ck3-bg:#0f141c;--ck3-panel:#171e28;--ck3-panel-2:#1d2632;--ck3-border:#344254;--ck3-text:#e8edf4;--ck3-muted:#9aa8b8;--ck3-accent:#5ba6df;--ck3-accent-soft:#244d6d;--ck3-warning:#e4bc63;--ck3-danger:#d96b6b;--ck3-container:#2e78ad70;--ck3-button:#a865247f;--ck3-text-node:#26764f70;--ck3-image:#68459570;--ck3-generic:#54667a70}
*{box-sizing:border-box}
html,body{margin:0;min-width:760px;height:100%;overflow:hidden;background:var(--ck3-bg);color:var(--ck3-text);font-family:"Noto Sans CJK SC","Microsoft YaHei UI","Segoe UI",sans-serif;font-size:13px}
button,input,select{font:inherit}
button{color:inherit}
.ck3-inspector{display:grid;grid-template-rows:auto minmax(0,1fr) auto;height:100vh;background:var(--ck3-bg)}
.ck3-toolbar{display:flex;align-items:center;gap:10px;min-height:54px;padding:8px 12px;border-bottom:1px solid var(--ck3-border);background:var(--ck3-panel);white-space:nowrap}
.ck3-identity{display:flex;align-items:center;gap:7px;min-width:190px}.ck3-identity>span{color:var(--ck3-muted)}
.ck3-badge,.ck3-behavior-counts span{display:inline-flex;align-items:center;padding:2px 6px;border:1px solid var(--ck3-border);border-radius:999px;background:var(--ck3-panel-2);font-size:11px}
.ck3-warning{border-color:var(--ck3-warning);color:var(--ck3-warning)}
.ck3-field{display:flex;align-items:center;gap:6px;color:var(--ck3-muted)}
.ck3-field input[type=search],.ck3-field input[type=text],.ck3-field select{height:30px;border:1px solid var(--ck3-border);border-radius:4px;background:var(--ck3-bg);color:var(--ck3-text);padding:4px 7px;outline:none}
.ck3-field input:focus{border-color:var(--ck3-accent);box-shadow:0 0 0 2px var(--ck3-accent-soft)}
.ck3-search input{width:170px}.ck3-zoom input{width:105px}.ck3-zoom output{min-width:34px;color:var(--ck3-text)}.ck3-language select{width:105px}
.ck3-control{height:30px;padding:4px 9px;border:1px solid var(--ck3-border);border-radius:4px;background:var(--ck3-panel-2);cursor:pointer}.ck3-control:hover:not(:disabled){border-color:var(--ck3-accent)}.ck3-control:disabled{opacity:.45;cursor:not-allowed}.ck3-primary{background:var(--ck3-accent-soft);border-color:var(--ck3-accent)}
.ck3-check{display:flex;align-items:center;gap:5px;color:var(--ck3-muted)}.ck3-check input{accent-color:var(--ck3-accent)}
.ck3-behavior-counts{display:flex;gap:4px;margin-left:auto}.ck3-behavior-counts span{color:var(--ck3-muted)}
.ck3-workspace{display:grid;grid-template-columns:260px minmax(360px,1fr) 330px;min-height:0}
.ck3-panel{min-height:0;background:var(--ck3-panel)}.ck3-tree-panel{border-right:1px solid var(--ck3-border)}.ck3-detail-panel{border-left:1px solid var(--ck3-border);overflow:auto}
.ck3-panel-heading{display:flex;align-items:center;justify-content:space-between;height:38px;padding:0 10px;border-bottom:1px solid var(--ck3-border);color:var(--ck3-muted)}
.ck3-tree{height:calc(100% - 38px);overflow:auto;padding:5px}
.ck3-tree-item{display:grid;grid-template-columns:minmax(70px,auto) minmax(0,1fr) auto;gap:6px;align-items:center;width:100%;min-height:28px;margin:0;padding:3px 5px 3px calc(5px + var(--ck3-depth)*12px);border:0;border-radius:3px;background:transparent;text-align:left;cursor:pointer}.ck3-tree-item:hover{background:var(--ck3-panel-2)}.ck3-tree-item.is-selected{background:var(--ck3-accent-soft);outline:1px solid var(--ck3-accent)}.ck3-tree-item.is-search-hidden{display:none}
.ck3-tree-kind{color:var(--ck3-accent);overflow:hidden;text-overflow:ellipsis}.ck3-tree-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.ck3-tree-row{color:#d9b86c;font-size:10px}.ck3-tree-index{color:var(--ck3-muted);font-size:11px}
.ck3-viewport{position:relative;overflow:auto;background-color:var(--ck3-bg);background-image:linear-gradient(var(--ck3-border) 1px,transparent 1px),linear-gradient(90deg,var(--ck3-border) 1px,transparent 1px);background-size:64px 64px;cursor:grab}.ck3-viewport.is-panning{cursor:grabbing;user-select:none}
.ck3-stage-shell{position:relative;min-width:100%;min-height:100%;padding:36px}.ck3-canvas{position:relative;transform-origin:top left;background:#111823;box-shadow:0 12px 38px #0008}
.ck3-node{position:absolute;display:flex;align-items:flex-start;justify-content:flex-start;overflow:hidden;margin:0;padding:0;border:1px solid #8ba8c7;background:var(--ck3-generic);color:var(--ck3-text);font-size:12px;line-height:1.25;text-align:left;cursor:pointer;opacity:calc(var(--ck3-alpha,1)*var(--ck3-state-alpha,1));transform:translate(var(--ck3-state-offset-x,0px),calc(var(--ck3-down-offset,0px) + var(--ck3-state-offset-y,0px))) scale(var(--ck3-scale,1)) scale(var(--ck3-state-scale,1));transform-origin:center}.ck3-node:hover{outline:2px solid var(--ck3-text);outline-offset:1px}.ck3-node.is-selected{outline:3px solid var(--ck3-accent);outline-offset:2px}.ck3-node.is-search-muted{opacity:.16}.ck3-node.is-sim-hidden,.ck3-node.is-effective-hidden{opacity:.08;filter:grayscale(1)}.ck3-node.is-flow-ignored{display:none}.ck3-node.is-scroll-clipped{display:none}.ck3-node.is-sim-disabled{filter:grayscale(1);border-color:var(--ck3-danger)}.ck3-node.is-sim-down{--ck3-down-offset:1px;box-shadow:inset 0 2px 5px #000b;filter:saturate(.82)}.ck3-node.is-sim-selected-state{border-color:#f0cf72;box-shadow:inset 0 0 0 1px #f0cf7255}.ck3-behavior-node,.ck3-tooltip-root,.ck3-tooltip-content{display:none}.ck3-tooltip-content.is-tooltip-open{display:flex;box-shadow:0 10px 28px #0009}
.ck3-scrollbar{position:absolute;display:block;width:12px;margin:0;padding:0;border:0;background:#15202bdd;border-radius:6px;box-shadow:0 0 0 1px #6f8195aa;overflow:hidden}.ck3-scrollbar.is-scrollbar-idle{display:none}.ck3-scrollbar input{display:block;width:100%;height:100%;margin:0;padding:0;accent-color:var(--ck3-accent);cursor:ns-resize;writing-mode:vertical-lr;direction:rtl}.ck3-visually-hidden{position:absolute!important;width:1px!important;height:1px!important;padding:0!important;margin:-1px!important;overflow:hidden!important;clip:rect(0,0,0,0)!important;white-space:nowrap!important;border:0!important}
.ck3-container{background:var(--ck3-container);border-color:#58a6e6}.ck3-button{align-items:center;justify-content:center;background:linear-gradient(180deg,#bc7b34aa,#754315aa);border-color:#eba149;text-align:center}.ck3-text{align-items:center;background:var(--ck3-text-node);border-color:#5ccc8e;padding:2px 4px}.ck3-image{align-items:flex-end;background:repeating-linear-gradient(135deg,#68459588 0,#68459588 8px,#50356f88 8px,#50356f88 16px);border-color:#b17ae5;padding:2px 4px}.ck3-expand{background:transparent;border-color:var(--ck3-muted)}.ck3-line{overflow:visible!important;background:transparent!important;border:0!important;pointer-events:auto}.ck3-line>.ck3-line-stroke{position:absolute;height:var(--ck3-line-width,1px);background-color:var(--ck3-tint-color,#b17ae5);background-blend-mode:multiply;background-position:left center;background-repeat:repeat-x;background-size:auto 100%;transform:translateY(-50%) rotate(var(--ck3-line-angle,0deg));transform-origin:0 50%;pointer-events:none}.ck3-line>.ck3-line-stroke.ck3-line-cap{border-radius:999px}.ck3-line>.ck3-caption{display:none}.ck3-approximate{border-color:var(--ck3-warning);border-style:dashed}.ck3-inspector:not(.show-approx) .ck3-approximate{opacity:.2}
.ck3-texture-modifier{background:transparent;border-color:transparent}.ck3-blend-screen{mix-blend-mode:screen}.ck3-blend-multiply{mix-blend-mode:multiply}.ck3-blend-overlay{mix-blend-mode:overlay}.ck3-blend-color-dodge{mix-blend-mode:color-dodge}
.ck3-texture{position:absolute;inset:0;width:100%;height:100%;background-position:center;background-repeat:no-repeat;background-size:100% 100%;pointer-events:none;user-select:none;transform:rotate(var(--ck3-rotate-uv,0deg)) scale(var(--ck3-mirror-x,1),var(--ck3-mirror-y,1));transform-origin:center}.ck3-image>.ck3-texture,.ck3-button>.ck3-texture{background-size:contain}.ck3-texture.ck3-framed{background-position:var(--ck3-frame-up-x) var(--ck3-frame-up-y)}.ck3-node:hover>.ck3-texture.ck3-framed{background-position:var(--ck3-frame-over-x) var(--ck3-frame-over-y)}.ck3-node:active>.ck3-texture.ck3-framed,.ck3-node.is-sim-down>.ck3-texture.ck3-framed{background-position:var(--ck3-frame-down-x) var(--ck3-frame-down-y)}.ck3-node:disabled>.ck3-texture.ck3-framed,.ck3-node.is-sim-disabled>.ck3-texture.ck3-framed{background-position:var(--ck3-frame-disabled-x) var(--ck3-frame-disabled-y)}.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-up-image)}.ck3-node:hover>.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-over-image)}.ck3-node:active>.ck3-texture.ck3-framed-images,.ck3-node.is-sim-down>.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-down-image)}.ck3-node:disabled>.ck3-texture.ck3-framed-images,.ck3-node.is-sim-disabled>.ck3-texture.ck3-framed-images{--ck3-active-frame-image:var(--ck3-frame-disabled-image)}.ck3-texture.ck3-framed-images:not(.ck3-nine-slice){background-image:var(--ck3-active-frame-image)!important;background-size:100% 100%}.ck3-texture.ck3-nine-slice{background-image:none!important;border-style:solid;border-width:var(--ck3-slice-y) var(--ck3-slice-x);border-image-source:var(--ck3-active-frame-image,var(--ck3-texture-image));border-image-slice:var(--ck3-source-slice-y) var(--ck3-source-slice-x) fill;border-image-width:var(--ck3-slice-y) var(--ck3-slice-x);border-image-repeat:stretch}.ck3-texture.ck3-nine-slice-tiled{border-image-repeat:round}.ck3-texture.ck3-mirror-horizontal{--ck3-mirror-x:-1}.ck3-texture.ck3-mirror-vertical{--ck3-mirror-y:-1}.ck3-texture.ck3-mirror-both{--ck3-mirror-x:-1;--ck3-mirror-y:-1}
.ck3-progresspie>.ck3-texture{-webkit-mask-image:conic-gradient(from -90deg,#000 0deg var(--ck3-progress-angle,360deg),transparent var(--ck3-progress-angle,360deg) 360deg);mask-image:conic-gradient(from -90deg,#000 0deg var(--ck3-progress-angle,360deg),transparent var(--ck3-progress-angle,360deg) 360deg)}.ck3-progressbar>.ck3-progress-fill{clip-path:inset(0 var(--ck3-progress-inverse,0%) 0 0)}
.ck3-checkmark{position:absolute;inset:0;z-index:2;display:flex;align-items:center;justify-content:center;opacity:0;color:#f1d98b;font-size:18px;font-weight:700;line-height:1;text-shadow:0 1px 3px #000;pointer-events:none}.ck3-node.ck3-checkable.is-sim-checked>.ck3-checkmark{opacity:1}
.ck3-runtime-tooltip{position:absolute;z-index:100000;display:none;max-width:min(380px,calc(100% - 16px));padding:9px 12px;border:1px solid #b99a5e;border-radius:3px;background:linear-gradient(180deg,#25221e,#151719f5);box-shadow:0 8px 24px #000c,inset 0 0 0 1px #0008;color:#f0ece2;font-size:13px;line-height:1.4;white-space:pre-wrap;overflow-wrap:anywhere;pointer-events:none;text-align:left}.ck3-runtime-tooltip.is-tooltip-open{display:block}
.ck3-caption{position:relative;z-index:1;display:block;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;text-shadow:0 1px 2px #000}.ck3-node[data-ck3-multiline=true]>.ck3-caption{width:100%;white-space:normal;overflow-wrap:anywhere;text-overflow:clip}.ck3-container>.ck3-caption{padding:2px;color:var(--ck3-muted);font-size:10px}.ck3-inspector:not(.show-labels) .ck3-diagnostic-caption{display:none}
.ck3-inspector.visual-mode .ck3-canvas{background-color:#090e15;background-image:none}.ck3-inspector.visual-mode .ck3-node{background:transparent;border-color:transparent}.ck3-inspector.visual-mode .ck3-node[data-ck3-texture]:not(.ck3-has-texture){background:repeating-linear-gradient(135deg,#552b2b88 0,#552b2b88 6px,#24191988 6px,#24191988 12px);border-color:#a85e5e}.ck3-inspector.visual-mode .ck3-node.is-effective-hidden{display:none}.ck3-inspector.visual-mode .ck3-node.is-sim-disabled{filter:grayscale(1) brightness(.65)}.ck3-inspector.visual-mode .ck3-node.is-selected{outline-color:var(--ck3-accent)}
.ck3-properties{margin:0;padding:8px 10px}.ck3-properties>div{display:grid;grid-template-columns:88px minmax(0,1fr);gap:8px;padding:5px 0;border-bottom:1px solid #273344}.ck3-properties dt{color:var(--ck3-muted)}.ck3-properties dd{margin:0;overflow-wrap:anywhere}
.ck3-simulation{padding:4px 10px 12px}.ck3-simulation h2{margin:8px 0;font-size:14px}.ck3-note{margin:0 0 9px;color:var(--ck3-muted);font-size:11px;line-height:1.4}.ck3-sim-row{display:grid;grid-template-columns:auto 58px minmax(0,1fr);min-height:34px;border-top:1px solid #273344}.ck3-sim-row code,.ck3-sim-field code{overflow:hidden;color:var(--ck3-muted);font-size:11px;text-overflow:ellipsis;white-space:nowrap}.ck3-sim-field{display:grid;grid-template-columns:88px minmax(0,1fr);padding:6px 0;border-top:1px solid #273344}.ck3-sim-field code{grid-column:1/-1;padding-top:4px}.ck3-sim-field input{width:100%}.ck3-runtime-facts{border-top:1px solid var(--ck3-border)}.ck3-runtime-fact{display:grid;grid-template-columns:minmax(0,1fr) 86px;gap:4px 8px;padding:7px 0;border-top:1px solid #273344}.ck3-runtime-fact>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.ck3-runtime-fact>input,.ck3-runtime-fact>select{min-width:0;width:86px;height:27px;border:1px solid var(--ck3-border);border-radius:3px;background:var(--ck3-bg);color:var(--ck3-text)}.ck3-runtime-fact>small{grid-column:1/-1;color:var(--ck3-muted)}
.ck3-action-row{display:flex;gap:6px;margin-top:10px}.ck3-action-log{display:block;min-height:38px;margin-top:8px;padding:7px;border:1px solid var(--ck3-border);border-radius:4px;background:var(--ck3-bg);color:var(--ck3-muted);overflow-wrap:anywhere}
.ck3-status{min-height:28px;padding:6px 10px;border-top:1px solid var(--ck3-border);background:var(--ck3-panel);color:var(--ck3-muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
@media(max-width:1080px){.ck3-workspace{grid-template-columns:220px minmax(360px,1fr)}.ck3-detail-panel{position:absolute;right:0;top:54px;bottom:28px;width:320px;z-index:100;border-left:1px solid var(--ck3-border);box-shadow:-12px 0 28px #0008}.ck3-viewport{margin-right:320px}.ck3-behavior-counts{display:none}}
`

const guiHTMLInspectorScript = `(function(){
'use strict';
var root=document.getElementById('ck3-gui-inspector');
if(!root){return;}
var canvas=document.getElementById('ck3-canvas');
var shell=document.getElementById('ck3-stage-shell');
var viewport=document.getElementById('ck3-viewport');
var tree=document.getElementById('ck3-tree');
var nodes=Array.prototype.slice.call(root.querySelectorAll('[data-ck3-stage-node]'));
var treeItems=Array.prototype.slice.call(root.querySelectorAll('[data-ck3-tree-index]'));
var runtimeFactControls=Array.prototype.slice.call(root.querySelectorAll('[data-ck3-runtime-fact]'));
var scrollControls=Array.prototype.slice.call(root.querySelectorAll('[data-ck3-scroll-control]'));
var runtimePlans={};
var runtimeTextPlans={};
var runtimeActions={};
Array.prototype.slice.call(root.querySelectorAll('[data-ck3-runtime-plan]')).forEach(function(element){try{runtimePlans[element.dataset.ck3RuntimePlan]={supported:element.dataset.ck3RuntimeSupported==='true',tokens:JSON.parse(atob(element.dataset.ck3RuntimeTokens))};}catch(ignore){runtimePlans[element.dataset.ck3RuntimePlan]={supported:false,tokens:[]};}});
Array.prototype.slice.call(root.querySelectorAll('[data-ck3-runtime-text-plan]')).forEach(function(element){try{runtimeTextPlans[element.dataset.ck3RuntimeTextPlan]={supported:element.dataset.ck3RuntimeSupported==='true',tokens:JSON.parse(atob(element.dataset.ck3RuntimeTokens))};}catch(ignore){runtimeTextPlans[element.dataset.ck3RuntimeTextPlan]={supported:false,tokens:[]};}});
Array.prototype.slice.call(root.querySelectorAll('[data-ck3-runtime-action]')).forEach(function(element){var updates=[];try{updates=element.dataset.ck3RuntimeUpdates?JSON.parse(atob(element.dataset.ck3RuntimeUpdates)):[];}catch(ignore){updates=[];}runtimeActions[element.dataset.ck3RuntimeAction]={operation:element.dataset.ck3RuntimeOperation,fact:Number(element.dataset.ck3RuntimeFactIndex),argument:element.dataset.ck3RuntimeArgument||'',dataExpression:element.dataset.ck3RuntimeDataExpression||'',source:element.dataset.ck3RuntimeSource||'',updates:updates};});
var selected=null;
var width=Number(root.dataset.ck3Width)||1280;
var height=Number(root.dataset.ck3Height)||720;
var currentLanguage=root.dataset.ck3Language||'raw';
var runtimeTooltip=document.getElementById('ck3-runtime-tooltip');
var runtimeTooltipOwner=null;
function byId(id){return document.getElementById(id);}
function text(id,value){var element=byId(id);if(element){element.textContent=value||'—';}}
function nodeByIndex(index){return nodes.find(function(node){return node.dataset.ck3Index===String(index);})||null;}
function sameModelRow(left,right){if(!left||!right||left.dataset.ck3ModelRowSource===undefined){return true;}return right.dataset.ck3ModelRowSource!==undefined&&left.dataset.ck3ModelRowCollection===right.dataset.ck3ModelRowCollection&&left.dataset.ck3ModelRowIndex===right.dataset.ck3ModelRowIndex;}
function modelRowLabel(node){return node&&node.dataset.ck3ModelRowSource!==undefined?' row '+node.dataset.ck3ModelRowId+'['+node.dataset.ck3ModelRowIndex+']':'';}
function matching(expression,key){if(!expression){return selected?[selected]:[];}return nodes.filter(function(node){return node.dataset[key]===expression&&sameModelRow(selected,node);});}
function setControl(id,enabled,value){var control=byId(id);control.disabled=!enabled;if(control.type==='checkbox'){control.checked=!!value;}else{control.value=value||'';}}
function languageSuffix(language){if(language==='simp_chinese'){return 'SimpChinese';}if(language==='bilingual'){return 'Bilingual';}if(language==='english'){return 'English';}return 'Raw';}
function localized(node,prefix,language){var value=node.dataset[prefix+languageSuffix(language)]||'';if(!value&&language!=='raw'){value=node.dataset[prefix+'Raw']||'';}return value;}
function labelFor(node){return localized(node,'ck3Label',currentLanguage)||node.dataset.ck3OriginalLabel||'';}
function disabledTooltipFor(node){if(node.dataset.simTooltipWhenDisabled!==undefined){return node.dataset.simTooltipWhenDisabled;}return localized(node,'ck3TooltipWhenDisabled',currentLanguage)||node.dataset.ck3TooltipWhenDisabled||'';}
function tooltipFor(node){if(node.dataset.simEnabled==='false'||node.classList.contains('is-sim-disabled')||node.getAttribute('aria-disabled')==='true'){var disabled=disabledTooltipFor(node);if(disabled){return disabled;}}if(node.dataset.simTooltip!==undefined){return node.dataset.simTooltip;}return localized(node,'ck3Tooltip',currentLanguage)||node.dataset.ck3Tooltip||node.dataset.ck3RawTooltipDisplay||node.dataset.ck3RawTooltip||'';}
function initializeScenario(node){if(node.dataset.ck3SimText!==undefined){node.dataset.simText=node.dataset.ck3SimText;}if(node.dataset.ck3SimVisible!==undefined){node.dataset.simVisible=node.dataset.ck3SimVisible;}if(node.dataset.ck3SimEnabled!==undefined){node.dataset.simEnabled=node.dataset.ck3SimEnabled;}if(node.dataset.ck3SimDown!==undefined){node.dataset.simDown=node.dataset.ck3SimDown;}if(node.dataset.ck3SimSelected!==undefined){node.dataset.simSelected=node.dataset.ck3SimSelected;}if(node.dataset.ck3SimChecked!==undefined){node.dataset.simChecked=node.dataset.ck3SimChecked;}if(node.dataset.ck3SimStateTrigger!==undefined){node.dataset.simStateTrigger=node.dataset.ck3SimStateTrigger;}if(node.dataset.ck3SimTooltipVisible!==undefined){node.dataset.simTooltipVisible=node.dataset.ck3SimTooltipVisible;}syncPressed(node);syncChecked(node);}
var childrenByParent={};
nodes.forEach(function(node){var parent=String(node.dataset.ck3Parent);if(!childrenByParent[parent]){childrenByParent[parent]=[];}childrenByParent[parent].push(node);});
var tooltipNodesByOwner={};
nodes.forEach(function(node){if(node.dataset.ck3OverlayOwner===undefined){return;}var owner=String(node.dataset.ck3OverlayOwner);if(!tooltipNodesByOwner[owner]){tooltipNodesByOwner[owner]=[];}tooltipNodesByOwner[owner].push(node);});
var scrollControlByOwner={};
scrollControls.forEach(function(control){scrollControlByOwner[String(control.dataset.ck3ScrollControl)]=control;});
function numericData(node,key,fallback){var value=Number(node.dataset[key]);return Number.isFinite(value)?value:fallback;}
function nodeRect(node){return {left:numericData(node,'ck3CurrentLeft',numericData(node,'ck3BaseLeft',0)),top:numericData(node,'ck3CurrentTop',numericData(node,'ck3BaseTop',0)),width:numericData(node,'ck3CurrentWidth',numericData(node,'ck3BaseWidth',1)),height:numericData(node,'ck3CurrentHeight',numericData(node,'ck3BaseHeight',1))};}
function setNodeRect(node,rect){var left=Math.round(rect.left);var top=Math.round(rect.top);var widthValue=Math.max(0,Math.round(rect.width));var heightValue=Math.max(0,Math.round(rect.height));node.dataset.ck3CurrentLeft=String(left);node.dataset.ck3CurrentTop=String(top);node.dataset.ck3CurrentWidth=String(widthValue);node.dataset.ck3CurrentHeight=String(heightValue);node.dataset.ck3Bounds=left+','+top+' '+widthValue+'x'+heightValue;node.style.left=left+'px';node.style.top=top+'px';node.style.width=widthValue+'px';node.style.height=heightValue+'px';}
function runtimeLayoutNumber(node,name,fallback){var staticValue=numericData(node,'ck3'+runtimePropertyTitle(name),fallback);var result=runtimeNodeNumber(node,name,staticValue);return result.known?result.value:fallback;}
function runtimeLayoutDimension(node,name){return Math.max(0,Math.round(runtimeLayoutNumber(node,name,0)));}
function clampRuntimeLayoutDimension(node,value,horizontal){var minimum=runtimeLayoutDimension(node,horizontal?'minWidth':'minHeight');var maximum=runtimeLayoutDimension(node,horizontal?'maxWidth':'maxHeight');var result=Math.max(0,Math.round(value));if(minimum>0){result=Math.max(result,minimum);}if(maximum>0){result=Math.min(result,maximum);}return result;}
function applyRuntimeLayoutConstraints(){nodes.forEach(function(node){var rect=nodeRect(node);setNodeRect(node,{left:rect.left,top:rect.top,width:clampRuntimeLayoutDimension(node,rect.width,true),height:clampRuntimeLayoutDimension(node,rect.height,false)});});}
function walkSubtree(node,visitor){visitor(node);(childrenByParent[String(node.dataset.ck3Index)]||[]).forEach(function(child){walkSubtree(child,visitor);});}
function translateSubtree(node,deltaX,deltaY){if(!deltaX&&!deltaY){return;}walkSubtree(node,function(item){var rect=nodeRect(item);setNodeRect(item,{left:rect.left+deltaX,top:rect.top+deltaY,width:rect.width,height:rect.height});});}
function resetDynamicLayout(){nodes.forEach(function(node){node.classList.remove('is-flow-ignored','is-scroll-clipped');node.style.removeProperty('clip-path');setNodeRect(node,{left:numericData(node,'ck3BaseLeft',0),top:numericData(node,'ck3BaseTop',0),width:numericData(node,'ck3BaseWidth',1),height:numericData(node,'ck3BaseHeight',1)});});}
function syncEffectiveVisibility(){nodes.forEach(function(node){var parent=nodeByIndex(node.dataset.ck3Parent);var hidden=node.dataset.simVisible==='false'||!!(parent&&parent.dataset.ck3EffectiveHidden==='true');node.dataset.ck3EffectiveHidden=String(hidden);node.classList.toggle('is-effective-hidden',hidden);if(hidden){node.setAttribute('aria-hidden','true');}else if(node.dataset.simVisible!=='false'){node.removeAttribute('aria-hidden');}});}
function directFlowItems(container){return (childrenByParent[String(container.dataset.ck3Index)]||[]).filter(function(child){return child.dataset.ck3FlowItem==='true'&&child.dataset.ck3StateName===undefined&&child.dataset.ck3OverlayRole===undefined;});}
function directGridItems(container){return (childrenByParent[String(container.dataset.ck3Index)]||[]).filter(function(child){return child.dataset.ck3GridItem==='true'&&child.dataset.ck3StateName===undefined&&child.dataset.ck3OverlayRole===undefined;});}
function isMainAxisExpanding(node,horizontal){return node.dataset[horizontal?'ck3ExpandHorizontal':'ck3ExpandVertical']==='true';}
function mainAxisStretch(node,horizontal){return Math.max(1,Math.round(numericData(node,horizontal?'ck3StretchHorizontal':'ck3StretchVertical',1)));}
function syncFillChildren(parent){var parentRect=nodeRect(parent);(childrenByParent[String(parent.dataset.ck3Index)]||[]).forEach(function(child){if(child.dataset.ck3FillParent!=='true'){return;}var childRect=nodeRect(child);translateSubtree(child,parentRect.left-childRect.left,parentRect.top-childRect.top);setNodeRect(child,{left:parentRect.left,top:parentRect.top,width:parentRect.width,height:parentRect.height});});}
function reflowContainer(container){
  var direction=container.dataset.ck3FlowDirection;
  if(direction!=='horizontal'&&direction!=='vertical'){return;}
  var horizontal=direction==='horizontal';
  var scrollViewport=container.dataset.ck3ScrollViewport==='true';
  var ignoreInvisible=container.dataset.ck3IgnoreInvisible==='true';
  var items=directFlowItems(container);
  items.forEach(function(item){item.classList.toggle('is-flow-ignored',ignoreInvisible&&item.dataset.simVisible==='false');});
  var visible=items.filter(function(item){return !(ignoreInvisible&&item.dataset.simVisible==='false');});
  if(!visible.length){return;}
  var spacing=numericData(container,'ck3FlowSpacing',0);
  var fixed=spacing*Math.max(0,visible.length-1);
  var stretchTotal=0;
  visible.forEach(function(item){
    var rect=nodeRect(item);
    var marginBefore=runtimeLayoutNumber(item,horizontal?'marginLeft':'marginTop',0);
    var marginAfter=runtimeLayoutNumber(item,horizontal?'marginRight':'marginBottom',0);
    if(isMainAxisExpanding(item,horizontal)){stretchTotal+=mainAxisStretch(item,horizontal);}else{fixed+=(horizontal?rect.width:rect.height)+marginBefore+marginAfter;}
  });
  var parentRect=nodeRect(container);
  var available=horizontal?parentRect.width:parentRect.height;
  var remainingExtra=stretchTotal>0&&available>fixed?available-fixed:0;
  var remainingStretch=stretchTotal;
  var cursor=horizontal?parentRect.left:parentRect.top;
  visible.forEach(function(item){
    var before=runtimeLayoutNumber(item,horizontal?'marginLeft':'marginTop',0);
    var after=runtimeLayoutNumber(item,horizontal?'marginRight':'marginBottom',0);
    var crossBefore=runtimeLayoutNumber(item,horizontal?'marginTop':'marginLeft',0);
    var crossAfter=runtimeLayoutNumber(item,horizontal?'marginBottom':'marginRight',0);
    cursor+=before;
    var current=nodeRect(item);
    var preserveScrollContent=scrollViewport&&(item.dataset.ck3Kind||'').toLowerCase()!=='expand';
    var target={left:horizontal?cursor:parentRect.left+crossBefore,top:horizontal?parentRect.top+crossBefore:cursor,width:current.width,height:current.height};
    var allocation=0;
    if(isMainAxisExpanding(item,horizontal)&&remainingStretch>0){var stretch=mainAxisStretch(item,horizontal);allocation=Math.floor(remainingExtra*stretch/remainingStretch);remainingExtra-=allocation;remainingStretch-=stretch;}
    if(horizontal){
      if(isMainAxisExpanding(item,true)&&allocation>0){target.width=preserveScrollContent?Math.max(current.width,allocation):allocation;}
      if(item.dataset.ck3ExpandVertical==='true'){target.height=Math.max(1,parentRect.height-crossBefore-crossAfter);}
    }else{
      if(isMainAxisExpanding(item,false)&&allocation>0){target.height=preserveScrollContent?Math.max(current.height,allocation):allocation;}
      if(item.dataset.ck3ExpandHorizontal==='true'){target.width=Math.max(1,parentRect.width-crossBefore-crossAfter);}
    }
    target.width=clampRuntimeLayoutDimension(item,target.width,true);
    target.height=clampRuntimeLayoutDimension(item,target.height,false);
    translateSubtree(item,target.left-current.left,target.top-current.top);
    setNodeRect(item,target);
    cursor+=(horizontal?target.width:target.height)+after+spacing;
  });
}
function gridOrigin(container,parent,contentWidth,contentHeight){var raw=(container.dataset.ck3GridLayoutAnchor||'').toLowerCase().replace(/[, ]+/g,'|');if(raw===''){return {left:parent.left,top:parent.top};}var left=parent.left;var top=parent.top;if(raw.indexOf('right')>=0){left=parent.left+parent.width-contentWidth;}else if(raw.indexOf('hcenter')>=0||raw==='center'){left=parent.left+(parent.width-contentWidth)/2;}if(raw.indexOf('bottom')>=0){top=parent.top+parent.height-contentHeight;}else if(raw.indexOf('vcenter')>=0||raw==='center'){top=parent.top+(parent.height-contentHeight)/2;}return {left:left,top:top};}
function reflowGrid(container){var columns=Math.max(1,Math.round(numericData(container,'ck3GridColumns',1)));var columnStep=Math.max(1,numericData(container,'ck3GridColumnStep',1));var rowStep=Math.max(1,numericData(container,'ck3GridRowStep',1));var flip=container.dataset.ck3GridFlip==='true';var ignoreInvisible=container.dataset.ck3IgnoreInvisible==='true';var items=directGridItems(container);items.forEach(function(item){item.classList.toggle('is-flow-ignored',ignoreInvisible&&item.dataset.simVisible==='false');});var visible=items.filter(function(item){return !(ignoreInvisible&&item.dataset.simVisible==='false');});var rows=Math.ceil(visible.length/columns);var parent=nodeRect(container);var origin=gridOrigin(container,parent,columns*columnStep,rows*rowStep);visible.forEach(function(item,index){var row=Math.floor(index/columns);var column=index%columns;if(flip){row=rows-1-row;column=columns-1-column;}var current=nodeRect(item);var left=origin.left+column*columnStep+runtimeLayoutNumber(item,'marginLeft',0);var top=origin.top+row*rowStep+runtimeLayoutNumber(item,'marginTop',0);translateSubtree(item,left-current.left,top-current.top);item.dataset.ck3GridCurrentRow=String(row);item.dataset.ck3GridCurrentColumn=String(column);});}
function measureAutoResizeTextNodes(){nodes.forEach(function(node){if(node.dataset.ck3AutoResize!=='true'||node.dataset.ck3Multiline!=='true'||node.dataset.ck3EffectiveHidden==='true'){return;}var caption=node.querySelector('.ck3-caption');if(!caption){return;}var rect=nodeRect(node);var minimum=Math.max(rect.height,runtimeLayoutDimension(node,'minHeight'));var hardLimit=Math.min(8192,Math.max(height*4,minimum));var maximum=runtimeLayoutDimension(node,'maxHeight');if(maximum<=0){maximum=hardLimit;}var required=Math.max(minimum,Math.ceil(caption.scrollHeight+4));setNodeRect(node,{left:rect.left,top:rect.top,width:rect.width,height:Math.max(1,Math.min(maximum,required))});});}
function intersectRects(left,right){var minLeft=Math.max(left.left,right.left);var minTop=Math.max(left.top,right.top);var maxRight=Math.min(left.left+left.width,right.left+right.width);var maxBottom=Math.min(left.top+left.height,right.top+right.height);return {left:minLeft,top:minTop,width:Math.max(0,maxRight-minLeft),height:Math.max(0,maxBottom-minTop)};}
function scrollAncestors(node){var result=[];var current=nodeByIndex(node&&node.dataset.ck3Parent);while(current){if(current.dataset.ck3ScrollViewport==='true'){result.push(current);}current=nodeByIndex(current.dataset.ck3Parent);}return result;}
function nearestScrollViewport(node){var current=node;while(current){if(current.dataset.ck3ScrollViewport==='true'){return current;}current=nodeByIndex(current.dataset.ck3Parent);}return null;}
function runtimeAllowOutside(node){if(!node){return false;}if(node.dataset.simAllowOutside!==undefined){return node.dataset.simAllowOutside==='true';}return node.dataset.ck3AllowOutside==='true';}
function hasAllowOutsideAncestor(node,viewport){var current=node;while(current&&current!==viewport){if(runtimeAllowOutside(current)){return true;}current=nodeByIndex(current.dataset.ck3Parent);}return false;}
function scrollContentExtent(container){var parentRect=nodeRect(container);var maxRight=parentRect.left+parentRect.width;var maxBottom=parentRect.top+parentRect.height;directFlowItems(container).filter(function(item){return !item.classList.contains('is-flow-ignored')&&!hasAllowOutsideAncestor(item,container);}).forEach(function(item){var rect=nodeRect(item);maxRight=Math.max(maxRight,rect.left+rect.width);maxBottom=Math.max(maxBottom,rect.top+rect.height);});return {width:Math.max(parentRect.width,maxRight-parentRect.left),height:Math.max(parentRect.height,maxBottom-parentRect.top)};}
function scrollVisibleRect(container){var visible=nodeRect(container);scrollAncestors(container).forEach(function(ancestor){visible=intersectRects(visible,nodeRect(ancestor));});return visible;}
function syncScrollControl(container,maximum,offset){var control=scrollControlByOwner[String(container.dataset.ck3Index)];if(!control){return;}var input=control.querySelector('input');if(input){input.max=String(Math.max(0,Math.round(maximum)));input.step=String(Math.max(1,Math.round(numericData(container,'ck3ScrollStep',24))));input.value=String(Math.max(0,Math.min(maximum,offset)));}var visible=scrollVisibleRect(container);var hidden=maximum<=0||visible.width<=0||visible.height<12||container.dataset.ck3EffectiveHidden==='true';control.classList.toggle('is-scrollbar-idle',hidden);if(hidden){return;}var ownerRect=nodeRect(container);control.style.left=Math.round(Math.min(ownerRect.left+ownerRect.width-12,visible.left+visible.width-12))+'px';control.style.top=Math.round(visible.top+2)+'px';control.style.height=Math.max(12,Math.round(visible.height-4))+'px';}
function applyScrollViewport(container){var extent=scrollContentExtent(container);container.dataset.ck3ScrollContentWidth=String(Math.round(extent.width));container.dataset.ck3ScrollContentHeight=String(Math.round(extent.height));var parentRect=nodeRect(container);var maximum=Math.max(0,extent.height-parentRect.height);var offset=Math.max(0,Math.min(maximum,numericData(container,'ck3ScrollOffset',0)));container.dataset.ck3ScrollOffset=String(offset);if(offset){directFlowItems(container).filter(function(item){return !item.classList.contains('is-flow-ignored');}).forEach(function(item){translateSubtree(item,0,-offset);});}syncScrollControl(container,maximum,offset);}
function applyScrollClipping(){nodes.forEach(function(node){if(node.dataset.ck3OverlayRole!==undefined){return;}var ancestors=scrollAncestors(node);if(!ancestors.length){node.classList.remove('is-scroll-clipped');node.style.removeProperty('clip-path');return;}var clip=nodeRect(ancestors[0]);for(var i=1;i<ancestors.length;i+=1){clip=intersectRects(clip,nodeRect(ancestors[i]));}var rect=nodeRect(node);var visible=intersectRects(rect,clip);var hidden=visible.width<=0||visible.height<=0;node.classList.toggle('is-scroll-clipped',hidden);if(hidden){node.style.removeProperty('clip-path');return;}var top=Math.max(0,visible.top-rect.top);var right=Math.max(0,rect.left+rect.width-(visible.left+visible.width));var bottom=Math.max(0,rect.top+rect.height-(visible.top+visible.height));var left=Math.max(0,visible.left-rect.left);if(top||right||bottom||left){node.style.clipPath='inset('+top+'px '+right+'px '+bottom+'px '+left+'px)';}else{node.style.removeProperty('clip-path');}});}
function applyScrollViewports(){nodes.filter(function(node){return node.dataset.ck3ScrollViewport==='true';}).forEach(applyScrollViewport);applyScrollClipping();}
function closeAllTooltips(){nodes.forEach(function(node){node.classList.remove('is-tooltip-open');});runtimeTooltipOwner=null;if(runtimeTooltip){runtimeTooltip.classList.remove('is-tooltip-open');runtimeTooltip.setAttribute('aria-hidden','true');runtimeTooltip.textContent='';}}
function tooltipAvailable(owner){return !!owner&&owner.dataset.simTooltipVisible!=='false';}
function tooltipOwnerFor(node){if(!node){return null;}if(node.dataset.ck3OverlayOwner!==undefined){return nodeByIndex(node.dataset.ck3OverlayOwner);}var current=node;while(current){if(tooltipNodesByOwner[String(current.dataset.ck3Index)]){return current;}current=nodeByIndex(current.dataset.ck3Parent);}return null;}
function openTooltip(owner){if(!tooltipAvailable(owner)||owner.dataset.ck3EffectiveHidden==='true'||owner.classList.contains('is-scroll-clipped')){return false;}var tooltipNodes=tooltipNodesByOwner[String(owner.dataset.ck3Index)]||[];var content=tooltipNodes.filter(function(node){return node.dataset.ck3OverlayRole==='tooltip_content'&&node.dataset.ck3StateName===undefined;});if(!content.length){return false;}closeAllTooltips();var minLeft=Infinity;var minTop=Infinity;var maxRight=-Infinity;var maxBottom=-Infinity;content.forEach(function(node){var rect=nodeRect(node);minLeft=Math.min(minLeft,rect.left);minTop=Math.min(minTop,rect.top);maxRight=Math.max(maxRight,rect.left+rect.width);maxBottom=Math.max(maxBottom,rect.top+rect.height);});var ownerRect=nodeRect(owner);var tooltipWidth=Math.max(1,maxRight-minLeft);var tooltipHeight=Math.max(1,maxBottom-minTop);var targetLeft=ownerRect.left+ownerRect.width+12;if(targetLeft+tooltipWidth>width-8){targetLeft=Math.max(8,ownerRect.left-tooltipWidth-12);}var targetTop=Math.max(8,Math.min(ownerRect.top,height-tooltipHeight-8));var deltaX=targetLeft-minLeft;var deltaY=targetTop-minTop;tooltipNodes.filter(function(node){return node.dataset.ck3OverlayRole==='tooltip_root';}).forEach(function(rootNode){translateSubtree(rootNode,deltaX,deltaY);});content.forEach(function(node){node.classList.add('is-tooltip-open');});return true;}
function tooltipTextOwnerFor(node){var current=node;while(current){if(tooltipFor(current)){return current;}current=nodeByIndex(current.dataset.ck3Parent);}return null;}
function openRuntimeTooltip(owner){if(!runtimeTooltip||!tooltipAvailable(owner)||owner.dataset.ck3EffectiveHidden==='true'||owner.classList.contains('is-scroll-clipped')){return false;}var value=tooltipFor(owner);if(!value){return false;}closeAllTooltips();runtimeTooltipOwner=owner;runtimeTooltip.textContent=value;runtimeTooltip.classList.add('is-tooltip-open');runtimeTooltip.setAttribute('aria-hidden','false');var ownerRect=nodeRect(owner);var tooltipWidth=Math.max(1,runtimeTooltip.offsetWidth);var tooltipHeight=Math.max(1,runtimeTooltip.offsetHeight);var targetLeft=ownerRect.left+ownerRect.width+12;if(targetLeft+tooltipWidth>width-8){targetLeft=Math.max(8,ownerRect.left-tooltipWidth-12);}var targetTop=Math.max(8,Math.min(ownerRect.top,height-tooltipHeight-8));runtimeTooltip.style.left=Math.round(targetLeft)+'px';runtimeTooltip.style.top=Math.round(targetTop)+'px';return true;}
function visualTooltipOwnerFor(node){return tooltipOwnerFor(node)||tooltipTextOwnerFor(node);}
function sameTooltipGroup(left,right){var leftOwner=visualTooltipOwnerFor(left);var rightOwner=visualTooltipOwnerFor(right);return !!leftOwner&&!!rightOwner&&leftOwner===rightOwner;}
function reflowDynamicLayouts(){syncEffectiveVisibility();closeAllTooltips();resetDynamicLayout();applyRuntimeLayoutConstraints();measureAutoResizeTextNodes();nodes.forEach(reflowContainer);nodes.forEach(reflowGrid);nodes.forEach(syncFillChildren);applyScrollViewports();if(selected){text('ck3-detail-bounds',selected.dataset.ck3Bounds);}}
function unknownValue(kind){return {known:false,kind:kind||'unknown'};}
function runtimeFactValue(index){var control=runtimeFactControls.find(function(item){return item.dataset.ck3RuntimeFact===String(index);});if(!control||control.value===''){return unknownValue(control?control.dataset.ck3RuntimeKind:'unknown');}var kind=control.dataset.ck3RuntimeKind;if(kind==='boolean'){return {known:true,kind:'boolean',value:control.value==='true'};}if(kind==='number'){var number=Number(control.value);return Number.isFinite(number)?{known:true,kind:'number',value:number}:unknownValue('number');}return {known:true,kind:kind||'string',value:control.value};}
function runtimeNot(value){return value.known&&value.kind==='boolean'?{known:true,kind:'boolean',value:!value.value}:unknownValue('boolean');}
function runtimeGroup(operation,values){var unknown=false;for(var i=0;i<values.length;i+=1){var value=values[i];if(!value.known||value.kind!=='boolean'){unknown=true;continue;}if(operation==='and'&&!value.value){return {known:true,kind:'boolean',value:false};}if(operation==='or'&&value.value){return {known:true,kind:'boolean',value:true};}}return unknown?unknownValue('boolean'):{known:true,kind:'boolean',value:operation==='and'};}
function runtimeCompare(operation,left,right){if(!left.known||!right.known||left.kind!==right.kind){return unknownValue('boolean');}var result=false;if(operation==='eq'){result=left.value===right.value;}else if(operation==='ne'){result=left.value!==right.value;}else if(operation==='lt'){result=left.value<right.value;}else if(operation==='le'){result=left.value<=right.value;}else if(operation==='gt'){result=left.value>right.value;}else if(operation==='ge'){result=left.value>=right.value;}return {known:true,kind:'boolean',value:result};}
function evaluateRuntimeTokens(tokens){var stack=[];function pop(){return stack.length?stack.pop():unknownValue();}for(var i=0;i<tokens.length;i+=1){var token=tokens[i];if(token.o==='select'){var condition=evaluateRuntimeTokens(token.c||[]);if(!condition.known||condition.kind!=='boolean'){stack.push(unknownValue());continue;}stack.push(evaluateRuntimeTokens(condition.value?(token.t||[]):(token.e||[])));}else if(token.o==='f'){stack.push(runtimeFactValue(token.f));}else if(token.o==='b'){stack.push({known:true,kind:'boolean',value:token.b});}else if(token.o==='n'){stack.push({known:true,kind:'number',value:token.n});}else if(token.o==='s'){stack.push({known:true,kind:'string',value:token.s});}else if(token.o==='not'){stack.push(runtimeNot(pop()));}else if(token.o==='and'||token.o==='or'){var arity=Number(token.a)||0;if(arity<1||arity>stack.length){stack.push(unknownValue('boolean'));continue;}var values=stack.splice(stack.length-arity,arity);stack.push(runtimeGroup(token.o,values));}else if(['eq','ne','lt','le','gt','ge'].includes(token.o)){var right=pop();var left=pop();stack.push(runtimeCompare(token.o,left,right));}}return stack.length===1?stack[0]:unknownValue();}
function evaluateRuntimePlan(planID){var plan=runtimePlans[String(planID)];return !plan||!plan.supported?unknownValue('boolean'):evaluateRuntimeTokens(plan.tokens);}
function formatRuntimeTextValue(value,format){if(!value.known){return null;}if(!format){return String(value.value);}if(value.kind!=='number'){return null;}var signed=format.indexOf('+=')===0;var precision=Number(signed?format.slice(2):format);if(!Number.isInteger(precision)||precision<0||precision>4){return null;}var factor=Math.pow(10,precision);var rounded=(value.value<0?-1:1)*Math.round(Math.abs(value.value)*factor)/factor;var result=rounded.toFixed(precision);return signed&&value.value>0?'+'+result:result;}
function evaluateRuntimeTextTokens(tokens){var output='';var missing=0;tokens.forEach(function(token){if(Array.isArray(token.c)&&token.c.length){var condition=evaluateRuntimeTokens(token.c);if(!condition.known||condition.kind!=='boolean'){output+='<unknown>';missing+=1;return;}var branch=condition.value?(token.t||[]):(token.e||[]);var nested=evaluateRuntimeTextTokens(branch);output+=nested.value;missing+=nested.missing;return;}if(token.f===undefined){output+=token.l||'';return;}var value=formatRuntimeTextValue(runtimeFactValue(token.f),token.x||'');if(value===null){output+='<unknown>';missing+=1;}else{output+=value;}});return {value:output,missing:missing};}
function evaluateRuntimeTextPlan(planID){var plan=runtimeTextPlans[String(planID)];return !plan||!plan.supported?null:evaluateRuntimeTextTokens(plan.tokens);}
function runtimeTextPlanIDs(node,prefix){var title=prefix==='text'?'Text':(prefix==='tooltipWhenDisabled'?'TooltipWhenDisabled':'Tooltip');var key='ck3'+title+'Plan';var raw=node.dataset[key+'Raw'];var english=node.dataset[key+'English'];var chinese=node.dataset[key+'SimpChinese'];if(currentLanguage==='english'){return [english!==undefined?english:raw];}if(currentLanguage==='simp_chinese'){return [chinese!==undefined?chinese:raw];}if(currentLanguage==='bilingual'&&(chinese!==undefined||english!==undefined)){return [chinese,english].filter(function(value){return value!==undefined;});}return raw!==undefined?[raw]:[];}
function evaluateRuntimeNodeText(node,prefix){var ids=runtimeTextPlanIDs(node,prefix);if(!ids.length){return null;}var values=[];var missing=0;ids.forEach(function(id){var result=evaluateRuntimeTextPlan(id);if(result){values.push(result.value);missing+=result.missing;}});return values.length?{value:values.join(' / '),missing:missing}:null;}
function applyRuntimeTexts(){var count=0;nodes.forEach(function(node){if(node.dataset.ck3TextOverride!=='scenario'){var textResult=evaluateRuntimeNodeText(node,'text');if(textResult){node.dataset.simText=textResult.value;var caption=node.querySelector('.ck3-caption');if(caption){caption.textContent=textResult.value;}count+=1;}}var tooltipResult=evaluateRuntimeNodeText(node,'tooltip');if(tooltipResult){node.dataset.simTooltip=tooltipResult.value;count+=1;}var disabledTooltipResult=evaluateRuntimeNodeText(node,'tooltipWhenDisabled');if(disabledTooltipResult){node.dataset.simTooltipWhenDisabled=disabledTooltipResult.value;count+=1;}});return count;}
function runtimePropertyTitle(property){return property.charAt(0).toUpperCase()+property.slice(1);}
function syncPressed(node){var semanticCount=0;var knownCount=0;var pressed=false;['Down','Selected'].forEach(function(title){if(node.dataset['ck3'+title]!==undefined){semanticCount+=1;if(node.dataset['sim'+title]!==undefined){knownCount+=1;pressed=pressed||node.dataset['sim'+title]==='true';}}});node.classList.toggle('is-sim-down',node.dataset.simDown==='true');node.classList.toggle('is-sim-selected-state',node.dataset.simSelected==='true');if(pressed){node.setAttribute('aria-pressed','true');}else if(semanticCount>0&&knownCount===semanticCount){node.setAttribute('aria-pressed','false');}else{node.removeAttribute('aria-pressed');}}
function syncChecked(node){if(node.dataset.ck3Checkable!=='true'){return;}var checked=node.dataset.simChecked==='true';node.classList.toggle('is-sim-checked',checked);}
function applyRuntimeProperty(node,property){var title=runtimePropertyTitle(property);var plan=node.dataset['ck3'+title+'Plan'];if(plan===undefined||node.dataset['ck3'+title+'Override']==='scenario'){return null;}var result=evaluateRuntimePlan(plan);var dataKey='sim'+title;if(result.known&&result.kind==='boolean'){node.dataset[dataKey]=String(result.value);if(property==='visible'){node.classList.toggle('is-sim-hidden',!result.value);node.setAttribute('aria-hidden',String(!result.value));}else if(property==='enabled'){node.classList.toggle('is-sim-disabled',!result.value);node.disabled=!result.value;node.setAttribute('aria-disabled',String(!result.value));}else if(property==='grayscale'){if(result.value){node.style.setProperty('filter','grayscale(1)');}else{node.style.removeProperty('filter');}}else if(property==='down'||property==='selected'){syncPressed(node);}else if(property==='checked'){syncChecked(node);}return true;}delete node.dataset[dataKey];if(property==='visible'){node.classList.remove('is-sim-hidden');node.removeAttribute('aria-hidden');}else if(property==='enabled'){node.classList.remove('is-sim-disabled');node.disabled=false;node.removeAttribute('aria-disabled');}else if(property==='grayscale'){node.style.removeProperty('filter');}else if(property==='down'||property==='selected'){syncPressed(node);}else if(property==='checked'){syncChecked(node);}return false;}
function runtimeNodeNumber(node,name,fallback){var title=runtimePropertyTitle(name);var data=node.dataset['sim'+title];if(data!==undefined){var number=Number(data);return Number.isFinite(number)?{known:true,value:number}:{known:false};}return node.dataset['ck3'+title+'Plan']===undefined&&Number.isFinite(fallback)?{known:true,value:fallback}:{known:false};}
function refreshRuntimeProgress(node){var value=runtimeNodeNumber(node,'value',NaN);if(!value.known){value=runtimeNodeNumber(node,'animatedProgressValue',NaN);}var minimum=runtimeNodeNumber(node,'min',0);var maximum=runtimeNodeNumber(node,'max',1);if(value.known&&minimum.known&&maximum.known&&maximum.value>minimum.value){var progress=Math.max(0,Math.min(1,(value.value-minimum.value)/(maximum.value-minimum.value)));node.style.setProperty('--ck3-progress',String(progress));node.style.setProperty('--ck3-progress-angle',String(progress*360)+'deg');node.style.setProperty('--ck3-progress-inverse',String((1-progress)*100)+'%');node.setAttribute('aria-valuemin',String(minimum.value));node.setAttribute('aria-valuemax',String(maximum.value));node.setAttribute('aria-valuenow',String(value.value));return true;}node.style.removeProperty('--ck3-progress');node.style.removeProperty('--ck3-progress-angle');node.style.removeProperty('--ck3-progress-inverse');node.removeAttribute('aria-valuemin');node.removeAttribute('aria-valuemax');node.removeAttribute('aria-valuenow');return false;}
function setRuntimeNumber(node,property,value){var title=runtimePropertyTitle(property);var dataKey='sim'+title;if(Number.isFinite(value)){node.dataset[dataKey]=String(value);return true;}delete node.dataset[dataKey];return false;}
function applyRuntimeNumber(node,property){var title=runtimePropertyTitle(property);var plan=node.dataset['ck3'+title+'Plan'];if(plan===undefined){return null;}var result=evaluateRuntimePlan(plan);return result.known&&result.kind==='number'?setRuntimeNumber(node,property,result.value):setRuntimeNumber(node,property,NaN);}
function normalizeRuntimeColor(raw){if(typeof raw!=='string'){return null;}var value=raw.trim();if(value.length>=2&&((value.charAt(0)==='"'&&value.charAt(value.length-1)==='"')||(value.charAt(0)==="'"&&value.charAt(value.length-1)==="'"))){value=value.slice(1,-1).trim();}if(value.length>=2&&value.charAt(0)==='{'&&value.charAt(value.length-1)==='}'){value=value.slice(1,-1).trim();}if(value.charAt(0)==='#'){var hex=value.toLowerCase();if(![4,5,7,9].includes(hex.length)||!/^#[0-9a-f]+$/.test(hex)){return null;}return hex;}var fields=value.replace(/[;,]/g,' ').trim().split(/\s+/);if(fields.length!==3&&fields.length!==4){return null;}var channels=[0,0,0,1];for(var index=0;index<fields.length;index+=1){var channel=Number(fields[index]);if(!Number.isFinite(channel)||channel<0||channel>1){return null;}channels[index]=channel;}return 'rgba('+Math.round(channels[0]*255)+','+Math.round(channels[1]*255)+','+Math.round(channels[2]*255)+','+channels[3].toFixed(3)+')';}
function syncRuntimeColors(node){var textureColor=node.dataset.simTintColor!==undefined?node.dataset.simTintColor:node.dataset.simColor;if(textureColor!==undefined){node.style.setProperty('--ck3-tint-color',textureColor);}else{node.style.removeProperty('--ck3-tint-color');}var fontColor=node.dataset.simFontTintColor;if(fontColor!==undefined){node.style.setProperty('--ck3-font-tint-color',fontColor);node.style.setProperty('color',fontColor);}else{node.style.removeProperty('--ck3-font-tint-color');node.style.removeProperty('color');}}
function applyRuntimeColor(node,property){var title=runtimePropertyTitle(property);var plan=node.dataset['ck3'+title+'Plan'];if(plan===undefined){return null;}var result=evaluateRuntimePlan(plan);var color=result.known&&result.kind==='string'?normalizeRuntimeColor(result.value):null;var dataKey='sim'+title;if(color!==null){node.dataset[dataKey]=color;syncRuntimeColors(node);return true;}delete node.dataset[dataKey];syncRuntimeColors(node);return false;}
function refreshRuntimeAlpha(node){var alpha=Number(node.dataset.simAlpha);if(Number.isFinite(alpha)){node.style.setProperty('--ck3-alpha',String(Math.max(0,Math.min(1,alpha))));return true;}node.style.removeProperty('--ck3-alpha');return false;}
function refreshRuntimeScale(node){var scale=Number(node.dataset.simScale);if(Number.isFinite(scale)){node.style.setProperty('--ck3-scale',String(Math.max(0,Math.min(8,scale))));return true;}node.style.removeProperty('--ck3-scale');return false;}
function refreshRuntimeRotateUV(node){var rotation=Number(node.dataset.simRotateUv);if(Number.isFinite(rotation)){rotation=rotation%360;if(Math.abs(rotation)<0.0005){rotation=0;}node.style.setProperty('--ck3-rotate-uv',String(rotation)+'deg');return true;}node.style.removeProperty('--ck3-rotate-uv');return false;}
function setRuntimeValue(node,value){var known=setRuntimeNumber(node,'value',value);refreshRuntimeProgress(node);return known;}
function applyRuntimeValue(node){var known=applyRuntimeNumber(node,'value');refreshRuntimeProgress(node);return known;}
function clearTriggeredStates(){nodes.forEach(function(node){if(node.dataset.simStateSource!=='trigger'){return;}node.style.removeProperty('opacity');node.style.removeProperty('--ck3-state-scale');node.style.removeProperty('--ck3-state-offset-x');node.style.removeProperty('--ck3-state-offset-y');node.style.removeProperty('transition');delete node.dataset.simState;delete node.dataset.simStateSource;});}
function applyStateTriggers(){var active=[];nodes.forEach(function(node){if(node.dataset.ck3StateName!==undefined&&node.dataset.simStateTrigger==='true'){active.push(node);}});clearTriggeredStates();active.forEach(function(definition){applyState(definition,'Triggered','trigger');});return active.length;}
function applyRuntimePlans(){var evaluated=0;var unknown=0;var numeric=0;var colors=0;nodes.forEach(function(node){['visible','enabled','down','selected','checked','stateTrigger','allowOutside','tooltipVisible','grayscale'].forEach(function(property){var title=runtimePropertyTitle(property);if(node.dataset['ck3'+title+'Plan']===undefined||node.dataset['ck3'+title+'Override']==='scenario'){return;}if(applyRuntimeProperty(node,property)){evaluated+=1;}else{unknown+=1;}});['min','max','value','animatedProgressValue','alpha','scale','rotateUv','minWidth','maxWidth','minHeight','maxHeight','marginLeft','marginRight','marginTop','marginBottom','statePositionX','statePositionY'].forEach(function(property){var title=runtimePropertyTitle(property);if(node.dataset['ck3'+title+'Plan']===undefined){return;}if(applyRuntimeNumber(node,property)){numeric+=1;}else{unknown+=1;}});['color','tintColor','fontTintColor'].forEach(function(property){var title=runtimePropertyTitle(property);if(node.dataset['ck3'+title+'Plan']===undefined){return;}if(applyRuntimeColor(node,property)){colors+=1;}else{unknown+=1;}});refreshRuntimeProgress(node);refreshRuntimeAlpha(node);refreshRuntimeScale(node);refreshRuntimeRotateUV(node);});applyStateTriggers();var textBindings=applyRuntimeTexts();reflowDynamicLayouts();text('ck3-runtime-status',evaluated+' boolean binding(s) and '+numeric+' numeric binding(s) evaluated; '+colors+' color binding(s) evaluated; '+unknown+' unknown; '+textBindings+' text binding(s) rendered. Values are provided or interactive, not observed game state.');if(selected){selectNode(selected.dataset.ck3Index);}}
function resetRuntimeFacts(){runtimeFactControls.forEach(function(control){control.value=control.dataset.ck3RuntimeInitial||'';});applyRuntimePlans();}
function runtimeControl(index){return runtimeFactControls.find(function(item){return item.dataset.ck3RuntimeFact===String(index);});}
function runtimeUpdateValue(control,update){if(!control){return {ok:false};}if(update.operation==='unset'){return {ok:true,value:''};}if(update.operation==='toggle'){if(control.dataset.ck3RuntimeKind!=='boolean'||control.value===''){return {ok:false};}return {ok:true,value:control.value==='true'?'false':'true'};}if(update.operation!=='set'){return {ok:false};}var kind=control.dataset.ck3RuntimeKind;if(kind==='boolean'){return typeof update.value==='boolean'?{ok:true,value:String(update.value)}:{ok:false};}if(kind==='number'){return typeof update.value==='number'&&Number.isFinite(update.value)?{ok:true,value:String(update.value)}:{ok:false};}return typeof update.value==='string'?{ok:true,value:update.value}:{ok:false};}
function applyActionUpdates(action,messages,label){var changes=[];for(var i=0;i<action.updates.length;i+=1){var update=action.updates[i];var control=runtimeControl(update.fact);var result=runtimeUpdateValue(control,update);if(!result.ok){messages.push(label+' rejected update '+(update.expression||update.fact)+'.');return false;}changes.push({control:control,value:result.value,update:update});}changes.forEach(function(change){change.control.value=change.value;});messages.push(label+' applied '+changes.map(function(change){return change.update.expression+' -> '+(change.value===''?'<unknown>':change.value);}).join(', '));return true;}
function applyProvidedEffect(action,messages){return applyActionUpdates(action,messages,'provided postcondition');}
function applyRuntimeAction(node,kind){var right=kind==='right';var planText=right?(node.dataset.ck3RightClickActionPlan||''):(node.dataset.ck3ActionPlans||node.dataset.ck3ActionPlan||'');var planIDs=planText.split(',').filter(function(value){return value!=='';});if(!planIDs.length){return false;}var messages=[];var replayed=0;planIDs.forEach(function(planID){var action=runtimeActions[planID];if(!action){return;}if(action.updates&&action.updates.length){var label=action.operation==='provided_effect'?'provided postcondition':'bounded '+action.operation;if(applyActionUpdates(action,messages,label)){replayed+=1;}return;}var control=runtimeControl(action.fact);if(!control||control.dataset.ck3RuntimeKind!=='boolean'){messages.push(action.operation+' requires an unavailable boolean fact.');return;}var next='';if(action.operation==='open_game_view'||action.operation==='set_variable'){next='true';}else if(action.operation==='close_game_view'||action.operation==='clear_variable'){next='false';}else if(action.operation==='set_map_mode'){runtimeFactControls.forEach(function(item){if((item.dataset.ck3RuntimeExpression||'').indexOf('IsMapMode(')===0&&item.dataset.ck3RuntimeKind==='boolean'){item.value='false';}});next='true';}else if(action.operation==='toggle_game_view'||action.operation==='toggle_game_view_data'||action.operation==='toggle_variable'){if(control.value===''){messages.push('Toggle '+action.argument+' requires a known initial fact.');return;}next=control.value==='true'?'false':'true';}else{return;}control.value=next;replayed+=1;var message=action.operation+' '+action.argument+' -> '+next;if(action.dataExpression){message+='; literal/data expression preserved: '+action.dataExpression;}messages.push(message);});applyRuntimePlans();var total=right?(node.dataset.ck3OnRightClick?1:0):(Number(node.dataset.ck3OnClickCount)||planIDs.length);var preserved=Math.max(0,total-planIDs.length);if(preserved){messages.push(preserved+' '+(right?'right-click':'click')+' effect(s) preserved but not executed.');}text('ck3-action-log',messages.join(' · ')+'. No game effect was executed.');text('ck3-status','Replayed '+replayed+' bounded '+(right?'right-click':'click')+' effect(s) for #'+node.dataset.ck3Index+modelRowLabel(node)+'.');return true;}
function applyLanguage(language){currentLanguage=language||'raw';root.dataset.ck3Language=currentLanguage;applyRuntimeTexts();nodes.forEach(function(node){if(node.dataset.simText!==undefined){return;}var caption=node.querySelector('.ck3-caption');if(caption){caption.textContent=labelFor(node);}});var tooltipOwner=runtimeTooltipOwner;reflowDynamicLayouts();if(tooltipOwner){openRuntimeTooltip(tooltipOwner);}if(selected){text('ck3-detail-localized',nodeByIndex(selected.dataset.ck3Index).dataset.simText!==undefined?nodeByIndex(selected.dataset.ck3Index).dataset.simText:labelFor(selected));text('ck3-detail-tooltip',tooltipFor(selected));}text('ck3-status','Preview language: '+currentLanguage+'; bounded text plans were recomputed and unresolved facts remain explicit.');}
function selectNode(index){
  var node=nodeByIndex(index);if(!node){return;}
  if(node.dataset.ck3OverlayOwner!==undefined){openTooltip(nodeByIndex(node.dataset.ck3OverlayOwner));}
  selected=node;
  nodes.forEach(function(item){item.classList.toggle('is-selected',item===node);});
  treeItems.forEach(function(item){var active=item.dataset.ck3TreeIndex===String(index);item.classList.toggle('is-selected',active);if(active){item.scrollIntoView({block:'nearest'});}});
  text('ck3-selected-index','#'+node.dataset.ck3Index);
  text('ck3-detail-kind',node.dataset.ck3Kind);
  text('ck3-detail-name',node.dataset.ck3Name);
  text('ck3-detail-parent',node.dataset.ck3Parent);
  text('ck3-detail-bounds',node.dataset.ck3Bounds);
  text('ck3-detail-source',(node.dataset.ck3Source||'—')+(node.dataset.ck3Line?' : '+node.dataset.ck3Line:''));
  var texture=node.dataset.ck3Texture||'—';if(node.dataset.ck3TextureResolved){texture+=' · '+(node.dataset.ck3TextureResolved==='true'?'resolved':'missing');}if(node.dataset.ck3TextureEmbedded==='true'){texture+=' · '+(node.dataset.ck3TextureFormat||'decoded')+(node.dataset.ck3TextureSize?' '+node.dataset.ck3TextureSize:'');if(node.dataset.ck3TextureResized==='true'&&node.dataset.ck3TextureSourceSize){texture+=' from '+node.dataset.ck3TextureSourceSize;}if(node.dataset.ck3TextureFrameGrid){texture+=' · frames '+node.dataset.ck3TextureFrameGrid;}if(node.dataset.ck3TextureFrameImages){texture+=' · split '+node.dataset.ck3TextureFrameImages;}}if(node.dataset.ck3SpriteBorder){texture+=' · nine-slice '+node.dataset.ck3SpriteBorder+(node.dataset.ck3SpriteType?' '+node.dataset.ck3SpriteType:'');}if(node.dataset.ck3Mirror){texture+=' · mirror '+node.dataset.ck3Mirror;}text('ck3-detail-texture',texture);
  text('ck3-detail-context',node.dataset.ck3DataContext);
  text('ck3-detail-localized',node.dataset.simText!==undefined?node.dataset.simText:labelFor(node));
  text('ck3-detail-tooltip',tooltipFor(node));
  text('ck3-detail-model-row',node.dataset.ck3ModelRowSource!==undefined?('source '+node.dataset.ck3ModelRowSource+' · '+node.dataset.ck3ModelRowId+' ['+node.dataset.ck3ModelRowIndex+'] · '+(node.dataset.ck3ModelRowDatamodel||node.dataset.ck3ModelRowTarget||'')):'');
  var clicks=[];if(node.dataset.ck3OnClicks||node.dataset.ck3OnClick){clicks.push('click '+(node.dataset.ck3OnClicks||node.dataset.ck3OnClick));}if(node.dataset.ck3OnRightClick){clicks.push('right-click '+node.dataset.ck3OnRightClick);}text('ck3-detail-clicks',clicks.join('\n'));
  var scenario=[];if(node.dataset.ck3ScenarioSource){scenario.push('source '+node.dataset.ck3ScenarioSource);}if(node.dataset.simVisible!==undefined){scenario.push('visible '+node.dataset.simVisible);}if(node.dataset.simEnabled!==undefined){scenario.push('enabled '+node.dataset.simEnabled);}if(node.dataset.simMin!==undefined){scenario.push('min '+node.dataset.simMin);}if(node.dataset.simMax!==undefined){scenario.push('max '+node.dataset.simMax);}if(node.dataset.simValue!==undefined){scenario.push('value '+node.dataset.simValue);}if(node.dataset.simAnimatedProgressValue!==undefined){scenario.push('animated_progress_value '+node.dataset.simAnimatedProgressValue);}if(node.dataset.simText!==undefined){scenario.push('text '+node.dataset.simText);}if(node.dataset.ck3ScenarioTexture!==undefined){scenario.push('texture '+node.dataset.ck3ScenarioTexture);}text('ck3-detail-scenario',scenario.join(' · '));
  var stateDefinition=node.dataset.ck3StateName||'';text('ck3-detail-state',stateDefinition?(stateDefinition+(node.dataset.ck3StateAlpha?' · alpha '+node.dataset.ck3StateAlpha:'')+(node.dataset.ck3StateScale?' · scale '+node.dataset.ck3StateScale:'')+(node.dataset.ck3StatePositionX?' · x '+node.dataset.ck3StatePositionX:'')+(node.dataset.ck3StatePositionY?' · y '+node.dataset.ck3StatePositionY:'')+(node.dataset.ck3StateDuration?' · '+node.dataset.ck3StateDuration+'s':'')+(node.dataset.ck3StateTrigger?' · trigger '+node.dataset.ck3StateTrigger:'')):'' );
  var visible=node.dataset.ck3Visible||'';var enabled=node.dataset.ck3Enabled||'';var down=node.dataset.ck3Down||'';var selectedExpression=node.dataset.ck3Selected||'';var checkedExpression=node.dataset.ck3Checked||'';var dynamic=node.dataset.ck3DynamicText==='true';var state=node.dataset.ck3State||stateDefinition;
  setControl('ck3-sim-visible',!!visible,node.dataset.simVisible!=='false');text('ck3-visible-expression',visible);
  setControl('ck3-sim-enabled',!!enabled,node.dataset.simEnabled!=='false');text('ck3-enabled-expression',enabled);
  setControl('ck3-sim-down',!!down,node.dataset.simDown==='true');text('ck3-down-expression',down);
  setControl('ck3-sim-selected-state',!!selectedExpression,node.dataset.simSelected==='true');text('ck3-selected-expression',selectedExpression);
  setControl('ck3-sim-checked',node.dataset.ck3Checkable==='true'&&!!checkedExpression,node.dataset.simChecked==='true');text('ck3-checked-expression',checkedExpression);
  setControl('ck3-sim-text',dynamic,node.dataset.simText!==undefined?node.dataset.simText:labelFor(node));text('ck3-text-expression',dynamic?(node.dataset.ck3RawText||node.dataset.ck3OriginalLabel):'');
  setControl('ck3-sim-state',!!state,node.dataset.simState||state);text('ck3-state-expression',state);
  byId('ck3-apply-state').disabled=!stateDefinition;
  byId('ck3-sim-click').disabled=!(node.dataset.ck3OnClick||node.dataset.ck3OnClicks);byId('ck3-sim-right-click').disabled=!node.dataset.ck3OnRightClick;
  text('ck3-status','Selected #'+node.dataset.ck3Index+' '+node.dataset.ck3Kind+modelRowLabel(node)+(node.dataset.ck3Name?' · '+node.dataset.ck3Name:''));
}
function updateZoom(percent){var scale=Math.max(.25,Math.min(2,Number(percent)/100));canvas.style.transform='scale('+scale+')';shell.style.width=Math.ceil(width*scale+72)+'px';shell.style.height=Math.ceil(height*scale+72)+'px';byId('ck3-zoom-value').textContent=Math.round(scale*100)+'%';}
function fit(){var scale=Math.min((viewport.clientWidth-72)/width,(viewport.clientHeight-72)/height);scale=Math.max(.25,Math.min(2,scale));var percent=Math.round(scale*100/5)*5;byId('ck3-zoom').value=String(percent);updateZoom(percent);viewport.scrollLeft=0;viewport.scrollTop=0;}
tree.addEventListener('click',function(event){var item=event.target.closest('[data-ck3-tree-index]');if(item){selectNode(item.dataset.ck3TreeIndex);}});
function actionOwnerFor(node,kind){var current=node;var right=kind==='right';while(current){if(right?current.dataset.ck3OnRightClick:(current.dataset.ck3OnClick||current.dataset.ck3OnClicks)){return current;}current=nodeByIndex(current.dataset.ck3Parent);}return null;}
canvas.addEventListener('click',function(event){var node=event.target.closest('[data-ck3-stage-node]');if(!node){return;}event.stopPropagation();var actionOwner=actionOwnerFor(node,'left');var replay=byId('ck3-replay-clicks').checked&&root.classList.contains('visual-mode')&&actionOwner;selectNode(replay?actionOwner.dataset.ck3Index:node.dataset.ck3Index);if(!replay){return;}if(actionOwner.dataset.simEnabled==='false'||actionOwner.dataset.ck3EffectiveHidden==='true'){text('ck3-action-log','Direct replay rejected for a disabled or hidden node.');return;}applyRuntimeAction(actionOwner,'left');});
canvas.addEventListener('contextmenu',function(event){var node=event.target.closest('[data-ck3-stage-node]');if(!node){return;}event.stopPropagation();var actionOwner=actionOwnerFor(node,'right');if(actionOwner){event.preventDefault();}var replay=byId('ck3-replay-clicks').checked&&root.classList.contains('visual-mode')&&actionOwner;selectNode(replay?actionOwner.dataset.ck3Index:node.dataset.ck3Index);if(!replay){return;}if(actionOwner.dataset.simEnabled==='false'||actionOwner.dataset.ck3EffectiveHidden==='true'){text('ck3-action-log','Direct right-click replay rejected for a disabled or hidden node.');return;}if(applyRuntimeAction(actionOwner,'right')){return;}text('ck3-action-log','Simulated right-click'+modelRowLabel(actionOwner)+' · '+actionOwner.dataset.ck3OnRightClick);text('ck3-status','Right-click recorded for #'+actionOwner.dataset.ck3Index+modelRowLabel(actionOwner)+'; no game effect was executed.');});
scrollControls.forEach(function(control){var input=control.querySelector('input');if(!input){return;}input.addEventListener('input',function(){var owner=nodeByIndex(control.dataset.ck3ScrollControl);if(!owner){return;}owner.dataset.ck3ScrollOffset=input.value;reflowDynamicLayouts();text('ck3-status','Scrolled viewport #'+owner.dataset.ck3Index+' to '+Math.round(Number(input.value)||0)+' px.');});});
canvas.addEventListener('wheel',function(event){var node=event.target.closest('[data-ck3-stage-node]');var owner=nearestScrollViewport(node);if(!owner){return;}var maximum=Math.max(0,numericData(owner,'ck3ScrollContentHeight',nodeRect(owner).height)-nodeRect(owner).height);if(maximum<=0){return;}event.preventDefault();var step=Math.max(1,numericData(owner,'ck3ScrollStep',24));var current=numericData(owner,'ck3ScrollOffset',0);owner.dataset.ck3ScrollOffset=String(Math.max(0,Math.min(maximum,current+(event.deltaY<0?-step:step))));reflowDynamicLayouts();text('ck3-status','Scrolled viewport #'+owner.dataset.ck3Index+' with the mouse wheel.');},{passive:false});
function stateDefinitionFor(node,name){var current=node;while(current){var currentIndex=current.dataset.ck3Index;var definition=nodes.find(function(candidate){return candidate.dataset.ck3Parent===currentIndex&&candidate.dataset.ck3StateName===name;});if(definition){return definition;}current=nodeByIndex(current.dataset.ck3Parent);}return null;}
function applyState(definition,reason,source){if(!definition||!definition.dataset.ck3StateName){return false;}var target=nodeByIndex(definition.dataset.ck3Parent);if(!target){return false;}var duration=Number(definition.dataset.ck3StateDuration);if(Number.isFinite(duration)&&duration>=0){target.style.transition='opacity '+duration+'s linear, transform '+duration+'s linear';}var alpha=Number(definition.dataset.ck3StateAlpha);if(Number.isFinite(alpha)){target.style.opacity=String(Math.max(0,Math.min(1,alpha)));}var scale=Number(definition.dataset.ck3StateScale);if(Number.isFinite(scale)&&scale>=0){target.style.setProperty('--ck3-state-scale',String(scale));}var positionX=Number(definition.dataset.simStatePositionX!==undefined?definition.dataset.simStatePositionX:definition.dataset.ck3StatePositionX);if(Number.isFinite(positionX)){target.style.setProperty('--ck3-state-offset-x',String(positionX)+'px');}var positionY=Number(definition.dataset.simStatePositionY!==undefined?definition.dataset.simStatePositionY:definition.dataset.ck3StatePositionY);if(Number.isFinite(positionY)){target.style.setProperty('--ck3-state-offset-y',String(positionY)+'px');}target.dataset.simState=definition.dataset.ck3StateName;target.dataset.simStateSource=source||'manual';text('ck3-status',(reason||'Applied')+' '+definition.dataset.ck3StateName+' to #'+target.dataset.ck3Index);return true;}
canvas.addEventListener('pointerover',function(event){var node=event.target.closest('[data-ck3-stage-node]');if(node){applyState(stateDefinitionFor(node,'_mouse_enter'),'Hover applied');var owner=tooltipOwnerFor(node);if(owner&&openTooltip(owner)){return;}var textOwner=tooltipTextOwnerFor(node);if(textOwner){openRuntimeTooltip(textOwner);}}});
canvas.addEventListener('pointerout',function(event){var node=event.target.closest('[data-ck3-stage-node]');if(!node){return;}var related=event.relatedTarget&&event.relatedTarget.closest?event.relatedTarget.closest('[data-ck3-stage-node]'):null;if(related&&sameTooltipGroup(node,related)){return;}applyState(stateDefinitionFor(node,'_mouse_leave'),'Leave applied');closeAllTooltips();});
byId('ck3-search').addEventListener('input',function(event){var query=event.target.value.trim().toLowerCase();treeItems.forEach(function(item){item.classList.toggle('is-search-hidden',!!query&&!item.dataset.ck3Search.includes(query));});nodes.forEach(function(node){var haystack=[node.dataset.ck3Kind,node.dataset.ck3Name,node.dataset.ck3Source,node.dataset.ck3LabelRaw,node.dataset.ck3LabelEnglish,node.dataset.ck3LabelSimpChinese,node.dataset.ck3Tooltip,node.dataset.ck3RawTooltip].join(' ').toLowerCase();node.classList.toggle('is-search-muted',!!query&&!haystack.includes(query));});});
byId('ck3-zoom').addEventListener('input',function(event){updateZoom(event.target.value);});
byId('ck3-language').addEventListener('change',function(event){applyLanguage(event.target.value);});
byId('ck3-fit').addEventListener('click',fit);
byId('ck3-visual-mode').addEventListener('change',function(event){root.classList.toggle('visual-mode',event.target.checked);});
byId('ck3-show-approx').addEventListener('change',function(event){root.classList.toggle('show-approx',event.target.checked);});
byId('ck3-show-labels').addEventListener('change',function(event){root.classList.toggle('show-labels',event.target.checked);});
runtimeFactControls.forEach(function(control){control.addEventListener(control.tagName==='SELECT'?'change':'input',applyRuntimePlans);});
byId('ck3-sim-visible').addEventListener('change',function(event){if(!selected){return;}var expression=selected.dataset.ck3Visible;matching(expression,'ck3Visible').forEach(function(node){node.dataset.simVisible=String(event.target.checked);node.classList.toggle('is-sim-hidden',!event.target.checked);node.setAttribute('aria-hidden',String(!event.target.checked));});reflowDynamicLayouts();});
byId('ck3-sim-enabled').addEventListener('change',function(event){if(!selected){return;}var expression=selected.dataset.ck3Enabled;matching(expression,'ck3Enabled').forEach(function(node){node.dataset.simEnabled=String(event.target.checked);node.classList.toggle('is-sim-disabled',!event.target.checked);node.disabled=!event.target.checked;});});
byId('ck3-sim-down').addEventListener('change',function(event){if(!selected){return;}var expression=selected.dataset.ck3Down;matching(expression,'ck3Down').forEach(function(node){node.dataset.simDown=String(event.target.checked);syncPressed(node);});});
byId('ck3-sim-selected-state').addEventListener('change',function(event){if(!selected){return;}var expression=selected.dataset.ck3Selected;matching(expression,'ck3Selected').forEach(function(node){node.dataset.simSelected=String(event.target.checked);syncPressed(node);});});
byId('ck3-sim-checked').addEventListener('change',function(event){if(!selected){return;}var expression=selected.dataset.ck3Checked;matching(expression,'ck3Checked').forEach(function(node){node.dataset.simChecked=String(event.target.checked);syncChecked(node);});});
byId('ck3-sim-text').addEventListener('input',function(event){if(!selected){return;}var expression=selected.dataset.ck3RawText||selected.dataset.ck3OriginalLabel;matching(expression,selected.dataset.ck3RawText?'ck3RawText':'ck3OriginalLabel').forEach(function(node){node.dataset.simText=event.target.value;var caption=node.querySelector('.ck3-caption');if(caption){caption.textContent=event.target.value;}});reflowDynamicLayouts();});
byId('ck3-sim-state').addEventListener('input',function(event){if(!selected){return;}var expression=selected.dataset.ck3State;matching(expression,'ck3State').forEach(function(node){node.dataset.simState=event.target.value;node.dataset.simStateSource='manual';});text('ck3-status','Simulated state for #'+selected.dataset.ck3Index+': '+event.target.value);});
byId('ck3-apply-state').addEventListener('click',function(){if(selected){applyState(selected,'Applied');}});
byId('ck3-sim-click').addEventListener('click',function(){if(!selected||!(selected.dataset.ck3OnClick||selected.dataset.ck3OnClicks)){return;}if(applyRuntimeAction(selected,'left')){return;}text('ck3-action-log','Simulated click'+modelRowLabel(selected)+' · '+(selected.dataset.ck3OnClicks||selected.dataset.ck3OnClick));text('ck3-status','Click recorded for #'+selected.dataset.ck3Index+modelRowLabel(selected)+'; no game effect was executed.');});
byId('ck3-sim-right-click').addEventListener('click',function(){if(!selected||!selected.dataset.ck3OnRightClick){return;}if(applyRuntimeAction(selected,'right')){return;}text('ck3-action-log','Simulated right-click'+modelRowLabel(selected)+' · '+selected.dataset.ck3OnRightClick);text('ck3-status','Right-click recorded for #'+selected.dataset.ck3Index+modelRowLabel(selected)+'; no game effect was executed.');});
function resetNode(node){node.classList.remove('is-sim-hidden','is-sim-disabled','is-sim-down','is-sim-selected-state','is-sim-checked');node.disabled=false;node.removeAttribute('aria-hidden');node.removeAttribute('aria-pressed');node.style.removeProperty('opacity');node.style.removeProperty('--ck3-state-scale');node.style.removeProperty('--ck3-state-offset-x');node.style.removeProperty('--ck3-state-offset-y');node.style.removeProperty('--ck3-rotate-uv');node.style.removeProperty('transition');delete node.dataset.simState;delete node.dataset.simStateSource;if(node.dataset.ck3InitialSimVisible!==undefined){node.dataset.simVisible=node.dataset.ck3InitialSimVisible;var visible=node.dataset.simVisible!=='false';node.classList.toggle('is-sim-hidden',!visible);node.setAttribute('aria-hidden',String(!visible));}else{delete node.dataset.simVisible;}if(node.dataset.ck3InitialSimEnabled!==undefined){node.dataset.simEnabled=node.dataset.ck3InitialSimEnabled;var enabled=node.dataset.simEnabled!=='false';node.classList.toggle('is-sim-disabled',!enabled);node.disabled=!enabled;node.setAttribute('aria-disabled',String(!enabled));}else{delete node.dataset.simEnabled;node.removeAttribute('aria-disabled');}if(node.dataset.ck3InitialSimDown!==undefined){node.dataset.simDown=node.dataset.ck3InitialSimDown;}else{delete node.dataset.simDown;}if(node.dataset.ck3InitialSimSelected!==undefined){node.dataset.simSelected=node.dataset.ck3InitialSimSelected;}else{delete node.dataset.simSelected;}if(node.dataset.ck3InitialSimChecked!==undefined){node.dataset.simChecked=node.dataset.ck3InitialSimChecked;}else{delete node.dataset.simChecked;}if(node.dataset.ck3InitialSimStateTrigger!==undefined){node.dataset.simStateTrigger=node.dataset.ck3InitialSimStateTrigger;}else{delete node.dataset.simStateTrigger;}if(node.dataset.ck3InitialSimAllowOutside!==undefined){node.dataset.simAllowOutside=node.dataset.ck3InitialSimAllowOutside;}else{delete node.dataset.simAllowOutside;}if(node.dataset.ck3InitialSimTooltipVisible!==undefined){node.dataset.simTooltipVisible=node.dataset.ck3InitialSimTooltipVisible;}else{delete node.dataset.simTooltipVisible;}syncPressed(node);syncChecked(node);['min','max','value','animatedProgressValue','alpha','scale','rotateUv','minWidth','maxWidth','minHeight','maxHeight','marginLeft','marginRight','marginTop','marginBottom','statePositionX','statePositionY'].forEach(function(property){var title=runtimePropertyTitle(property);var initial=node.dataset['ck3InitialSim'+title];setRuntimeNumber(node,property,initial!==undefined?Number(initial):NaN);});refreshRuntimeProgress(node);refreshRuntimeAlpha(node);refreshRuntimeScale(node);refreshRuntimeRotateUV(node);if(node.dataset.ck3InitialSimText!==undefined){node.dataset.simText=node.dataset.ck3InitialSimText;}else{delete node.dataset.simText;}var caption=node.querySelector('.ck3-caption');if(caption){caption.textContent=node.dataset.simText!==undefined?node.dataset.simText:labelFor(node);}}
byId('ck3-reset-node').addEventListener('click',function(){if(selected){var target=selected.dataset.ck3StateName?nodeByIndex(selected.dataset.ck3Parent):selected;resetNode(target||selected);reflowDynamicLayouts();selectNode(selected.dataset.ck3Index);text('ck3-action-log','Selected node reset.');}});
byId('ck3-reset-all').addEventListener('click',function(){nodes.forEach(function(node){resetNode(node);if(node.dataset.ck3ScrollViewport==='true'){node.dataset.ck3ScrollOffset='0';}});resetRuntimeFacts();byId('ck3-search').value='';treeItems.forEach(function(item){item.classList.remove('is-search-hidden');});nodes.forEach(function(node){node.classList.remove('is-search-muted');});text('ck3-action-log','Simulation, expression facts, and scroll positions reset.');if(selected){selectNode(selected.dataset.ck3Index);}});
var panning=false;var startX=0;var startY=0;var scrollX=0;var scrollY=0;
viewport.addEventListener('pointerdown',function(event){if(event.target.closest('[data-ck3-stage-node]')||event.target.closest('[data-ck3-scroll-control]')){return;}panning=true;startX=event.clientX;startY=event.clientY;scrollX=viewport.scrollLeft;scrollY=viewport.scrollTop;viewport.classList.add('is-panning');viewport.setPointerCapture(event.pointerId);});
viewport.addEventListener('pointermove',function(event){if(!panning){return;}viewport.scrollLeft=scrollX-(event.clientX-startX);viewport.scrollTop=scrollY-(event.clientY-startY);});
viewport.addEventListener('pointerup',function(event){panning=false;viewport.classList.remove('is-panning');if(viewport.hasPointerCapture(event.pointerId)){viewport.releasePointerCapture(event.pointerId);}});
root.classList.add('show-approx','visual-mode');nodes.forEach(initializeScenario);applyRuntimePlans();byId('ck3-language').value=currentLanguage;applyLanguage(currentLanguage);updateZoom(100);if(nodes.length){selectNode(nodes[0].dataset.ck3Index);}setTimeout(fit,0);
})();`
