package packager

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"ck3-index/internal/indexer"
)

type validatorFunc func(context.Context, []PreparedFile) (ValidationReport, error)

func (f validatorFunc) Validate(ctx context.Context, files []PreparedFile) (ValidationReport, error) {
	return f(ctx, files)
}

func validMetadata() Metadata {
	return Metadata{
		Name: "Apple Test Mod", Slug: "apple_test_mod", Version: "1.0",
		SupportedVersion: "1.19.*", Tags: []string{"Gameplay"}, Kind: "addon",
	}
}

func textInput(path, content string) FileInput {
	return FileInput{Path: path, Content: &content}
}

func binaryInput(path string, data []byte) FileInput {
	encoded := base64.StdEncoding.EncodeToString(data)
	return FileInput{Path: path, ContentBase64: &encoded}
}

func allowAllValidator(warnings int) Validator {
	return validatorFunc(func(_ context.Context, _ []PreparedFile) (ValidationReport, error) {
		return ValidationReport{Warnings: warnings, Summary: "fixture validation passed"}, nil
	})
}

func TestBuildPortableDeterministicPackage(t *testing.T) {
	root := t.TempDir()
	request := Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("common/scripted_triggers/apple_test.txt", "apple_test_trigger = { always = yes }\n"),
		textInput("localization/english/apple_test_l_english.yml", "l_english:\n apple_test_name:0 \"Apple\"\n"),
		binaryInput("gfx/interface/icons/apple_test.dds", []byte{0x44, 0x44, 0x53, 0x20, 1, 2, 3}),
	}}
	options := BuildOptions{ArtifactRoot: root, Retention: 7 * 24 * time.Hour, Limits: MCPLimits, Validator: allowAllValidator(1)}
	first, err := Build(context.Background(), request, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(context.Background(), request, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ready" || first.SHA256 == "" || first.SHA256 != second.SHA256 || first.ArchiveName != second.ArchiveName {
		t.Fatalf("package is not ready and deterministic: first=%+v second=%+v", first, second)
	}
	if strings.Contains(first.ArtifactRelPath, root) || filepath.IsAbs(first.ArtifactRelPath) {
		t.Fatalf("artifact result leaked an absolute path: %q", first.ArtifactRelPath)
	}
	record, owned := ownedArtifactRecord(root, first.ArchiveName)
	if !owned || record.ArtifactID != first.ArtifactID || record.SHA256 != first.SHA256 || record.Size != first.Size {
		t.Fatalf("generated package was not registered for safe cleanup: %+v", record)
	}
	files := readZipFiles(t, filepath.Join(root, first.ArtifactRelPath))
	for _, expected := range []string{
		"INSTALL.txt", "apple_test_mod.mod", "apple_test_mod/descriptor.mod",
		"apple_test_mod/common/scripted_triggers/apple_test.txt",
		"apple_test_mod/localization/english/apple_test_l_english.yml",
		"apple_test_mod/gfx/interface/icons/apple_test.dds", "ck3-package-manifest.json",
	} {
		if _, ok := files[expected]; !ok {
			t.Fatalf("portable archive is missing %s; entries=%v", expected, sortedKeys(files))
		}
	}
	launcher := string(files["apple_test_mod.mod"])
	if !strings.Contains(launcher, `path="mod/apple_test_mod"`) || strings.Contains(string(files["apple_test_mod/descriptor.mod"]), "path=") {
		t.Fatalf("descriptor pair is wrong: launcher=%q internal=%q", launcher, files["apple_test_mod/descriptor.mod"])
	}
	loc := files["apple_test_mod/localization/english/apple_test_l_english.yml"]
	if !hasUTF8BOM(loc) {
		t.Fatal("localization file did not receive UTF-8 BOM")
	}
	if got := files["apple_test_mod/gfx/interface/icons/apple_test.dds"]; string(got) != string([]byte{0x44, 0x44, 0x53, 0x20, 1, 2, 3}) {
		t.Fatalf("binary resource changed: %v", got)
	}
	var manifest Manifest
	if err := json.Unmarshal(files["ck3-package-manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.Validation.Warnings != 1 || len(manifest.Files) != len(files)-1 {
		t.Fatalf("manifest is incomplete: %+v", manifest)
	}
	if strings.Contains(string(files["ck3-package-manifest.json"]), root) {
		t.Fatal("manifest leaked the artifact root")
	}
}

func TestBuildConcurrentSameContentAcrossProcesses(t *testing.T) {
	if os.Getenv("CK3_PACKAGER_RACE_HELPER") == "1" {
		runPackagerRaceHelper(t)
		return
	}

	root := t.TempDir()
	start := filepath.Join(root, "start")
	const processCount = 6
	type child struct {
		command *exec.Cmd
		output  *bytes.Buffer
	}
	children := make([]child, 0, processCount)
	for index := 0; index < processCount; index++ {
		output := &bytes.Buffer{}
		command := exec.Command(os.Args[0], "-test.run=^TestBuildConcurrentSameContentAcrossProcesses$", "-test.count=1")
		command.Env = append(os.Environ(),
			"CK3_PACKAGER_RACE_HELPER=1",
			"CK3_PACKAGER_RACE_ROOT="+root,
			"CK3_PACKAGER_RACE_START="+start,
		)
		command.Stdout = output
		command.Stderr = output
		if err := command.Start(); err != nil {
			t.Fatalf("start packager race child %d: %v", index, err)
		}
		children = append(children, child{command: command, output: output})
	}
	if err := os.WriteFile(start, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, child := range children {
		if err := child.command.Wait(); err != nil {
			t.Fatalf("packager race child %d failed: %v\n%s", index, err, child.output.String())
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var archives, records []string
	for _, entry := range entries {
		switch {
		case isGeneratedArchiveName(entry.Name()):
			archives = append(archives, entry.Name())
		case strings.HasPrefix(entry.Name(), artifactRecordPrefix) && strings.HasSuffix(entry.Name(), artifactRecordSuffix):
			records = append(records, entry.Name())
		}
	}
	if len(archives) != 1 || len(records) != 1 {
		t.Fatalf("cross-process package race left archives=%v records=%v", archives, records)
	}
	record, ok := ownedArtifactRecord(root, archives[0])
	if !ok {
		t.Fatalf("cross-process package race left no valid ownership record")
	}
	hash, size, err := hashFile(filepath.Join(root, archives[0]))
	if err != nil {
		t.Fatal(err)
	}
	if record.SHA256 != hash || record.Size != size {
		t.Fatalf("cross-process package record does not match archive: record=%+v hash=%s size=%d", record, hash, size)
	}
}

func runPackagerRaceHelper(t *testing.T) {
	t.Helper()
	root := os.Getenv("CK3_PACKAGER_RACE_ROOT")
	start := os.Getenv("CK3_PACKAGER_RACE_START")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(start); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for packager race barrier")
		}
		time.Sleep(time.Millisecond)
	}
	request := Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("common/scripted_triggers/apple_race.txt", "apple_race_trigger = { always = yes }\n"),
		textInput("localization/english/apple_race_l_english.yml", "l_english:\n apple_race_name:0 \"Apple\"\n"),
	}}
	result, err := Build(context.Background(), request, BuildOptions{
		ArtifactRoot: root,
		Retention:    7 * 24 * time.Hour,
		Limits:       MCPLimits,
		Validator:    allowAllValidator(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || result.SHA256 == "" {
		t.Fatalf("race helper package result = %+v", result)
	}
}

func TestBuiltArchiveCanBeRescanned(t *testing.T) {
	artifactRoot := t.TempDir()
	request := Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("common/scripted_triggers/apple_rescan.txt", "apple_rescan_trigger = { always = yes }\n"),
		textInput("localization/english/apple_rescan_l_english.yml", "l_english:\n apple_rescan_name:0 \"Apple\"\n"),
	}}
	result, err := Build(context.Background(), request, BuildOptions{
		ArtifactRoot: artifactRoot, Limits: MCPLimits, Validator: allowAllValidator(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(t.TempDir(), "apple_test_mod")
	for name, data := range readZipFiles(t, filepath.Join(artifactRoot, result.ArtifactRelPath)) {
		const prefix = "apple_test_mod/"
		if strings.HasPrefix(name, prefix) {
			writeTestFile(t, project, strings.TrimPrefix(name, prefix), data)
		}
	}
	configRoot := t.TempDir()
	stats, err := indexer.Scan(context.Background(), indexer.Config{
		ConfigPath: filepath.Join(configRoot, "ck3-index.toml"), Database: "rescan.sqlite", ForceClean: true,
		Sources: []indexer.Source{{Name: "packaged", Path: project, Rank: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files < 2 || stats.Objects == 0 || stats.Localization == 0 {
		t.Fatalf("packaged fixture did not rescan correctly: %+v", stats)
	}
}

func TestBuildBlocksUnsafeAndInvalidPackages(t *testing.T) {
	tests := []struct {
		name  string
		files []FileInput
		code  string
	}{
		{"traversal", []FileInput{textInput("../events/bad.txt", "x={}")}, "package_invalid_path"},
		{"absolute", []FileInput{textInput("C:/temp/bad.txt", "x={}")}, "package_invalid_path"},
		{"reserved name", []FileInput{textInput("events/CON.txt", "x={}")}, "package_invalid_path"},
		{"case collision", []FileInput{textInput("events/Test.txt", "x={}"), textInput("events/test.txt", "y={}")}, "package_duplicate_path"},
		{"unknown root", []FileInput{textInput("README.md", "not loaded")}, "package_unsupported_root"},
		{"descriptor conflict", []FileInput{textInput("descriptor.mod", "name=\"Other\"\n")}, "package_descriptor_conflict"},
		{"descriptor only", []FileInput{{Path: "descriptor.mod", Content: stringPointer(string(descriptorContent(validMetadata(), false)))}}, "package_no_content"},
		{"invalid base64", []FileInput{{Path: "gfx/interface/bad.dds", ContentBase64: stringPointer("not-base64")}}, "package_invalid_content"},
		{"both content forms", []FileInput{{Path: "gfx/interface/bad.dds", Content: stringPointer("x"), ContentBase64: stringPointer("eA==")}}, "package_invalid_content"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			result, err := Build(context.Background(), Request{Metadata: validMetadata(), Files: test.files}, BuildOptions{
				ArtifactRoot: root, Limits: MCPLimits, Validator: allowAllValidator(0),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "blocked" || len(result.Validation.Diagnostics) == 0 || result.Validation.Diagnostics[0].Code != test.code {
				t.Fatalf("unexpected block result: %+v", result)
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatalf("blocked package left artifacts: %v", entries)
			}
		})
	}
}

func TestBuildEnforcesLimits(t *testing.T) {
	result, err := Build(context.Background(), Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("events/one.txt", "one={}"), textInput("events/two.txt", "two={}"),
	}}, BuildOptions{
		ArtifactRoot: t.TempDir(), Limits: Limits{MaxFiles: 1, MaxFileBytes: 8, MaxTotalBytes: 8}, Validator: allowAllValidator(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "blocked" || result.Validation.Diagnostics[0].Code != "package_too_many_files" {
		t.Fatalf("file-count limit was not enforced: %+v", result)
	}
}

func TestBuildEnforcesMetadataKinds(t *testing.T) {
	tests := []struct {
		name string
		meta Metadata
		code string
	}{
		{"submod dependency", Metadata{Name: "Submod", Slug: "test_submod", Version: "1", SupportedVersion: "1.19.*", Tags: []string{"Gameplay"}, Kind: "submod"}, "package_dependency_required"},
		{"addon replace path", Metadata{Name: "Addon", Slug: "test_addon", Version: "1", SupportedVersion: "1.19.*", Tags: []string{"Gameplay"}, ReplacePaths: []string{"common/culture"}}, "package_replace_path_forbidden"},
		{"unsafe replace path", Metadata{Name: "TC", Slug: "test_total", Version: "1", SupportedVersion: "1.19.*", Tags: []string{"Total Conversion"}, Kind: "total_conversion", ReplacePaths: []string{"../common"}}, "package_replace_path_invalid"},
		{"major wildcard", Metadata{Name: "Bad", Slug: "test_bad", Version: "1", SupportedVersion: "1.*", Tags: []string{"Gameplay"}}, "package_supported_version_invalid"},
		{"minor wildcard", Metadata{Name: "Bad", Slug: "test_bad", Version: "1", SupportedVersion: "1.*.*", Tags: []string{"Gameplay"}}, "package_supported_version_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Build(context.Background(), Request{Metadata: test.meta, Files: []FileInput{
				textInput("events/test.txt", "test={}"),
			}}, BuildOptions{ArtifactRoot: t.TempDir(), Limits: MCPLimits, Validator: allowAllValidator(0)})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "blocked" || result.Validation.Diagnostics[0].Code != test.code {
				t.Fatalf("metadata rule was not enforced: %+v", result)
			}
		})
	}
}

func TestBuildDoesNotPublishWhenValidatorBlocks(t *testing.T) {
	root := t.TempDir()
	validator := validatorFunc(func(_ context.Context, _ []PreparedFile) (ValidationReport, error) {
		return packageBlocked("missing_localization", "missing localization key", "events/test.txt"), nil
	})
	result, err := Build(context.Background(), Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("events/test.txt", "namespace = test\ntest.1 = { type = character_event }")}}, BuildOptions{
		ArtifactRoot: root, Limits: MCPLimits, Validator: validator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "blocked" {
		t.Fatalf("validator blocker was ignored: %+v", result)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("blocked validation published files: %v", entries)
	}
}

func TestRequestFromDirectoryExcludesDevelopmentFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "descriptor.mod", descriptorContent(validMetadata(), false))
	writeTestFile(t, root, "events/test.txt", []byte("namespace=test\ntest.1={ type=character_event option={ name=test_option } }"))
	writeTestFile(t, root, "gfx/interface/test.dds", []byte{1, 2, 3})
	writeTestFile(t, root, "README.md", []byte("development notes"))
	writeTestFile(t, root, ".git/config", []byte("secret-ish workspace metadata"))
	request, excluded, err := RequestFromDirectory(root, validMetadata(), DirectoryLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Files) != 3 {
		t.Fatalf("directory request files=%d want 3: %+v", len(request.Files), request.Files)
	}
	joined := strings.Join(excluded, "\n")
	if !strings.Contains(joined, "README.md") || !strings.Contains(joined, ".git/") {
		t.Fatalf("development exclusions were not reported: %v", excluded)
	}
}

func TestRequestFromDirectoryRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "events/real.txt", []byte("x={}"))
	link := filepath.Join(root, "events", "linked.txt")
	if err := os.Symlink(filepath.Join(root, "events", "real.txt"), link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, _, err := RequestFromDirectory(root, validMetadata(), DirectoryLimits); err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("directory symlink was not rejected: %v", err)
	}
}

func TestIndexerValidatorBlocksParseAndMissingLocalization(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	validator := IndexerValidator{DB: db}
	report, err := validator.Validate(context.Background(), []PreparedFile{
		{Path: "common/decisions/test.txt", Data: []byte("test_decision = { desc = missing_test_desc is_shown = { always = yes }\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Blocked || report.Blockers == 0 {
		t.Fatalf("release validator failed to block invalid content: %+v", report)
	}
}

func TestIndexerValidatorReturnsMissingReferenceDiagnostics(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := (IndexerValidator{DB: db}).Validate(context.Background(), []PreparedFile{{
		Path: "common/decisions/package_missing_refs.txt",
		Data: []byte(`package_missing_refs_decision = {
	desc = package_missing_refs_decision.desc
	icon = "gfx/interface/icons/package_missing.dds"
	is_shown = { has_trait = package_missing_trait }
}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Blocked || len(report.MissingLoc) == 0 || len(report.MissingRes) == 0 {
		t.Fatalf("missing references did not block release: %+v", report)
	}
	codes := map[string]bool{}
	for _, diagnostic := range report.Diagnostics {
		codes[diagnostic.Code] = true
	}
	for _, code := range []string{"missing_localization", "missing_resource", "missing_object_reference"} {
		if !codes[code] {
			t.Fatalf("missing %s diagnostic in %+v", code, report.Diagnostics)
		}
	}
}

func TestBuildCleansOnlyRegisteredExpiredArtifacts(t *testing.T) {
	root := t.TempDir()
	options := BuildOptions{ArtifactRoot: root, Retention: 7 * 24 * time.Hour, Limits: MCPLimits, Validator: allowAllValidator(0)}
	first, err := Build(context.Background(), Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("common/scripted_triggers/old.txt", "old_trigger = { always = yes }")}}, options)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, first.ArchiveName)
	oldRecord := filepath.Join(root, artifactRecordName(first.ArchiveName))
	userZip := filepath.Join(root, "my-manual-backup.zip")
	lookalikeZip := filepath.Join(root, "apple_test_mod-0123456789abcdef.zip")
	for _, path := range []string{userZip, lookalikeZip} {
		if err := os.WriteFile(path, []byte("user-owned"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	for _, path := range []string{old, oldRecord, userZip, lookalikeZip} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	_, err = Build(context.Background(), Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("common/scripted_triggers/new.txt", "new_trigger = { always = yes }")}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("registered expired artifact was not removed: %v", err)
	}
	if _, err := os.Stat(oldRecord); !os.IsNotExist(err) {
		t.Fatalf("record for removed artifact was not removed: %v", err)
	}
	for _, path := range []string{userZip, lookalikeZip} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup removed an unregistered user ZIP %s: %v", filepath.Base(path), err)
		}
	}
}

func TestCleanupPreservesModifiedRegisteredArtifact(t *testing.T) {
	root := t.TempDir()
	result, err := Build(context.Background(), Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("common/scripted_triggers/old.txt", "old_trigger = { always = yes }")}}, BuildOptions{
		ArtifactRoot: root, Retention: 7 * 24 * time.Hour, Limits: MCPLimits, Validator: allowAllValidator(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, result.ArchiveName)
	record := filepath.Join(root, artifactRecordName(result.ArchiveName))
	if err := os.WriteFile(archive, []byte("manually replaced archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	for _, path := range []string{archive, record} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupArtifacts(root, 7*24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{archive, record} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup removed a modified artifact or its record %s: %v", filepath.Base(path), err)
		}
	}
}

func TestBuildCleansStageOnArtifactCollision(t *testing.T) {
	request := Request{Metadata: validMetadata(), Files: []FileInput{
		textInput("common/scripted_triggers/collision.txt", "collision_trigger = { always = yes }"),
	}}
	firstRoot := t.TempDir()
	first, err := Build(context.Background(), request, BuildOptions{
		ArtifactRoot: firstRoot, Limits: MCPLimits, Validator: allowAllValidator(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	collisionRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(collisionRoot, first.ArchiveName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), request, BuildOptions{
		ArtifactRoot: collisionRoot, Limits: MCPLimits, Validator: allowAllValidator(0),
	}); err == nil || !strings.Contains(err.Error(), "artifact collision") {
		t.Fatalf("non-file artifact collision was not rejected: %v", err)
	}
	entries, err := os.ReadDir(collisionRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ck3-package-stage-") {
			t.Fatalf("failed publish left staging directory %s", entry.Name())
		}
	}
	corruptRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(corruptRoot, first.ArchiveName), []byte("not the expected ZIP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), request, BuildOptions{
		ArtifactRoot: corruptRoot, Limits: MCPLimits, Validator: allowAllValidator(0),
	}); err == nil || !strings.Contains(err.Error(), "unexpected content") {
		t.Fatalf("corrupt artifact collision was not rejected: %v", err)
	}
	entries, err = os.ReadDir(corruptRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ck3-package-stage-") {
			t.Fatalf("corrupt collision left staging directory %s", entry.Name())
		}
	}
}

func readZipFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result := map[string][]byte{}
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name] = data
	}
	return result
}

func writeTestFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringPointer(value string) *string { return &value }
