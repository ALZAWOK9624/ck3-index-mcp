package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProperty(description string, enum ...string) map[string]any {
	property := map[string]any{"type": "string"}
	if description != "" {
		property["description"] = description
	}
	if len(enum) > 0 {
		property["enum"] = enum
	}
	return property
}

func integerProperty(description string, minimum, maximum, defaultValue int) map[string]any {
	property := map[string]any{"type": "integer"}
	if description != "" {
		property["description"] = description
	}
	if minimum != 0 {
		property["minimum"] = minimum
	}
	if maximum != 0 {
		property["maximum"] = maximum
	}
	if defaultValue != 0 {
		property["default"] = defaultValue
	}
	return property
}

func booleanProperty(description string) map[string]any {
	property := map[string]any{"type": "boolean"}
	if description != "" {
		property["description"] = description
	}
	return property
}

func arrayProperty(description string, items map[string]any) map[string]any {
	property := map[string]any{"type": "array", "items": items}
	if description != "" {
		property["description"] = description
	}
	return property
}

func visibilityProperty() map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"private", "public"},
		"default":     "private",
		"description": "Use public to return only configured non-private source evidence; aggregate counts and operations requiring private workspace evidence are unavailable.",
	}
}

func limitProperty() map[string]any {
	return integerProperty("Maximum evidence items per section.", 1, 20, 8)
}

func pageProperty() map[string]any {
	return integerProperty("One-based evidence page. Pages are stable only for the published scan generation returned with the tool result.", 1, 25, 1)
}

func genericOutputSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true}
}

func cloneSchema(schema map[string]any) map[string]any {
	data, _ := json.Marshal(schema)
	var cloned map[string]any
	_ = json.Unmarshal(data, &cloned)
	return cloned
}

func validateArguments(raw json.RawMessage, schema map[string]any, compatibilityProperties []string) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return invalidArgument("", "arguments must be a JSON object")
	}
	if fields == nil {
		return invalidArgument("", "arguments must be a JSON object")
	}
	properties, _ := schema["properties"].(map[string]any)
	allowedCompatibility := make(map[string]bool, len(compatibilityProperties))
	for _, name := range compatibilityProperties {
		allowedCompatibility[name] = true
	}
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for name := range fields {
			if _, known := properties[name]; !known && !allowedCompatibility[name] {
				return invalidArgument(name, fmt.Sprintf("unknown argument field %q; remove it or use the documented input schema", name))
			}
		}
	}
	for _, required := range schemaStrings(schema["required"]) {
		value, exists := fields[required]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return missingArgument(required)
		}
	}
	for name, value := range fields {
		property, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		if err := validateProperty(name, value, property); err != nil {
			return invalidArgument(name, err.Error())
		}
	}
	if alternatives, ok := schema["anyOf"].([]any); ok && len(alternatives) > 0 {
		matched := false
		var choices []string
		for _, item := range alternatives {
			alternative, _ := item.(map[string]any)
			required := schemaStrings(alternative["required"])
			choices = append(choices, strings.Join(required, "+"))
			valid := len(required) > 0
			for _, name := range required {
				value, exists := fields[name]
				if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					valid = false
					break
				}
			}
			matched = matched || valid
		}
		if !matched {
			return invalidArgument("", fmt.Sprintf("arguments must include one of: %s", strings.Join(choices, ", ")))
		}
	}
	return nil
}

func validateProperty(name string, raw json.RawMessage, property map[string]any) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("argument field %q is invalid JSON", name)
	}
	return validateDecodedProperty(name, value, property)
}

func validateDecodedProperty(name string, value any, property map[string]any) error {
	if alternatives, ok := property["oneOf"].([]any); ok && len(alternatives) > 0 {
		matched := 0
		for _, alternative := range alternatives {
			candidate, ok := alternative.(map[string]any)
			if ok && validateDecodedProperty(name, value, candidate) == nil {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("argument field %q must match exactly one allowed schema", name)
		}
	}
	if alternatives, ok := property["anyOf"].([]any); ok && len(alternatives) > 0 {
		matched := false
		for _, alternative := range alternatives {
			candidate, ok := alternative.(map[string]any)
			if ok && validateDecodedProperty(name, value, candidate) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("argument field %q must match at least one allowed schema", name)
		}
	}
	wantType, _ := property["type"].(string)
	if !matchesJSONType(value, wantType) {
		return fmt.Errorf("argument field %q must be %s", name, wantType)
	}
	if enum, ok := property["enum"]; ok {
		allowed := schemaValues(enum)
		actual := fmt.Sprint(value)
		matched := false
		for _, candidate := range allowed {
			matched = matched || actual == fmt.Sprint(candidate)
		}
		if !matched {
			return fmt.Errorf("argument field %q received %q; expected one of %v", name, actual, allowed)
		}
	}
	if number, ok := value.(json.Number); ok {
		numeric, err := number.Float64()
		if err != nil || math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return fmt.Errorf("argument field %q must be a finite number", name)
		}
		if minimum, ok := schemaNumber(property["minimum"]); ok && numeric < minimum {
			return fmt.Errorf("argument field %q must be at least %v", name, minimum)
		}
		if maximum, ok := schemaNumber(property["maximum"]); ok && numeric > maximum {
			return fmt.Errorf("argument field %q must be at most %v", name, maximum)
		}
	}
	if text, ok := value.(string); ok {
		length := float64(utf8.RuneCountInString(text))
		if minimum, ok := schemaNumber(property["minLength"]); ok && length < minimum {
			return fmt.Errorf("argument field %q must contain at least %v characters", name, minimum)
		}
		if maximum, ok := schemaNumber(property["maxLength"]); ok && length > maximum {
			return fmt.Errorf("argument field %q must contain at most %v characters", name, maximum)
		}
	}
	if items, ok := value.([]any); ok {
		length := float64(len(items))
		if minimum, ok := schemaNumber(property["minItems"]); ok && length < minimum {
			return fmt.Errorf("argument field %q must contain at least %v items", name, minimum)
		}
		if maximum, ok := schemaNumber(property["maxItems"]); ok && length > maximum {
			return fmt.Errorf("argument field %q must contain at most %v items", name, maximum)
		}
		itemSchema, _ := property["items"].(map[string]any)
		for i, item := range items {
			if err := validateDecodedProperty(fmt.Sprintf("%s[%d]", name, i), item, itemSchema); err != nil {
				return err
			}
		}
	}
	if object, ok := value.(map[string]any); ok {
		length := float64(len(object))
		if maximum, ok := schemaNumber(property["maxProperties"]); ok && length > maximum {
			return fmt.Errorf("argument field %q must contain at most %v properties", name, maximum)
		}
		properties, _ := property["properties"].(map[string]any)
		for _, required := range schemaStrings(property["required"]) {
			if _, exists := object[required]; !exists {
				return fmt.Errorf("argument field %q requires property %q", name, required)
			}
		}
		for field, item := range object {
			if child, exists := properties[field].(map[string]any); exists {
				if err := validateDecodedProperty(name+"."+field, item, child); err != nil {
					return err
				}
				continue
			}
			switch additional := property["additionalProperties"].(type) {
			case bool:
				if !additional {
					return fmt.Errorf("argument field %q contains unknown property %q", name, field)
				}
			case map[string]any:
				if err := validateDecodedProperty(name+"."+field, item, additional); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func matchesJSONType(value any, want string) bool {
	if want == "" {
		return true
	}
	if value == nil {
		return want == "null"
	}
	switch want {
	case "null":
		return false
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return true
	}
}

func schemaStrings(value any) []string {
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func schemaValues(value any) []any {
	switch items := value.(type) {
	case []string:
		result := make([]any, len(items))
		for i := range items {
			result[i] = items[i]
		}
		return result
	case []int:
		result := make([]any, len(items))
		for i := range items {
			result[i] = items[i]
		}
		return result
	case []any:
		return items
	default:
		return nil
	}
}

func schemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func sortedSchemaPropertyNames(schema map[string]any) []string {
	properties, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
