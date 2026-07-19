package indexer

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
)

const (
	GUIScenarioPropertyText    = "text"
	GUIScenarioPropertyTexture = "texture"
	GUIScenarioPropertyVisible = "visible"
	GUIScenarioPropertyEnabled = "enabled"
	GUIScenarioMaxSamples      = 32
	guiScenarioMaxExpression   = 512
	guiScenarioMaxValue        = 512
)

// GUIScenarioSample is a caller-provided example result for one expression.
// It is never treated as an observed game fact and is matched literally.
type GUIScenarioSample struct {
	Property   string `json:"property"`
	Expression string `json:"expression"`
	Value      string `json:"value"`
}

type GUIPreviewScenario struct {
	Source  string                    `json:"source"`
	Applied int                       `json:"applied"`
	Unused  int                       `json:"unused"`
	Samples []GUIScenarioSampleResult `json:"samples,omitempty"`
}

type GUIScenarioSampleResult struct {
	Property     string `json:"property"`
	Expression   string `json:"expression"`
	Value        string `json:"value"`
	MatchedNodes int    `json:"matched_nodes"`
}

type GUINodeScenario struct {
	Source  string  `json:"source"`
	Text    *string `json:"text,omitempty"`
	Texture *string `json:"texture,omitempty"`
	Visible *bool   `json:"visible,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

type guiValidatedScenarioSample struct {
	Property     string
	Expression   string
	Value        string
	BooleanValue bool
}

func applyGUIPreviewScenario(preview *GUIPreviewResult, samples []GUIScenarioSample) error {
	if preview == nil || len(samples) == 0 {
		return nil
	}
	validated, err := validateGUIScenarioSamples(samples, GUIScenarioMaxSamples, "GUI scenario")
	if err != nil {
		return err
	}
	preview.Scenario = &GUIPreviewScenario{Source: "provided"}
	for _, sample := range validated {
		matched := 0
		for nodeIndex := range preview.Nodes {
			node := &preview.Nodes[nodeIndex]
			if !guiScenarioSampleMatches(*node, sample.Property, sample.Expression) {
				continue
			}
			applyValidatedGUIScenarioSample(node, sample)
			matched++
		}
		preview.Scenario.Samples = append(preview.Scenario.Samples, GUIScenarioSampleResult{
			Property: sample.Property, Expression: sample.Expression, Value: sample.Value, MatchedNodes: matched,
		})
		if matched == 0 {
			preview.Scenario.Unused++
		} else {
			preview.Scenario.Applied++
		}
	}
	if preview.Scenario.Unused > 0 {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("%d provided GUI scenario sample(s) did not exactly match a preview expression", preview.Scenario.Unused))
	}
	return nil
}

func validateGUIScenarioSamples(samples []GUIScenarioSample, maximum int, context string) ([]guiValidatedScenarioSample, error) {
	if len(samples) > maximum {
		return nil, fmt.Errorf("%s has %d samples; maximum is %d", context, len(samples), maximum)
	}
	validated := make([]guiValidatedScenarioSample, 0, len(samples))
	seen := map[string]string{}
	for index, sample := range samples {
		property := strings.ToLower(strings.TrimSpace(sample.Property))
		expression := strings.TrimSpace(sample.Expression)
		value := sample.Value
		if property != GUIScenarioPropertyText && property != GUIScenarioPropertyTexture &&
			property != GUIScenarioPropertyVisible && property != GUIScenarioPropertyEnabled {
			return nil, fmt.Errorf("%s sample %d property %q is invalid; expected text, texture, visible, or enabled", context, index, sample.Property)
		}
		if expression == "" {
			return nil, fmt.Errorf("%s sample %d requires an expression", context, index)
		}
		if len([]rune(expression)) > guiScenarioMaxExpression {
			return nil, fmt.Errorf("%s sample %d expression exceeds %d characters", context, index, guiScenarioMaxExpression)
		}
		if len([]rune(value)) > guiScenarioMaxValue {
			return nil, fmt.Errorf("%s sample %d value exceeds %d characters", context, index, guiScenarioMaxValue)
		}
		key := property + "\x00" + expression
		if previous, ok := seen[key]; ok {
			if previous != value {
				return nil, fmt.Errorf("%s has conflicting %s samples for expression %q", context, property, expression)
			}
			return nil, fmt.Errorf("%s repeats the same %s sample for expression %q", context, property, expression)
		}
		seen[key] = value
		if property == GUIScenarioPropertyTexture {
			normalized, err := normalizeGUIScenarioTexture(value)
			if err != nil {
				return nil, fmt.Errorf("%s texture sample %q: %w", context, expression, err)
			}
			value = normalized
		}
		item := guiValidatedScenarioSample{Property: property, Expression: expression, Value: value}
		if property == GUIScenarioPropertyVisible || property == GUIScenarioPropertyEnabled {
			parsed, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(value)))
			if err != nil {
				return nil, fmt.Errorf("%s %s sample %q requires boolean value true or false", context, property, expression)
			}
			item.BooleanValue = parsed
		}
		validated = append(validated, item)
	}
	return validated, nil
}

func applyValidatedGUIScenarioSample(node *GUIPreviewNode, sample guiValidatedScenarioSample) {
	if node.Scenario == nil {
		node.Scenario = &GUINodeScenario{Source: "provided"}
	}
	switch sample.Property {
	case GUIScenarioPropertyText:
		copyValue := sample.Value
		node.Scenario.Text = &copyValue
	case GUIScenarioPropertyTexture:
		copyValue := sample.Value
		node.Scenario.Texture = &copyValue
		node.Texture = copyValue
		node.TextureRef = nil
	case GUIScenarioPropertyVisible:
		copyValue := sample.BooleanValue
		node.Scenario.Visible = &copyValue
	case GUIScenarioPropertyEnabled:
		copyValue := sample.BooleanValue
		node.Scenario.Enabled = &copyValue
	}
}

func guiScenarioSampleMatches(node GUIPreviewNode, property, expression string) bool {
	switch property {
	case GUIScenarioPropertyText:
		if node.Semantics != nil && strings.TrimSpace(node.Semantics.RawText) == expression {
			return true
		}
		if strings.TrimSpace(node.Text) == expression && (strings.Contains(node.Text, "[") || node.TextLocalization != nil) {
			return true
		}
	case GUIScenarioPropertyTexture:
		if node.Semantics != nil && strings.TrimSpace(node.Semantics.RawTexture) == expression {
			return true
		}
		return strings.TrimSpace(node.Texture) == expression && strings.Contains(node.Texture, "[")
	case GUIScenarioPropertyVisible:
		return node.Semantics != nil && strings.TrimSpace(node.Semantics.Visible) == expression
	case GUIScenarioPropertyEnabled:
		return node.Semantics != nil && strings.TrimSpace(node.Semantics.Enabled) == expression
	}
	return false
}

func normalizeGUIScenarioTexture(value string) (string, error) {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" {
		return "", fmt.Errorf("requires a literal indexed resource path")
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("contains control characters")
	}
	if !strings.HasPrefix(strings.ToLower(value), "gfx/") {
		return "", fmt.Errorf("must be a source-root-relative gfx/ path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("contains an invalid path segment")
		}
	}
	switch strings.ToLower(path.Ext(value)) {
	case ".dds", ".png", ".tga":
	default:
		return "", fmt.Errorf("must end in .dds, .png, or .tga")
	}
	return value, nil
}
