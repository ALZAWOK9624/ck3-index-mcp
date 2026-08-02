package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The tool catalog is the one payload every single request pays for, before
// the caller has asked for anything. Its cost was invisible: assertCatalogGolden
// marshals the catalog and hashes it, so any change is detected but the size is
// never reported. A hash tells you something moved; it does not tell you the
// surface grew by ten kilobytes.
//
// This golden breaks the catalog down by component so a regression names its
// own cause, and so work aimed at shrinking descriptions can be measured
// against the part it actually targets.

const catalogSizeGolden = "tool_catalog_size.golden.json"

// catalogSizeBreakdown accounts for the serialized tools/list payload.
// Component totals are measured on each tool's own encoding, so they exclude
// the array framing and therefore do not sum exactly to TotalBytes.
type catalogSizeBreakdown struct {
	Tools             int    `json:"tools"`
	TotalBytes        int    `json:"total_bytes"`
	DescriptionBytes  int    `json:"description_bytes"`
	InputSchemaBytes  int    `json:"input_schema_bytes"`
	OutputSchemaBytes int    `json:"output_schema_bytes"`
	NameAndTitleBytes int    `json:"name_and_title_bytes"`
	AnnotationBytes   int    `json:"annotation_bytes"`
	LargestToolName   string `json:"largest_tool_name"`
	LargestToolBytes  int    `json:"largest_tool_bytes"`
}

func TestToolCatalogSizeMatchesGolden(t *testing.T) {
	got := measureToolCatalog(t)
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("testdata", catalogSizeGolden)
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nrecord the current breakdown as:\n%s", catalogSizeGolden, err, encoded)
	}
	if normalizeGoldenText(string(wantBytes)) == normalizeGoldenText(string(encoded)) {
		return
	}
	var want catalogSizeBreakdown
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("%s is not valid JSON: %v", catalogSizeGolden, err)
	}
	t.Fatalf("tool catalog size changed: total %d -> %d (%+d, %+.1f%%), descriptions %d -> %d, input schemas %d -> %d\nrecord the new breakdown in testdata/%s as:\n%s",
		want.TotalBytes, got.TotalBytes, got.TotalBytes-want.TotalBytes, percentDelta(want.TotalBytes, got.TotalBytes),
		want.DescriptionBytes, got.DescriptionBytes,
		want.InputSchemaBytes, got.InputSchemaBytes,
		catalogSizeGolden, encoded)
}

// TestToolCatalogPerToolSizesAreReported is a reporting test, not an
// assertion. Shrinking the catalog requires knowing which tools are expensive,
// and that ranking is tedious to reconstruct by hand every time.
func TestToolCatalogPerToolSizesAreReported(t *testing.T) {
	type toolSize struct {
		name  string
		bytes int
	}
	sizes := make([]toolSize, 0, len(mcpTools()))
	for _, tool := range mcpTools() {
		encoded, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := tool["name"].(string)
		sizes = append(sizes, toolSize{name: name, bytes: len(encoded)})
	}
	sort.Slice(sizes, func(i, j int) bool {
		if sizes[i].bytes != sizes[j].bytes {
			return sizes[i].bytes > sizes[j].bytes
		}
		return sizes[i].name < sizes[j].name
	})
	var report strings.Builder
	report.WriteString("serialized bytes per advertised tool, largest first:\n")
	for _, size := range sizes {
		fmt.Fprintf(&report, "  %6d  %s\n", size.bytes, size.name)
	}
	t.Log(report.String())
}

func measureToolCatalog(t *testing.T) catalogSizeBreakdown {
	t.Helper()
	tools := mcpTools()
	total, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	breakdown := catalogSizeBreakdown{Tools: len(tools), TotalBytes: len(total)}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		title, _ := tool["title"].(string)
		description, _ := tool["description"].(string)
		breakdown.NameAndTitleBytes += len(name) + len(title)
		breakdown.DescriptionBytes += len(description)
		breakdown.InputSchemaBytes += marshalledLen(t, tool["inputSchema"])
		breakdown.OutputSchemaBytes += marshalledLen(t, tool["outputSchema"])
		breakdown.AnnotationBytes += marshalledLen(t, tool["annotations"])
		if size := marshalledLen(t, tool); size > breakdown.LargestToolBytes {
			breakdown.LargestToolBytes = size
			breakdown.LargestToolName = name
		}
	}
	return breakdown
}

func marshalledLen(t *testing.T, value any) int {
	t.Helper()
	if value == nil {
		return 0
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(encoded)
}
