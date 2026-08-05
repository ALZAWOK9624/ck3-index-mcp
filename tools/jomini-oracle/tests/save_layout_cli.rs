//! Layout coverage for the hash-bound scalar editor.
//!
//! Every canonical binary layout is exercised against both logical sections, so
//! a regression that only affects, say, ZIP-stored metadata cannot hide behind
//! the `unified_binary` fixtures in `save_plan_cli`.

use ck3_index_jomini_oracle::{PathSegment, RawTokenIdentity, StructuralValue, walk_binary};
use flate2::{Compression, write::DeflateEncoder};
use jomini::{Scalar, binary::Token};
use serde_json::{Value, json};
use std::{
    fs,
    io::Write,
    path::{Path, PathBuf},
    process::{Command, Output},
    sync::atomic::{AtomicU64, Ordering},
};

static NEXT_TEMP: AtomicU64 = AtomicU64::new(0);

const ROOT_KEY: u16 = 0x1000;
const TARGET_KEY: u16 = 0x2000;
const NESTED_KEY: u16 = 0x3000;
const COUNT_KEY: u16 = 0x4000;
const TOKEN_MAP: &[u8] = b"0x1000 root\n0x2000 target\n0x3000 nested\n0x4000 count\n";

const META_FIRST: &[u8] = b"meta-first";
const META_SECOND: &[u8] = b"meta-second";
const GAME_FIRST: &[u8] = b"game-first";
const GAME_SECOND: &[u8] = b"game-second";
const LONGER_REPLACEMENT: &[u8] = b"replacement-that-is-longer";

const STORE: u16 = 0;
const DEFLATE: u16 = 8;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Layout {
    BinaryUncompressed,
    UnifiedZip,
    SplitZip,
}

impl Layout {
    const ALL: [Self; 3] = [Self::BinaryUncompressed, Self::UnifiedZip, Self::SplitZip];

    const fn header_kind(self) -> u16 {
        match self {
            Self::BinaryUncompressed => 1,
            Self::UnifiedZip => 3,
            Self::SplitZip => 5,
        }
    }

    const fn name(self) -> &'static str {
        match self {
            Self::BinaryUncompressed => "binary_uncompressed",
            Self::UnifiedZip => "unified_binary_zip",
            Self::SplitZip => "split_binary_zip",
        }
    }

    const fn strategy(self, section: &str) -> &'static str {
        match (self, section.as_bytes()[0]) {
            // 'm' for metadata, 'g' for gamestate.
            (Self::SplitZip, b'm') => "rebuild_zip_entry",
            (_, b'm') => "splice_inline_metadata",
            (Self::BinaryUncompressed, _) => "splice_inline_gamestate",
            _ => "rebuild_zip_entry",
        }
    }
}

fn save_editor() -> Command {
    Command::new(env!("CARGO_BIN_EXE_ck3-index-jomini-edit"))
}

struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        let sequence = NEXT_TEMP.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "ck3-index-jomini-layout-test-{}-{sequence}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("temporary directory should be creatable");
        Self(path)
    }

    fn path(&self) -> &Path {
        &self.0
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn run(args: &[&str]) -> Output {
    save_editor()
        .args(args)
        .output()
        .expect("save editor should start")
}

fn parse_success(output: Output) -> Value {
    assert!(output.status.success(), "{output:?}");
    assert!(output.stderr.is_empty(), "{output:?}");
    serde_json::from_slice(&output.stdout).expect("stdout should be valid JSON")
}

fn assert_failure(output: Output, needle: &str) {
    assert!(!output.status.success(), "{output:?}");
    assert!(output.stdout.is_empty(), "{output:?}");
    assert!(
        !String::from_utf8_lossy(&output.stderr).contains("panicked"),
        "untrusted input must be rejected without a process panic: {output:?}"
    );
    assert!(
        String::from_utf8_lossy(&output.stderr).contains(needle),
        "{output:?}"
    );
}

fn path_text(path: &Path) -> &str {
    path.to_str().expect("temporary path should be Unicode")
}

fn encode(tokens: &[Token<'_>]) -> Vec<u8> {
    let mut output = Vec::new();
    for token in tokens {
        token.write(&mut output).expect("token should encode");
    }
    output
}

/// `root = { target = FIRST nested = { target = SECOND } count = 7 }`
fn nested_section(first: &[u8], second: &[u8]) -> Vec<u8> {
    encode(&[
        Token::Id(ROOT_KEY),
        Token::Equal,
        Token::Open,
        Token::Id(TARGET_KEY),
        Token::Equal,
        Token::Quoted(Scalar::new(first)),
        Token::Id(NESTED_KEY),
        Token::Equal,
        Token::Open,
        Token::Id(TARGET_KEY),
        Token::Equal,
        Token::Quoted(Scalar::new(second)),
        Token::Close,
        Token::Id(COUNT_KEY),
        Token::Equal,
        Token::U32(7),
        Token::Close,
    ])
}

fn metadata_section() -> Vec<u8> {
    nested_section(META_FIRST, META_SECOND)
}

fn gamestate_section() -> Vec<u8> {
    nested_section(GAME_FIRST, GAME_SECOND)
}

fn header(kind: u16, metadata_len: usize) -> Vec<u8> {
    let value = format!("SAV01{kind:02x}deadbeef{metadata_len:08x}\n");
    assert_eq!(value.len(), 24);
    value.into_bytes()
}

struct ZipEntrySpec<'a> {
    name: &'a [u8],
    data: &'a [u8],
    method: u16,
}

fn build_save(layout: Layout, metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    build_save_with_extras(layout, metadata, gamestate, &[])
}

fn build_save_with_extras(
    layout: Layout,
    metadata: &[u8],
    gamestate: &[u8],
    extras: &[ZipEntrySpec<'_>],
) -> Vec<u8> {
    match layout {
        Layout::BinaryUncompressed => {
            assert!(extras.is_empty(), "an uncompressed save has no ZIP entries");
            let mut output = header(layout.header_kind(), metadata.len());
            output.extend_from_slice(metadata);
            output.extend_from_slice(gamestate);
            output
        }
        Layout::UnifiedZip => {
            let mut entries = vec![ZipEntrySpec {
                name: b"gamestate",
                data: gamestate,
                method: DEFLATE,
            }];
            entries.extend(extras.iter().map(|extra| ZipEntrySpec { ..*extra }));
            let mut output = header(layout.header_kind(), metadata.len());
            output.extend_from_slice(metadata);
            output.extend_from_slice(&zip_entries(&entries));
            output
        }
        Layout::SplitZip => {
            let mut entries = vec![
                ZipEntrySpec {
                    name: b"meta",
                    data: metadata,
                    method: DEFLATE,
                },
                ZipEntrySpec {
                    name: b"gamestate",
                    data: gamestate,
                    method: DEFLATE,
                },
            ];
            entries.extend(extras.iter().map(|extra| ZipEntrySpec { ..*extra }));
            let mut output = header(layout.header_kind(), 0);
            output.extend_from_slice(&zip_entries(&entries));
            output
        }
    }
}

fn zip_entries(entries: &[ZipEntrySpec<'_>]) -> Vec<u8> {
    let encoded: Vec<_> = entries
        .iter()
        .map(|entry| {
            let payload = if entry.method == DEFLATE {
                let mut encoder = DeflateEncoder::new(Vec::new(), Compression::default());
                encoder.write_all(entry.data).unwrap();
                encoder.finish().unwrap()
            } else {
                entry.data.to_vec()
            };
            (
                entry.name.to_vec(),
                entry.data.len(),
                crc32(entry.data),
                payload,
                entry.method,
            )
        })
        .collect();
    let mut output = Vec::new();
    let mut local_offsets = Vec::new();
    for (name, data_len, crc, compressed, method) in &encoded {
        local_offsets.push(output.len() as u32);
        put_u32(&mut output, 0x0403_4b50);
        put_u16(&mut output, 20);
        put_u16(&mut output, 0);
        put_u16(&mut output, *method);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u32(&mut output, *crc);
        put_u32(&mut output, compressed.len() as u32);
        put_u32(&mut output, *data_len as u32);
        put_u16(&mut output, name.len() as u16);
        put_u16(&mut output, 0);
        output.extend_from_slice(name);
        output.extend_from_slice(compressed);
    }

    let central_offset = output.len() as u32;
    for ((name, data_len, crc, compressed, method), local_offset) in
        encoded.iter().zip(local_offsets)
    {
        put_u32(&mut output, 0x0201_4b50);
        put_u16(&mut output, 20);
        put_u16(&mut output, 20);
        put_u16(&mut output, 0);
        put_u16(&mut output, *method);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u32(&mut output, *crc);
        put_u32(&mut output, compressed.len() as u32);
        put_u32(&mut output, *data_len as u32);
        put_u16(&mut output, name.len() as u16);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u32(&mut output, 0);
        put_u32(&mut output, local_offset);
        output.extend_from_slice(name);
    }

    let central_size = output.len() as u32 - central_offset;
    put_u32(&mut output, 0x0605_4b50);
    put_u16(&mut output, 0);
    put_u16(&mut output, 0);
    put_u16(&mut output, encoded.len() as u16);
    put_u16(&mut output, encoded.len() as u16);
    put_u32(&mut output, central_size);
    put_u32(&mut output, central_offset);
    put_u16(&mut output, 0);
    output
}

fn put_u16(output: &mut Vec<u8>, value: u16) {
    output.extend_from_slice(&value.to_le_bytes());
}

fn put_u32(output: &mut Vec<u8>, value: u32) {
    output.extend_from_slice(&value.to_le_bytes());
}

fn crc32(data: &[u8]) -> u32 {
    let mut crc = u32::MAX;
    for byte in data {
        crc ^= u32::from(*byte);
        for _ in 0..8 {
            let mask = (crc & 1).wrapping_neg();
            crc = (crc >> 1) ^ (0xedb8_8320 & mask);
        }
    }
    !crc
}

fn declared_metadata_len(save: &[u8]) -> u64 {
    let text = std::str::from_utf8(&save[15..23]).expect("length field should be ASCII");
    u64::from_str_radix(text, 16).expect("length field should be hexadecimal")
}

/// Every `target` value in a section, in source order.
fn target_values(section: &[u8]) -> Vec<Vec<u8>> {
    walk_binary(section)
        .expect("binary section structure should parse")
        .events
        .into_iter()
        .filter_map(|event| {
            let is_target = event
                .key
                .as_ref()
                .is_some_and(|key| key.raw == RawTokenIdentity::Id { token: TARGET_KEY });
            match (is_target, event.value) {
                (
                    true,
                    StructuralValue::Scalar {
                        raw: RawTokenIdentity::Text { bytes_hex, .. },
                        ..
                    },
                ) => Some(decode_hex(&bytes_hex)),
                _ => None,
            }
        })
        .collect()
}

fn decode_hex(value: &str) -> Vec<u8> {
    value
        .as_bytes()
        .chunks(2)
        .map(|pair| u8::from_str_radix(std::str::from_utf8(pair).unwrap(), 16).expect("hex byte"))
        .collect()
}

struct Fixture {
    _dir: TempDir,
    dir: PathBuf,
    save: PathBuf,
    map: PathBuf,
    source: Vec<u8>,
}

fn fixture(layout: Layout) -> Fixture {
    fixture_with_extras(layout, &[])
}

fn fixture_with_extras(layout: Layout, extras: &[ZipEntrySpec<'_>]) -> Fixture {
    let dir = TempDir::new();
    let source = build_save_with_extras(layout, &metadata_section(), &gamestate_section(), extras);
    let save = dir.path().join("source.ck3");
    let map = dir.path().join("tokens.txt");
    fs::write(&save, &source).unwrap();
    fs::write(&map, TOKEN_MAP).unwrap();
    Fixture {
        dir: dir.path().to_path_buf(),
        _dir: dir,
        save,
        map,
        source,
    }
}

/// Plans the nested `target` replacement for one section.
fn plan_nested(fixture: &Fixture, section: &str, plan: &Path, extra: &[&str]) -> Output {
    let expected = format!(
        "quoted:{}",
        std::str::from_utf8(if section == "metadata" {
            META_SECOND
        } else {
            GAME_SECOND
        })
        .unwrap()
    );
    let replacement = format!(
        "quoted:{}",
        std::str::from_utf8(LONGER_REPLACEMENT).unwrap()
    );
    let mut args = vec![
        "plan-scalar",
        "--section",
        section,
        "--token-map",
        path_text(&fixture.map),
        "--raw-key",
        "0x2000",
        "--expect",
        &expected,
        "--value",
        &replacement,
        "--plan",
        path_text(plan),
    ];
    args.extend_from_slice(extra);
    args.push(path_text(&fixture.save));
    run(&args)
}

fn apply(fixture: &Fixture, plan: &Path, output: &Path) -> Output {
    run(&[
        "apply-plan",
        "--token-map",
        path_text(&fixture.map),
        "--plan",
        path_text(plan),
        path_text(&fixture.save),
        path_text(output),
    ])
}

#[test]
fn every_layout_and_section_round_trips_through_plan_and_apply() {
    for layout in Layout::ALL {
        for section in ["metadata", "gamestate"] {
            let fixture = fixture(layout);
            let plan = fixture.dir.join(format!("{section}.plan.json"));
            let plan_report = parse_success(plan_nested(
                &fixture,
                section,
                &plan,
                &["--match-index", "1"],
            ));

            assert_eq!(plan_report["layout"], layout.name(), "{layout:?}/{section}");
            assert_eq!(
                plan_report["plan_schema"],
                "ck3-index-jomini-save-edit-plan/v2"
            );
            assert_eq!(plan_report["selected_by"], "match_index");
            assert_eq!(
                plan_report["rebuild"]["strategy"],
                layout.strategy(section),
                "{layout:?}/{section}"
            );
            assert_eq!(plan_report["selection"]["raw_key_match_count"], 2);

            let stored_in_zip = plan_report["rebuild"]["zip_entry"].is_string();
            assert_eq!(
                plan_report["section"]["integrity_checked"], stored_in_zip,
                "{layout:?}/{section}"
            );

            let output = fixture.dir.join(format!("{section}.edited.ck3"));
            let apply_report = parse_success(apply(&fixture, &plan, &output));
            assert_eq!(apply_report["complete"], true);
            assert_eq!(apply_report["layout"], layout.name());
            // The gamestate is read on every edit, so its CRC verification
            // depends on where it lives, not on which section was edited.
            assert_eq!(
                apply_report["gamestate_integrity_checked"],
                layout != Layout::BinaryUncompressed,
                "{layout:?}/{section}"
            );
            assert_eq!(apply_report["unmodified_regions_verified"], true);
            assert_eq!(
                apply_report["unmodified_regions"], plan_report["unmodified_regions"],
                "{layout:?}/{section}"
            );

            let edited = fs::read(&output).expect("output should be published");
            assert_eq!(
                fs::read(&fixture.save).unwrap(),
                fixture.source,
                "the source must never be modified"
            );
            assert_ne!(edited, fixture.source);

            // Only the nested occurrence of the edited section changed.
            let (edited_metadata, edited_gamestate) = read_sections(layout, &edited);
            let (expected_first, expected_untouched) = if section == "metadata" {
                (META_FIRST.to_vec(), gamestate_section())
            } else {
                (GAME_FIRST.to_vec(), metadata_section())
            };
            let (changed, untouched) = if section == "metadata" {
                (&edited_metadata, &edited_gamestate)
            } else {
                (&edited_gamestate, &edited_metadata)
            };
            assert_eq!(
                target_values(changed),
                vec![expected_first, LONGER_REPLACEMENT.to_vec()],
                "{layout:?}/{section}"
            );
            assert_eq!(untouched, &expected_untouched, "{layout:?}/{section}");

            // Replaying the same plan is byte-for-byte deterministic.
            let replay = fixture.dir.join(format!("{section}.replay.ck3"));
            parse_success(apply(&fixture, &plan, &replay));
            assert_eq!(fs::read(&replay).unwrap(), edited);

            // The plan is bound to the original bytes, so it cannot be reused
            // against its own output.
            let stale = fixture.dir.join(format!("{section}.stale.ck3"));
            fs::write(fixture.dir.join("edited-source.ck3"), &edited).unwrap();
            assert_failure(
                run(&[
                    "apply-plan",
                    "--token-map",
                    path_text(&fixture.map),
                    "--plan",
                    path_text(&plan),
                    path_text(&fixture.dir.join("edited-source.ck3")),
                    path_text(&stale),
                ]),
                "differ from the edit plan",
            );
            assert!(!stale.exists());
        }
    }
}

/// Reads both sections back out of a save without going through the editor.
fn read_sections(layout: Layout, save: &[u8]) -> (Vec<u8>, Vec<u8>) {
    let declared = declared_metadata_len(save) as usize;
    let header_len = 24;
    match layout {
        Layout::BinaryUncompressed => (
            save[header_len..header_len + declared].to_vec(),
            save[header_len + declared..].to_vec(),
        ),
        Layout::UnifiedZip => (
            save[header_len..header_len + declared].to_vec(),
            read_zip_entry(save, "gamestate"),
        ),
        Layout::SplitZip => {
            assert_eq!(declared, 0, "split metadata never has a declared length");
            (
                read_zip_entry(save, "meta"),
                read_zip_entry(save, "gamestate"),
            )
        }
    }
}

fn read_zip_entry(save: &[u8], name: &str) -> Vec<u8> {
    use jomini::envelope::{JominiFile, JominiFileKind};
    use std::io::Read;

    let file = JominiFile::from_slice(save).expect("save should parse");
    let JominiFileKind::Zip(zip) = file.kind() else {
        panic!("fixture must be a ZIP save")
    };
    let mut reader = zip
        .read_entry_verified(name)
        .expect("entry should open and verify");
    let mut bytes = Vec::new();
    reader.read_to_end(&mut bytes).expect("entry should read");
    bytes
}

#[test]
fn inline_metadata_length_is_recomputed_and_split_metadata_length_stays_zero() {
    for layout in Layout::ALL {
        let fixture = fixture(layout);
        let plan = fixture.dir.join("metadata.plan.json");
        let plan_report = parse_success(plan_nested(
            &fixture,
            "metadata",
            &plan,
            &["--match-index", "1"],
        ));
        let output = fixture.dir.join("metadata.edited.ck3");
        parse_success(apply(&fixture, &plan, &output));
        let edited = fs::read(&output).unwrap();

        let before = declared_metadata_len(&fixture.source);
        let after = declared_metadata_len(&edited);
        let rebuild = &plan_report["rebuild"];
        assert_eq!(rebuild["header_metadata_len_before"], before);
        assert_eq!(rebuild["header_metadata_len_after"], after);
        match layout {
            Layout::SplitZip => {
                assert_eq!(before, 0);
                assert_eq!(after, 0);
                assert_eq!(rebuild["header_metadata_len_rewritten"], false);
            }
            _ => {
                let growth = LONGER_REPLACEMENT.len() - META_SECOND.len();
                assert_eq!(after, before + growth as u64, "{layout:?}");
                assert_eq!(rebuild["header_metadata_len_rewritten"], true);
            }
        }
    }
}

#[test]
fn fixed_width_scalar_edits_keep_every_length_and_offset() {
    for layout in Layout::ALL {
        for section in ["metadata", "gamestate"] {
            let fixture = fixture(layout);
            let plan = fixture.dir.join("count.plan.json");
            parse_success(run(&[
                "plan-scalar",
                "--section",
                section,
                "--token-map",
                path_text(&fixture.map),
                "--raw-key",
                "0x4000",
                "--expect",
                "u32:7",
                "--value",
                "u32:9",
                "--plan",
                path_text(&plan),
                path_text(&fixture.save),
            ]));
            let output = fixture.dir.join("count.edited.ck3");
            let report = parse_success(apply(&fixture, &plan, &output));
            let edited = fs::read(&output).unwrap();

            assert_eq!(report["section"], section);
            assert_eq!(
                declared_metadata_len(&edited),
                declared_metadata_len(&fixture.source),
                "{layout:?}/{section}"
            );
            assert_eq!(
                report["source_value_span"], report["output_value_span"],
                "a fixed-width replacement must not move the value"
            );
            if layout == Layout::BinaryUncompressed {
                assert_eq!(edited.len(), fixture.source.len());
            }
        }
    }
}

fn deflate(data: &[u8]) -> Vec<u8> {
    let mut encoder = DeflateEncoder::new(Vec::new(), Compression::default());
    encoder.write_all(data).unwrap();
    encoder.finish().unwrap()
}

fn contains(haystack: &[u8], needle: &[u8]) -> bool {
    haystack
        .windows(needle.len())
        .any(|window| window == needle)
}

/// A STORE-compressed *section* cannot be read at all: jomini 0.35.0 only
/// decompresses DEFLATE. The refusal is asserted so the boundary stays visible
/// if that ever changes.
#[test]
fn a_stored_section_entry_is_refused_rather_than_misread() {
    let dir = TempDir::new();
    let map = dir.path().join("tokens.txt");
    fs::write(&map, TOKEN_MAP).unwrap();
    let gamestate = gamestate_section();
    let mut source = header(3, metadata_section().len());
    source.extend_from_slice(&metadata_section());
    source.extend_from_slice(&zip_entries(&[ZipEntrySpec {
        name: b"gamestate",
        data: &gamestate,
        method: STORE,
    }]));
    let save = dir.path().join("stored-gamestate.ck3");
    let plan = dir.path().join("stored.plan.json");
    fs::write(&save, &source).unwrap();

    assert_failure(
        run(&[
            "plan-scalar",
            "--section",
            "gamestate",
            "--token-map",
            path_text(&map),
            "--raw-key",
            "0x4000",
            "--expect",
            "u32:7",
            "--value",
            "u32:9",
            "--plan",
            path_text(&plan),
            path_text(&save),
        ]),
        "unsupported compression",
    );
    assert!(!plan.exists());
    assert_eq!(fs::read(&save).unwrap(), source);
}

#[test]
fn stored_and_deflated_extra_entries_survive_a_gamestate_rebuild() {
    let stored = b"stored-entry-payload".to_vec();
    let deflated = b"deflated-entry-payload-deflated-entry-payload".to_vec();
    for layout in [Layout::UnifiedZip, Layout::SplitZip] {
        let fixture = fixture_with_extras(
            layout,
            &[
                ZipEntrySpec {
                    name: b"extra_stored",
                    data: &stored,
                    method: STORE,
                },
                ZipEntrySpec {
                    name: b"extra_deflated",
                    data: &deflated,
                    method: DEFLATE,
                },
            ],
        );
        let plan = fixture.dir.join("gamestate.plan.json");
        let plan_report = parse_success(plan_nested(
            &fixture,
            "gamestate",
            &plan,
            &["--match-index", "1"],
        ));

        let region_ids: Vec<&str> = plan_report["unmodified_regions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|region| region["id"].as_str().unwrap())
            .collect();
        assert!(
            region_ids.contains(&"zip_entry_compressed:extra_stored"),
            "{region_ids:?}"
        );
        assert!(
            region_ids.contains(&"zip_entry_compressed:extra_deflated"),
            "{region_ids:?}"
        );
        assert!(!region_ids.iter().any(|id| id.ends_with(":gamestate")));

        let output = fixture.dir.join("gamestate.edited.ck3");
        parse_success(apply(&fixture, &plan, &output));
        let edited = fs::read(&output).unwrap();

        // Unchanged entries keep their exact stored payload, whatever their
        // compression method was; the edited entry is the only one recompressed.
        assert!(contains(&edited, &stored), "{layout:?}: STORE payload lost");
        assert!(
            contains(&edited, &deflate(&deflated)),
            "{layout:?}: DEFLATE payload was recompressed"
        );
        assert_eq!(read_zip_entry(&edited, "extra_deflated"), deflated);
        assert!(!contains(&edited, GAME_SECOND));
    }
}

fn write_path_file(path: &Path, section: &str, canonical: &Value) {
    let document = json!({
        "format": "ck3-index-jomini-raw-path/v1",
        "section": section,
        "canonical_raw_path": canonical,
    });
    fs::write(path, serde_json::to_vec_pretty(&document).unwrap()).unwrap();
}

#[test]
fn path_selection_and_index_selection_produce_identical_plans() {
    for layout in Layout::ALL {
        for section in ["metadata", "gamestate"] {
            let fixture = fixture(layout);
            let by_index = fixture.dir.join("by-index.plan.json");
            let index_report = parse_success(plan_nested(
                &fixture,
                section,
                &by_index,
                &["--match-index", "1"],
            ));

            let path_file = fixture.dir.join("target.path.json");
            write_path_file(
                &path_file,
                section,
                &index_report["target"]["canonical_raw_path"],
            );
            let by_path = fixture.dir.join("by-path.plan.json");
            let path_report = parse_success(plan_nested(
                &fixture,
                section,
                &by_path,
                &["--path-file", path_text(&path_file)],
            ));

            assert_eq!(path_report["selected_by"], "canonical_raw_path");
            assert_eq!(index_report["selected_by"], "match_index");
            assert_eq!(path_report["plan_id"], index_report["plan_id"]);
            assert_eq!(
                fs::read(&by_path).unwrap(),
                fs::read(&by_index).unwrap(),
                "{layout:?}/{section}: the same target must yield the same plan"
            );
            assert_eq!(
                path_report["selection"]["selected_match_index"],
                index_report["selection"]["selected_match_index"]
            );
        }
    }
}

#[test]
fn raw_path_files_are_strictly_validated_and_never_publish_on_failure() {
    let fixture = fixture(Layout::UnifiedZip);
    let reference = fixture.dir.join("reference.plan.json");
    let report = parse_success(plan_nested(
        &fixture,
        "gamestate",
        &reference,
        &["--match-index", "1"],
    ));
    let canonical = report["target"]["canonical_raw_path"].clone();

    let root_only =
        json!([{ "kind": "field", "key": { "kind": "id", "token": ROOT_KEY }, "occurrence": 0 }]);
    let unknown_occurrence = json!([
        { "kind": "field", "key": { "kind": "id", "token": ROOT_KEY }, "occurrence": 0 },
        { "kind": "field", "key": { "kind": "id", "token": NESTED_KEY }, "occurrence": 0 },
        { "kind": "field", "key": { "kind": "id", "token": TARGET_KEY }, "occurrence": 9 },
    ]);

    let cases: Vec<(&str, Value, &str)> = vec![
        (
            "wrong-format",
            json!({
                "format": "ck3-index-jomini-raw-path/v99",
                "section": "gamestate",
                "canonical_raw_path": canonical,
            }),
            "unsupported raw path format",
        ),
        (
            "wrong-section",
            json!({
                "format": "ck3-index-jomini-raw-path/v1",
                "section": "metadata",
                "canonical_raw_path": canonical,
            }),
            "--section is gamestate",
        ),
        (
            "unknown-field",
            json!({
                "format": "ck3-index-jomini-raw-path/v1",
                "section": "gamestate",
                "canonical_raw_path": canonical,
                "attacker_controlled": true,
            }),
            "unknown field",
        ),
        (
            "empty-path",
            json!({
                "format": "ck3-index-jomini-raw-path/v1",
                "section": "gamestate",
                "canonical_raw_path": [],
            }),
            "raw path is empty",
        ),
        (
            "wrong-terminal-key",
            json!({
                "format": "ck3-index-jomini-raw-path/v1",
                "section": "gamestate",
                "canonical_raw_path": root_only,
            }),
            "does not end at --raw-key",
        ),
        (
            "absent-path",
            json!({
                "format": "ck3-index-jomini-raw-path/v1",
                "section": "gamestate",
                "canonical_raw_path": unknown_occurrence,
            }),
            "was not found",
        ),
    ];

    for (label, document, needle) in cases {
        let path_file = fixture.dir.join(format!("{label}.path.json"));
        let plan = fixture.dir.join(format!("{label}.plan.json"));
        fs::write(&path_file, serde_json::to_vec_pretty(&document).unwrap()).unwrap();
        assert_failure(
            plan_nested(
                &fixture,
                "gamestate",
                &plan,
                &["--path-file", path_text(&path_file)],
            ),
            needle,
        );
        assert!(!plan.exists(), "{label} must not publish a plan");
    }

    let oversized = fixture.dir.join("oversized.path.json");
    fs::write(&oversized, vec![b' '; 64 * 1024 + 1]).unwrap();
    let oversized_plan = fixture.dir.join("oversized.plan.json");
    assert_failure(
        plan_nested(
            &fixture,
            "gamestate",
            &oversized_plan,
            &["--path-file", path_text(&oversized)],
        ),
        "raw path file exceeds",
    );
    assert!(!oversized_plan.exists());

    let both = fixture.dir.join("both.plan.json");
    let usable = fixture.dir.join("usable.path.json");
    write_path_file(
        &usable,
        "gamestate",
        &report["target"]["canonical_raw_path"],
    );
    assert_failure(
        plan_nested(
            &fixture,
            "gamestate",
            &both,
            &["--match-index", "1", "--path-file", path_text(&usable)],
        ),
        "mutually exclusive",
    );
    assert!(!both.exists());
    assert_eq!(fs::read(&fixture.save).unwrap(), fixture.source);
}

#[test]
fn plans_never_transfer_across_layouts_or_saves() {
    let unified = fixture(Layout::UnifiedZip);
    let plan = unified.dir.join("gamestate.plan.json");
    parse_success(plan_nested(
        &unified,
        "gamestate",
        &plan,
        &["--match-index", "1"],
    ));

    for layout in [Layout::BinaryUncompressed, Layout::SplitZip] {
        let other = fixture(layout);
        let output = other.dir.join("cross-layout.ck3");
        assert_failure(
            run(&[
                "apply-plan",
                "--token-map",
                path_text(&other.map),
                "--plan",
                path_text(&plan),
                path_text(&other.save),
                path_text(&output),
            ]),
            "edit plan targets a unified_binary_zip save",
        );
        assert!(!output.exists());
        assert_eq!(fs::read(&other.save).unwrap(), other.source);
    }

    // A different save of the same layout still fails, on bytes rather than
    // on layout.
    let sibling = unified.dir.join("sibling.ck3");
    fs::write(
        &sibling,
        build_save(
            Layout::UnifiedZip,
            &metadata_section(),
            &nested_section(GAME_FIRST, b"different-second"),
        ),
    )
    .unwrap();
    let output = unified.dir.join("cross-save.ck3");
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&unified.map),
            "--plan",
            path_text(&plan),
            path_text(&sibling),
            path_text(&output),
        ]),
        "differ from the edit plan",
    );
    assert!(!output.exists());
}

#[test]
fn v1_plans_stay_available_for_unified_saves_only() {
    let unified = fixture(Layout::UnifiedZip);
    let plan = unified.dir.join("v1.plan.json");
    let report = parse_success(plan_nested(
        &unified,
        "gamestate",
        &plan,
        &["--match-index", "1", "--plan-format", "v1"],
    ));
    assert_eq!(report["plan_schema"], "ck3-index-jomini-save-edit-plan/v1");

    let document: Value = serde_json::from_slice(&fs::read(&plan).unwrap()).unwrap();
    assert_eq!(document["schema"], "ck3-index-jomini-save-edit-plan/v1");
    assert!(document["body"]["unmodified_regions"].is_null());

    let output = unified.dir.join("v1-output.ck3");
    let apply_report = parse_success(apply(&unified, &plan, &output));
    assert_eq!(
        apply_report["plan_schema"],
        "ck3-index-jomini-save-edit-plan/v1"
    );
    assert_eq!(apply_report["unmodified_regions_verified"], true);
    assert!(output.exists());

    for layout in [Layout::BinaryUncompressed, Layout::SplitZip] {
        let other = fixture(layout);
        let refused = other.dir.join("refused.plan.json");
        assert_failure(
            plan_nested(
                &other,
                "gamestate",
                &refused,
                &["--match-index", "1", "--plan-format", "v1"],
            ),
            "only describes unified_binary_zip saves",
        );
        assert!(!refused.exists());
    }
}

#[test]
fn a_header_that_disagrees_with_its_container_is_refused() {
    let dir = TempDir::new();
    let map = dir.path().join("tokens.txt");
    fs::write(&map, TOKEN_MAP).unwrap();
    let metadata = metadata_section();
    let gamestate = gamestate_section();

    // A binary header wrapped around a real ZIP.
    let mut zip_behind_binary = header(1, metadata.len());
    zip_behind_binary.extend_from_slice(&metadata);
    zip_behind_binary.extend_from_slice(&zip_entries(&[ZipEntrySpec {
        name: b"gamestate",
        data: &gamestate,
        method: DEFLATE,
    }]));

    // A unified header with no ZIP at all.
    let mut unified_without_zip = header(3, metadata.len());
    unified_without_zip.extend_from_slice(&metadata);
    unified_without_zip.extend_from_slice(&gamestate);

    // A split header that also claims inline metadata.
    let mut split_with_inline_len = header(5, metadata.len());
    split_with_inline_len.extend_from_slice(&zip_entries(&[
        ZipEntrySpec {
            name: b"meta",
            data: &metadata,
            method: DEFLATE,
        },
        ZipEntrySpec {
            name: b"gamestate",
            data: &gamestate,
            method: DEFLATE,
        },
    ]));

    // A split header whose ZIP has no meta entry.
    let mut split_without_meta = header(5, 0);
    split_without_meta.extend_from_slice(&zip_entries(&[ZipEntrySpec {
        name: b"gamestate",
        data: &gamestate,
        method: DEFLATE,
    }]));

    // A text header is not writable at all.
    let mut text_header = header(0, metadata.len());
    text_header.extend_from_slice(&metadata);
    text_header.extend_from_slice(&gamestate);

    for (label, source, needle) in [
        (
            "zip-behind-binary",
            zip_behind_binary,
            "an embedded ZIP was found",
        ),
        (
            "unified-without-zip",
            unified_without_zip,
            "no valid embedded ZIP container was found",
        ),
        (
            "split-with-inline-length",
            split_with_inline_len,
            "metadata belongs to the ZIP",
        ),
        ("split-without-meta", split_without_meta, "no meta entry"),
        ("text-header", text_header, "unsupported save header kind"),
    ] {
        let save = dir.path().join(format!("{label}.ck3"));
        let plan = dir.path().join(format!("{label}.plan.json"));
        fs::write(&save, &source).unwrap();
        assert_failure(
            run(&[
                "plan-scalar",
                "--section",
                "gamestate",
                "--token-map",
                path_text(&map),
                "--raw-key",
                "0x4000",
                "--expect",
                "u32:7",
                "--value",
                "u32:9",
                "--plan",
                path_text(&plan),
                path_text(&save),
            ]),
            needle,
        );
        assert!(!plan.exists(), "{label} must not publish a plan");
        assert_eq!(fs::read(&save).unwrap(), source);
    }
}

#[test]
fn a_tampered_v2_plan_body_never_publishes() {
    let fixture = fixture(Layout::SplitZip);
    let plan = fixture.dir.join("gamestate.plan.json");
    parse_success(plan_nested(
        &fixture,
        "gamestate",
        &plan,
        &["--match-index", "1"],
    ));
    let original = fs::read(&plan).unwrap();
    let document: Value = serde_json::from_slice(&original).unwrap();

    let mut region_attack = document.clone();
    region_attack["body"]["unmodified_regions"][0]["sha256"] = Value::String("0".repeat(64));
    let mut layout_attack = document.clone();
    layout_attack["body"]["source"]["layout"] = Value::String("unified_binary_zip".to_owned());
    let mut strategy_attack = document.clone();
    strategy_attack["body"]["rebuild"]["strategy"] =
        Value::String("splice_inline_gamestate".to_owned());
    let mut unknown_attack = document;
    unknown_attack["body"]["rebuild"]["attacker_controlled"] = Value::Bool(true);

    for (label, attack, needle) in [
        ("region", region_attack, "does not match its typed body"),
        ("layout", layout_attack, "disagree with its layout"),
        ("strategy", strategy_attack, "does not match its layout"),
        ("unknown", unknown_attack, "unknown field"),
    ] {
        let attack_plan = fixture.dir.join(format!("{label}.plan.json"));
        let output = fixture.dir.join(format!("{label}-output.ck3"));
        fs::write(&attack_plan, serde_json::to_vec_pretty(&attack).unwrap()).unwrap();
        assert_failure(apply(&fixture, &attack_plan, &output), needle);
        assert!(!output.exists(), "{label} must not publish output");
    }

    assert_eq!(fs::read(&plan).unwrap(), original);
    assert_eq!(fs::read(&fixture.save).unwrap(), fixture.source);
    for entry in fs::read_dir(&fixture.dir).unwrap() {
        let name = entry.unwrap().file_name().to_string_lossy().into_owned();
        assert!(
            !name.contains(".tmp"),
            "temporary artifact remained: {name}"
        );
    }
}

#[test]
fn every_layout_reports_its_storage_and_archive_manifest() {
    for layout in Layout::ALL {
        let fixture = fixture(layout);
        let plan = fixture.dir.join("plan.json");
        parse_success(plan_nested(
            &fixture,
            "gamestate",
            &plan,
            &["--match-index", "1"],
        ));
        let document: Value = serde_json::from_slice(&fs::read(&plan).unwrap()).unwrap();
        let source = &document["body"]["source"];
        assert_eq!(source["layout"], layout.name());

        match layout {
            Layout::BinaryUncompressed => {
                assert_eq!(source["container"], "uncompressed");
                assert_eq!(source["storage"]["metadata"], "inline_after_header");
                assert_eq!(source["storage"]["gamestate"], "inline_after_metadata");
                assert!(source["zip"].is_null());
            }
            Layout::UnifiedZip => {
                assert_eq!(source["container"], "zip");
                assert_eq!(source["storage"]["metadata"], "inline_after_header");
                assert_eq!(source["storage"]["gamestate_zip_entry"], "gamestate");
                let names = entry_names(source);
                assert_eq!(names, vec!["gamestate"]);
            }
            Layout::SplitZip => {
                assert_eq!(source["storage"]["metadata_zip_entry"], "meta");
                assert_eq!(source["storage"]["gamestate_zip_entry"], "gamestate");
                let names = entry_names(source);
                assert_eq!(names, vec!["meta", "gamestate"]);
            }
        }
    }
}

fn entry_names(source: &Value) -> Vec<String> {
    source["zip"]["entries"]
        .as_array()
        .expect("a ZIP layout must list its entries")
        .iter()
        .map(|entry| entry["name"].as_str().unwrap().to_owned())
        .collect()
}

#[test]
fn the_canonical_path_of_a_repeated_key_is_stable_across_sections() {
    let fixture = fixture(Layout::SplitZip);
    let mut paths = Vec::new();
    for section in ["metadata", "gamestate"] {
        let plan = fixture.dir.join(format!("{section}.plan.json"));
        let report = parse_success(plan_nested(
            &fixture,
            section,
            &plan,
            &["--match-index", "1"],
        ));
        paths.push(report["target"]["canonical_raw_path"].clone());
    }
    assert_eq!(
        paths[0], paths[1],
        "identical structures must produce identical canonical paths"
    );

    let segments: Vec<PathSegment> = serde_json::from_value(paths[0].clone()).unwrap();
    assert_eq!(segments.len(), 3);
    assert!(matches!(
        segments[2],
        PathSegment::Field {
            key: RawTokenIdentity::Id { token: TARGET_KEY },
            occurrence: 0
        }
    ));
}
