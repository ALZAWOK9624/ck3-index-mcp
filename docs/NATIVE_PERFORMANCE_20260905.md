# Native performance measurements — 2026-09-05

The native relief renderer now dispatches between scalar x86-64, AVX2 (four
pixels), and AVX-512 (eight pixels). The SIMD variants share one arithmetic
implementation. Gray16 input is read directly, respecting subimage origins,
byte stride, and unaligned storage, instead of allocating and converting a
second full height field. Small images execute synchronously; larger images
give each worker at least 64K pixels.

The Windows SHA-NI file reader uses a 64 KiB local buffer for small files and a
file-sized heap buffer capped at 4 MiB for larger files. UTF-8 paths are
converted once, using local UTF-16 storage for short paths. Empty in-memory
hashes return the standard constant. The shared-file flags, read-until-EOF
behavior, short-read handling, size/mtime checks, and reopened-path identity
checks are preserved. Embedded NUL paths are rejected rather than silently
hashing a prefix path.

## Measured results

Windows/amd64, Ryzen 7 9700X, Go 1.26.5, GCC 16.1.0. Both before and after
executables use `ck3_native sqlite_fts5`; the comparison measures improvements
over the existing native backend.
The initial working tree's unrelated edits were preserved in both versions.
Baseline native source files were captured before replacement, and the
additional real-image benchmark was built against the originals using a Go
overlay. Other project source files and dependencies stayed the same.

Values below are the median of three independent benchmark repetitions. File
reads are warm-cache repeated reads. Real relief timings use five render calls
per repetition and exclude PNG decoding, disk I/O, database work, and output
encoding. `GOMAXPROCS` is set explicitly. No tests or compilers were run
concurrently with the measurements.

| Workload | Before | After | Before / after |
| --- | ---: | ---: | ---: |
| Actual Godherja 8192×4096 heightmap, 1 worker | 364.62 ms | 61.77 ms | 5.90× |
| Actual Godherja 8192×4096 heightmap, 8 workers | 79.83 ms | 15.31 ms | 5.21× |
| Warm 4 KiB file hash, 1 worker | 345.39 µs | 55.83 µs | 6.19× |
| Warm 64 KiB file hash, 1 worker | 367.79 µs | 83.99 µs | 4.38× |
| Warm 1 MiB file hash, 1 worker | 760.30 µs | 572.75 µs | 1.33× |
| Warm 32 MiB file hash, 1 worker | 15.31 ms | 15.48 ms | 0.99× |

The large-file result is essentially unchanged in this sample (about 1.1%
slower); the improvement is concentrated on small files. No claim of a
whole-index or cold-storage speedup is made from these microbenchmarks.

For the real heightmap, allocated bytes per operation drop from approximately
160 MiB to 96 MiB: the eliminated uint16 field costs exactly 64 MiB. These are
Go allocation measurements, not process RSS or total input/output memory.

## Handwritten assembly experiment

A GNU extended-assembly AVX2 normalization candidate was implemented and
tested. It explicitly issued `vmulpd`, `vaddpd`, `vsqrtpd`, and three `vdivpd`
operations with early-clobber register constraints, preserving the reference
rounding. It passed pixel parity but did not consistently beat the compiler's
intrinsics implementation:

| Preliminary AVX2 candidate, before eliminating the input copy | Intrinsics | Handwritten normalization |
| --- | ---: | ---: |
| 512×512, 1 worker | 1.116 ms | 1.138 ms |
| 2048×2048, 1 worker | 16.522 ms | 16.457 ms |
| 2048×2048, 8 workers | 5.860 ms | 5.958 ms |

The experimental assembly is not in the production path. Its complete source
is retained locally at `cache/perf-20260905/handwritten-avx2-experiment.h`, with
measurements in `candidate.txt`. Production disassembly was checked: both SIMD
variants contain packed double square root and division, with no fused
multiply-add instructions. CPU and OS feature checks select the supported
variant; the binary is not compiled with `-march=native`.

## Correctness and verification

- The complete real heightmap's hillshade, detail and elevation planes match
  `buildMultiScaleReliefGo` byte for byte. AVX2 fallback was also tested against
  the real map with AVX-512 disabled.
- Differential fixtures cover zero dimensions, widths 0–33 and heights 0–15,
  SIMD tails, random heights, all 65,536 elevations, flat and alternating
  extreme terrain, near-flat rounding boundaries, Gray8 input, Gray16 subimages
  with a nonzero origin, odd byte strides, and worker counts 1, 3, and 8.
- Hash tests cover both sides of the 64 KiB and 4 MiB buffer boundaries,
  concurrent file hashing, randomized short chunks, Unicode and extended-length
  paths, embedded NUL rejection, missing files, and SHA-NI fallback.
- Full regression passes in all three modes: default, `ck3_native`, and
  `ck3_native sqlite_fts5`. Native `go vet` passes. Native tests also pass with
  the Go race detector. C memory accesses themselves are outside the Go race
  detector's instrumentation; differential and concurrent tests provide the
  additional native coverage.
- No live index refresh, plugin replacement, or Mod source modification is
  needed to reproduce these tests. Linux execution was not performed on this
  Windows host.

The actual map's output SHA-256 values are:

```text
hillshade c2046f4d940749a7305e376753716fddce727f6ec0b8428fc607bd9c390c5b0b
detail    359f13b7b42cd42bc02dffc544d25a0068d506b52e966243dd436796200f6fc0
elevation 79cf4a57c401a51c8d38a1223a57163e1ca42df322941f95286ea53cc94a0fb9
```

## Build and reproduce

`tools/build_local.ps1 -Native` enables cgo and both native build tags and writes
`bin/ck3-index-native.exe` by default. The ordinary invocation keeps its original
build mode and output name. A C compiler must be installed. The supplied build
for this work is `bin/ck3-index-native-perf.exe`, stamped `0.5.0` and
`3f7a287-dirty`. It includes the working tree's existing unpublished changes.

```powershell
./tools/build_local.ps1 -Native
go test -tags 'ck3_native sqlite_fts5' ./...
go test -tags ck3_native ./...
go test ./...
go vet -tags 'ck3_native sqlite_fts5' ./...
go test -tags 'ck3_native sqlite_fts5' ./internal/indexer -run '^$' `
  -bench '^BenchmarkNative' -benchmem -benchtime 250ms -count 3 -cpu 1,8

$env:CK3_INDEX_BENCH_HEIGHTMAP = '<path to an actual Gray16 heightmap.png>'
go test -tags 'ck3_native sqlite_fts5' ./internal/indexer `
  -run '^TestNativeRealHeightmapMatchesGo$' -v
go test -tags 'ck3_native sqlite_fts5' ./internal/indexer -run '^$' `
  -bench '^BenchmarkNativeRealHeightmap$' -benchmem -benchtime 5x -count 3 -cpu 1,8
```

On this host, GCC needs an ASCII build-temporary path. Go 1.26 also uses
`GOTMPDIR` for test fixtures, while migration tests deliberately reject path
aliases. The local `cache/perf-20260905/run-test.py` runner preserves the ASCII
compiler path and gives only the test process a canonical temporary directory.
This is test environment setup, not a relaxation of migration path validation.

Raw measurements, baseline test binaries, output hashes, disassembly, test
logs, the baseline overlay, and `summary.json` are retained under
`cache/perf-20260905/`. The native executable SHA-256 is
`0560d1c32748b3aa9f1edc771aa0d9fae14afa1388d8e61070e8ade9584b27a3`.
