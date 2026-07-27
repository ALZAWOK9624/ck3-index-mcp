package migrator

import (
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/indexer"
)

func TestRebaseNewDiagnosticsUsesFingerprintsNotNetCounts(t *testing.T) {
	baseline := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{{Severity: "error", Fingerprint: "old"}}}
	migrated := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{{Severity: "error", Fingerprint: "new"}}}
	added := rebaseNewDiagnostics(baseline, migrated, "error")
	if len(added) != 1 || added[0].Fingerprint != "new" {
		t.Fatalf("new diagnostic was hidden by a cancelled count delta: %+v", added)
	}
}

func TestRebaseNewDiagnosticsGatesSurplusFingerprintOccurrences(t *testing.T) {
	baseline := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{{
		Severity: "warning", Code: "missing_resource", Fingerprint: "missing_resource:gfx/shared.dds", Occurrences: 1,
	}}}
	migrated := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{{
		Severity: "warning", Code: "missing_resource", Fingerprint: "missing_resource:gfx/shared.dds", Occurrences: 2,
	}}}
	added := rebaseNewDiagnostics(baseline, migrated, "warning")
	if len(added) != 1 || added[0].Occurrences != 1 {
		t.Fatalf("surplus same-fingerprint diagnostic occurrence was hidden: %+v", added)
	}
}

func TestRebaseNewMigrationStackDiagnosticsSeparatesTargetAndBaseline(t *testing.T) {
	oldStack := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{
		{Severity: "error", Code: "parse_error", Fingerprint: "old-stack-error"},
	}}
	targetOnly := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{
		{Severity: "error", Code: "parse_error", Fingerprint: "target-stack-error"},
		{Severity: "warning", Code: "missing_resource", Fingerprint: "target-resource"},
	}}
	migrated := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{
		{Severity: "error", Code: "parse_error", Fingerprint: "old-stack-error"},
		{Severity: "error", Code: "parse_error", Fingerprint: "target-stack-error"},
		{Severity: "warning", Code: "missing_resource", Fingerprint: "target-resource"},
		{Severity: "warning", Code: "missing_resource", Fingerprint: "new-resource"},
		{Severity: "warning", Code: "resource_resolution_uncertain", Confidence: "low", Fingerprint: "uncertain"},
	}}
	added := rebaseNewMigrationStackDiagnostics(oldStack, targetOnly, migrated)
	if len(added) != 1 || added[0].Fingerprint != "new-resource" {
		t.Fatalf("unexpected migration stack gate diagnostics: %+v", added)
	}
}

func TestRebaseNewMigrationStackDiagnosticsGatesSurplusTargetOccurrence(t *testing.T) {
	targetOnly := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{{
		Severity: "warning", Code: "missing_resource", Fingerprint: "missing_resource:gfx/shared.dds", Occurrences: 1,
	}}}
	migrated := indexer.ValidationReport{Diagnostics: []indexer.Diagnostic{{
		Severity: "warning", Code: "missing_resource", Fingerprint: "missing_resource:gfx/shared.dds", Occurrences: 2,
	}}}
	added := rebaseNewMigrationStackDiagnostics(indexer.ValidationReport{}, targetOnly, migrated)
	if len(added) != 1 || added[0].Occurrences != 1 {
		t.Fatalf("surplus target-only diagnostic occurrence was hidden: %+v", added)
	}
}

func TestRebaseValidationSourceFingerprintsDetectDrift(t *testing.T) {
	root := t.TempDir()
	game := filepath.Join(root, "game")
	writeRebaseValidationFile(t, filepath.Join(game, "common", "rules.txt"), []byte("rule = base\n"))
	profile := RebaseProfile{
		Project: "project", Base: "base", Target: "target", ValidationSources: []string{"game"},
	}
	cfg := indexer.Config{Sources: []indexer.Source{{Name: "game", Path: game, Role: indexer.SourceRoleDependency}}}
	fingerprints, err := rebaseValidationSourceFingerprints(cfg, profile)
	if err != nil || len(fingerprints) != 1 || fingerprints["game"] == "" {
		t.Fatalf("validation source snapshot failed: fingerprints=%+v err=%v", fingerprints, err)
	}
	transaction := RebaseTransaction{Profile: profile, ValidationSourceFingerprints: fingerprints}
	if err := rebaseVerifyValidationSourceFingerprints(cfg, transaction); err != nil {
		t.Fatalf("unchanged validation source was rejected: %v", err)
	}
	writeRebaseValidationFile(t, filepath.Join(game, "common", "rules.txt"), []byte("rule = changed\n"))
	if err := rebaseVerifyValidationSourceFingerprints(cfg, transaction); err == nil {
		t.Fatal("changed validation source was accepted")
	}
}

func TestRebaseSourceIdentityFingerprintsRejectSameContentRetarget(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	projectRetarget := filepath.Join(root, "project-retarget")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	for _, directory := range []string{project, projectRetarget, base, target} {
		writeRebaseValidationFile(t, filepath.Join(directory, "common", "same.txt"), []byte("same bytes\n"))
	}
	projectSource := indexer.Source{Name: "project", Path: project}
	baseSource := indexer.Source{Name: "base", Path: base}
	targetSource := indexer.Source{Name: "target", Path: target}
	identities, err := rebaseSourceIdentityFingerprints(projectSource, baseSource, targetSource)
	if err != nil {
		t.Fatal(err)
	}
	transaction := RebaseTransaction{SourceIdentityFingerprints: identities}
	if err := rebaseVerifySourceIdentityFingerprints(transaction, projectSource, baseSource, targetSource); err != nil {
		t.Fatalf("planned source roots were rejected: %v", err)
	}
	if err := rebaseVerifySourceIdentityFingerprints(transaction, indexer.Source{Name: "project", Path: projectRetarget}, baseSource, targetSource); err == nil {
		t.Fatal("byte-identical project retarget was accepted")
	}
}

func TestRebaseMapAuditInheritanceRequiresIdenticalActiveFile(t *testing.T) {
	root := t.TempDir()
	baselineRoot := filepath.Join(root, "baseline")
	migratedRoot := filepath.Join(root, "migrated")
	path := "map_data/rivers.png"
	writeRebaseValidationFile(t, filepath.Join(baselineRoot, filepath.FromSlash(path)), []byte("same-map-bytes"))
	writeRebaseValidationFile(t, filepath.Join(migratedRoot, filepath.FromSlash(path)), []byte("same-map-bytes"))
	baselineCfg := indexer.Config{Sources: []indexer.Source{{Name: "baseline", Path: baselineRoot}}}
	migratedCfg := indexer.Config{Sources: []indexer.Source{{Name: "migrated", Path: migratedRoot}}}
	oldFinding := indexer.MapAssetAuditFinding{Code: "map_rivers_topology", Severity: "warning", Path: path, Source: "baseline", Message: "topology warning", Count: 1}
	newFinding := oldFinding
	newFinding.Source = "migrated"
	added, err := rebaseNewMapAuditFindings(indexer.MapAssetAuditResult{Findings: []indexer.MapAssetAuditFinding{oldFinding}}, indexer.MapAssetAuditResult{Findings: []indexer.MapAssetAuditFinding{newFinding}}, baselineCfg, migratedCfg)
	if err != nil || len(added) != 0 {
		t.Fatalf("identical inherited map warning was not accepted: added=%+v err=%v", added, err)
	}
	writeRebaseValidationFile(t, filepath.Join(migratedRoot, filepath.FromSlash(path)), []byte("changed-map-bytes"))
	added, err = rebaseNewMapAuditFindings(indexer.MapAssetAuditResult{Findings: []indexer.MapAssetAuditFinding{oldFinding}}, indexer.MapAssetAuditResult{Findings: []indexer.MapAssetAuditFinding{newFinding}}, baselineCfg, migratedCfg)
	if err != nil || len(added) != 1 || added[0].Code != newFinding.Code {
		t.Fatalf("changed map file inherited an old warning: added=%+v err=%v", added, err)
	}
}

func TestRebaseMapAuditAcceptsUnchangedTargetOnlyFinding(t *testing.T) {
	root := t.TempDir()
	targetRoot := filepath.Join(root, "target")
	migratedRoot := filepath.Join(root, "migrated")
	path := "map_data/rivers.png"
	writeRebaseValidationFile(t, filepath.Join(targetRoot, filepath.FromSlash(path)), []byte("target-map-warning"))
	writeRebaseValidationFile(t, filepath.Join(migratedRoot, filepath.FromSlash(path)), []byte("target-map-warning"))
	targetCfg := indexer.Config{Sources: []indexer.Source{{Name: "target", Path: targetRoot}}}
	migratedCfg := indexer.Config{Sources: []indexer.Source{{Name: "migrated", Path: migratedRoot}}}
	finding := indexer.MapAssetAuditFinding{Code: "map_rivers_topology", Severity: "warning", Path: path, Source: "target", Message: "existing target topology warning", Count: 1}
	migratedFinding := finding
	migratedFinding.Source = "migrated"
	added, err := rebaseNewMapAuditFindingsWithTarget(
		indexer.MapAssetAuditResult{},
		indexer.MapAssetAuditResult{Findings: []indexer.MapAssetAuditFinding{finding}},
		indexer.MapAssetAuditResult{Findings: []indexer.MapAssetAuditFinding{migratedFinding}},
		indexer.Config{}, targetCfg, migratedCfg,
	)
	if err != nil || len(added) != 0 {
		t.Fatalf("unchanged target-only map finding was treated as migration-added: added=%+v err=%v", added, err)
	}
}

func writeRebaseValidationFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
