package indexer

import (
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"unicode"
)

const (
	GUIScenarioPropertyText             = "text"
	GUIScenarioPropertyTexture          = "texture"
	GUIScenarioPropertyVideo            = "video"
	GUIScenarioPropertyVisible          = "visible"
	GUIScenarioPropertyEnabled          = "enabled"
	GUIScenarioPropertyCoatOfArmsMask   = "coat_of_arms_mask"
	GUIScenarioPropertyCoatOfArmsOffset = "coat_of_arms_offset"
	GUIScenarioPropertyCoatOfArmsScale  = "coat_of_arms_scale"
	GUIScenarioPropertyFrom             = "from"
	GUIScenarioPropertyTo               = "to"
	GUIScenarioMaxSamples               = 32
	guiScenarioMaxExpression            = 512
	guiScenarioMaxValue                 = 512
)

var guiScenarioSamplePropertyNames = []string{
	GUIScenarioPropertyText,
	GUIScenarioPropertyTexture,
	GUIScenarioPropertyVideo,
	GUIScenarioPropertyVisible,
	GUIScenarioPropertyEnabled,
	GUIScenarioPropertyCoatOfArmsMask,
	GUIScenarioPropertyCoatOfArmsOffset,
	GUIScenarioPropertyCoatOfArmsScale,
	GUIScenarioPropertyFrom,
	GUIScenarioPropertyTo,
}

// GUIScenarioSamplePropertyNames returns the exact property names accepted by
// preview and model-row scenario samples. Callers receive a copy so transport
// schemas can share the same contract without mutating indexer state.
func GUIScenarioSamplePropertyNames() []string {
	return append([]string(nil), guiScenarioSamplePropertyNames...)
}

func isGUIScenarioSampleProperty(value string) bool {
	for _, candidate := range guiScenarioSamplePropertyNames {
		if value == candidate {
			return true
		}
	}
	return false
}

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
	Source           string     `json:"source"`
	Text             *string    `json:"text,omitempty"`
	Texture          *string    `json:"texture,omitempty"`
	Video            bool       `json:"video,omitempty"`
	CoatOfArms       bool       `json:"coat_of_arms,omitempty"`
	CoatOfArmsMask   *string    `json:"coat_of_arms_mask,omitempty"`
	CoatOfArmsOffset *GUIVector `json:"coat_of_arms_offset,omitempty"`
	CoatOfArmsScale  *GUIVector `json:"coat_of_arms_scale,omitempty"`
	LineFrom         *GUIVector `json:"from,omitempty"`
	LineTo           *GUIVector `json:"to,omitempty"`
	Visible          *bool      `json:"visible,omitempty"`
	Enabled          *bool      `json:"enabled,omitempty"`
}

type guiValidatedScenarioSample struct {
	Property     string
	Expression   string
	Value        string
	BooleanValue bool
	VectorValue  *GUIVector
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
		if !isGUIScenarioSampleProperty(property) {
			return nil, fmt.Errorf("%s sample %d property %q is invalid; expected one of %s", context, index, sample.Property, strings.Join(guiScenarioSamplePropertyNames, ", "))
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
		if property == GUIScenarioPropertyTexture || property == GUIScenarioPropertyVideo || property == GUIScenarioPropertyCoatOfArmsMask {
			normalized, err := normalizeGUIScenarioTexture(value)
			if err != nil {
				return nil, fmt.Errorf("%s %s sample %q: %w", context, property, expression, err)
			}
			value = normalized
		}
		item := guiValidatedScenarioSample{Property: property, Expression: expression, Value: value}
		if property == GUIScenarioPropertyCoatOfArmsOffset || property == GUIScenarioPropertyCoatOfArmsScale || property == GUIScenarioPropertyFrom || property == GUIScenarioPropertyTo {
			vector, normalized, err := normalizeGUIScenarioVector(value)
			if err != nil {
				return nil, fmt.Errorf("%s %s sample %q: %w", context, property, expression, err)
			}
			item.Value = normalized
			item.VectorValue = &vector
		}
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
		node.Scenario.Video = false
		node.Scenario.CoatOfArms = node.Semantics != nil && strings.TrimSpace(node.Semantics.CoatOfArmsTexture) == sample.Expression
		node.Texture = copyValue
		node.TextureRef = nil
	case GUIScenarioPropertyVideo:
		copyValue := sample.Value
		node.Scenario.Texture = &copyValue
		node.Scenario.Video = true
		node.Scenario.CoatOfArms = false
		node.Texture = copyValue
		node.TextureRef = nil
	case GUIScenarioPropertyCoatOfArmsMask:
		copyValue := sample.Value
		node.Scenario.CoatOfArmsMask = &copyValue
		node.CoatOfArmsMask = copyValue
		node.CoatOfArmsMaskRef = nil
	case GUIScenarioPropertyCoatOfArmsOffset:
		if sample.VectorValue == nil {
			return
		}
		copyValue := *sample.VectorValue
		node.Scenario.CoatOfArmsOffset = &copyValue
		node.CoatOfArmsOffset = &copyValue
	case GUIScenarioPropertyCoatOfArmsScale:
		if sample.VectorValue == nil {
			return
		}
		copyValue := *sample.VectorValue
		node.Scenario.CoatOfArmsScale = &copyValue
		node.CoatOfArmsScale = &copyValue
	case GUIScenarioPropertyFrom:
		if sample.VectorValue == nil {
			return
		}
		copyValue := *sample.VectorValue
		node.Scenario.LineFrom = &copyValue
		guiPreviewSetLineEndpoint(node, true, copyValue)
	case GUIScenarioPropertyTo:
		if sample.VectorValue == nil {
			return
		}
		copyValue := *sample.VectorValue
		node.Scenario.LineTo = &copyValue
		guiPreviewSetLineEndpoint(node, false, copyValue)
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
		if node.Semantics != nil && strings.TrimSpace(node.Semantics.PortraitTexture) == expression {
			return true
		}
		if node.Semantics != nil && strings.TrimSpace(node.Semantics.CoatOfArmsTexture) == expression {
			return true
		}
		return strings.TrimSpace(node.Texture) == expression && strings.Contains(node.Texture, "[")
	case GUIScenarioPropertyVideo:
		return node.Semantics != nil && strings.TrimSpace(node.Semantics.Video) == expression
	case GUIScenarioPropertyCoatOfArmsMask:
		return node.Semantics != nil && strings.TrimSpace(node.Semantics.CoatOfArmsMask) == expression
	case GUIScenarioPropertyCoatOfArmsOffset:
		return node.Semantics != nil && strings.TrimSpace(node.Semantics.CoatOfArmsOffset) == expression
	case GUIScenarioPropertyCoatOfArmsScale:
		return node.Semantics != nil && strings.TrimSpace(node.Semantics.CoatOfArmsScale) == expression
	case GUIScenarioPropertyFrom:
		return node.LineGeometry != nil && node.Semantics != nil && strings.TrimSpace(node.Semantics.LineFrom) == expression
	case GUIScenarioPropertyTo:
		return node.LineGeometry != nil && node.Semantics != nil && strings.TrimSpace(node.Semantics.LineTo) == expression
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

func normalizeGUIScenarioVector(value string) (GUIVector, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return GUIVector{}, "", fmt.Errorf("requires two finite numeric components")
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return GUIVector{}, "", fmt.Errorf("contains control characters")
	}
	parts := strings.Fields(strings.NewReplacer("{", " ", "}", " ", ",", " ").Replace(value))
	if len(parts) != 2 {
		return GUIVector{}, "", fmt.Errorf("requires exactly two numeric components")
	}
	values := [2]float64{}
	for index, part := range parts {
		parsed, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return GUIVector{}, "", fmt.Errorf("component %d is not finite", index+1)
		}
		values[index] = parsed
	}
	x := strconv.FormatFloat(values[0], 'f', -1, 64)
	y := strconv.FormatFloat(values[1], 'f', -1, 64)
	return GUIVector{X: x, Y: y}, "{ " + x + " " + y + " }", nil
}
