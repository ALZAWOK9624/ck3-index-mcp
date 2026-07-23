package indexer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/script"
)

func BenchmarkScriptedEffectsRecursion100(b *testing.B) {
	var source strings.Builder
	for index := 0; index < 100; index++ {
		fmt.Fprintf(&source, "effect_%03d = { effect_%03d = yes if = { limit = { always = yes } add_gold = 1 } }\n", index, (index+1)%100)
	}
	parsed := script.Parse(source.String())
	if len(parsed.Errors) != 0 {
		b.Fatalf("parse fixture: %+v", parsed.Errors)
	}
	legacy := benchmarkLegacyMode()
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for _, definition := range parsed.Nodes {
			children := definition.Children
			if legacy {
				children = parsed.Nodes
			}
			_ = checkScriptEffectRecursion(children, "common/scripted_effects/bench.txt", definition.Key)
		}
	}
}

func BenchmarkParseLocalization(b *testing.B) {
	var source strings.Builder
	source.WriteString("l_english:\n")
	for index := 0; index < 10000; index++ {
		fmt.Fprintf(&source, " benchmark_key_%05d:0 \"Benchmark localization value %05d\"\n", index, index)
	}
	data := []byte(source.String())
	legacy := benchmarkLegacyMode()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		var err error
		if legacy {
			_, err = parseLocBytesLegacyBenchmark("localization/english/bench_l_english.yml", data)
		} else {
			_, err = parseLocBytes("localization/english/bench_l_english.yml", data)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtractObjectsRefsFields(b *testing.B) {
	var source strings.Builder
	for index := 0; index < 1000; index++ {
		fmt.Fprintf(&source, "bench_decision_%04d = { title = bench_decision_%04d.t is_shown = { has_trait = bench_trait_%04d } effect = { add_gold = %d } }\n", index, index, index, index)
	}
	parsed := script.Parse(source.String())
	if len(parsed.Errors) != 0 {
		b.Fatalf("parse fixture: %+v", parsed.Errors)
	}
	record := fileRecord{SourceName: "project", SourceRank: 1, Path: "bench.txt", RelPath: "common/decisions/bench.txt", Kind: "script"}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		objects := extractObjects(record, parsed.Nodes)
		_ = extractRefs(record, parsed.Nodes, objects)
		_ = extractObjectFields(record, parsed.Nodes, objects)
	}
}

func BenchmarkLargeResourceStreamHash(b *testing.B) {
	const size = 32 << 20
	path := filepath.Join(b.TempDir(), "large.dds")
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		b.Fatal(err)
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}
	job := fileJob{
		src:  Source{Name: "project", Rank: 1},
		path: path,
		rel:  "gfx/interface/large.dds",
		kind: "resource",
	}
	legacy := benchmarkLegacyMode()
	b.ReportAllocs()
	b.SetBytes(size)
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if legacy {
			data, err := os.ReadFile(path)
			if err != nil {
				b.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if sum == ([sha256.Size]byte{}) {
				b.Fatal("legacy resource hash is empty")
			}
			continue
		}
		result := parseOneFile(job)
		if result.err != nil || result.sum == "" {
			b.Fatalf("stream hash: %+v", result.err)
		}
	}
}

func BenchmarkFullSyntheticScan(b *testing.B) {
	cfg, _ := benchmarkIndexFixture(b, 100)
	cfg.ForceClean = true
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := Scan(context.Background(), cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSingleFileRefresh(b *testing.B) {
	cfg, project := benchmarkIndexFixture(b, 100)
	if _, err := Scan(context.Background(), cfg); err != nil {
		b.Fatal(err)
	}
	rel := "common/decisions/bench_0000.txt"
	path := filepath.Join(project, filepath.FromSlash(rel))
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		b.StopTimer()
		content := fmt.Sprintf("bench_decision_0000 = { title = bench_decision_0000.t ai_check_interval = %d }\n", iteration+1)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := ScanFiles(context.Background(), cfg, []string{rel}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchExact(b *testing.B) {
	benchmarkSearch(b, "bench_decision_0050")
}

func BenchmarkSearchMissing(b *testing.B) {
	benchmarkSearch(b, "definitely_missing_benchmark_symbol")
}

func BenchmarkSearchChineseLocalization(b *testing.B) {
	benchmarkSearch(b, "基准中文文本")
}

func benchmarkSearch(b *testing.B, query string) {
	cfg, _ := benchmarkIndexFixture(b, 100)
	if _, err := Scan(context.Background(), cfg); err != nil {
		b.Fatal(err)
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		b.Fatal(err)
	}
	db, err := OpenReadOnly(dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	options := SearchOptions{Query: query, LLMOptions: LLMOptions{AllowProject: true, Limit: 20}}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := db.LLMSearch(context.Background(), options); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkIndexFixture(b *testing.B, files int) (Config, string) {
	b.Helper()
	root := b.TempDir()
	project := filepath.Join(root, "project")
	decisions := filepath.Join(project, "common", "decisions")
	localization := filepath.Join(project, "localization", "simp_chinese")
	if err := os.MkdirAll(decisions, 0755); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(localization, 0755); err != nil {
		b.Fatal(err)
	}
	for index := 0; index < files; index++ {
		path := filepath.Join(decisions, fmt.Sprintf("bench_%04d.txt", index))
		content := fmt.Sprintf("bench_decision_%04d = { title = bench_decision_%04d.t is_shown = { always = yes } effect = { add_gold = %d } }\n", index, index, index)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			b.Fatal(err)
		}
	}
	loc := "l_simp_chinese:\n bench_decision_0050.t:0 \"基准中文文本\"\n"
	if err := os.WriteFile(filepath.Join(localization, "bench_l_simp_chinese.yml"), []byte(loc), 0644); err != nil {
		b.Fatal(err)
	}
	return Config{
		ConfigPath: filepath.Join(root, "ck3-index.toml"),
		Database:   "cache/bench.sqlite",
		GISEnabled: false,
		Sources: []Source{{
			Name: "project",
			Path: project,
			Rank: 1,
		}},
	}, project
}

func benchmarkLegacyMode() bool {
	return os.Getenv("CK3_INDEX_BENCH_LEGACY") == "1"
}

func parseLocBytesLegacyBenchmark(rel string, data []byte) ([]locEntry, error) {
	lang := languageFromPath(rel)
	replace := 0
	if strings.Contains(filepath.ToSlash(strings.ToLower(rel)), "/replace/") {
		replace = 1
	}
	var out []locEntry
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 1<<20), localizationScannerMaxToken)
	line := 0
	for scanner.Scan() {
		line++
		match := locLine.FindStringSubmatch(scanner.Text())
		if match == nil {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(match[2], `"`), `"`)
		out = append(out, locEntry{key: match[1], lang: lang, val: value, line: line, replace: replace})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan localization %s: %w", rel, err)
	}
	return out, nil
}
