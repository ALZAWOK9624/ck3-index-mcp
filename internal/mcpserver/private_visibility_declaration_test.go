package mcpserver

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The declared list and the handlers that enforce the constraint have to stay
// equal: a tool that rejects public without saying so puts the caller back to
// discovering it one failed call at a time, and a tool that advertises
// private-only while accepting public loses a legitimate query. Deriving the
// enforcing set from the source keeps the two from drifting apart silently.
func TestPrivateOnlyVisibilityToolsMatchTheHandlers(t *testing.T) {
	enforcing := map[string]bool{}
	handlerPattern := regexp.MustCompile(`(?m)^func (handle[A-Za-z0-9]+)\(`)
	for _, file := range []string{"tools_map.go", "tools_map_edit.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		matches := handlerPattern.FindAllStringSubmatchIndex(source, -1)
		for i, match := range matches {
			end := len(source)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			if !strings.Contains(source[match[0]:end], "requireSourceTrackedMapVisibility(visibility)") {
				continue
			}
			enforcing[source[match[2]:match[3]]] = true
		}
	}
	if len(enforcing) == 0 {
		t.Fatal("found no handler enforcing requireSourceTrackedMapVisibility; the scan is broken, not the list")
	}

	declared := map[string]bool{}
	for name := range privateOnlyVisibilityTools {
		declared[handlerNameForTool(name)] = true
	}
	var missing, extra []string
	for handler := range enforcing {
		if !declared[handler] {
			missing = append(missing, handler)
		}
	}
	for handler := range declared {
		if !enforcing[handler] {
			extra = append(extra, handler)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("handlers reject public visibility but the schema does not declare it: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("schema declares private-only for tools whose handler accepts public: %v", extra)
	}
}

// handlerNameForTool converts map_province_info into handleMapProvinceInfo,
// which is the naming convention every canonical map handler follows.
func handlerNameForTool(tool string) string {
	name := "handle"
	for _, part := range strings.Split(tool, "_") {
		if part == "" {
			continue
		}
		name += strings.ToUpper(part[:1]) + part[1:]
	}
	return name
}

func TestPrivateOnlyVisibilityIsAdvertisedInTheCatalog(t *testing.T) {
	for _, definition := range registry() {
		properties, _ := definition.InputSchema["properties"].(map[string]any)
		visibility, _ := properties["visibility"].(map[string]any)
		if visibility == nil {
			continue
		}
		values := schemaStrings(visibility["enum"])
		privateOnly := len(values) == 1 && values[0] == "private"
		if privateOnlyVisibilityTools[definition.Name] != privateOnly {
			t.Errorf("%s advertises visibility enum %v, want privateOnly=%v", definition.Name, values, privateOnlyVisibilityTools[definition.Name])
		}
	}
}

// A public call must now fail at the schema, before the handler runs, with an
// error that names the only legal value.
func TestPublicVisibilityOnMapToolFailsWithTheAllowedValue(t *testing.T) {
	definition, ok := findCanonicalTool("map_title_context")
	if !ok {
		t.Fatal("map_title_context is not registered")
	}
	err := validateArguments(json.RawMessage(`{"id":"d_dilongon","year":1254,"visibility":"public"}`), definition.InputSchema, definition.CompatibilityProperties)
	if err == nil {
		t.Fatal("public visibility must still be rejected")
	}
	if !strings.Contains(err.Error(), "private") {
		t.Fatalf("rejection %q does not name the allowed value", err)
	}
}
