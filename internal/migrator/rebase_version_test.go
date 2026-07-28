package migrator

import (
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/indexer"
)

func TestRebaseGameVersionGateSeparatesSameAndCrossVersionModes(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sources := []indexer.Source{
		{Name: "project", Path: project},
		{Name: "base", Path: base},
		{Name: "target", Path: target},
	}
	same, err := resolveRebaseGameVersionGate(RebaseProfile{
		MigrationMode: "same_game_version", BaseGameVersion: "1.19.*", TargetGameVersion: "1.19.0.6",
	}, sources[0], sources[1], sources[2])
	if err != nil {
		t.Fatal(err)
	}
	if same.Status != "compatible" || !same.SemanticMergeAllowed || same.CompatibilityAdapter != "ck3-1.19" {
		t.Fatalf("same-version gate = %+v", same)
	}

	cross, err := resolveRebaseGameVersionGate(RebaseProfile{
		MigrationMode: "cross_game_version", BaseGameVersion: "1.18.*", TargetGameVersion: "1.19.*",
	}, sources[0], sources[1], sources[2])
	if err != nil {
		t.Fatal(err)
	}
	if cross.Status != "hash_and_assets_only" || cross.SemanticMergeAllowed || cross.BlockingCode != "cross_version_semantic_adapter_required" || !cross.CoordinateDeltaAllowed {
		t.Fatalf("cross-version gate = %+v", cross)
	}

	unsupported, err := resolveRebaseGameVersionGate(RebaseProfile{
		MigrationMode: "same_game_version", BaseGameVersion: "1.20.*", TargetGameVersion: "1.20.*",
	}, sources[0], sources[1], sources[2])
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.BlockingCode != "target_game_version_unsupported" || unsupported.SemanticMergeAllowed {
		t.Fatalf("unsupported target gate = %+v", unsupported)
	}
}

func TestRebaseGameVersionGateChecksDescriptorEvidence(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "descriptor.mod"), "supported_version=\"1.19.*\"\n")
	writeText(t, filepath.Join(base, "descriptor.mod"), "supported_version=\"1.18.*\"\n")
	writeText(t, filepath.Join(target, "descriptor.mod"), "supported_version=\"1.19.*\"\n")
	gate, err := resolveRebaseGameVersionGate(RebaseProfile{
		MigrationMode: "same_game_version", BaseGameVersion: "1.19.*", TargetGameVersion: "1.19.*",
	}, indexer.Source{Path: project}, indexer.Source{Path: base}, indexer.Source{Path: target})
	if err != nil {
		t.Fatal(err)
	}
	if gate.BlockingCode != "game_version_descriptor_mismatch" || gate.DescriptorVersions["base"] != "1.18.*" {
		t.Fatalf("descriptor mismatch gate = %+v", gate)
	}
}

func TestValidateRebaseProfileRequiresModeToMatchVersionFamilies(t *testing.T) {
	profile := RebaseProfile{
		SchemaVersion: RebaseProfileSchemaVersion,
		Name:          "test", Project: "project", Base: "base", Target: "target",
		MigrationMode: "same_game_version", BaseGameVersion: "1.18.*", TargetGameVersion: "1.19.*",
		MapAuthority: "disabled", UnknownPolicy: "block",
	}
	if err := validateRebaseProfile(profile); err == nil {
		t.Fatal("same_game_version accepted different CK3 major.minor families")
	}
	profile.MigrationMode = "cross_game_version"
	if err := validateRebaseProfile(profile); err != nil {
		t.Fatalf("cross_game_version rejected different CK3 families: %v", err)
	}
}
