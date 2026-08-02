package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

// Response size is a first-class budget: every byte a core tool returns is a
// byte of the caller's context window that cannot be spent on the actual task.
// Nothing measured it before, so a change that doubled a payload was invisible
// until someone noticed the model running out of room. These goldens make the
// cost of the highest-traffic calls explicit and reviewable in a diff.
//
// The numbers are deliberately exact rather than upper bounds. An improvement
// has to be re-recorded, which is the point: the diff is the evidence that the
// work did something.

const responseSizeGolden = "response_size.golden.json"

// responseSizeCase is one representative call against the shared fixture.
type responseSizeCase struct {
	Name      string         `json:"name"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// responseSizeMeasurement records where a response's bytes actually went.
// ContentBytes and StructuredBytes are reported separately because the two
// currently carry the same payload; keeping them apart is what makes that
// duplication visible instead of hiding inside a single total.
type responseSizeMeasurement struct {
	Name            string `json:"name"`
	WireBytes       int    `json:"wire_bytes"`
	ContentBytes    int    `json:"content_bytes"`
	StructuredBytes int    `json:"structured_bytes"`
	EvidenceItems   int    `json:"evidence_items"`
}

func responseSizeCases() []responseSizeCase {
	return []responseSizeCase{
		{Name: "search/prefix", Tool: "ck3_search", Arguments: map[string]any{"query": "size_probe"}},
		{Name: "search/prefix_limit20", Tool: "ck3_search", Arguments: map[string]any{"query": "size_probe", "limit": 20}},
		{Name: "search/exact", Tool: "ck3_search", Arguments: map[string]any{"query": "size_probe_trait_alpha"}},
		{Name: "inspect/aggregate", Tool: "ck3_inspect", Arguments: map[string]any{"id": "size_probe_trait_alpha"}},
		{Name: "inspect/aggregate_limit20", Tool: "ck3_inspect", Arguments: map[string]any{"id": "size_probe_trait_alpha", "limit": 20}},
		{Name: "inspect/references", Tool: "ck3_inspect", Arguments: map[string]any{"id": "size_probe_trait_alpha", "operation": "references", "limit": 20}},
		{Name: "inspect/definition", Tool: "ck3_inspect", Arguments: map[string]any{"id": "size_probe_trait_alpha", "operation": "definition"}},
		{Name: "dependencies/neighborhood", Tool: "ck3_dependencies", Arguments: map[string]any{"id": "size_probe_trait_alpha", "limit": 20, "depth": 2}},
	}
}

func TestCoreToolResponseSizesMatchGolden(t *testing.T) {
	db, cfg := openResponseSizeFixture(t)
	measurements := make([]responseSizeMeasurement, 0, len(responseSizeCases()))
	for _, testCase := range responseSizeCases() {
		result := callToolForTest(t, db, cfg, testCase.Tool, testCase.Arguments)
		if result["isError"] == true {
			t.Fatalf("%s returned a tool error: %+v", testCase.Name, result)
		}
		measurements = append(measurements, measureToolResult(t, testCase.Name, result))
	}
	compareResponseSizeGolden(t, measurements)
}

// TestCoreToolResponseSizesAreDeterministic guards the goldens themselves. A
// payload assembled from Go map iteration would produce a different size per
// run, which would make every future comparison meaningless.
func TestCoreToolResponseSizesAreDeterministic(t *testing.T) {
	db, cfg := openResponseSizeFixture(t)
	for _, testCase := range responseSizeCases() {
		first := measureToolResult(t, testCase.Name, callToolForTest(t, db, cfg, testCase.Tool, testCase.Arguments))
		second := measureToolResult(t, testCase.Name, callToolForTest(t, db, cfg, testCase.Tool, testCase.Arguments))
		if first != second {
			t.Fatalf("%s response size is not reproducible: %+v then %+v", testCase.Name, first, second)
		}
	}
}

func measureToolResult(t *testing.T, name string, result map[string]any) responseSizeMeasurement {
	t.Helper()
	wire, err := json.Marshal(stableResultEnvelope(result))
	if err != nil {
		t.Fatal(err)
	}
	measurement := responseSizeMeasurement{Name: name, WireBytes: len(wire)}
	if blocks, ok := result["content"].([]map[string]any); ok {
		for _, block := range blocks {
			text, _ := block["text"].(string)
			measurement.ContentBytes += len(text)
		}
	}
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		encoded, err := json.Marshal(structured)
		if err != nil {
			t.Fatal(err)
		}
		measurement.StructuredBytes = len(encoded)
		if evidence, ok := structured["evidence"].([]any); ok {
			measurement.EvidenceItems = len(evidence)
		}
	}
	return measurement
}

// stableResultEnvelope removes the one part of a tool result whose encoded
// length is wall-clock dependent. Every response carries indexState, and its
// scan_committed_at is a timestamp whose fractional seconds are formatted with
// trailing zeros trimmed — so an index committed at a moment whose microseconds
// end in zero serializes one byte shorter than one that does not. That shifted
// every wire measurement by a byte at random and would have made the goldens
// flaky roughly one run in eight, for a reason that has nothing to do with
// response size. The field is replaced rather than deleted so the envelope
// still costs what it really costs.
func stableResultEnvelope(result map[string]any) map[string]any {
	stable := make(map[string]any, len(result))
	for key, value := range result {
		stable[key] = value
	}
	indexState, ok := stable["indexState"].(map[string]any)
	if !ok {
		return stable
	}
	normalized := make(map[string]any, len(indexState))
	for key, value := range indexState {
		normalized[key] = value
	}
	if _, exists := normalized["scan_committed_at"]; exists {
		normalized["scan_committed_at"] = "2026-01-01T00:00:00.0000000Z"
	}
	stable["indexState"] = normalized
	return stable
}

func compareResponseSizeGolden(t *testing.T, measurements []responseSizeMeasurement) {
	t.Helper()
	encoded, err := json.MarshalIndent(measurements, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("testdata", responseSizeGolden)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nrecord the current measurements as:\n%s", responseSizeGolden, err, encoded)
	}
	if normalizeGoldenText(string(want)) == normalizeGoldenText(string(encoded)) {
		return
	}
	var previous []responseSizeMeasurement
	if err := json.Unmarshal(want, &previous); err != nil {
		t.Fatalf("%s is not valid JSON: %v", responseSizeGolden, err)
	}
	t.Fatalf("core tool response sizes changed:\n%s\nrecord the new measurements in testdata/%s as:\n%s",
		renderResponseSizeDelta(previous, measurements), responseSizeGolden, encoded)
}

// renderResponseSizeDelta turns a golden mismatch into the actual review
// artifact: which call moved, by how much, and in which direction.
func renderResponseSizeDelta(previous, current []responseSizeMeasurement) string {
	before := make(map[string]responseSizeMeasurement, len(previous))
	for _, measurement := range previous {
		before[measurement.Name] = measurement
	}
	var report strings.Builder
	for _, measurement := range current {
		old, existed := before[measurement.Name]
		if !existed {
			fmt.Fprintf(&report, "  + %-28s wire=%d structured=%d evidence=%d (new)\n",
				measurement.Name, measurement.WireBytes, measurement.StructuredBytes, measurement.EvidenceItems)
			continue
		}
		if old == measurement {
			continue
		}
		fmt.Fprintf(&report, "  ~ %-28s wire %d -> %d (%+d, %+.1f%%)  structured %d -> %d  evidence %d -> %d\n",
			measurement.Name,
			old.WireBytes, measurement.WireBytes,
			measurement.WireBytes-old.WireBytes, percentDelta(old.WireBytes, measurement.WireBytes),
			old.StructuredBytes, measurement.StructuredBytes,
			old.EvidenceItems, measurement.EvidenceItems)
	}
	seen := make(map[string]bool, len(current))
	for _, measurement := range current {
		seen[measurement.Name] = true
	}
	removed := make([]string, 0, len(before))
	for name := range before {
		if !seen[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	for _, name := range removed {
		fmt.Fprintf(&report, "  - %-28s (removed)\n", name)
	}
	return strings.TrimRight(report.String(), "\n")
}

func percentDelta(before, after int) float64 {
	if before == 0 {
		return 0
	}
	return (float64(after) - float64(before)) / float64(before) * 100
}

// normalizeGoldenText compares goldens by content rather than by line-ending
// representation. The working copy is checked out with CRLF, so a byte compare
// against a file Git normalized to LF fails for a reason that has nothing to do
// with the measurements.
func normalizeGoldenText(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n")
}

// openResponseSizeFixture builds a project that reaches every section
// ck3_inspect aggregates: a definition (overridden across two layers), a
// localization value, a resource, incoming references, and outgoing ones. A
// fixture that only had objects would report a size the real tool never
// returns.
func openResponseSizeFixture(t *testing.T) (*indexer.DB, indexer.Config) {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	write := func(root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var traits strings.Builder
	for _, name := range []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"} {
		fmt.Fprintf(&traits, `size_probe_trait_%s = {
	category = personality
	icon = size_probe_trait_%s.dds
	martial = 2
	monthly_prestige = 0.5
	desc = size_probe_trait_%s_desc
}
`, name, name, name)
	}
	write(game, "common/traits/00_size_probe_traits.txt", traits.String())

	// The project overrides one trait so the response carries override
	// evidence, which is the layered case the tool exists to explain.
	write(project, "common/traits/00_size_probe_traits.txt", `size_probe_trait_alpha = {
	category = personality
	icon = size_probe_trait_alpha.dds
	martial = 4
	monthly_prestige = 0.75
	desc = size_probe_trait_alpha_desc
}
`)

	var events strings.Builder
	for index, name := range []string{"alpha", "beta", "gamma", "delta"} {
		fmt.Fprintf(&events, `size_probe.%d = {
	type = character_event
	title = size_probe_event_%s_title
	desc = size_probe_event_%s_desc
	trigger = { has_trait = size_probe_trait_%s }
	immediate = { add_trait = size_probe_trait_%s }
	option = { name = size_probe_event_%s_a remove_trait = size_probe_trait_alpha }
}
`, index+1, name, name, name, name, name)
	}
	write(game, "events/size_probe_events.txt", events.String())

	write(game, "common/scripted_effects/size_probe_effects.txt", `size_probe_grant_alpha = {
	add_trait = size_probe_trait_alpha
	add_prestige = 100
}
size_probe_revoke_alpha = {
	remove_trait = size_probe_trait_alpha
}
`)
	write(game, "common/scripted_triggers/size_probe_triggers.txt", `size_probe_has_alpha = {
	has_trait = size_probe_trait_alpha
}
`)

	var localization strings.Builder
	localization.WriteString("l_english:\n")
	for _, name := range []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"} {
		fmt.Fprintf(&localization, " size_probe_trait_%s:0 \"Size Probe %s\"\n", name, name)
		fmt.Fprintf(&localization, " size_probe_trait_%s_desc:0 \"Description for the %s size probe trait.\"\n", name, name)
	}
	write(game, "localization/english/size_probe_l_english.yml", localization.String())

	for _, name := range []string{"alpha", "beta", "gamma"} {
		write(game, "gfx/interface/icons/traits/size_probe_trait_"+name+".dds", "fixture")
	}

	cfg := indexer.Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []indexer.Source{
			{Name: "project", Path: project, Rank: 1, Role: indexer.SourceRoleProject},
			{Name: "game", Path: game, Rank: 2, Role: indexer.SourceRoleGame},
		},
	}
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, cfg
}
