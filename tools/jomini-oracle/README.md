# ck3-index Jomini oracle

This directory contains a development-only Jomini save-analysis suite:

- `ck3-index-jomini-oracle` is the original differential oracle for the generic
  Clausewitz plaintext subset. It emits a deterministic JSON view of Jomini's
  `TextTape`.
- `ck3-index-jomini-save` inspects save envelopes, finds exact keys, and counts
  binary identifiers without writing the save.
- `ck3-index-jomini-token-map` builds a save-version-matched map for the binary
  identifiers observed in one save by invoking an explicit Rakaly executable.
- `ck3-index-jomini-locate` performs a bounded full structural walk of one
  binary section and reports canonical paths and exact byte spans.
- `ck3-index-jomini-edit` creates hash-bound exact-path scalar edit plans and
  applies them copy-on-write to a new save file across all three canonical
  binary save layouts; a legacy unique-key command is retained for
  compatibility.

`rakaly/jomini` is a third-party parser whose name collides with Paradox's Jomini
engine; it is not Paradox engine source code.

It is intentionally isolated from the Go module:

- it is not imported by ck3-index;
- it does not use CGO or a Rust shared library;
- it is not built or bundled by the Go release process;
- the read and locate tools never modify a save;
- the token-map tool only creates a new map, and the editor only creates a new
  plan or save at a distinct, previously nonexistent output path.

The project is pinned to exactly `jomini 0.35.0` with default features disabled.
Only Jomini's `envelope` and required `serde` features are enabled. The original
oracle still uses only the low-level plaintext tape API; the companion owns the
additional envelope, ZIP, and streaming-token dependencies.

Rust 1.88 or newer is required. Although the upstream crate does not publish a
`rust-version`, Jomini 0.35.0 uses Rust 2024 let-chains (stabilized in 1.88) and
pointer APIs stabilized in 1.87; compiling it with Rust 1.85 fails before the
oracle itself is built.

## Output contract

Successful runs write `ck3-index-jomini-text-tape/v1` JSON to stdout. The output
contains:

- the pinned Jomini version and UTF-8 BOM flag;
- every tape token in deterministic tape order;
- object/array pairing indices and mixed-container flags;
- operator names and symbols;
- exact scalar bytes as lowercase hexadecimal plus an optional UTF-8 view.

Input paths, timestamps, platform separators, and error-display strings are not
part of successful JSON, so the same bytes produce the same document on every
platform. Exact bytes remain available even when a scalar is not valid UTF-8.

Jomini omits ordinary equals tokens from its compact tape, and its tape does not
preserve comments or source offsets. Those are expected representation
differences, not ck3-index parser defects.

## Usage

With a Rust toolchain installed:

```text
cargo test --manifest-path tools/jomini-oracle/Cargo.toml --locked
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked -- path/to/file.txt
type path\to\file.txt | cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked -- -

cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-save -- overview path/to/save.ck3
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-save -- find-key target_key path/to/save.ck3
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-save -- token-ids --section both path/to/save.ck3

cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-token-map -- from-rakaly --rakaly path/to/rakaly.exe --output path/to/version.tokens.txt path/to/save.ck3
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-locate -- locate-key --section gamestate --token-map path/to/version.tokens.txt field_name path/to/save.ck3
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-edit -- plan-scalar --section gamestate --token-map path/to/version.tokens.txt --raw-key 0x1234 --match-index 0 --expect quoted:old --value quoted:new --plan path/to/edit-plan.json path/to/input.ck3
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-edit -- plan-scalar --section gamestate --token-map path/to/version.tokens.txt --raw-key 0x1234 --path-file path/to/target.path.json --expect quoted:old --value quoted:new --plan path/to/edit-plan.json path/to/input.ck3
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-edit -- apply-plan --token-map path/to/version.tokens.txt --plan path/to/edit-plan.json path/to/input.ck3 path/to/new-output.ck3

# Legacy globally-unique-key mode:
cargo run --manifest-path tools/jomini-oracle/Cargo.toml --locked --bin ck3-index-jomini-edit -- set-scalar --section metadata --expect quoted:old --value quoted:new 0x1234 path/to/input.ck3 path/to/new-output.ck3
```

The committed `Cargo.lock` was generated by Cargo and keeps the development
oracle reproducible. Candidate output belongs under the ignored `actual/`
directory until it has been generated by this binary and reviewed against
ck3-index's normalized token stream. Reviewed fixture output lives in
`fixtures/golden/` and is enforced against canonical LF bytes by the integration
test. `.gitattributes` keeps those bytes stable across Windows worktrees.

## Save reader companion

`overview FILE` writes `ck3-index-jomini-save-overview/v1` JSON. It reports the
actual ZIP or uncompressed container detected by Jomini, header kind/version,
text or binary encoding, bounded metadata, and the gamestate's uncompressed size
hint. It retains and inspects at most 1 MiB of metadata, then reads one
additional byte only to detect truncation. It never scans the gamestate, so it
is safe as a quick first look at a large save. `scanned=false` and
`integrity_checked=false` are explicit in the output. Unknown header kinds and
headers that require ZIP but have no valid ZIP container are rejected instead
of being silently reinterpreted as plaintext.

`find-key KEY FILE` streams the gamestate and writes
`ck3-index-jomini-save-find-key/v1` JSON. It returns the exact decompressed key
end offset relative to the selected section, container depth, operator, and
bounded value preview. Useful options:

```text
--section metadata|gamestate|both
--limit 100
--max-bytes 268435456
--token-map path/to/tokens.txt
```

`token-ids FILE` writes `ck3-index-jomini-save-token-ids/v1` JSON and counts
every binary identifier token in the selected sections, including identifiers
used as values rather than field keys. It is the complete observed-id inventory
used by the token-map builder. It accepts `--section metadata|gamestate|both`
and the same bounded `--max-bytes` policy as `find-key`.

Text scans compare exact key bytes. Binary scans can search raw identifiers such
as `0x2d82` without a token map. Searching binary field names requires a
patch-matched map in Jomini's simple format:

```text
0x2d82 field_name
0x2d83 another_field
```

Without a matching map, the report retains raw identifiers and counts unresolved
identifier keys instead of pretending that the save has been interpreted.
Token maps are capped at 16 MiB, each line at 4 KiB, and each resolved name at
256 bytes. Scalar previews are capped at 256 bytes, result counts at 1,000 per
selected section, and the scanner uses a fixed 1 MiB streaming buffer. These
bounds also keep the buffered JSON document finite. Successful JSON never
contains the input path.

Each selected section has a 256 MiB decompressed scan budget by default.
`--max-bytes` can raise it explicitly up to 16 GiB. The reader consumes at most
that many bytes for parsing, plus one disclosed lookahead byte used only to tell
an exact-boundary EOF from truncation. `complete=false` and `stop_reason` report
byte-budget and match-limit stops; a bounded partial result must not be treated
as proof that a key is absent. Callers embedding this development CLI should
also impose their own wall-clock timeout.

A complete ZIP gamestate scan uses Jomini's CRC32 and declared-size verifier and
reports `integrity_checked=true`. Early-stop results, uncompressed sections, and
metadata report it as false. `syntax_checked=false` is also explicit: the
streaming finder rejects basic structural damage but is not a complete CK3 save
grammar validator. Text values such as `rgb { ... }` are preserved as headered
containers rather than being mislabeled as the scalar `rgb`.

For uncompressed saves, Jomini's low-level envelope reader exposes the complete
post-header body, including the metadata prefix. This companion deliberately
skips the header-declared metadata bytes before scanning or sizing `gamestate`,
so `metadata` and `gamestate` remain disjoint in its public reports.

## Version-matched token maps

`ck3-index-jomini-token-map from-rakaly --rakaly EXE --output MAP SAVE` reads a
binary save completely, inventories every observed identifier, constructs a
bounded synthetic probe, and asks the explicitly supplied Rakaly executable to
melt that probe with unknown identifiers treated as errors. It writes
`ck3-index-jomini-token-map/v1` JSON to stdout and publishes the map only when
Rakaly returned one unique name for every observed identifier. The report
includes source and map SHA-256 values, byte counts, mapping counts, and section
integrity state.

`SAVE` is opened read-only. `MAP` must not exist and is published through a
same-directory temporary file with no-clobber semantics. The Rakaly executable
is neither downloaded nor discovered implicitly by this tool. The source save's
byte length and SHA-256 must be identical before and after identifier
collection, so a concurrently changing input is rejected. Each launched Rakaly
process has a 60-second wall-clock limit in addition to bounded stdout/stderr;
the child is terminated on timeout.

The result is an **observed, version-matched map**, not a universal CK3 token
dictionary. A map that resolved every identifier in one CK3 patch/save may be
incomplete or wrong for another patch. Generate or verify a matching map again
when the CK3 version changes, and retain the reported source/map hashes with any
analysis artifact.

## Structural locator

`ck3-index-jomini-locate locate-key [OPTIONS] KEY SAVE` writes
`ck3-index-jomini-save-locate-key/v1` JSON for one binary metadata or gamestate
section. `KEY` may be a raw `0xNNNN` identifier or an exact name when a token map
is supplied. Unlike the streaming finder, it requires the selected section to
fit its complete-read and structural-walk budgets, then reports:

- the source and selected-section SHA-256 values;
- the token-map byte count, SHA-256, and observed-ID coverage;
- the complete match count, independently of the bounded returned result list;
- canonical raw-key/occurrence paths for repeated fields and anonymous items;
- exact key, equality, and value byte spans; and
- observed token/depth/memory-budget evidence.

The default section cap is 64 MiB and the hard cap is 256 MiB. Additional token,
depth, and estimated walk-memory limits account for dynamic text/hex strings and
ancestor-path cloning, preventing an apparently small hostile section from
causing an unbounded structural expansion. A ZIP gamestate is read through its
CRC/declared-size verifier. Named queries require the supplied map to resolve
every identifier observed in the selected section; raw queries may use a partial
map, but the report then marks coverage incomplete. Resolved display names are
retained for identifier and lookup scalar values as well as keys, while
canonical paths remain based only on raw identities.

## Hash-bound copy-on-write scalar editor

`plan-scalar` followed by `apply-plan` is the preferred editing workflow. The
planning stage validates the source, map, envelope, complete selected section,
and expected scalar; performs a bounded full structural walk; enumerates every
source-order occurrence of the requested raw 16-bit key; and resolves the
selection to a canonical raw path. A repeated key requires an explicit
selection. The plan is published atomically to a new path and contains the
source/header, selected-section, and exact token-map byte counts and SHA-256
values; complete observed-ID coverage; the raw-key match count and selection;
the canonical path, byte spans, and exact encoded expected scalar; the typed
replacement; the pinned ZIP-rebuild dependency/backend profile; and dry-run
output size and SHA-256 predictions. Its `plan_id` is the SHA-256 of the typed
plan body. It is a content-integrity identifier, not a signature, authorization,
or proof that a human approved the replacement.

### Selecting the target

`--match-index N` picks the Nth source-order occurrence of the raw key.
`--path-file FILE` instead names the target by the `canonical_raw_path` a read,
locate, or earlier plan report already produced, which removes the need to count
occurrences by hand. The two options are mutually exclusive, and selecting the
same field either way produces a byte-identical plan: the path is resolved back
to its source-order index before the plan body is built, and how the caller
chose is reported but never recorded in the plan.

The file uses a strict `ck3-index-jomini-raw-path/v1` document, capped at 64 KiB,
with no unknown fields allowed:

```json
{
  "format": "ck3-index-jomini-raw-path/v1",
  "section": "gamestate",
  "canonical_raw_path": [
    { "kind": "field", "key": { "kind": "id", "token": 10805 }, "occurrence": 0 },
    { "kind": "field", "key": { "kind": "id", "token": 3283 }, "occurrence": 0 }
  ]
}
```

`section` must match `--section`, and the last segment's key must match
`--raw-key`. A path that is absent, ambiguous, or malformed refuses without
publishing a plan. One can be built directly from a locate report:

```text
jq '{format:"ck3-index-jomini-raw-path/v1", section:"gamestate", canonical_raw_path:.matches[0].canonical_path}' locate.json > target.path.json
```

### Supported layouts

`plan-scalar` and `apply-plan` handle the three canonical binary layouts, in
either section:

| layout                | header kind | metadata            | gamestate             | rebuild strategy for that section |
|-----------------------|-------------|---------------------|-----------------------|-----------------------------------|
| `binary_uncompressed` | 1           | inline after header | inline after metadata | splice, no archive involved       |
| `unified_binary_zip`  | 3           | inline after header | ZIP entry `gamestate` | splice metadata / rebuild the ZIP |
| `split_binary_zip`    | 5           | ZIP entry `meta`    | ZIP entry `gamestate` | rebuild the ZIP                   |

The header kind alone selects the layout, and the container actually present
must agree with it. A `binary` header wrapped around a real ZIP, a
`unified_binary`/`split_binary` header with no valid archive, a `split_binary`
header that also declares an inline metadata length, a `unified_binary` archive
carrying a competing `meta` entry, and a `split_binary` archive with no `meta`
entry are all rejected rather than reinterpreted. Text header kinds are refused
outright, which is what keeps text sections out of every writing path.

Because jomini 0.35.0 only decompresses DEFLATE, a section stored with the ZIP
STORE method cannot be read and is refused. Unchanged STORE entries elsewhere in
the archive are still preserved byte-for-byte.

### Plan envelopes

New plans default to `ck3-index-jomini-save-edit-plan/v2`, which additionally
records:

- the resolved layout, container, and per-section storage location;
- the archive manifest — entry names, compression methods, CRC32 values, and
  compressed/uncompressed sizes — plus the archive's own offset, size, and
  SHA-256;
- `unmodified_regions`: an ordered, hashed list of every byte region the edit
  promises to reproduce exactly — the header bytes outside its eight-byte
  metadata-length field, the section that is not being edited, each archive
  entry other than the one carrying the edit, and, for an inline-metadata edit
  of a ZIP layout, the entire archive tail;
- `rebuild`: the strategy, the archive entry it touches, and the declared
  metadata length before and after.

`apply-plan` accepts v1 and v2. A v1 plan keeps its original semantics exactly
and still applies only to `unified_binary_zip` saves; `--plan-format v1` can
produce one, and is refused for the other two layouts. A v2 apply recomputes the
unmodified regions from the source, requires them to equal the plan's, rebuilds,
then recomputes them from the output and requires them to match again. The
layout is compared before the byte hashes, so a plan carried to a different
layout fails with that reason rather than a generic hash mismatch.

Canonical paths use raw field identities plus their parent-local occurrence
numbers, and container-local indices for anonymous items. Resolver display names
are audit metadata only and never path authority. `apply-plan` strictly parses
the bounded plan, verifies its ID and producer/path contract, requires the map
and source bytes to match their bound hashes, rechecks the section and complete
coverage, repeats the full structural walk, and requires the match count,
source-order selection, canonical path, spans, and expected encoded token to
identify exactly the planned scalar. It then rebuilds and rereads the candidate
and refuses publication unless its bytes match the dry-run prediction. A stale
source, different map, tampered or incompatible plan, moved/ambiguous target,
expectation mismatch, or existing output therefore fails closed.

Both commands edit exactly one binary scalar in either metadata or gamestate.
Supported scalar kinds are `quoted`, `unquoted`, `bool`, `u32`, `i32`, `u64`,
`i64`, `f32bits`, and `f64bits`; the replacement must retain the scalar kind and
must not be a no-op. `f32bits` and `f64bits` accept exactly 8 or 16 lowercase
hexadecimal digits, respectively, representing the token's raw little-endian
payload bytes. They do not accept decimal floating-point values, so no host
rounding, NaN normalization, or CK3 fixed-point interpretation is introduced.
Inline-metadata edits update only the header's eight-byte declared-length field
when needed and preserve the remaining header bytes and their original spelling;
in a ZIP layout they additionally require the archive tail to stay
byte-for-byte identical. ZIP-stored sections are consumed through the
CRC/size-verified entry, only the logical ZIP is rebuilt, and the result is
reopened and verified. Unchanged ZIP entries retain their raw compressed
payload; duplicate, encrypted, multi-disk, malformed, prelude/trailing-data, and
over-limit archives are rejected instead of being normalized silently.
Local/central flags, CRC/size claims, data descriptors, EOCD bounds, and
complete record layout must agree. Per-entry extra fields, comments, nonzero DOS
timestamps, and attributes are currently rejected rather than silently discarded
by the rebuilt archive. Every writing entrypoint validates this complete
manifest even for an inline-metadata-only edit.

Publication never opens the source writable. Plans and outputs use a
same-directory temporary file, durable flush and reread, then no-clobber
publication to a distinct path. The output directory is therefore a trusted
local execution boundary; this is not an adversarial multi-writer transaction
protocol. Editing is bounded to a 128 MiB source, a 64 MiB
selected section, 8,000,000 structural tokens, 512 container levels, 2,000,000
structural events, 16,000,000 cumulative cloned path segments, and 384 MiB of
cumulative dynamic allocation work for owned strings, map keys, and path clones.
A plan is limited to 1 MiB; a token map is limited to 16 MiB, with 4 KiB per line
and 256 bytes per name. The selected section must be traversed completely, and
the original ZIP gamestate must pass CRC/declared-size verification, before
either a plan or output is published.

This is not a general save editor. It does not support text save envelopes;
field/container insertion, removal, or replacement; scalar-kind changes;
non-canonical scalar encodings (including compact fixed5 tokens decoded by
Jomini as F64); batch or multi-target edits; gameplay object or cross-reference
validation; ironman/checksum guarantees; or proof that CK3 will load the result.
`set-scalar` remains available as a legacy shortcut for `unified_binary_zip`
saves only, and only when the raw key is globally unique in the selected
section; it is not the preferred workflow for repeated keys or other layouts.

## What "complete" means

There are three separate completeness claims:

1. **Byte and token completeness** means the selected metadata/gamestate bytes
   were consumed to EOF within the declared budget, containers were balanced,
   and a ZIP gamestate passed CRC/size verification.
2. **Identifier-name completeness** means every identifier observed in that
   specific save was resolved by a version-matched map. It does not claim that
   every identifier used by CK3, another save, or a later patch is known.
3. **CK3 semantic validity** would require game-specific object relationships,
   runtime rules, ironman/checksum meaning, and ultimately a CK3 load test. These
   tools do not provide that engine-level proof.

A successful full Rakaly melt is useful independent parser evidence for the
first two claims. It is not equivalent to CK3 accepting or correctly simulating
an edited save.

## Authority boundary

The text oracle can answer one narrow question: how Jomini 0.35.0 structures syntax
that both parsers claim to support. It is not an engine specification and does
not replace ck3-index behavior for:

- `types Namespace { ... }`, GUI inheritance, templates, blocks, or
  `blockoverride`;
- CK3 script math such as `@[ ... ]` and other CK3-only extensions;
- byte/line/column positions, error recovery, or comment handling;
- universal binary token resolution, ironman meaning, CK3 trigger evaluation,
  or an in-game save-load guarantee.

When a shared-subset differential check disagrees, retain both token streams and
classify the difference before changing production code. Existing CK3 fixtures
and indexed game/Mod examples remain authoritative for CK3-specific behavior.

## Upstream API references

The implementation uses only APIs documented for Jomini 0.35.0:

- [`TextTape::from_slice` and `TextTape::tokens`](https://docs.rs/jomini/0.35.0/jomini/struct.TextTape.html)
- [`TextToken`](https://docs.rs/jomini/0.35.0/jomini/enum.TextToken.html)
- [`Scalar::as_bytes`](https://docs.rs/jomini/0.35.0/jomini/struct.Scalar.html)
- [`text::Operator::name` and `symbol`](https://docs.rs/jomini/0.35.0/jomini/text/enum.Operator.html)
- [`envelope::JominiFile`](https://docs.rs/jomini/0.35.0/jomini/envelope/struct.JominiFile.html)
- [`text::TokenReader`](https://docs.rs/jomini/0.35.0/jomini/text/struct.TokenReader.html)
- [`binary::TokenReader`](https://docs.rs/jomini/0.35.0/jomini/binary/struct.TokenReader.html)
