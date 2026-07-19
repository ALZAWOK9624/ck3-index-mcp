package indexer

import (
	"fmt"
	"sort"
	"strings"
)

const maxGUIExpansionDepth = 128

const (
	guiSummaryPropertyLimit       = 40
	guiSummaryRuntimeHotspotLimit = 20
)

type GUIModelInput struct {
	Path  string   `json:"path"`
	Model GUIModel `json:"model"`
}

type GUIResolution struct {
	Types       []ResolvedGUIType `json:"types,omitempty"`
	Templates   []GUITemplate     `json:"templates,omitempty"`
	Roots       []GUIElement      `json:"roots,omitempty"`
	Diagnostics []GUIDiagnostic   `json:"diagnostics,omitempty"`
}

type GUIResolutionSummary struct {
	Types              int                          `json:"types"`
	Templates          int                          `json:"templates"`
	Roots              int                          `json:"roots"`
	Diagnostics        int                          `json:"diagnostics"`
	DiagnosticsBy      map[string]int               `json:"diagnostics_by_code,omitempty"`
	UnresolvedHotspots []GUIDiagnosticSymbolSummary `json:"unresolved_hotspots,omitempty"`
	Samples            map[string][]GUIDiagnostic   `json:"diagnostic_samples,omitempty"`
	PropertyUsage      []GUIPropertyUsage           `json:"property_usage,omitempty"`
	RuntimeHotspots    []GUIPropertyUsage           `json:"runtime_property_hotspots,omitempty"`
}

type GUIPropertyUsage struct {
	Name        string `json:"name"`
	Count       int    `json:"count"`
	Expressions int    `json:"runtime_expressions,omitempty"`
	Support     string `json:"preview_support"`
}

type GUIDiagnosticSymbolSummary struct {
	Code    string `json:"code"`
	Symbol  string `json:"symbol"`
	Count   int    `json:"count"`
	Sources int    `json:"sources"`
}

type ResolvedGUIType struct {
	Name      string     `json:"name"`
	Base      string     `json:"base"`
	Namespace string     `json:"namespace,omitempty"`
	Source    string     `json:"source,omitempty"`
	Element   GUIElement `json:"element"`
}

type GUIDiagnostic struct {
	Code     string     `json:"code"`
	Severity string     `json:"severity"`
	Message  string     `json:"message"`
	Symbol   string     `json:"symbol,omitempty"`
	Source   string     `json:"source,omitempty"`
	Span     SourceSpan `json:"span"`
}

type guiTypeDefinition struct {
	typeRule GUIType
	source   string
}

type guiTemplateDefinition struct {
	template GUITemplate
	source   string
}

type guiResolver struct {
	types       map[string]guiTypeDefinition
	templates   map[string]guiTemplateDefinition
	resolved    map[string]ResolvedGUIType
	resolvedTpl map[string]GUIElement
	typeState   map[string]int
	templateUse map[string]int
	unresolved  map[string]bool
	diagSeen    map[string]bool
	diagnostics []GUIDiagnostic
}

// ResolveGUIModels expands templates, custom type inheritance, and block
// overrides into renderer-ready trees. Inputs are ordered from low to high
// priority; later definitions replace earlier ones and produce a diagnostic.
func ResolveGUIModels(inputs []GUIModelInput) GUIResolution {
	resolver := &guiResolver{
		types: map[string]guiTypeDefinition{}, templates: map[string]guiTemplateDefinition{},
		resolved: map[string]ResolvedGUIType{}, resolvedTpl: map[string]GUIElement{},
		typeState: map[string]int{}, templateUse: map[string]int{}, unresolved: map[string]bool{}, diagSeen: map[string]bool{},
	}
	var roots []GUIElement
	for _, input := range inputs {
		for _, parseErr := range input.Model.ParseErrors {
			resolver.addDiagnostic(GUIDiagnostic{
				Code: "gui_parse_error", Severity: "error", Source: input.Path,
				Message: parseErr.Message, Span: SourceSpan{Line: parseErr.Line, Column: parseErr.Col, EndLine: parseErr.Line, EndCol: parseErr.Col},
			})
		}
		for _, template := range input.Model.Templates {
			template.Element = cloneGUIElement(template.Element)
			setGUIElementSource(&template.Element, input.Path)
			if previous, exists := resolver.templates[template.Name]; exists {
				resolver.addDiagnostic(GUIDiagnostic{
					Code: "gui_duplicate_template", Severity: "warning", Symbol: template.Name, Source: input.Path,
					Message: fmt.Sprintf("GUI template %q replaces an earlier definition from %s", template.Name, previous.source), Span: template.Span,
				})
			}
			resolver.templates[template.Name] = guiTemplateDefinition{template: template, source: input.Path}
		}
		for _, namespace := range input.Model.Namespaces {
			for _, typeRule := range namespace.Types {
				typeRule.Element = cloneGUIElement(typeRule.Element)
				setGUIElementSource(&typeRule.Element, input.Path)
				if previous, exists := resolver.types[typeRule.Name]; exists {
					resolver.addDiagnostic(GUIDiagnostic{
						Code: "gui_duplicate_type", Severity: "warning", Symbol: typeRule.Name, Source: input.Path,
						Message: fmt.Sprintf("GUI type %q replaces an earlier definition from %s", typeRule.Name, previous.source), Span: typeRule.Span,
					})
				}
				resolver.types[typeRule.Name] = guiTypeDefinition{typeRule: typeRule, source: input.Path}
			}
		}
		for _, root := range input.Model.Roots {
			root = cloneGUIElement(root)
			setGUIElementSource(&root, input.Path)
			roots = append(roots, root)
		}
	}

	typeNames := sortedKeys(resolver.types)
	resolution := GUIResolution{Diagnostics: resolver.diagnostics}
	for _, name := range typeNames {
		resolved, ok := resolver.resolveType(name, nil)
		if ok {
			resolution.Types = append(resolution.Types, resolved)
		}
	}
	templateNames := sortedKeys(resolver.templates)
	for _, name := range templateNames {
		definition := resolver.templates[name]
		template := definition.template
		template.Element = resolver.resolveTemplate(name, nil)
		resolution.Templates = append(resolution.Templates, template)
	}
	for index := range roots {
		roots[index] = resolver.resolveElement(roots[index], nil)
	}
	resolution.Roots = roots
	resolution.Diagnostics = resolver.diagnostics
	return resolution
}

func (resolution GUIResolution) Summary() GUIResolutionSummary {
	summary := GUIResolutionSummary{
		Types: len(resolution.Types), Templates: len(resolution.Templates), Roots: len(resolution.Roots),
		Diagnostics: len(resolution.Diagnostics), DiagnosticsBy: map[string]int{}, Samples: map[string][]GUIDiagnostic{},
	}
	type diagnosticAggregate struct {
		count   int
		sources map[string]struct{}
	}
	hotspots := map[string]*diagnosticAggregate{}
	for _, diagnostic := range resolution.Diagnostics {
		summary.DiagnosticsBy[diagnostic.Code]++
		if len(summary.Samples[diagnostic.Code]) < 3 {
			summary.Samples[diagnostic.Code] = append(summary.Samples[diagnostic.Code], diagnostic)
		}
		if diagnostic.Symbol != "" && (diagnostic.Code == "gui_missing_template" || diagnostic.Code == "gui_unresolved_external_type") {
			key := diagnostic.Code + "\x00" + diagnostic.Symbol
			aggregate := hotspots[key]
			if aggregate == nil {
				aggregate = &diagnosticAggregate{sources: map[string]struct{}{}}
				hotspots[key] = aggregate
			}
			aggregate.count++
			if diagnostic.Source != "" {
				aggregate.sources[diagnostic.Source] = struct{}{}
			}
		}
	}
	for key, aggregate := range hotspots {
		code, symbol, _ := strings.Cut(key, "\x00")
		summary.UnresolvedHotspots = append(summary.UnresolvedHotspots, GUIDiagnosticSymbolSummary{
			Code: code, Symbol: symbol, Count: aggregate.count, Sources: len(aggregate.sources),
		})
	}
	sort.Slice(summary.UnresolvedHotspots, func(i, j int) bool {
		left, right := summary.UnresolvedHotspots[i], summary.UnresolvedHotspots[j]
		if left.Count != right.Count {
			return left.Count > right.Count
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Symbol < right.Symbol
	})
	if len(summary.UnresolvedHotspots) > 32 {
		summary.UnresolvedHotspots = summary.UnresolvedHotspots[:32]
	}
	propertyUsage := map[string]*GUIPropertyUsage{}
	var visitElement func(GUIElement)
	visitElement = func(element GUIElement) {
		for _, property := range element.Properties {
			name := strings.ToLower(strings.TrimSpace(property.Name))
			if name == "" {
				continue
			}
			usage := propertyUsage[name]
			if usage == nil {
				usage = &GUIPropertyUsage{Name: name, Support: guiPreviewPropertySupport(name)}
				propertyUsage[name] = usage
			}
			usage.Count++
			if guiPropertyHasRuntimeExpression(property) {
				usage.Expressions++
			}
		}
		for _, child := range element.Children {
			visitElement(child)
		}
		for _, linked := range element.Linked {
			visitElement(linked.Element)
		}
	}
	for _, typeRule := range resolution.Types {
		visitElement(typeRule.Element)
	}
	for _, template := range resolution.Templates {
		visitElement(template.Element)
	}
	for _, root := range resolution.Roots {
		visitElement(root)
	}
	for _, usage := range propertyUsage {
		summary.PropertyUsage = append(summary.PropertyUsage, *usage)
		if usage.Expressions > 0 && usage.Support != "simulated" {
			summary.RuntimeHotspots = append(summary.RuntimeHotspots, *usage)
		}
	}
	sortGUIPropertyUsage(summary.PropertyUsage)
	sortGUIPropertyUsage(summary.RuntimeHotspots)
	if len(summary.PropertyUsage) > guiSummaryPropertyLimit {
		summary.PropertyUsage = summary.PropertyUsage[:guiSummaryPropertyLimit]
	}
	if len(summary.RuntimeHotspots) > guiSummaryRuntimeHotspotLimit {
		summary.RuntimeHotspots = summary.RuntimeHotspots[:guiSummaryRuntimeHotspotLimit]
	}
	return summary
}

func guiPropertyHasRuntimeExpression(property GUIProperty) bool {
	if strings.Contains(property.Value, "[") || strings.Contains(property.Value, "]") {
		return true
	}
	for _, value := range property.Values {
		if strings.Contains(value, "[") || strings.Contains(value, "]") {
			return true
		}
	}
	return false
}

func guiPreviewPropertySupport(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "visible", "enabled", "down", "selected", "min", "max", "value", "onclick", "text", "tooltip":
		return "simulated"
	case "position", "size", "minsize", "maxsize", "minimumsize", "maximumsize", "parentanchor", "widgetanchor",
		"layoutpolicy_horizontal", "layoutpolicy_vertical", "margin", "margins", "spacing", "expand", "ignoreinvisible",
		"datamodelwrap", "addcolumn", "addrow", "direction", "autoresize", "multiline", "texture", "progresstexture",
		"noprogresstexture", "framesize", "frame", "upframe", "overframe", "downframe", "disableframe", "spriteborder",
		"spritetype", "texture_density", "mirror":
		return "rendered"
	case "datacontext", "datamodel", "using", "state", "tooltipwidget":
		return "preserved"
	default:
		return "unmodeled"
	}
}

func sortGUIPropertyUsage(items []GUIPropertyUsage) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Expressions != items[j].Expressions {
			return items[i].Expressions > items[j].Expressions
		}
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Name < items[j].Name
	})
}

func (resolver *guiResolver) resolveType(name string, stack []string) (ResolvedGUIType, bool) {
	if resolved, ok := resolver.resolved[name]; ok {
		return resolved, true
	}
	definition, ok := resolver.types[name]
	if !ok {
		return ResolvedGUIType{}, false
	}
	if resolver.typeState[name] == 1 {
		resolver.addDiagnostic(GUIDiagnostic{
			Code: "gui_inheritance_cycle", Severity: "error", Symbol: name, Source: definition.source,
			Message: fmt.Sprintf("GUI type inheritance cycle: %s", strings.Join(append(stack, name), " -> ")), Span: definition.typeRule.Span,
		})
		return ResolvedGUIType{}, false
	}
	resolver.typeState[name] = 1
	typeRule := definition.typeRule
	var element GUIElement
	if base, exists := resolver.types[typeRule.Base]; exists {
		resolvedBase, resolved := resolver.resolveType(base.typeRule.Name, append(stack, name))
		if resolved {
			element = cloneGUIElement(resolvedBase.Element)
		} else {
			element.incomplete = true
			resolver.unresolved[name] = true
		}
	} else if guiBuiltinTypes[strings.ToLower(typeRule.Base)] {
		element.Kind = typeRule.Base
	} else {
		element.Kind = typeRule.Base
		element.incomplete = true
		resolver.unresolved[name] = true
		resolver.addDiagnostic(GUIDiagnostic{
			Code: "gui_unresolved_external_type", Severity: "info", Symbol: typeRule.Base, Source: definition.source,
			Message: fmt.Sprintf("GUI type %q inherits unresolved external type %q; provide the defining GUI file for full slot validation", name, typeRule.Base), Span: typeRule.Span,
		})
	}
	overlay := cloneGUIElement(typeRule.Element)
	overlay.Kind = ""
	element = resolver.resolveElementWithBase(overlay, element, name, append(stack, name))
	normalizeGUIElement(&element)
	element.TypeChain = appendGUITypeName(element.TypeChain, name)
	resolved := ResolvedGUIType{
		Name: name, Base: typeRule.Base, Namespace: typeRule.Namespace, Source: definition.source, Element: element,
	}
	resolver.typeState[name] = 2
	resolver.resolved[name] = resolved
	return resolved, true
}

func (resolver *guiResolver) resolveTemplate(name string, stack []string) GUIElement {
	if resolved, ok := resolver.resolvedTpl[name]; ok {
		return cloneGUIElement(resolved)
	}
	definition, ok := resolver.templates[name]
	if !ok {
		return GUIElement{incomplete: true}
	}
	if resolver.templateUse[name] == 1 {
		resolver.addDiagnostic(GUIDiagnostic{
			Code: "gui_template_cycle", Severity: "error", Symbol: name, Source: definition.source,
			Message: fmt.Sprintf("GUI template cycle: %s", strings.Join(append(stack, name), " -> ")), Span: definition.template.Span,
		})
		return GUIElement{}
	}
	resolver.templateUse[name] = 1
	element := cloneGUIElement(definition.template.Element)
	element = resolver.resolveElementWithBase(element, GUIElement{}, name, append(stack, name))
	resolver.templateUse[name] = 2
	resolver.resolvedTpl[name] = cloneGUIElement(element)
	return element
}

func (resolver *guiResolver) resolveElement(element GUIElement, stack []string) GUIElement {
	return resolver.resolveElementWithBase(element, GUIElement{}, element.Kind, stack)
}

func (resolver *guiResolver) resolveElementWithBase(element, inherited GUIElement, owner string, stack []string) GUIElement {
	if element.resolved && !hasGUIElementContent(inherited) {
		return element
	}
	if len(stack) >= maxGUIExpansionDepth {
		resolver.addDiagnostic(GUIDiagnostic{
			Code: "gui_expansion_depth", Severity: "info", Symbol: element.Kind, Source: element.Source,
			Message: fmt.Sprintf("GUI expansion stopped after %d nested custom types/templates", maxGUIExpansionDepth), Span: element.Span,
		})
		element.resolved = true
		element.incomplete = true
		return element
	}
	base := cloneGUIElement(inherited)
	for _, using := range guiUsingValues(element) {
		if definition, exists := resolver.templates[using]; exists {
			base = resolver.mergeElement(base, resolver.resolveTemplate(using, stack), using, definition.source)
		} else {
			element.incomplete = true
			resolver.addDiagnostic(GUIDiagnostic{
				Code: "gui_missing_template", Severity: "info", Symbol: using, Source: element.Source,
				Message: fmt.Sprintf("GUI element uses missing template %q", using), Span: element.Span,
			})
		}
	}
	instanceType := element.Template
	if instanceType == "" {
		if _, exists := resolver.types[element.Kind]; exists {
			instanceType = element.Kind
		}
	}
	if instanceType != "" && guiStackContains(stack, instanceType) {
		resolver.addDiagnostic(GUIDiagnostic{
			Code: "gui_instance_cycle", Severity: "info", Symbol: instanceType, Source: element.Source,
			Message: fmt.Sprintf("GUI instance expansion cycle: %s", strings.Join(append(stack, instanceType), " -> ")), Span: element.Span,
		})
		for index := range element.Children {
			element.Children[index] = resolver.resolveElement(element.Children[index], stack)
		}
		normalizeGUIElement(&element)
		element.resolved = true
		element.incomplete = true
		return element
	}
	if instanceType != "" {
		if resolved, ok := resolver.resolveType(instanceType, stack); ok {
			base = resolver.mergeElement(base, resolved.Element, instanceType, resolved.Source)
		} else {
			// The engine and base GUI can provide custom element types outside
			// the selected file set. Without their slot definitions an override
			// cannot be proven invalid.
			resolver.unresolved[element.Kind] = true
			element.incomplete = true
		}
		stack = append(stack, instanceType)
	} else if isExternalGUIElementKind(element.Kind) {
		resolver.unresolved[element.Kind] = true
		element.incomplete = true
	}
	element = resolver.resolveLinkedGUIElements(element, stack)
	// A sibling blockoverride may target a slot introduced by a custom child
	// type. Expand ordinary children first so the slot search sees that type's
	// inherited/template content. Override replacement bodies are resolved only
	// after they have been inserted.
	for index := range element.Children {
		if !element.Children[index].Override {
			element.Children[index] = resolver.resolveElement(element.Children[index], stack)
		}
	}
	if owner == "" {
		owner = element.Kind
	}
	element = resolver.mergeElement(base, element, owner, "")
	for index := range element.Children {
		element.Children[index] = resolver.resolveElement(element.Children[index], stack)
	}
	normalizeGUIElement(&element)
	element.resolved = true
	return element
}

func hasGUIElementContent(element GUIElement) bool {
	return element.Kind != "" || element.Name != "" || element.Template != "" || element.Slot != "" ||
		len(element.Properties) > 0 || len(element.Children) > 0 || len(element.Linked) > 0 || element.incomplete
}

var guiLinkedTypeProperties = map[string]bool{
	"tooltipwidget": true,
}

// Some engine primitives expose overridable blocks that are not declared in
// any loadable GUI source. Keep this list at the primitive level: project
// evidence can prove that icon has such an internal slot surface, but it
// cannot prove a complete catalog of slot names. This avoids a slot-name
// whitelist while preserving typo diagnostics for ordinary containers.
var guiBuiltinOpaqueSlotTypes = map[string]bool{
	"icon": true,
}

func (resolver *guiResolver) resolveLinkedGUIElements(element GUIElement, stack []string) GUIElement {
	for _, property := range element.Properties {
		if !guiLinkedTypeProperties[strings.ToLower(property.Name)] || strings.TrimSpace(property.Value) == "" {
			continue
		}
		target := strings.TrimSpace(property.Value)
		if guiStackContains(stack, target) {
			element.incomplete = true
			continue
		}
		var linked GUIElement
		if _, exists := resolver.types[target]; exists {
			resolved, ok := resolver.resolveType(target, stack)
			if !ok {
				element.incomplete = true
				continue
			}
			linked = resolved.Element
		} else if _, exists := resolver.templates[target]; exists {
			linked = resolver.resolveTemplate(target, stack)
		} else {
			element.incomplete = true
			continue
		}
		element.Linked = append(element.Linked, GUILinkedElement{
			Property: property.Name, Target: target, Element: cloneGUIElement(linked),
		})
	}
	return element
}

func isExternalGUIElementKind(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" || kind == "template" || kind == "block" || kind == "blockoverride" {
		return false
	}
	return !guiBuiltinTypes[kind]
}

func guiStackContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (resolver *guiResolver) mergeElement(base, overlay GUIElement, owner, source string) GUIElement {
	result := cloneGUIElement(base)
	result.resolved = false
	result.incomplete = base.incomplete || overlay.incomplete
	if result.Kind == "" && overlay.Kind != "template" && overlay.Kind != "blockoverride" {
		result.Kind = overlay.Kind
	}
	if overlay.Name != "" {
		result.Name = overlay.Name
	}
	if overlay.Template != "" {
		result.Template = overlay.Template
	}
	if overlay.Slot != "" {
		result.Slot = overlay.Slot
	}
	if overlay.Override {
		result.Override = true
	}
	if overlay.Span.Line != 0 {
		result.Span = overlay.Span
	}
	if overlay.Source != "" {
		result.Source = overlay.Source
	}
	for _, name := range overlay.TypeChain {
		result.TypeChain = appendGUITypeName(result.TypeChain, name)
	}
	result.Properties = mergeGUIProperties(result.Properties, overlay.Properties)
	result.Linked = mergeGUILinkedElements(result.Linked, overlay.Linked)
	// Slot declarations and custom child instances are order-independent for
	// override lookup. Build the complete ordinary child tree first, then apply
	// every blockoverride, including overrides written before their target.
	for _, child := range overlay.Children {
		if child.Override {
			continue
		}
		result.Children = append(result.Children, cloneGUIElement(child))
	}
	for _, child := range overlay.Children {
		if !child.Override {
			continue
		}
		found := replaceGUIBlockSlot(&result, child)
		if !found {
			if resolver.unresolved[owner] || hasIncompleteGUIElement(result) || guiBuiltinOpaqueSlotTypes[strings.ToLower(result.Kind)] {
				result.Children = append(result.Children, cloneGUIElement(child))
				continue
			}
			diagnosticSource := child.Source
			if diagnosticSource == "" {
				diagnosticSource = source
			}
			resolver.addDiagnostic(GUIDiagnostic{
				Code: "gui_unknown_blockoverride", Severity: "warning", Symbol: child.Slot, Source: diagnosticSource,
				Message: fmt.Sprintf("GUI %q overrides unknown block slot %q", owner, child.Slot), Span: child.Span,
			})
		}
	}
	normalizeGUIElement(&result)
	return result
}

func hasIncompleteGUIElement(element GUIElement) bool {
	if element.incomplete {
		return true
	}
	for _, child := range element.Children {
		if hasIncompleteGUIElement(child) {
			return true
		}
	}
	for _, linked := range element.Linked {
		if hasIncompleteGUIElement(linked.Element) {
			return true
		}
	}
	return false
}

func replaceGUIBlockSlot(element *GUIElement, override GUIElement) bool {
	for index := range element.Children {
		if element.Children[index].Slot == override.Slot && !element.Children[index].Override {
			replacement := cloneGUIElement(override)
			replacement.Kind = element.Children[index].Kind
			replacement.Override = false
			// A CK3 block slot is an insertion point inside its containing
			// element. Direct properties in blockoverride therefore modify the
			// containing button/widget, while nested elements replace the
			// structural block body. Keeping the properties on the synthetic
			// block would make layout skip real texture/click/down semantics.
			element.Properties = mergeGUIProperties(element.Properties, replacement.Properties)
			replacement.Properties = nil
			normalizeGUIElement(element)
			normalizeGUIElement(&replacement)
			element.Children[index] = replacement
			// The slot may sit inside a previously resolved inherited/template
			// subtree. Invalidate every ancestor on the replacement path so
			// the post-merge pass descends into the newly inserted body and
			// expands its custom child types instead of returning early.
			element.resolved = false
			return true
		}
		if replaceGUIBlockSlot(&element.Children[index], override) {
			element.resolved = false
			return true
		}
	}
	for index := range element.Linked {
		if replaceGUIBlockSlot(&element.Linked[index].Element, override) {
			element.resolved = false
			return true
		}
	}
	return false
}

func mergeGUILinkedElements(base, overlay []GUILinkedElement) []GUILinkedElement {
	result := cloneGUILinkedElements(base)
	for _, linked := range overlay {
		replaced := false
		for index := range result {
			if result[index].Property == linked.Property {
				result[index] = GUILinkedElement{Property: linked.Property, Target: linked.Target, Element: cloneGUIElement(linked.Element)}
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, GUILinkedElement{Property: linked.Property, Target: linked.Target, Element: cloneGUIElement(linked.Element)})
		}
	}
	return result
}

func (resolver *guiResolver) addDiagnostic(diagnostic GUIDiagnostic) {
	fingerprint := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", diagnostic.Code, diagnostic.Symbol, diagnostic.Source, diagnostic.Span.Line, diagnostic.Span.Column)
	if resolver.diagSeen[fingerprint] {
		return
	}
	resolver.diagSeen[fingerprint] = true
	resolver.diagnostics = append(resolver.diagnostics, diagnostic)
}

func mergeGUIProperties(base, overlay []GUIProperty) []GUIProperty {
	result := append([]GUIProperty(nil), base...)
	repeatableReplaced := map[string]bool{}
	for _, property := range overlay {
		name := strings.ToLower(property.Name)
		if name == "using" {
			result = append(result, property)
			continue
		}
		if name == "onclick" {
			if !repeatableReplaced[name] {
				filtered := result[:0]
				for _, existing := range result {
					if !strings.EqualFold(existing.Name, property.Name) {
						filtered = append(filtered, existing)
					}
				}
				result = filtered
				repeatableReplaced[name] = true
			}
			result = append(result, property)
			continue
		}
		replaced := false
		for index := len(result) - 1; index >= 0; index-- {
			if strings.EqualFold(result[index].Name, property.Name) {
				result[index] = property
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, property)
		}
	}
	return result
}

func guiUsingValues(element GUIElement) []string {
	var values []string
	for _, property := range element.Properties {
		if property.Name == "using" && property.Value != "" {
			values = append(values, property.Value)
		}
	}
	return values
}

func normalizeGUIElement(element *GUIElement) {
	element.Name, element.Texture = "", ""
	element.Position, element.Size = nil, nil
	for _, property := range element.Properties {
		switch strings.ToLower(property.Name) {
		case "name":
			element.Name = property.Value
		case "texture":
			element.Texture = property.Value
		case "position":
			value := guiVector(property)
			element.Position = &value
		case "size":
			value := guiVector(property)
			element.Size = &value
		}
	}
}

func cloneGUIElement(element GUIElement) GUIElement {
	clone := element
	clone.TypeChain = append([]string(nil), element.TypeChain...)
	clone.Properties = append([]GUIProperty(nil), element.Properties...)
	clone.Children = make([]GUIElement, len(element.Children))
	for index := range element.Children {
		clone.Children[index] = cloneGUIElement(element.Children[index])
	}
	clone.Linked = cloneGUILinkedElements(element.Linked)
	if element.Position != nil {
		value := *element.Position
		clone.Position = &value
	}
	if element.Size != nil {
		value := *element.Size
		clone.Size = &value
	}
	return clone
}

func appendGUITypeName(values []string, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return values
	}
	for _, value := range values {
		if strings.EqualFold(value, name) {
			return values
		}
	}
	return append(values, name)
}

func cloneGUILinkedElements(values []GUILinkedElement) []GUILinkedElement {
	cloned := make([]GUILinkedElement, len(values))
	for index := range values {
		cloned[index] = GUILinkedElement{
			Property: values[index].Property, Target: values[index].Target, Element: cloneGUIElement(values[index].Element),
		}
	}
	return cloned
}

func setGUIElementSource(element *GUIElement, source string) {
	element.Source = source
	for index := range element.Children {
		setGUIElementSource(&element.Children[index], source)
	}
	for index := range element.Linked {
		setGUIElementSource(&element.Linked[index].Element, source)
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
