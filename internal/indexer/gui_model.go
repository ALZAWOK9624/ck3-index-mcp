package indexer

import (
	"strings"

	"ck3-index/internal/script"
)

// GUIModel is a renderer/editor-neutral representation of a CK3 Jomini GUI
// file. It deliberately keeps source spans and unknown properties so a future
// preview can render known fields without destroying syntax it does not know.
type GUIModel struct {
	Namespaces  []GUINamespace      `json:"namespaces,omitempty"`
	Templates   []GUITemplate       `json:"templates,omitempty"`
	Roots       []GUIElement        `json:"roots,omitempty"`
	ParseErrors []script.ParseError `json:"parse_errors,omitempty"`
}

type GUITemplate struct {
	Name    string     `json:"name"`
	Local   bool       `json:"local,omitempty"`
	Element GUIElement `json:"element"`
	Span    SourceSpan `json:"span"`
}

type GUINamespace struct {
	Name  string     `json:"name"`
	Types []GUIType  `json:"types,omitempty"`
	Span  SourceSpan `json:"span"`
}

type GUIType struct {
	Name      string     `json:"name"`
	Base      string     `json:"base"`
	Namespace string     `json:"namespace,omitempty"`
	Element   GUIElement `json:"element"`
	Span      SourceSpan `json:"span"`
}

type GUIElement struct {
	Source     string             `json:"source,omitempty"`
	Kind       string             `json:"kind"`
	TypeChain  []string           `json:"type_chain,omitempty"`
	Name       string             `json:"name,omitempty"`
	Template   string             `json:"template,omitempty"`
	Slot       string             `json:"slot,omitempty"`
	Override   bool               `json:"override,omitempty"`
	Position   *GUIVector         `json:"position,omitempty"`
	Size       *GUIVector         `json:"size,omitempty"`
	Texture    string             `json:"texture,omitempty"`
	Properties []GUIProperty      `json:"properties,omitempty"`
	Children   []GUIElement       `json:"children,omitempty"`
	Linked     []GUILinkedElement `json:"linked,omitempty"`
	Span       SourceSpan         `json:"span"`
	resolved   bool
	incomplete bool
	modelRow   *guiModelRowMarker
}

// GUILinkedElement is a non-layout GUI subtree referenced through a typed
// property such as tooltipwidget = epidemic_tooltip. Keeping it separate from
// Children prevents tooltip content from being rendered as ordinary layout.
type GUILinkedElement struct {
	Property string     `json:"property"`
	Target   string     `json:"target"`
	Element  GUIElement `json:"element"`
}

type GUIProperty struct {
	Name     string     `json:"name"`
	Operator string     `json:"operator,omitempty"`
	Value    string     `json:"value,omitempty"`
	Values   []string   `json:"values,omitempty"`
	Span     SourceSpan `json:"span"`
}

type GUIVector struct {
	X       string `json:"x,omitempty"`
	Y       string `json:"y,omitempty"`
	Width   string `json:"width,omitempty"`
	Height  string `json:"height,omitempty"`
	Percent bool   `json:"percent,omitempty"`
}

type SourceSpan struct {
	Line    int `json:"line"`
	Column  int `json:"column"`
	EndLine int `json:"end_line"`
	EndCol  int `json:"end_column"`
}

var guiVectorProperties = map[string]bool{
	"position": true, "size": true, "minsize": true, "maxsize": true,
	"minimumsize": true, "maximumsize": true,
	"margin": true, "margins": true, "padding": true, "expand": true,
	"spriteborder": true, "framesize": true, "color": true, "tintcolor": true, "fonttintcolor": true,
}

// BuildGUIModel parses a CK3 GUI file into a stable tree suitable for a layer
// panel, preview renderer, or controlled source write-back.
func BuildGUIModel(content string) GUIModel {
	parsed := script.ParseGUI(content)
	model := GUIModel{ParseErrors: parsed.Errors}
	for _, node := range parsed.Nodes {
		if node.Kind == "block" && (node.Operator == "template" || node.Operator == "local_template") {
			element := guiElementFromNode(node)
			element.Kind = "template"
			model.Templates = append(model.Templates, GUITemplate{
				Name: node.Key, Local: node.Operator == "local_template", Element: element, Span: sourceSpan(node),
			})
			continue
		}
		if node.Kind == "block" && node.Key == "types" && node.Operator == "namespace" {
			ns := GUINamespace{Name: node.Value, Span: sourceSpan(node)}
			for _, child := range node.Children {
				if child.Kind != "block" || child.Operator != "type" {
					continue
				}
				element := guiElementFromNode(child)
				element.Kind = child.Value
				ns.Types = append(ns.Types, GUIType{
					Name: child.Key, Base: child.Value, Namespace: node.Value,
					Element: element, Span: sourceSpan(child),
				})
			}
			model.Namespaces = append(model.Namespaces, ns)
			continue
		}
		if node.Kind == "block" {
			model.Roots = append(model.Roots, guiElementFromNode(node))
		}
	}
	return model
}

func guiElementFromNode(node *script.Node) GUIElement {
	element := GUIElement{Kind: node.Key, Span: sourceSpan(node)}
	if node.Operator == "=" && node.Value != "" {
		element.Template = node.Value
	}
	if node.Operator == "slot" {
		element.Slot = node.Value
		element.Override = node.Key == "blockoverride"
	}
	for _, child := range node.Children {
		if child.Kind == "block" && !isGUIPropertyBlock(child) {
			element.Children = append(element.Children, guiElementFromNode(child))
			continue
		}
		property := guiPropertyFromNode(child)
		element.Properties = append(element.Properties, property)
		switch strings.ToLower(property.Name) {
		case "name":
			element.Name = property.Value
		case "texture":
			element.Texture = property.Value
		case "position":
			v := guiVector(property)
			element.Position = &v
		case "size":
			v := guiVector(property)
			element.Size = &v
		}
	}
	return element
}

func isGUIPropertyBlock(node *script.Node) bool {
	return guiVectorProperties[strings.ToLower(node.Key)]
}

func guiPropertyFromNode(node *script.Node) GUIProperty {
	property := GUIProperty{
		Name: node.Key, Operator: node.Operator, Value: node.Value, Span: sourceSpan(node),
	}
	if node.Kind == "bare" {
		property.Value = node.Key
	}
	if node.Kind == "block" {
		for _, child := range node.Children {
			if child.Kind == "bare" {
				property.Values = append(property.Values, child.Key)
				continue
			}
			if child.Value != "" {
				property.Values = append(property.Values, child.Key+child.Operator+child.Value)
			}
		}
	}
	return property
}

func guiVector(property GUIProperty) GUIVector {
	values := property.Values
	vector := GUIVector{}
	if len(values) > 0 {
		vector.X = values[0]
		vector.Width = values[0]
	}
	if len(values) > 1 {
		vector.Y = values[1]
		vector.Height = values[1]
	}
	for _, value := range values {
		if strings.Contains(value, "%") {
			vector.Percent = true
		}
		if key, value, ok := strings.Cut(value, "="); ok {
			switch strings.ToLower(key) {
			case "x":
				vector.X = value
			case "y":
				vector.Y = value
			case "width":
				vector.Width = value
			case "height":
				vector.Height = value
			}
		}
	}
	return vector
}

func sourceSpan(node *script.Node) SourceSpan {
	return SourceSpan{Line: node.Line, Column: node.Col, EndLine: node.EndLine, EndCol: node.EndCol}
}
