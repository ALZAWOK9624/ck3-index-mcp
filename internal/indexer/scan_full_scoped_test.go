package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFullScanScopedFinalizerPropagatesProviderRename(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	const (
		providerRel  = "common/traits/provider.txt"
		consumerRel  = "common/decisions/consumer.txt"
		oldProvider  = "scoped_provider_old_trait"
		newProvider  = "scoped_provider_new_trait"
		stableObject = "scoped_provider_stable_trait"
	)
	write(providerRel, oldProvider+" = {}\n")
	write(consumerRel, `scoped_provider_consumer = {
	is_shown = { has_trait = scoped_provider_old_trait }
}
`)
	write("common/traits/stable.txt", stableObject+" = {}\n")
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	assertConsumerResolution := func(want int) {
		t.Helper()
		var got int
		err := db.sql.QueryRowContext(ctx, `SELECT r.resolved
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE f.source_name='project' AND f.rel_path=? AND f.overridden=0
				AND r.ref_kind='trait' AND r.ref_name=?`, consumerRel, oldProvider).Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("consumer reference resolution=%d, want %d", got, want)
		}
	}
	assertMissingObjectDiagnostic := func(want int) {
		t.Helper()
		var got int
		err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM diagnostics d JOIN files f ON f.id=d.file_id
			WHERE d.source='validator' AND d.code='missing_object_reference'
				AND f.source_name='project' AND f.rel_path=?`, consumerRel).Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("consumer missing-object diagnostics=%d, want %d", got, want)
		}
	}
	assertConsumerResolution(1)
	assertMissingObjectDiagnostic(0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	write(providerRel, newProvider+" = {}\n")
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatalf("provider rename incorrectly reused published index: %+v", stats)
	}
	for _, key := range []string{"resolve_refs_scoped", "validator_scoped", "semantic_fts_scoped"} {
		if _, ok := stats.TimingsMillis[key]; !ok {
			t.Fatalf("small provider update did not report %s: %+v", key, stats.TimingsMillis)
		}
	}
	if _, ok := stats.TimingsMillis["load_symbols"]; ok {
		t.Fatalf("small provider update unexpectedly used global symbol load: %+v", stats.TimingsMillis)
	}

	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertConsumerResolution(0)
	assertMissingObjectDiagnostic(1)
	assertFTSObjectCount := func(name string, want int) {
		t.Helper()
		var got int
		if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_fts WHERE kind='object' AND name=?`, name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("semantic FTS object rows for %q=%d, want %d", name, got, want)
		}
	}
	assertFTSObjectCount(oldProvider, 0)
	assertFTSObjectCount(newProvider, 1)
	assertFTSObjectCount(stableObject, 1)
}

func TestFullScanFallsBackFromScopedFinalizerForLargeChangeSet(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	changeCount := scopedFinalizerFileLimit/2 + 1 // old + new file records exceed the scoped limit.
	for i := 0; i < changeCount; i++ {
		write(fmt.Sprintf("common/traits/provider_%03d.txt", i), fmt.Sprintf("scoped_fallback_old_%03d = {}\n", i))
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < changeCount; i++ {
		write(fmt.Sprintf("common/traits/provider_%03d.txt", i), fmt.Sprintf("scoped_fallback_new_%03d = {}\n", i))
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatalf("large change set incorrectly reused published index: %+v", stats)
	}
	if _, ok := stats.TimingsMillis["resolve_refs_scoped"]; ok {
		t.Fatalf("large change set unexpectedly retained scoped resolver: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["validator_scoped"]; ok {
		t.Fatalf("large change set unexpectedly retained scoped validator: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["load_symbols"]; !ok {
		t.Fatalf("large change set did not report global finalizer fallback: %+v", stats.TimingsMillis)
	}
}

func TestFullScanFallsBackFromScopedFinalizerForHighFanoutProvider(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	const oldProvider = "scoped_fanout_old_trait"
	write("common/traits/provider.txt", oldProvider+" = {}\n")
	for i := 0; i < scopedValidatorFileLimit+1; i++ {
		write(fmt.Sprintf("common/decisions/consumer_%03d.txt", i), fmt.Sprintf("scoped_fanout_consumer_%03d = { is_shown = { has_trait = %s } }\n", i, oldProvider))
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	write("common/traits/provider.txt", "scoped_fanout_new_trait = {}\n")
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatalf("high-fanout provider update incorrectly reused published index: %+v", stats)
	}
	if _, ok := stats.TimingsMillis["resolve_refs_scoped"]; ok {
		t.Fatalf("high-fanout provider update unexpectedly retained scoped resolver: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["validator_scoped"]; ok {
		t.Fatalf("high-fanout provider update unexpectedly retained scoped validator: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["load_symbols"]; !ok {
		t.Fatalf("high-fanout provider update did not report global finalizer fallback: %+v", stats.TimingsMillis)
	}
}

func TestScanFilesFallsBackFromScopedFinalizerForHighFanoutProvider(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	const (
		providerRel = "common/traits/provider.txt"
		oldProvider = "scan_files_fanout_old_trait"
	)
	write(providerRel, oldProvider+" = {}\n")
	for i := 0; i < scopedValidatorFileLimit+1; i++ {
		write(fmt.Sprintf("common/decisions/consumer_%03d.txt", i), fmt.Sprintf("scan_files_fanout_consumer_%03d = { is_shown = { has_trait = %s } }\n", i, oldProvider))
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	write(providerRel, "scan_files_fanout_new_trait = {}\n")
	stats, err := ScanFiles(ctx, cfg, []string{providerRel})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["resolve_refs_scoped"]; ok {
		t.Fatalf("high-fanout scan --files update unexpectedly retained scoped resolver: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["validator_scoped"]; ok {
		t.Fatalf("high-fanout scan --files update unexpectedly retained scoped validator: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["load_symbols"]; !ok {
		t.Fatalf("high-fanout scan --files update did not report global finalizer fallback: %+v", stats.TimingsMillis)
	}
}

func TestFullScanOverrideWinnerTransitionReconcilesResolverAndValidator(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	const (
		providerRel  = "common/traits/shared.txt"
		consumerRel  = "common/decisions/consumer.txt"
		projectTrait = "scoped_override_project_trait"
	)
	projectProvider := filepath.Join(project, filepath.FromSlash(providerRel))
	gameProvider := filepath.Join(game, filepath.FromSlash(providerRel))
	consumer := filepath.Join(project, filepath.FromSlash(consumerRel))
	for _, path := range []string{projectProvider, gameProvider, consumer} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(projectProvider, []byte(projectTrait+" = {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gameProvider, []byte("scoped_override_game_trait = {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(consumer, []byte("scoped_override_consumer = { is_shown = { has_trait = "+projectTrait+" } }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		GISEnabled: false,
		Sources: []Source{
			{Name: "project", Path: project, Rank: 1},
			{Name: "game", Path: game, Rank: 2},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projectProvider); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"resolve_refs_scoped", "validator_scoped"} {
		if _, ok := stats.TimingsMillis[key]; !ok {
			t.Fatalf("override winner transition did not take scoped finalizer (%s): %+v", key, stats.TimingsMillis)
		}
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var resolved int
	if err := db.sql.QueryRowContext(ctx, `SELECT r.resolved
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE f.source_name='project' AND f.rel_path=? AND r.ref_kind='trait' AND r.ref_name=?`, consumerRel, projectTrait).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 0 {
		t.Fatalf("consumer reference still resolved after its override provider disappeared: %d", resolved)
	}
	var diagnostics int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM diagnostics d JOIN files f ON f.id=d.file_id
		WHERE d.source='validator' AND d.code='missing_object_reference'
			AND f.source_name='project' AND f.rel_path=?`, consumerRel).Scan(&diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics == 0 {
		t.Fatal("consumer missing-object diagnostic was not refreshed after override winner transition")
	}
}
