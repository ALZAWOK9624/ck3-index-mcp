package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectRebaseOverlayFilesPreservesAllRegularProjectFiles(t *testing.T) {
	root := t.TempDir()
	for rel, value := range map[string]string{
		"README.md":                                  "project notes\n",
		".git/HEAD":                                  "ref: refs/heads/main\n",
		"tools/build.ps1":                            "Write-Output build\n",
		"source/provinces.psd":                       "source-image",
		"common/k10_custom.txt":                      "k10_value = yes\n",
		"cache/index.bin":                            "keep",
		"tmp/session.txt":                            "keep",
		"backups/previous.txt":                       "keep",
		".map-editor-backups/previous.txt":           "keep",
		"rebase-transactions/stale/transaction.json": "keep",
		".ck3-rebase-build-stale/partial.txt":        "keep",
		"notes.tmp":                                  "keep",
	} {
		writeText(t, filepath.Join(root, filepath.FromSlash(rel)), value)
	}

	files, excluded, err := collectRebaseOverlayFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	actual := fileMap(files)
	for _, rel := range []string{
		"README.md", ".git/HEAD", "tools/build.ps1", "source/provinces.psd", "common/k10_custom.txt",
		"cache/index.bin", "tmp/session.txt", "backups/previous.txt", ".map-editor-backups/previous.txt",
		"rebase-transactions/stale/transaction.json", ".ck3-rebase-build-stale/partial.txt", "notes.tmp",
	} {
		if actual[strings.ToLower(rel)] == nil {
			t.Fatalf("pass-through file was omitted from full-tree inventory: %s", rel)
		}
	}
	if len(excluded) != 0 {
		t.Fatalf("full-tree inventory silently excluded project files: %v", excluded)
	}
}

func TestCollectRebaseOverlayFilesRejectsSymbolicLinks(t *testing.T) {
	root := t.TempDir()
	writeText(t, filepath.Join(root, "README.md"), "target\n")
	link := filepath.Join(root, "tools", "linked-readme.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "README.md"), link); err != nil {
		// Windows without Developer Mode or the symlink privilege cannot create
		// a test link. The collector still rejects one when it is present.
		t.Skipf("symbolic links are unavailable in this test environment: %v", err)
	}
	if _, _, err := collectRebaseOverlayFiles(root); err == nil || !strings.Contains(err.Error(), "symbolic links are not supported") {
		t.Fatalf("full-tree inventory accepted symbolic link: %v", err)
	}
}

func TestBuildRebasePreservesPassThroughProjectFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")
	writeText(t, filepath.Join(project, "README.md"), "project notes\n")
	writeText(t, filepath.Join(project, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeText(t, filepath.Join(project, "tools", "build.ps1"), "Write-Output build\n")
	writeText(t, filepath.Join(project, "source", "preview.psd"), "source-image")
	writeText(t, filepath.Join(project, "tmp", "session.txt"), "still project-owned\n")

	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA256(transaction.ProjectTreeFingerprint) {
		t.Fatalf("plan did not record a full-tree project fingerprint: %+v", transaction)
	}
	transaction, err = BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"README.md":          "project notes\n",
		".git/HEAD":          "ref: refs/heads/main\n",
		"tools/build.ps1":    "Write-Output build\n",
		"source/preview.psd": "source-image",
		"tmp/session.txt":    "still project-owned\n",
	} {
		got, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("pass-through output %s = %q err=%v, want %q", rel, got, err, want)
		}
	}
	if transaction.Validation.CopySHA256 == "" {
		t.Fatal("build did not fingerprint complete migration output")
	}
}

func TestRebasePromotionRetainsPassThroughProjectFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")
	for rel, value := range map[string]string{
		"README.md":                         "project notes\n",
		".git/HEAD":                         "ref: refs/heads/main\n",
		"tools/build.ps1":                   "Write-Output build\n",
		"source/provinces.psd":              "source-image",
		"backups/map-copy.txt":              "backup material\n",
		".map-editor-backups/heightmap.txt": "editor backup\n",
	} {
		writeText(t, filepath.Join(project, filepath.FromSlash(rel)), value)
	}

	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = ValidateRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApproveRebaseSmoke(ctx, cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	transaction, err = PromoteRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"README.md":                         "project notes\n",
		".git/HEAD":                         "ref: refs/heads/main\n",
		"tools/build.ps1":                   "Write-Output build\n",
		"source/provinces.psd":              "source-image",
		"backups/map-copy.txt":              "backup material\n",
		".map-editor-backups/heightmap.txt": "editor backup\n",
	} {
		got, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("promoted pass-through %s = %q err=%v, want %q", rel, got, err, want)
		}
	}
	if transaction.Promotion == nil || transaction.Promotion.PreviousSHA256 != transaction.ProjectTreeFingerprint {
		t.Fatalf("promotion receipt did not record full-tree prior-project hash: %+v", transaction.Promotion)
	}
	transaction, err = RollbackRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"README.md":                         "project notes\n",
		".git/HEAD":                         "ref: refs/heads/main\n",
		"tools/build.ps1":                   "Write-Output build\n",
		"source/provinces.psd":              "source-image",
		"backups/map-copy.txt":              "backup material\n",
		".map-editor-backups/heightmap.txt": "editor backup\n",
	} {
		got, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("rolled-back pass-through %s = %q err=%v, want %q", rel, got, err, want)
		}
	}
}

func TestRebaseValidationBlocksPassThroughDrift(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")
	writeText(t, filepath.Join(project, "tools", "build.ps1"), "before\n")
	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(project, "tools", "build.ps1"), "after\n")
	if _, err := ValidateRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err == nil || !strings.Contains(err.Error(), "project full tree changed") {
		t.Fatalf("pass-through project drift did not block validation: %v", err)
	}
}

func TestRebaseValidationBlocksPassThroughOutputMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")
	writeText(t, filepath.Join(project, "README.md"), "before\n")
	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(output, "README.md"), "changed outside transaction\n")
	if _, err := ValidateRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err == nil || !strings.Contains(err.Error(), "migration copy changed after build") {
		t.Fatalf("pass-through output mutation did not block validation: %v", err)
	}
}

func TestRebasePromoteBlocksPassThroughProjectDrift(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")
	writeText(t, filepath.Join(project, "source", "source.txt"), "before\n")
	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = ValidateRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApproveRebaseSmoke(ctx, cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(project, "source", "source.txt"), "after\n")
	if _, err := PromoteRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err == nil || !strings.Contains(err.Error(), "project full tree changed") {
		t.Fatalf("pass-through project drift did not block promotion: %v", err)
	}
	if _, err := os.Stat(project); err != nil {
		t.Fatalf("promotion drift check damaged formal project: %v", err)
	}
}

func TestVerifyRebaseOverlayRequiresExactPassThroughFiles(t *testing.T) {
	root := t.TempDir()
	writeText(t, filepath.Join(root, "README.md"), "changed\n")
	passThrough := []SnapshotFile{{Path: "README.md", SHA256: hashBytes([]byte("original\n")), Size: int64(len("original\n")), Text: true}}
	if _, err := verifyRebaseOverlay(root, nil, passThrough); err == nil || !strings.Contains(err.Error(), "pass-through") {
		t.Fatalf("pass-through verification accepted changed file: %v", err)
	}
}
