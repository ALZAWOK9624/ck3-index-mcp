# Verification status

Verified status:

- Jomini API shape was checked against the published 0.35.0 documentation.
- The dependency is exactly pinned and has `default-features = false`; only the
  `envelope` and required `serde` features are enabled.
- Cargo generated and resolved the committed `Cargo.lock` to Jomini 0.35.0.
- Rust 1.85 was intentionally tried and rejected: Jomini itself uses let-chains
  stabilized in Rust 1.88 and pointer APIs stabilized in Rust 1.87. The package
  therefore declares Rust 1.88 as its actual minimum.
- Rust 1.88.0 compiled the pinned crate and the development binaries.
- The completed gates cover the original oracle, save-envelope reader,
  structural walker, hash helper, legacy unique-key editor, and the preferred
  hash-bound `plan-scalar`/`apply-plan` workflow. The fixture suite exercises
  canonical-LF goldens, stdin/file parity, text and binary saves, ZIP and
  uncompressed read envelopes, resolver/no-resolver behavior,
  metadata/gamestate separation, bounded output, malformed structures, CRC
  corruption, structural paths/spans, repeated-key selection, stale source and
  wrong-map rejection, plan-ID/body tampering, unknown and hostile plan fields,
  input/output aliasing, bit-exact F32/F64 replacement, malformed float-bit
  input, compact fixed5 refusal, ambiguous ZIP metadata, duplicate ZIP names,
  original header-spelling preservation, and no-clobber publication.
- `save_layout_cli` adds the layout matrix: all three canonical binary layouts
  against both sections, fixed- and variable-width scalars, STORE and DEFLATE
  extra entries surviving a rebuild byte-for-byte, a STORE-stored section being
  refused, inline metadata-length recomputation versus a split header staying at
  zero, path- and index-selection producing identical plans, strict raw-path
  rejections, header/container mismatches, v1 plans remaining unified-only, and
  cross-layout and cross-save plan refusal without publication.
- `cargo fmt --check` passes.
- `cargo clippy --all-targets --locked -- -D warnings` passes.
- The full Go module regression suite passes with `go test ./...`.
- The committed goldens were captured from the successfully compiled binary;
  none were inferred or hand-authored as upstream output.

## Real-save evidence

The current end-to-end experiment uses a read-only original and disposable
copies of one CK3 1.19.0.6 `unified_binary` save. This is evidence for that exact
format/version sample, not a compatibility claim for every CK3 save.

- The reader consumed the complete binary metadata and all 4,625,838
  decompressed gamestate bytes. The ZIP gamestate completed CRC and declared-size
  verification.
- The complete observed-token inventory contained 423 unique identifier IDs and
  246,725 identifier occurrences. A map produced through Rakaly resolved
  423/423 observed IDs; subsequent full metadata and gamestate scans reported no
  unresolved identifier keys.
- The structural locator fingerprints the 7,973-byte map, reports 423/423
  selected-gamestate coverage, retains resolved names for identifier values,
  and estimates the real gamestate walk at 217,920,936 bytes including dynamic
  strings and path cloning, below its 512 MiB refusal threshold.
- An independent Rakaly full melt of the unedited sample succeeded with unknown
  identifiers treated as errors. This confirms parser/resolver coverage for the
  sample, but it is not CK3 engine validation.
- The preferred two-stage workflow was exercised against repeated raw key
  `0x0cd3` (`localized_name`). The gamestate contained 554 source-order matches;
  planning selected zero-based `match_index=0` and recorded canonical raw path
  `0x2a35#0 / 0x2e5e#0 / u32:5998#0 / 0x0cd3#0`, rather than treating the
  resolved name as authority.
- The final 3,356-byte plan also bound the tool/path contract and pinned ZIP
  rebuild profile. It bound the 599,336-byte source SHA-256
  `4bc593d235ac3b8ceaf1f869ef1e3843476b51458538c5f1ae557a096be009a6`,
  the 4,625,838-byte gamestate SHA-256
  `786f3b47d0789ca8104bdc1ce3b98444d0cce4536727d1e0c6bfe60791119f7f`,
  and the exact 7,973-byte map SHA-256
  `3e150f92bb1ff2d52fd7d9e5bb237457ee2c3c339f69c04cf1c6082e07abff45`.
  Its ID was
  `sha256:8aa7e9124d325a4d84d7c92edc93aa13bb52b7c5592df5513c621235db7b7e66`
  and the serialized plan SHA-256 was
  `20dfa1b149c36608bd29d26606e1ebaca0dc03bbedc7f76af7bd979425a6edae`.
- Applying that plan changed the selected quoted scalar from raw UTF-8 bytes
  `e6b395e789b9e8b5ab` to
  `e88bb9e69e9ce8b7afe5be84e5ae9ee9aa8c`. The decompressed gamestate grew from
  4,625,838 to 4,625,847 bytes and the 569,055-byte output SHA-256 was
  `98f253913c2f4a61d82d946f2a454cf3f6bd8141159c32b1674cbf7cbbee9f4b`.
- Exact-path readback still found all 554 `0x0cd3` fields and exactly one
  replacement. The separate `u32:5803` object retained its original value, so
  the decompressed logical comparison confirmed only the selected target
  changed. The baseline source hash remained unchanged.
- The rebuilt output completed a full structural walk, retained 423/423 map
  coverage, and passed ZIP gamestate CRC/declared-size verification. Independent
  Rakaly CLI 0.8.19 readback melted the output successfully to 6,623,771 bytes
  and its full text diff against the baseline was exactly one removed line and
  one added line: the selected `localized_name` replacement. Replaying the same
  plan produced the identical output SHA-256; applying it to the edited source
  failed the stale-source gate and published no output.
- A second real-save plan/apply changed the unique metadata `0x29e6` quoted
  scalar from `谢赫巴达` to `苹果元数据计划`. The declared metadata length and
  section size changed from 34,072 to 34,081 bytes, the output SHA-256 was
  `d2e5cb9d39e311ee5131be560c24f87429c3b72562252f90bd2eee7d25472db6`,
  and the entire 565,240-byte embedded ZIP tail remained byte-for-byte identical.
- A bit-exact F32 plan selected zero-based match 0 of the three `age` fields by
  canonical raw path and replaced payload bytes `0ad7a33e` with `0000003f`.
  The section length and six-byte token span were unchanged. The 569,032-byte
  output SHA-256 was
  `d9c5614611b07761d21e660ebe8c143deeb31904b0f72daca1ebbe96648124ac`;
  deterministic replay matched it, the two unselected values were unchanged,
  and the Rakaly full-melt diff contained only `age=0.320000` ->
  `age=0.500000`.
- A bit-exact F64 plan selected zero-based match 0 of the 586 `income` fields
  and replaced payload bytes `1af2020000000000` with
  `1bf2020000000000`. The section length and ten-byte token span were unchanged.
  The 569,059-byte output SHA-256 was
  `adc2193bac66d9288e1e18645170cce0c7dbdc4269c2a215109468493a1b099e`;
  deterministic replay matched it, the next `income` value was unchanged, and
  the Rakaly full-melt diff contained only `income=1.9305` ->
  `income=1.93051`.
- Both float experiments used lowercase raw little-endian payload hex end to
  end. They performed no decimal parsing or host floating-point conversion,
  retained 423/423 token-map coverage, and passed the complete structural and
  ZIP integrity gates. Fixture tests separately reject wrong-width, non-hex,
  uppercase, cross-kind, no-op, and compact fixed5 inputs without publication.

## Layout coverage on real save bytes

The layout work was exercised on the same read-only CK3 1.19.0.6 sample. The
`unified_binary` original was re-enveloped into `binary_uncompressed` (4,659,934
bytes) and `split_binary_zip` (532,866 bytes) copies carrying the **same real
34,072-byte metadata and 4,625,838-byte gamestate**. Those two files are
fixtures, not saves CK3 wrote; they establish that the layout handling works on
genuine CK3 section content, not that CK3 emits or accepts those envelopes.

- Six positive cases ran end to end — three layouts × metadata and gamestate.
  Each one planned by `--match-index`, re-planned the same field by
  `--path-file`, and produced a byte-identical plan; applied; replayed to a
  byte-identical second output; and refused its own output as a stale source
  without publishing anything.
- Each case was checked independently of the tool's own reports: the untouched
  section was byte-identical, the edited section changed by exactly the nine-byte
  scalar delta, header bytes outside the metadata-length field were identical,
  and the published output matched the plan's dry-run SHA-256 prediction.
- The declared metadata length was recomputed for the four inline-metadata edits
  and left alone for the two ZIP-stored ones, where the split header stayed at
  zero. For `unified_binary` metadata the entire embedded ZIP stayed
  byte-for-byte identical; for both split edits the untouched entry kept its
  exact compressed payload and the entry order was preserved.
- Applying the `unified_binary_zip` plan to the `binary_uncompressed` and
  `split_binary_zip` copies failed on the layout gate and published no output.
- The v2 gamestate edit of the `unified_binary` sample reproduced the phase-1 v1
  artifact byte-for-byte: output SHA-256
  `98f253913c2f4a61d82d946f2a454cf3f6bd8141159c32b1674cbf7cbbee9f4b`, the value
  recorded above. The layout refactor therefore changed no output bytes on the
  path that phase 1 had already verified.
- Rakaly CLI 0.8.19 melted the edited `split_binary_zip` output and produced text
  identical to the melted `unified_binary` output, an independent confirmation
  that the split rebuild is semantically equivalent. It also melted the edited
  `binary_uncompressed` output; that melt differs from the others only by the
  inline metadata block, which an uncompressed save legitimately includes in its
  body. The melted gamestate diff against the unedited baseline was exactly one
  removed and one added line, the selected `localized_name`.
- All six outputs were re-read completely afterwards, with both sections
  reporting `complete=true` and ZIP-stored gamestates passing CRC/declared-size
  verification.

This verifies exact-path scalar replacement, deterministic plan/apply replay,
layout-aware rebuilding, and ZIP reconstruction for the tested CK3 1.19.0.6
sample and the two layouts derived from it. **No CK3 in-game load test was
performed**, so this is not engine-level acceptance proof and does not expand
the supported version boundaries in the README. The in-game load, pause,
re-save, and re-read loop remains the outstanding acceptance step for this
phase.

The editor additionally enforces a 128 MiB source cap, a 64 MiB selected-section
cap, an 8,000,000-token scan cap, a 512-level nesting cap, 2,000,000 structural
events, 16,000,000 cumulative cloned path segments, a 384 MiB cumulative dynamic
allocation-work cap, a 1 MiB plan cap, a 64 KiB raw-path-file cap, and a 16 MiB
token-map cap (4 KiB per line and 256 bytes per name). Its success report explicitly records
`section_scan_complete=true` and
`gamestate_integrity_checked=true`. Token-map generation imposes a 60-second
wall-clock limit on each Rakaly child and rejects a source whose byte length or
SHA-256 changes while identifier IDs are collected.

These checks establish complete byte/token traversal and observed-ID name
coverage for the sample. They do not establish universal CK3 token coverage,
interpret every gameplay relationship, validate ironman/checksum semantics, or
prove that CK3 itself will load and simulate an edited save.

Reproduce the verification with:

```text
cargo fmt --manifest-path tools/jomini-oracle/Cargo.toml -- --check
cargo test --manifest-path tools/jomini-oracle/Cargo.toml --locked
cargo clippy --manifest-path tools/jomini-oracle/Cargo.toml --all-targets --locked -- -D warnings
go test ./...
```

The test command includes the original oracle contract plus the save reader's
text, ZIP, binary, structural, legacy editing, exact-path plan/apply, and
failure-path fixtures. It does not invoke or modify the Go release build. If a
GNU toolchain is used from a non-ASCII workspace path, place its sysroot and Cargo
target on ASCII-safe paths; that is a host linker constraint, not a runtime
dependency.
