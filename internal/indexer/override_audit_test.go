package indexer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditOverrideDriftClassifiesSemanticFormatAndAmbiguousChanges(t *testing.T) {
	project := t.TempDir()
	godherja := t.TempDir()
	writeOverrideAuditFixture(t, project, "common/fixture.txt", `alpha = { value = 1 } # source formatting
beta = { value = source }
source_only = { ok = yes }
duplicate = { item = source_one }
duplicate = { item = source_two }
namespace = source_namespace
`)
	writeOverrideAuditFixture(t, godherja, "common/fixture.txt", `# baseline formatting
alpha = {
	value = 1
}
beta = { value = base }
base_only = { ok = yes }
duplicate = { item = base }
namespace = base_namespace
`)
	writeOverrideAuditFixture(t, project, "events/format_only.txt", `namespace = format_fixture
format_fixture.1 = { trigger = { always = yes } }
`)
	writeOverrideAuditFixture(t, godherja, "events/format_only.txt", `# no semantic difference
namespace = format_fixture
format_fixture.1 = {
	trigger = { always = yes }
}
`)
	writeOverrideAuditFixture(t, project, "history/characters/ignored.txt", `1066.1.1 = { employer = 1 }
`)
	writeOverrideAuditFixture(t, godherja, "history/characters/ignored.txt", `1066.1.1 = { employer = 2 }
`)

	report, err := AuditOverrideDrift(context.Background(), Config{Sources: []Source{
		{Name: "project", Path: project, Rank: 1},
		{Name: "godherja", Path: godherja, Rank: 2},
	}}, OverrideDriftAuditOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || report.Counts["source_files"] != 2 || report.Counts["semantic_drift_files"] != 1 || report.Counts["format_only_files"] != 1 {
		t.Fatalf("unexpected summary: %+v", report)
	}
	semantic := overrideDriftFinding(t, report.Findings, "common/fixture.txt")
	if semantic.Classification != "semantic_drift" || semantic.Source != "project" || semantic.Base != "godherja" {
		t.Fatalf("unexpected semantic finding: %+v", semantic)
	}
	changes := map[string]OverrideDriftBlock{}
	for _, change := range semantic.Changes {
		changes[change.Key] = change
	}
	for key, want := range map[string]string{
		"beta":        "semantic_changed",
		"source_only": "source_only_definition",
		"base_only":   "base_only_definition",
		"duplicate":   "ambiguous_definition",
		"namespace":   "semantic_changed",
	} {
		if got := changes[key].Classification; got != want {
			t.Fatalf("change %s = %+v, want %s", key, changes[key], want)
		}
	}
	if _, exists := changes["alpha"]; exists {
		t.Fatalf("comment/whitespace-only alpha was reported as drift: %+v", semantic.Changes)
	}
	if changes["duplicate"].SourceOccurrences != 2 || changes["duplicate"].BaseOccurrences != 1 {
		t.Fatalf("duplicate definition did not remain ambiguous: %+v", changes["duplicate"])
	}
	formatOnly := overrideDriftFinding(t, report.Findings, "events/format_only.txt")
	if formatOnly.Classification != "format_only" || len(formatOnly.Changes) != 0 {
		t.Fatalf("format-only file became semantic drift: %+v", formatOnly)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), filepath.ToSlash(project)) || strings.Contains(string(encoded), filepath.ToSlash(godherja)) {
		t.Fatalf("override audit leaked a physical source path: %s", encoded)
	}
}

func TestAuditOverrideDriftUsesNearestAvailableBaseAndValidatesSourceNames(t *testing.T) {
	project := t.TempDir()
	godherja := t.TempDir()
	game := t.TempDir()
	writeOverrideAuditFixture(t, project, "common/fallback.txt", `fixture = { value = source }
`)
	writeOverrideAuditFixture(t, game, "common/fallback.txt", `fixture = { value = base }
`)
	cfg := Config{Sources: []Source{
		{Name: "project", Path: project, Rank: 1},
		{Name: "godherja", Path: godherja, Rank: 2},
		{Name: "game", Path: game, Rank: 3},
	}}
	report, err := AuditOverrideDrift(context.Background(), cfg, OverrideDriftAuditOptions{PathPrefix: "common/fallback.txt"})
	if err != nil {
		t.Fatal(err)
	}
	finding := overrideDriftFinding(t, report.Findings, "common/fallback.txt")
	if finding.Base != "game" || finding.Classification != "semantic_drift" {
		t.Fatalf("audit did not fall back to the nearest available base file: %+v", finding)
	}
	if _, err := AuditOverrideDrift(context.Background(), cfg, OverrideDriftAuditOptions{Source: "godherja", Base: "project"}); err == nil {
		t.Fatal("higher-precedence base source was accepted")
	}
	if _, err := AuditOverrideDrift(context.Background(), cfg, OverrideDriftAuditOptions{PathPrefix: "../outside"}); err == nil {
		t.Fatal("path traversal prefix was accepted")
	}
}

func writeOverrideAuditFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func overrideDriftFinding(t *testing.T, findings []OverrideDriftFile, rel string) OverrideDriftFile {
	t.Helper()
	for _, finding := range findings {
		if finding.Path == rel {
			return finding
		}
	}
	t.Fatalf("missing %s in %+v", rel, findings)
	return OverrideDriftFile{}
}
