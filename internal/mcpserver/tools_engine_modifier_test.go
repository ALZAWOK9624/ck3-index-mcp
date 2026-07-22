package mcpserver

import (
	"strings"
	"testing"
)

func TestLookupModifierToolKeepsFormatOnlyUseAreasUnknown(t *testing.T) {
	raw, err := lookupModifierTool("afar_opinion")
	if err != nil {
		t.Fatal(err)
	}
	got := raw.(map[string]any)
	if got["found"] != true || got["source"] != "vanilla_modifier_format" {
		t.Fatalf("format-only modifier lookup = %+v", got)
	}
	areas, ok := got["use_areas"].([]string)
	if !ok || len(areas) != 0 {
		t.Fatalf("format-only modifier inherited use areas: %#v", got["use_areas"])
	}
	guidance, ok := got["guidance"].([]string)
	if !ok || len(guidance) != 1 || !strings.Contains(guidance[0], "did not publish a use-area contract") {
		t.Fatalf("format-only modifier guidance = %#v", got["guidance"])
	}
}
