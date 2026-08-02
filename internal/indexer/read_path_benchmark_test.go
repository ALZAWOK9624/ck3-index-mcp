package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The existing search benchmarks run against benchmarkIndexFixture(b, 100):
// a hundred single-object files, one localization row, and essentially no
// references. None of the read path's real costs are visible at that shape.
// The production index has ~72k objects, ~620k refs and ~2.5M localization
// rows, and the queries that hurt are the ones whose cost scales with how many
// rows share a prefix rather than with the requested limit.
//
// This fixture is small enough to build in a few seconds and large enough that
// those two behaviours diverge: a query bounded by its index reads a fixed
// handful of rows, while one that materialises and sorts a whole prefix range
// before applying LIMIT reads thousands. A change that removes such a sort
// shows up here and nowhere else in the suite.

const (
	// readPathObjects is the number of objects sharing the "bench_probe_"
	// prefix. The prefix searchers order by an unindexable CASE expression, so
	// this is the number of rows SQLite must sort to return eight.
	readPathObjects = 3000
	// readPathHotRefs is how many distinct sites reference one hot identifier.
	// QueryRefs materialises up to 500 of these and sorts them by columns that
	// live in a joined table.
	readPathHotRefs = 800
	readPathHotID   = "bench_probe_trait_0000"
)

var (
	readPathOnce    sync.Once
	readPathConfig  Config
	readPathDBPath  string
	readPathBuildEr error
)

// readPathFixture builds the index once for the whole benchmark binary.
// Rebuilding per benchmark would dominate the measurement and make the
// comparison worthless.
func readPathFixture(b *testing.B) (*DB, Config) {
	b.Helper()
	readPathOnce.Do(func() {
		readPathConfig, readPathDBPath, readPathBuildEr = buildReadPathIndex()
	})
	if readPathBuildEr != nil {
		b.Fatal(readPathBuildEr)
	}
	db, err := OpenReadOnly(readPathDBPath)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	return db, readPathConfig
}

func buildReadPathIndex() (Config, string, error) {
	root, err := os.MkdirTemp("", "ck3-read-path-bench-")
	if err != nil {
		return Config{}, "", err
	}
	game := filepath.Join(root, "game")
	write := func(rel, content string) error {
		path := filepath.Join(game, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}

	// A wide band of objects on one prefix. Split across files so the scanner
	// behaves like it does on a real tree rather than parsing one huge file.
	const perFile = 200
	for start := 0; start < readPathObjects; start += perFile {
		var block strings.Builder
		for index := start; index < start+perFile && index < readPathObjects; index++ {
			fmt.Fprintf(&block, `bench_probe_trait_%04d = {
	category = personality
	icon = bench_probe_trait_%04d.dds
	desc = bench_probe_trait_%04d_desc
	martial = %d
}
`, index, index, index, index%8)
		}
		if err := write(fmt.Sprintf("common/traits/bench_probe_%04d.txt", start/perFile), block.String()); err != nil {
			return Config{}, "", err
		}
	}

	// A matching band of localization keys. localization is the largest table
	// in production and shares the same unindexable ordering.
	var localization strings.Builder
	localization.WriteString("l_english:\n")
	for index := 0; index < readPathObjects; index++ {
		fmt.Fprintf(&localization, " bench_probe_trait_%04d:0 \"Bench Probe %04d\"\n", index, index)
		fmt.Fprintf(&localization, " bench_probe_trait_%04d_desc:0 \"Description for bench probe trait %04d.\"\n", index, index)
	}
	if err := write("localization/english/bench_probe_l_english.yml", localization.String()); err != nil {
		return Config{}, "", err
	}

	// Concentrate references on one identifier so the ref query has a real
	// result set to sort instead of the two rows the old fixture produced.
	const refsPerFile = 100
	for start := 0; start < readPathHotRefs; start += refsPerFile {
		var block strings.Builder
		for index := start; index < start+refsPerFile && index < readPathHotRefs; index++ {
			fmt.Fprintf(&block, `bench_probe.%d = {
	type = character_event
	desc = bench_probe_event_%d_desc
	trigger = { has_trait = %s }
	immediate = { add_trait = %s remove_trait = bench_probe_trait_%04d }
}
`, index, index, readPathHotID, readPathHotID, index%readPathObjects)
		}
		if err := write(fmt.Sprintf("events/bench_probe_%04d.txt", start/refsPerFile), block.String()); err != nil {
			return Config{}, "", err
		}
	}

	cfg := Config{
		ConfigPath: filepath.Join(root, "ck3-index.toml"),
		Database:   "cache/bench.sqlite",
		GISEnabled: false,
		Sources: []Source{
			{Name: "game", Path: game, Rank: 1, Role: SourceRoleProject},
		},
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		return Config{}, "", err
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return Config{}, "", err
	}
	return cfg, dbPath, nil
}

// BenchmarkReadPathSearchPrefix is the case the prefix searchers are worst at:
// a short prefix shared by thousands of rows, returning a handful.
func BenchmarkReadPathSearchPrefix(b *testing.B) {
	benchmarkReadPathSearch(b, "bench_probe_")
}

// BenchmarkReadPathSearchExact returns the same handful from a prefix matched
// by exactly one row, isolating per-call overhead from range-scan cost.
func BenchmarkReadPathSearchExact(b *testing.B) {
	benchmarkReadPathSearch(b, readPathHotID)
}

// BenchmarkReadPathSearchMissing takes the substring fallback, which is the
// path an agent hits whenever it guesses an identifier wrong.
func BenchmarkReadPathSearchMissing(b *testing.B) {
	benchmarkReadPathSearch(b, "bench_probe_absent_symbol")
}

func benchmarkReadPathSearch(b *testing.B, query string) {
	db, _ := readPathFixture(b)
	options := SearchOptions{Query: query, LLMOptions: LLMOptions{AllowProject: true, Limit: 8}}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := db.LLMSearch(context.Background(), options); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReadPathInspectAggregate covers the default ck3_inspect operation,
// the single most expensive read in the tool surface.
func BenchmarkReadPathInspectAggregate(b *testing.B) {
	db, _ := readPathFixture(b)
	options := LLMOptions{AllowProject: true, Limit: 8}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := db.LLMInspectSmart(context.Background(), readPathHotID, options); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReadPathFindRefs isolates the ref query that ck3_inspect currently
// runs twice per call.
func BenchmarkReadPathFindRefs(b *testing.B) {
	db, _ := readPathFixture(b)
	options := LLMOptions{AllowProject: true, Limit: 8}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := db.LLMFindRefs(context.Background(), readPathHotID, options); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReadPathDependencyGraph covers the frontier expansion, which issues
// one ref query per discovered node.
func BenchmarkReadPathDependencyGraph(b *testing.B) {
	db, _ := readPathFixture(b)
	options := LLMOptions{AllowProject: true, Limit: 8, Depth: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := db.LLMDependencyGraph(context.Background(), readPathHotID, options); err != nil {
			b.Fatal(err)
		}
	}
}
