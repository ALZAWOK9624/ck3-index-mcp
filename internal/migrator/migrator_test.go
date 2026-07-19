package migrator

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ck3-index/internal/indexer"
)

func TestMergeTextNonOverlappingAndConflict(t *testing.T) {
	oldText := "a = 1\nanchor = yes\nb = 1\n"
	project := "a = 2\nanchor = yes\nb = 1\n"
	target := "a = 1\nanchor = yes\nb = 2\n"
	merged, conflict := mergeText("common/test.txt", oldText, project, target, nil)
	if conflict != nil || merged != "a = 2\nanchor = yes\nb = 2\n" {
		t.Fatalf("unexpected merge: %q conflict=%+v", merged, conflict)
	}
	_, conflict = mergeText("common/test.txt", "a = 1\n", "a = 2\n", "a = 3\n", nil)
	if conflict == nil || !strings.HasPrefix(conflict.ID, "migration-conflict-") {
		t.Fatalf("expected stable conflict, got %+v", conflict)
	}
}

func TestNormalizeRelRejectsTraversalAndReservedNames(t *testing.T) {
	for _, value := range []string{"../escape.txt", "C:/absolute.txt", "common/CON.txt", "common/name. ", "common/a:b.txt"} {
		if _, err := normalizeRel(value); err == nil {
			t.Fatalf("unsafe path was accepted: %q", value)
		}
	}
	if got, err := normalizeRel("common/test/file.txt"); err != nil || got != "common/test/file.txt" {
		t.Fatalf("safe path rejected: %q err=%v", got, err)
	}
}

func TestResultForExistingPropagatesHashFailure(t *testing.T) {
	root := t.TempDir()
	if _, err := resultForExisting(root, "common/missing.txt", "target", "target_unchanged", 0); err == nil {
		t.Fatal("missing migration output was recorded without a hash error")
	}
	path := filepath.Join(root, "common", "present.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("value = yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := resultForExisting(root, "common/present.txt", "target", "target_unchanged", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.SHA256 == "" || result.Size != int64(len("value = yes\n")) {
		t.Fatalf("unexpected existing-file result: %+v", result)
	}
}

func TestSnapshotManifestRejectsTamperingAndRevalidatesBaseline(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	for _, dir := range []string{project, base} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMapFixture(t, base, []int{1, 1, 1, 1}, map[int]color.RGBA{1: {10, 0, 0, 255}})
	writeText(t, filepath.Join(base, "common/test.txt"), "value = old\n")
	writeText(t, filepath.Join(project, "common/test.txt"), "value = project\n")
	cfg := indexer.Config{
		MigrationSnapshotRoot: filepath.Join(root, "snapshots"),
		Sources: []indexer.Source{
			{Name: "project", Path: project, Rank: 1},
			{Name: "base", Path: base, Rank: 2},
		},
	}
	snapshot, err := CreateSnapshot(context.Background(), cfg, SnapshotSpec{Project: "project", Base: "base"})
	if err != nil {
		t.Fatal(err)
	}
	snapshotRoot := filepath.Join(cfg.MigrationSnapshotRoot, snapshot.SnapshotID)
	manifestPath := filepath.Join(snapshotRoot, "snapshot.json")
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(manifestPath, original, 0o600) })

	var baseline SnapshotManifest
	if err := json.Unmarshal(original, &baseline); err != nil {
		t.Fatal(err)
	}
	blobIndex := -1
	for i, file := range baseline.BaseFiles {
		if file.Blob != "" {
			blobIndex = i
			break
		}
	}
	if blobIndex < 0 {
		t.Fatal("fixture did not persist an overlapping text baseline")
	}
	writeManifest := func(t *testing.T, manifest SnapshotManifest) {
		t.Helper()
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restoreManifest := func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(manifestPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("blob traversal", func(t *testing.T) {
		tampered := baseline
		tampered.BaseFiles = append([]SnapshotFile(nil), baseline.BaseFiles...)
		tampered.BaseFiles[blobIndex].Blob = "../../outside"
		writeManifest(t, tampered)
		if _, err := loadSnapshotManifest(snapshotRoot); err == nil {
			t.Fatal("snapshot manifest accepted a traversing blob name")
		}
		restoreManifest(t)
	})
	t.Run("inventory traversal", func(t *testing.T) {
		tampered := baseline
		tampered.ProjectFiles = append([]SnapshotFile(nil), baseline.ProjectFiles...)
		tampered.ProjectFiles[0].Path = "../outside.txt"
		writeManifest(t, tampered)
		if _, err := loadSnapshotManifest(snapshotRoot); err == nil {
			t.Fatal("snapshot manifest accepted a traversing inventory path")
		}
		restoreManifest(t)
	})
	t.Run("content identity", func(t *testing.T) {
		tampered := baseline
		tampered.Project = "different-project"
		writeManifest(t, tampered)
		if _, err := loadSnapshotManifest(snapshotRoot); err == nil || !strings.Contains(err.Error(), "content identity") {
			t.Fatalf("snapshot manifest content identity was not enforced: %v", err)
		}
		restoreManifest(t)
	})
	t.Run("blob hash metadata", func(t *testing.T) {
		tampered := baseline
		tampered.BaseFiles = append([]SnapshotFile(nil), baseline.BaseFiles...)
		tampered.BaseFiles[blobIndex].Blob = strings.Repeat("a", 64)
		writeManifest(t, tampered)
		if _, err := loadSnapshotManifest(snapshotRoot); err == nil {
			t.Fatal("snapshot manifest accepted a blob name different from its SHA-256")
		}
		restoreManifest(t)
	})

	restoreManifest(t)
	loaded, err := loadSnapshotManifest(snapshotRoot)
	if err != nil {
		t.Fatal(err)
	}
	file := loaded.BaseFiles[blobIndex]
	blobPath := filepath.Join(snapshotRoot, "blobs", file.Blob)
	blobData, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), blobData...)
	corrupt[0] ^= 0xff
	if err := os.WriteFile(blobPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshotBaseline(snapshotRoot, file); err == nil {
		t.Fatal("baseline read accepted same-size content with the wrong SHA-256")
	}
	if _, err := loadSnapshotManifest(snapshotRoot); err == nil {
		t.Fatal("snapshot load accepted a corrupted stored blob")
	}
	if err := os.WriteFile(blobPath, blobData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSnapshotManifest(snapshotRoot); err != nil {
		t.Fatalf("restored snapshot did not validate: %v", err)
	}
}

func TestSnapshotIDUsesPreBlobManifestIdentity(t *testing.T) {
	hash := strings.Repeat("a", 64)
	manifest := SnapshotManifest{
		SchemaVersion: SchemaVersion,
		Project:       "project",
		Base:          "base",
		BaseFiles: []SnapshotFile{{
			Path: "common/test.txt", SHA256: hash, Size: 1, Text: true,
		}},
	}
	before, err := snapshotIDForManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.BaseFiles[0].Blob = hash
	afterBlob, err := snapshotIDForManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if afterBlob != before {
		t.Fatalf("persisting a baseline blob changed the compatible snapshot id: %s != %s", afterBlob, before)
	}
	manifest.Project = "changed-project"
	afterContent, err := snapshotIDForManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if afterContent == before {
		t.Fatal("non-blob snapshot content did not affect the snapshot id")
	}
}

func TestCleanupMigrationArtifactsRemovesOnlyExpiredOwnedEntries(t *testing.T) {
	root := t.TempDir()
	expired := filepath.Join(root, "ck3-map-migration-expired")
	expiredStage := filepath.Join(root, ".ck3-migration-stage-expired")
	recent := filepath.Join(root, "ck3-map-migration-recent")
	unrelated := filepath.Join(root, "keep-me")
	for _, path := range []string{expired, expiredStage, recent, unrelated} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{expired, expiredStage, unrelated} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupMigrationArtifacts(root, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{expired, expiredStage} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired owned entry was not removed: %s err=%v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{recent, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("entry should have been preserved: %s err=%v", filepath.Base(path), err)
		}
	}
	if err := cleanupMigrationArtifacts(filepath.Join(root, "missing"), time.Hour); err == nil {
		t.Fatal("missing artifact root was silently ignored")
	}
}

func TestAutomaticPolicyRequiresKnownWaterTypeAndRejectsComplexEdges(t *testing.T) {
	mapping := indexer.MapProvinceMappingResult{
		Source: indexer.MapMappingSnapshot{WaterClassificationKnown: false}, Target: indexer.MapMappingSnapshot{WaterClassificationKnown: false},
		Sources: []indexer.MapProvinceMappingSource{{ProvinceID: 1, Coverage: 1, Classification: "renumbered", Candidates: []indexer.MapProvinceMappingCandidate{{ProvinceID: 10, Confidence: 1, TargetShare: 1}}}},
	}
	policy, err := buildPolicy(mapping, nil, map[int]bool{}, map[int]bool{}, map[int]bool{10: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, conflict := policy.targetsFor(1, "scalar", "test.txt", 1); conflict == nil {
		t.Fatal("automatic scalar mapping was accepted without known water classification")
	}
	mapping.Source.WaterClassificationKnown, mapping.Target.WaterClassificationKnown = true, true
	mapping.Sources[0].Classification = "split"
	mapping.Sources[0].Candidates = []indexer.MapProvinceMappingCandidate{{ProvinceID: 10, Confidence: 1, TargetShare: 1, Relation: "complex"}, {ProvinceID: 11, Confidence: 1, TargetShare: 1, Relation: "complex"}}
	policy, err = buildPolicy(mapping, nil, map[int]bool{}, map[int]bool{}, map[int]bool{10: true, 11: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, conflict := policy.targetsFor(1, "collection", "test.txt", 1); conflict == nil || !strings.Contains(conflict.Message, "complex") {
		t.Fatalf("complex mapping was not blocked: %+v", conflict)
	}
}

func TestRewriteSemanticRespectsCommentsStringsAndScalarSafety(t *testing.T) {
	policy := &migrationPolicy{decisions: map[int]mapDecision{
		1: {Source: 1, Targets: []int{10}, SafeScalar: true, SafeCollection: true},
		2: {Source: 2, Targets: []int{20, 21}, Kind: "split", SafeCollection: true},
	}, resolutions: map[string]Resolution{}, bySource: map[int]Resolution{}, targetIDs: map[int]bool{10: true, 20: true, 21: true}, sourceWater: map[int]bool{}, targetWater: map[int]bool{}}
	landed := "b_test = {\n  province = 1\n  # province = 1\n  name = \"province:1\"\n  capital = province:1\n  other = province:2\n}\n"
	result := rewriteSemantic("common/landed_titles/test.txt", []byte(landed), policy)
	if !strings.Contains(result.Content, "province = 10") || !strings.Contains(result.Content, "# province = 1") || !strings.Contains(result.Content, `"province:1"`) || !strings.Contains(result.Content, "capital = province:10") {
		t.Fatalf("semantic rewrite damaged protected text:\n%s", result.Content)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].SourceProvince != 2 {
		t.Fatalf("expected scalar split conflict, got %+v", result.Conflicts)
	}
	policy.bySource[1] = Resolution{SourceProvince: 1, Action: "drop"}
	droppedScalar := rewriteSemantic("common/landed_titles/test.txt", []byte("b_test = { province = 1 }\n"), policy)
	delete(policy.bySource, 1)
	if len(droppedScalar.Conflicts) != 1 || droppedScalar.Conflicts[0].Code != "scalar_drop_requires_review" || !strings.Contains(droppedScalar.Content, "province = 1") {
		t.Fatalf("scalar drop silently created an invalid owner: %+v", droppedScalar)
	}
	collection := rewriteSemantic("common/geographical_region/test.txt", []byte("x = { provinces = { 1 2 # keep 2\n } }\n"), policy)
	if !strings.Contains(collection.Content, "10 20 21") || !strings.Contains(collection.Content, "# keep 2") || len(collection.Conflicts) != 0 {
		t.Fatalf("unexpected collection rewrite: %q conflicts=%+v", collection.Content, collection.Conflicts)
	}
	oneLine := rewriteSemantic("history/provinces/compact.txt", []byte("1 = { culture = a } 2 = { culture = b }\n"), policy)
	if !strings.Contains(oneLine.Content, "10 = { culture = a }") || !strings.Contains(oneLine.Content, "20 = { culture = b }") || !strings.Contains(oneLine.Content, "21 = { culture = b }") {
		t.Fatalf("compact top-level records were over-captured: %q", oneLine.Content)
	}
	uncertain := rewriteSemantic("common/scripted_effects/test.txt", []byte("x = { capital_province_id = 1 weight = 1 }\n"), policy)
	if uncertain.Content != "x = { capital_province_id = 1 weight = 1 }\n" || len(uncertain.Diagnostics) != 1 || uncertain.Diagnostics[0].Code != "uncertain_province_number" {
		t.Fatalf("uncertain province field was not reported without rewriting: %+v", uncertain)
	}
	localization := rewriteSemantic("localization/english/test_l_english.yml", []byte(" key:0 province:1\n"), policy)
	if localization.Content != " key:0 province:1\n" || localization.Replacements != 0 {
		t.Fatalf("localization text was treated as executable province syntax: %+v", localization)
	}
}

func TestSnapshotAndMigrationBuildCompleteFork(t *testing.T) {
	root := t.TempDir()
	project, base, target := filepath.Join(root, "project"), filepath.Join(root, "base"), filepath.Join(root, "target")
	for _, dir := range []string{project, base, target} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMapFixture(t, base, []int{1, 1, 2, 2}, map[int]color.RGBA{1: {10, 0, 0, 255}, 2: {20, 0, 0, 255}})
	writeText(t, filepath.Join(base, "history/provinces/test.txt"), "1 = {\n  culture = old\n}\n2 = {\n  culture = old\n}\n")
	writeText(t, filepath.Join(base, "common/landed_titles/test.txt"), "b_one = { province = 1 }\nb_two = { province = 2 }\n")
	writeText(t, filepath.Join(project, "history/provinces/test.txt"), "1 = {\n  culture = custom\n}\n2 = {\n  culture = old\n}\n")
	writeText(t, filepath.Join(project, "descriptor.mod"), "name=\"Project\"\n")

	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts"), MigrationSnapshotRoot: filepath.Join(root, "snapshots"), ArtifactRetentionHours: 168,
		Sources: []indexer.Source{{Name: "project", Path: project, Rank: 1}, {Name: "base", Path: base, Rank: 2}, {Name: "target", Path: target, Rank: 3}}}
	snapshot, err := CreateSnapshot(context.Background(), cfg, SnapshotSpec{Project: "project", Base: "base"})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := CreateSnapshot(context.Background(), cfg, SnapshotSpec{Project: "project", Base: "base"})
	if err != nil || !reused.Reused || reused.SnapshotID != snapshot.SnapshotID {
		t.Fatalf("snapshot was not content-addressed: first=%+v second=%+v err=%v", snapshot, reused, err)
	}

	writeMapFixture(t, target, []int{10, 10, 20, 20}, map[int]color.RGBA{10: {30, 0, 0, 255}, 20: {40, 0, 0, 255}})
	writeText(t, filepath.Join(target, "history/provinces/test.txt"), "10 = {\n  culture = old\n}\n20 = {\n  culture = old\n}\n")
	writeText(t, filepath.Join(target, "common/landed_titles/test.txt"), "b_one = { province = 10 }\nb_two = { province = 20 }\n")
	writeText(t, filepath.Join(target, "common/new_upstream/test.txt"), "new_upstream = yes\n")
	before, _, err := hashPath(filepath.Join(project, "history/provinces/test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildMigration(context.Background(), cfg, MigrationSpec{SnapshotID: snapshot.SnapshotID, Target: "target", OutputName: "fork"}, BuildOptions{ArtifactRoot: cfg.ArtifactRoot})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" {
		t.Fatalf("migration blocked: %+v", result)
	}
	artifact := filepath.Join(cfg.ArtifactRoot, result.ArtifactID, "fork")
	history, err := os.ReadFile(filepath.Join(artifact, "history/provinces/test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(history), "10 = {") || !strings.Contains(string(history), "culture = custom") || strings.Contains(string(history), "1 = {") {
		t.Fatalf("project history was not safely replayed:\n%s", history)
	}
	if _, err := os.Stat(filepath.Join(artifact, "common/new_upstream/test.txt")); err != nil {
		t.Fatalf("new upstream file missing: %v", err)
	}
	after, _, _ := hashPath(filepath.Join(project, "history/provinces/test.txt"))
	if before != after {
		t.Fatal("migration modified original project")
	}
	if _, err := os.Stat(filepath.Join(cfg.ArtifactRoot, result.ArtifactID, "migration-manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSplitScalarBlocksUntilExplicitResolution(t *testing.T) {
	root := t.TempDir()
	project, base, target := filepath.Join(root, "project"), filepath.Join(root, "base"), filepath.Join(root, "target")
	for _, dir := range []string{project, base, target} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMapFixture(t, base, []int{1, 1, 1, 1}, map[int]color.RGBA{1: {10, 0, 0, 255}})
	writeText(t, filepath.Join(base, "common/landed_titles/test.txt"), "b_one = {\n  province = 1\n}\n")
	writeText(t, filepath.Join(project, "common/landed_titles/test.txt"), "b_one = {\n  province = 1\n  color = { 1 2 3 }\n}\n")
	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts"), MigrationSnapshotRoot: filepath.Join(root, "snapshots"), ArtifactRetentionHours: 168,
		Sources: []indexer.Source{{Name: "project", Path: project, Rank: 1}, {Name: "base", Path: base, Rank: 2}, {Name: "target", Path: target, Rank: 3}}}
	snapshot, err := CreateSnapshot(context.Background(), cfg, SnapshotSpec{Project: "project", Base: "base"})
	if err != nil {
		t.Fatal(err)
	}
	writeMapFixture(t, target, []int{10, 11, 10, 11}, map[int]color.RGBA{10: {30, 0, 0, 255}, 11: {40, 0, 0, 255}})
	writeText(t, filepath.Join(target, "common/landed_titles/test.txt"), "b_one = {\n  province = 10\n}\n")
	blocked, err := BuildMigration(context.Background(), cfg, MigrationSpec{SnapshotID: snapshot.SnapshotID, Target: "target", OutputName: "fork"}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != "blocked" || blocked.ConflictCount == 0 {
		t.Fatalf("scalar split unexpectedly published: %+v", blocked)
	}
	artifact := filepath.Join(cfg.ArtifactRoot, blocked.ArtifactID)
	if _, err := os.Stat(filepath.Join(artifact, "fork")); !os.IsNotExist(err) {
		t.Fatalf("blocked migration left a runnable fork: %v", err)
	}
	if _, err := os.Stat(filepath.Join(artifact, "migration-report.json")); err != nil {
		t.Fatal(err)
	}
	resolved, err := BuildMigration(context.Background(), cfg, MigrationSpec{SnapshotID: snapshot.SnapshotID, Target: "target", OutputName: "fork",
		Resolutions: []Resolution{{SourceProvince: 1, Action: "select_target", TargetProvinces: []int{10}}}}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "ready" {
		t.Fatalf("explicit scalar resolution did not unblock migration: %+v", resolved)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ArtifactRoot, resolved.ArtifactID, "fork", "common/landed_titles/test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "province = 10") || !strings.Contains(string(data), "color = { 1 2 3 }") {
		t.Fatalf("resolved project change was not replayed:\n%s", data)
	}
}

func TestSnapshotProjectDriftBlocksWithoutFork(t *testing.T) {
	root := t.TempDir()
	project, base, target := filepath.Join(root, "project"), filepath.Join(root, "base"), filepath.Join(root, "target")
	for _, dir := range []string{project, base, target} {
		writeMapFixture(t, dir, []int{1, 1, 1, 1}, map[int]color.RGBA{1: {10, 0, 0, 255}})
	}
	writeText(t, filepath.Join(project, "descriptor.mod"), "name=\"Before\"\n")
	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts"), MigrationSnapshotRoot: filepath.Join(root, "snapshots"),
		Sources: []indexer.Source{{Name: "project", Path: project, Rank: 1}, {Name: "base", Path: base, Rank: 2}, {Name: "target", Path: target, Rank: 3}}}
	snapshot, err := CreateSnapshot(context.Background(), cfg, SnapshotSpec{Project: "project", Base: "base"})
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(project, "descriptor.mod"), "name=\"After\"\n")
	result, err := BuildMigration(context.Background(), cfg, MigrationSpec{SnapshotID: snapshot.SnapshotID, Target: "target", OutputName: "fork"}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "blocked" || len(result.Conflicts) == 0 || result.Conflicts[0].Code != "project_changed_after_snapshot" {
		t.Fatalf("project drift was not a strict blocker: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(cfg.ArtifactRoot, result.ArtifactID, "fork")); !os.IsNotExist(err) {
		t.Fatalf("drift blocker left output fork: %v", err)
	}
}

func TestExplicitOutputPublishesOnlyToNewDirectory(t *testing.T) {
	root := t.TempDir()
	project, base, target := filepath.Join(root, "project"), filepath.Join(root, "base"), filepath.Join(root, "target")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMapFixture(t, base, []int{1, 1, 1, 1}, map[int]color.RGBA{1: {10, 0, 0, 255}})
	writeMapFixture(t, target, []int{10, 10, 10, 1}, map[int]color.RGBA{10: {30, 0, 0, 255}, 1: {40, 0, 0, 255}})
	writeText(t, filepath.Join(target, "events/native.txt"), "native.1 = { trigger = { capital = province:1 } }\n")
	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts"), MigrationSnapshotRoot: filepath.Join(root, "snapshots"),
		Sources: []indexer.Source{{Name: "project", Path: project, Rank: 1}, {Name: "base", Path: base, Rank: 2}, {Name: "target", Path: target, Rank: 3}}}
	snapshot, err := CreateSnapshot(context.Background(), cfg, SnapshotSpec{Project: "project", Base: "base"})
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "published-fork")
	result, err := BuildMigration(context.Background(), cfg, MigrationSpec{SnapshotID: snapshot.SnapshotID, Target: "target", OutputName: "fork"}, BuildOptions{OutputDir: output})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" {
		t.Fatalf("explicit output migration blocked: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(output, "map_data", "definition.csv")); err != nil {
		t.Fatalf("published fork incomplete: %v", err)
	}
	native, err := os.ReadFile(filepath.Join(output, "events", "native.txt"))
	if err != nil || !strings.Contains(string(native), "province:1") {
		t.Fatalf("new-upstream native province id was rewritten or lost: %q err=%v", native, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ArtifactRoot, result.ArtifactID, "fork")); !os.IsNotExist(err) {
		t.Fatalf("CLI output was unexpectedly duplicated into artifact: %v", err)
	}
	if _, err := BuildMigration(context.Background(), cfg, MigrationSpec{SnapshotID: snapshot.SnapshotID, Target: "target", OutputName: "fork"}, BuildOptions{OutputDir: output}); err == nil {
		t.Fatal("existing explicit output directory was overwritten")
	}
}

func TestRewritePreservesBOMAndCRLFAndDedupesIdenticalMerge(t *testing.T) {
	policy := &migrationPolicy{decisions: map[int]mapDecision{
		1: {Source: 1, Targets: []int{10}, Kind: "merge", SafeCollection: true},
		2: {Source: 2, Targets: []int{10}, Kind: "merge", SafeCollection: true},
	}, resolutions: map[string]Resolution{}, bySource: map[int]Resolution{}, targetIDs: map[int]bool{10: true}, sourceWater: map[int]bool{}, targetWater: map[int]bool{}}
	input := "\ufeff1 = {\r\n  culture = same\r\n}\r\n2 = {\r\n  culture = same\r\n}\r\n"
	result := rewriteSemantic("history/provinces/test.txt", []byte(input), policy)
	if !strings.HasPrefix(result.Content, "\ufeff") || strings.Contains(strings.ReplaceAll(result.Content, "\r\n", ""), "\n") {
		t.Fatalf("BOM or CRLF was not preserved: %q", result.Content)
	}
	if strings.Count(result.Content, "10 = {") != 1 {
		t.Fatalf("identical merged history was not deduplicated: %q", result.Content)
	}
}

func TestConsolidateIdenticalRecordsPrefersTargetAndKeepsDifferentConflictEvidence(t *testing.T) {
	root := t.TempDir()
	writeText(t, filepath.Join(root, "history/provinces/target.txt"), "10 = { culture = same }\n11 = { culture = target }\n")
	writeText(t, filepath.Join(root, "history/provinces/project.txt"), "10 = { culture = same }\n11 = { culture = project }\n")
	files := []FileResult{
		{Path: "history/provinces/project.txt", Origin: "project", Merge: "project_replayed"},
		{Path: "history/provinces/target.txt", Origin: "target", Merge: "target_native"},
	}
	updated, patches, err := consolidateIdenticalRecords(root, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectData, _ := os.ReadFile(filepath.Join(root, "history/provinces/project.txt"))
	if strings.Contains(string(projectData), "10 =") || !strings.Contains(string(projectData), "11 = { culture = project }") {
		t.Fatalf("identical/different record handling is wrong: %q", projectData)
	}
	if len(patches) != 1 || !strings.Contains(updated[0].Merge, "dedupe") {
		t.Fatalf("consolidation provenance missing: files=%+v patches=%+v", updated, patches)
	}
}

func writeMapFixture(t *testing.T, root string, pixels []int, colors map[int]color.RGBA) {
	t.Helper()
	if len(pixels) != 4 {
		t.Fatal("fixture needs four pixels")
	}
	if err := os.MkdirAll(filepath.Join(root, "map_data"), 0o755); err != nil {
		t.Fatal(err)
	}
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	definition := "0;0;0;0;x;x\n"
	seen := map[int]bool{}
	for x, id := range pixels {
		imageData.SetRGBA(x%2, x/2, colors[id])
		if !seen[id] {
			seen[id] = true
			c := colors[id]
			definition += strconv.Itoa(id) + ";" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B)) + ";x;x\n"
		}
	}
	file, err := os.Create(filepath.Join(root, "map_data", "provinces.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, imageData); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(root, "map_data", "definition.csv"), definition)
	writeText(t, filepath.Join(root, "map_data", "default.map"), "definitions = \"definition.csv\"\nprovinces = \"provinces.png\"\nsea_zones = LIST { }\nlakes = LIST { }\n")
}

func writeText(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
