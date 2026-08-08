use ck3_index_jomini_oracle::{PathSegment, RawTokenIdentity, StructuralValue, walk_binary};
use flate2::{Compression, write::DeflateEncoder};
use jomini::{
    Scalar,
    binary::Token,
    envelope::{JominiFile, JominiFileKind, SaveContentKind},
};
use serde_json::Value;
use std::{
    fs,
    io::{Read, Write},
    path::{Path, PathBuf},
    process::{Command, Output},
    sync::atomic::{AtomicU64, Ordering},
};

static NEXT_TEMP: AtomicU64 = AtomicU64::new(0);

const ROOT_KEY: u16 = 0x1000;
const TARGET_KEY: u16 = 0x2000;
const NESTED_KEY: u16 = 0x3000;
const METADATA_KEY: u16 = 0x4000;
const TOKEN_MAP: &[u8] = b"0x1000 root\n0x2000 target\n0x3000 nested\n0x4000 metadata\n";
const FIRST_VALUE: &[u8] = b"first";
const SECOND_VALUE: &[u8] = b"second";
const REPLACEMENT: &[u8] = b"second-value-expanded";
const METADATA_VALUE: &[u8] = b"metadata";
const METADATA_REPLACEMENT: &[u8] = b"metadata-value-expanded";
const F32_METADATA_BITS: [u8; 4] = [0x0a, 0xd7, 0xa3, 0x3e];
const F32_METADATA_REPLACEMENT_BITS: [u8; 4] = [0x00, 0x00, 0x00, 0x3f];
const F64_FIRST_BITS: [u8; 8] = [0xa0, 0x86, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00];
const F64_SECOND_BITS: [u8; 8] = [0x1a, 0xf2, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00];
const F64_REPLACEMENT_BITS: [u8; 8] = [0x20, 0xa1, 0x07, 0x00, 0x00, 0x00, 0x00, 0x00];

fn save_editor() -> Command {
    Command::new(env!("CARGO_BIN_EXE_ck3-index-jomini-edit"))
}

struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        let sequence = NEXT_TEMP.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "ck3-index-jomini-plan-test-{}-{sequence}",
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

fn nested_repeated_gamestate() -> Vec<u8> {
    encode(&[
        Token::Id(ROOT_KEY),
        Token::Equal,
        Token::Open,
        Token::Id(TARGET_KEY),
        Token::Equal,
        Token::Quoted(Scalar::new(FIRST_VALUE)),
        Token::Id(NESTED_KEY),
        Token::Equal,
        Token::Open,
        Token::Id(TARGET_KEY),
        Token::Equal,
        Token::Quoted(Scalar::new(SECOND_VALUE)),
        Token::Close,
        Token::Close,
    ])
}

fn metadata(value: u32) -> Vec<u8> {
    encode(&[Token::Id(METADATA_KEY), Token::Equal, Token::U32(value)])
}

fn quoted_metadata(value: &[u8]) -> Vec<u8> {
    encode(&[
        Token::Id(METADATA_KEY),
        Token::Equal,
        Token::Quoted(Scalar::new(value)),
    ])
}

fn f32_metadata(bits: [u8; 4]) -> Vec<u8> {
    encode(&[Token::Id(METADATA_KEY), Token::Equal, Token::F32(bits)])
}

fn nested_repeated_f64_gamestate() -> Vec<u8> {
    encode(&[
        Token::Id(ROOT_KEY),
        Token::Equal,
        Token::Open,
        Token::Id(TARGET_KEY),
        Token::Equal,
        Token::F64(F64_FIRST_BITS),
        Token::Id(NESTED_KEY),
        Token::Equal,
        Token::Open,
        Token::Id(TARGET_KEY),
        Token::Equal,
        Token::F64(F64_SECOND_BITS),
        Token::Close,
        Token::Close,
    ])
}

fn compact_fixed5_metadata(value: u8) -> Vec<u8> {
    let mut output = encode(&[Token::Id(METADATA_KEY), Token::Equal]);
    put_u16(&mut output, 0x0d48); // Jomini FIXED5_U8: F64 identity, compact payload.
    output.push(value);
    output
}

fn header(metadata_len: usize) -> Vec<u8> {
    let value = format!("SAV0103deadbeef{metadata_len:08x}\n");
    assert_eq!(value.len(), 24);
    value.into_bytes()
}

fn unified_binary_save(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    unified_binary_save_with_entries(metadata, &[(b"gamestate", gamestate)])
}

fn unified_binary_save_with_entries(metadata: &[u8], entries: &[(&[u8], &[u8])]) -> Vec<u8> {
    let archive = zip_entries(entries);
    let mut output = header(metadata.len());
    output.extend_from_slice(metadata);
    output.extend_from_slice(&archive);
    output
}

fn zip_entries(entries: &[(&[u8], &[u8])]) -> Vec<u8> {
    let encoded: Vec<_> = entries
        .iter()
        .map(|(name, data)| {
            let mut encoder = DeflateEncoder::new(Vec::new(), Compression::default());
            encoder.write_all(data).unwrap();
            let compressed = encoder.finish().unwrap();
            (name.to_vec(), data.len(), crc32(data), compressed)
        })
        .collect();
    let mut output = Vec::new();
    let mut local_offsets = Vec::new();
    for (name, data_len, crc, compressed) in &encoded {
        local_offsets.push(output.len() as u32);
        put_u32(&mut output, 0x0403_4b50);
        put_u16(&mut output, 20);
        put_u16(&mut output, 0);
        put_u16(&mut output, 8);
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
    for ((name, data_len, crc, compressed), local_offset) in encoded.iter().zip(local_offsets) {
        put_u32(&mut output, 0x0201_4b50);
        put_u16(&mut output, 20);
        put_u16(&mut output, 20);
        put_u16(&mut output, 0);
        put_u16(&mut output, 8);
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
    put_u16(&mut output, entries.len() as u16);
    put_u16(&mut output, entries.len() as u16);
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

fn verified_binary_gamestate(save_bytes: &[u8]) -> Vec<u8> {
    let save = JominiFile::from_slice(save_bytes).expect("output envelope should parse");
    let JominiFileKind::Zip(zip) = save.kind() else {
        panic!("fixture must be a ZIP save")
    };
    let SaveContentKind::Binary(mut reader) = zip
        .gamestate_verified()
        .expect("gamestate CRC and length should verify")
    else {
        panic!("fixture must have a binary gamestate")
    };
    let mut gamestate = Vec::new();
    reader.read_to_end(&mut gamestate).unwrap();
    gamestate
}

fn metadata_bounds(save_bytes: &[u8]) -> (usize, usize) {
    let save = JominiFile::from_slice(save_bytes).expect("save envelope should parse");
    let start = save.header().header_len();
    let metadata_len = usize::try_from(save.header().metadata_len())
        .expect("fixture metadata length should fit usize");
    let end = start
        .checked_add(metadata_len)
        .expect("fixture metadata end should not overflow");
    assert!(end <= save_bytes.len(), "metadata must lie inside save");
    (start, end)
}

fn field_values(section: &[u8], key_token: u16) -> Vec<(Vec<PathSegment>, RawTokenIdentity)> {
    walk_binary(section)
        .expect("binary section structure should parse")
        .events
        .into_iter()
        .filter_map(|event| {
            let is_target = event
                .key
                .as_ref()
                .is_some_and(|key| key.raw == RawTokenIdentity::Id { token: key_token });
            if !is_target {
                return None;
            }
            let StructuralValue::Scalar { raw, .. } = event.value else {
                panic!("target should be scalar")
            };
            Some((event.path, raw))
        })
        .collect()
}

fn target_values(gamestate: &[u8]) -> Vec<(Vec<PathSegment>, RawTokenIdentity)> {
    field_values(gamestate, TARGET_KEY)
}

fn text_identity(bytes: &[u8]) -> RawTokenIdentity {
    RawTokenIdentity::Text {
        representation: ck3_index_jomini_oracle::TextRepresentation::Quoted,
        bytes_hex: bytes_hex(bytes),
    }
}

fn f32_identity(bits: &[u8; 4]) -> RawTokenIdentity {
    RawTokenIdentity::F32 {
        bits_hex: bytes_hex(bits),
    }
}

fn f64_identity(bits: &[u8; 8]) -> RawTokenIdentity {
    RawTokenIdentity::F64 {
        bits_hex: bytes_hex(bits),
    }
}

fn bytes_hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn write_standard_fixture(dir: &TempDir) -> (PathBuf, Vec<u8>, PathBuf) {
    let source = unified_binary_save(&metadata(1), &nested_repeated_gamestate());
    let save = dir.path().join("source.ck3");
    let map = dir.path().join("tokens.txt");
    fs::write(&save, &source).unwrap();
    fs::write(&map, TOKEN_MAP).unwrap();
    (save, source, map)
}

fn create_second_match_plan(save: &Path, map: &Path, plan: &Path) -> Value {
    parse_success(run(&[
        "plan-scalar",
        "--section",
        "gamestate",
        "--token-map",
        path_text(map),
        "--raw-key",
        "0x2000",
        "--match-index",
        "1",
        "--expect",
        "quoted:second",
        "--value",
        "quoted:second-value-expanded",
        "--plan",
        path_text(plan),
        path_text(save),
    ]))
}

#[test]
fn ambiguous_or_duplicate_unified_zip_entries_reject_before_plan_publication() {
    let dir = TempDir::new();
    let metadata = metadata(1);
    let gamestate = nested_repeated_gamestate();
    let map = dir.path().join("tokens.txt");
    fs::write(&map, TOKEN_MAP).unwrap();

    let ambiguous_source = unified_binary_save_with_entries(
        &metadata,
        &[(b"meta", &metadata), (b"gamestate", &gamestate)],
    );
    let ambiguous_save = dir.path().join("ambiguous-meta.ck3");
    let ambiguous_plan = dir.path().join("ambiguous-meta.plan.json");
    fs::write(&ambiguous_save, &ambiguous_source).unwrap();
    assert_failure(
        run(&[
            "plan-scalar",
            "--section",
            "metadata",
            "--token-map",
            path_text(&map),
            "--raw-key",
            "0x4000",
            "--expect",
            "u32:1",
            "--value",
            "u32:2",
            "--plan",
            path_text(&ambiguous_plan),
            path_text(&ambiguous_save),
        ]),
        "carries a meta entry",
    );
    assert!(!ambiguous_plan.exists());
    assert_eq!(fs::read(&ambiguous_save).unwrap(), ambiguous_source);

    let duplicate_source = unified_binary_save_with_entries(
        &metadata,
        &[(b"gamestate", &gamestate), (b"gamestate", &gamestate)],
    );
    let duplicate_save = dir.path().join("duplicate-gamestate.ck3");
    let duplicate_plan = dir.path().join("duplicate-gamestate.plan.json");
    fs::write(&duplicate_save, &duplicate_source).unwrap();
    assert_failure(
        run(&[
            "plan-scalar",
            "--section",
            "metadata",
            "--token-map",
            path_text(&map),
            "--raw-key",
            "0x4000",
            "--expect",
            "u32:1",
            "--value",
            "u32:2",
            "--plan",
            path_text(&duplicate_plan),
            path_text(&duplicate_save),
        ]),
        "duplicate ZIP entry name",
    );
    assert!(!duplicate_plan.exists());
    assert_eq!(fs::read(&duplicate_save).unwrap(), duplicate_source);
}

#[test]
fn repeated_key_requires_selection_then_edits_only_the_nested_second_match() {
    let dir = TempDir::new();
    let (save, source, map) = write_standard_fixture(&dir);
    let plan = dir.path().join("edit.plan.json");

    assert_failure(
        run(&[
            "plan-scalar",
            "--section",
            "gamestate",
            "--token-map",
            path_text(&map),
            "--raw-key",
            "0x2000",
            "--expect",
            "quoted:second",
            "--value",
            "quoted:second-value-expanded",
            "--plan",
            path_text(&plan),
            path_text(&save),
        ]),
        "--match-index is required",
    );
    assert!(!plan.exists());
    assert_eq!(fs::read(&save).unwrap(), source);

    let plan_report = create_second_match_plan(&save, &map, &plan);
    assert_eq!(
        plan_report["schema"],
        "ck3-index-jomini-save-edit-plan-report/v2"
    );
    assert_eq!(plan_report["selection"]["raw_key_match_count"], 2);
    assert_eq!(plan_report["selection"]["selected_match_index"], 1);
    let planned_path = plan_report["target"]["canonical_raw_path"]
        .as_array()
        .expect("plan path should be an array");
    assert_eq!(planned_path.len(), 3);
    assert_eq!(planned_path[0]["key"]["token"], ROOT_KEY);
    assert_eq!(planned_path[1]["key"]["token"], NESTED_KEY);
    assert_eq!(planned_path[2]["key"]["token"], TARGET_KEY);
    assert_eq!(planned_path[2]["occurrence"], 0);

    let output = dir.path().join("edited.ck3");
    let apply_report = parse_success(run(&[
        "apply-plan",
        "--token-map",
        path_text(&map),
        "--plan",
        path_text(&plan),
        path_text(&save),
        path_text(&output),
    ]));
    assert_eq!(
        apply_report["schema"],
        "ck3-index-jomini-save-edit-apply/v2"
    );
    assert_eq!(apply_report["complete"], true);
    assert_eq!(
        apply_report["canonical_raw_path"],
        plan_report["target"]["canonical_raw_path"]
    );

    let edited = fs::read(&output).expect("new output should be published");
    let original_values = target_values(&verified_binary_gamestate(&source));
    let edited_values = target_values(&verified_binary_gamestate(&edited));
    assert_eq!(original_values.len(), 2);
    assert_eq!(edited_values.len(), 2);
    assert_eq!(original_values[0].1, text_identity(FIRST_VALUE));
    assert_eq!(original_values[1].1, text_identity(SECOND_VALUE));
    assert_eq!(edited_values[0], original_values[0]);
    assert_eq!(edited_values[1].0, original_values[1].0);
    assert_eq!(edited_values[1].1, text_identity(REPLACEMENT));
    assert_ne!(
        edited.len(),
        source.len(),
        "replacement is deliberately wider"
    );
    assert_eq!(
        fs::read(&save).unwrap(),
        source,
        "source must remain read-only"
    );
}

#[test]
fn metadata_plan_apply_expands_quoted_scalar_and_preserves_zip_tail() {
    let dir = TempDir::new();
    let source_metadata = quoted_metadata(METADATA_VALUE);
    let replacement_metadata = quoted_metadata(METADATA_REPLACEMENT);
    let gamestate = nested_repeated_gamestate();
    let mut source = unified_binary_save(&source_metadata, &gamestate);
    source[7..15].copy_from_slice(b"DEADBEEF");
    let save = dir.path().join("metadata-source.ck3");
    let map = dir.path().join("metadata-tokens.txt");
    fs::write(&save, &source).unwrap();
    fs::write(&map, TOKEN_MAP).unwrap();

    let occupied_plan = dir.path().join("occupied.plan.json");
    fs::write(&occupied_plan, b"keep existing plan").unwrap();
    assert_failure(
        run(&[
            "plan-scalar",
            "--section",
            "metadata",
            "--token-map",
            path_text(&map),
            "--raw-key",
            "0x4000",
            "--expect",
            "quoted:metadata",
            "--value",
            "quoted:metadata-value-expanded",
            "--plan",
            path_text(&occupied_plan),
            path_text(&save),
        ]),
        "output already exists",
    );
    assert_eq!(fs::read(&occupied_plan).unwrap(), b"keep existing plan");
    assert_eq!(fs::read(&save).unwrap(), source);

    let plan = dir.path().join("metadata.plan.json");
    let plan_report = parse_success(run(&[
        "plan-scalar",
        "--section",
        "metadata",
        "--token-map",
        path_text(&map),
        "--raw-key",
        "0x4000",
        "--expect",
        "quoted:metadata",
        "--value",
        "quoted:metadata-value-expanded",
        "--plan",
        path_text(&plan),
        path_text(&save),
    ]));
    assert_eq!(plan_report["section"]["name"], "metadata");
    assert_eq!(
        plan_report["source"]["header"]["declared_metadata_bytes"],
        source_metadata.len() as u64
    );
    assert_eq!(
        plan_report["predicted_output"]["section_bytes"],
        replacement_metadata.len() as u64
    );
    let planned_path = plan_report["target"]["canonical_raw_path"]
        .as_array()
        .expect("metadata plan path should be an array");
    assert_eq!(planned_path.len(), 1);
    assert_eq!(planned_path[0]["kind"], "field");
    assert_eq!(planned_path[0]["key"]["token"], METADATA_KEY);
    assert_eq!(planned_path[0]["occurrence"], 0);

    let output = dir.path().join("metadata-edited.ck3");
    let apply_report = parse_success(run(&[
        "apply-plan",
        "--token-map",
        path_text(&map),
        "--plan",
        path_text(&plan),
        path_text(&save),
        path_text(&output),
    ]));
    assert_eq!(apply_report["section"], "metadata");
    assert_eq!(apply_report["complete"], true);
    assert_eq!(
        apply_report["canonical_raw_path"],
        plan_report["target"]["canonical_raw_path"]
    );

    let edited = fs::read(&output).expect("metadata output should be published");
    let (source_metadata_start, source_metadata_end) = metadata_bounds(&source);
    let (edited_metadata_start, edited_metadata_end) = metadata_bounds(&edited);
    assert_eq!(source_metadata_start, edited_metadata_start);
    assert_eq!(&source[..15], &edited[..15]);
    assert_eq!(
        &source[23..source_metadata_start],
        &edited[23..edited_metadata_start],
        "variable-width metadata edits may only rewrite header[15..23]"
    );
    assert_eq!(
        JominiFile::from_slice(&edited)
            .expect("edited envelope should parse")
            .header()
            .metadata_len(),
        replacement_metadata.len() as u64,
        "header metadata length must track the wider scalar"
    );
    assert_eq!(
        &source[source_metadata_end..],
        &edited[edited_metadata_end..],
        "embedded ZIP tail must remain byte-for-byte identical"
    );
    assert_eq!(
        verified_binary_gamestate(&edited),
        gamestate,
        "metadata editing must not change logical gamestate bytes"
    );

    let original_values = field_values(
        &source[source_metadata_start..source_metadata_end],
        METADATA_KEY,
    );
    let edited_values = field_values(
        &edited[edited_metadata_start..edited_metadata_end],
        METADATA_KEY,
    );
    let expected_path = vec![PathSegment::Field {
        key: RawTokenIdentity::Id {
            token: METADATA_KEY,
        },
        occurrence: 0,
    }];
    assert_eq!(original_values.len(), 1);
    assert_eq!(edited_values.len(), 1);
    assert_eq!(original_values[0].0, expected_path);
    assert_eq!(edited_values[0].0, original_values[0].0);
    assert_eq!(original_values[0].1, text_identity(METADATA_VALUE));
    assert_eq!(edited_values[0].1, text_identity(METADATA_REPLACEMENT));
    assert_eq!(
        edited.len(),
        source.len() + replacement_metadata.len() - source_metadata.len()
    );
    assert_eq!(
        fs::read(&save).unwrap(),
        source,
        "source must remain read-only"
    );
}

#[test]
fn metadata_f32bits_plan_apply_is_bit_exact_and_preserves_zip_tail() {
    let dir = TempDir::new();
    let source_metadata = f32_metadata(F32_METADATA_BITS);
    let gamestate = nested_repeated_gamestate();
    let mut source = unified_binary_save(&source_metadata, &gamestate);
    assert_eq!(&source[15..23], b"0000000a");
    source[22] = b'A';
    let save = dir.path().join("f32-metadata-source.ck3");
    let map = dir.path().join("f32-metadata-tokens.txt");
    let plan = dir.path().join("f32-metadata.plan.json");
    fs::write(&save, &source).unwrap();
    fs::write(&map, TOKEN_MAP).unwrap();

    let expected = format!("f32bits:{}", bytes_hex(&F32_METADATA_BITS));
    let replacement = format!("f32bits:{}", bytes_hex(&F32_METADATA_REPLACEMENT_BITS));
    let plan_report = parse_success(run(&[
        "plan-scalar",
        "--section",
        "metadata",
        "--token-map",
        path_text(&map),
        "--raw-key",
        "0x4000",
        "--expect",
        &expected,
        "--value",
        &replacement,
        "--plan",
        path_text(&plan),
        path_text(&save),
    ]));
    assert_eq!(plan_report["section"]["name"], "metadata");
    assert_eq!(
        plan_report["target"]["expected"],
        serde_json::json!({
            "kind": "f32",
            "bits_hex": bytes_hex(&F32_METADATA_BITS),
        })
    );
    assert_eq!(
        plan_report["replacement"],
        serde_json::json!({
            "kind": "f32",
            "bits_hex": bytes_hex(&F32_METADATA_REPLACEMENT_BITS),
        })
    );
    assert_eq!(
        plan_report["predicted_output"]["section_bytes"],
        source_metadata.len() as u64,
        "canonical F32 replacement must be fixed-width"
    );

    let output = dir.path().join("f32-metadata-edited.ck3");
    let apply_report = parse_success(run(&[
        "apply-plan",
        "--token-map",
        path_text(&map),
        "--plan",
        path_text(&plan),
        path_text(&save),
        path_text(&output),
    ]));
    assert_eq!(apply_report["section"], "metadata");
    assert_eq!(apply_report["complete"], true);
    assert_eq!(apply_report["old"], plan_report["target"]["expected"]);
    assert_eq!(apply_report["new"], plan_report["replacement"]);

    let edited = fs::read(&output).expect("F32 metadata output should be published");
    let (source_metadata_start, source_metadata_end) = metadata_bounds(&source);
    let (edited_metadata_start, edited_metadata_end) = metadata_bounds(&edited);
    assert_eq!(source_metadata_start, edited_metadata_start);
    assert_eq!(source_metadata_end, edited_metadata_end);
    assert_eq!(
        &source[..source_metadata_start],
        &edited[..edited_metadata_start],
        "equal-width metadata edits must preserve the original header spelling"
    );
    assert_eq!(
        &source[source_metadata_end..],
        &edited[edited_metadata_end..],
        "metadata F32 edit must preserve the embedded ZIP tail byte-for-byte"
    );
    assert_eq!(
        verified_binary_gamestate(&edited),
        gamestate,
        "metadata F32 edit must preserve logical gamestate bytes"
    );
    let original_values = field_values(
        &source[source_metadata_start..source_metadata_end],
        METADATA_KEY,
    );
    let edited_values = field_values(
        &edited[edited_metadata_start..edited_metadata_end],
        METADATA_KEY,
    );
    assert_eq!(original_values.len(), 1);
    assert_eq!(edited_values.len(), 1);
    assert_eq!(original_values[0].0, edited_values[0].0);
    assert_eq!(original_values[0].1, f32_identity(&F32_METADATA_BITS));
    assert_eq!(
        edited_values[0].1,
        f32_identity(&F32_METADATA_REPLACEMENT_BITS)
    );
    assert_eq!(edited.len(), source.len());
    assert_eq!(fs::read(&save).unwrap(), source);
}

#[test]
fn gamestate_repeated_f64bits_edits_only_selected_match_and_verifies_crc_and_structure() {
    let dir = TempDir::new();
    let gamestate = nested_repeated_f64_gamestate();
    let source = unified_binary_save(&metadata(1), &gamestate);
    let save = dir.path().join("f64-gamestate-source.ck3");
    let map = dir.path().join("f64-gamestate-tokens.txt");
    let plan = dir.path().join("f64-gamestate.plan.json");
    fs::write(&save, &source).unwrap();
    fs::write(&map, TOKEN_MAP).unwrap();

    let expected = format!("f64bits:{}", bytes_hex(&F64_SECOND_BITS));
    let replacement = format!("f64bits:{}", bytes_hex(&F64_REPLACEMENT_BITS));
    let plan_report = parse_success(run(&[
        "plan-scalar",
        "--section",
        "gamestate",
        "--token-map",
        path_text(&map),
        "--raw-key",
        "0x2000",
        "--match-index",
        "1",
        "--expect",
        &expected,
        "--value",
        &replacement,
        "--plan",
        path_text(&plan),
        path_text(&save),
    ]));
    assert_eq!(plan_report["selection"]["raw_key_match_count"], 2);
    assert_eq!(plan_report["selection"]["selected_match_index"], 1);
    assert_eq!(
        plan_report["target"]["expected"],
        serde_json::json!({
            "kind": "f64",
            "bits_hex": bytes_hex(&F64_SECOND_BITS),
        })
    );
    assert_eq!(
        plan_report["replacement"],
        serde_json::json!({
            "kind": "f64",
            "bits_hex": bytes_hex(&F64_REPLACEMENT_BITS),
        })
    );

    let output = dir.path().join("f64-gamestate-edited.ck3");
    let apply_report = parse_success(run(&[
        "apply-plan",
        "--token-map",
        path_text(&map),
        "--plan",
        path_text(&plan),
        path_text(&save),
        path_text(&output),
    ]));
    assert_eq!(apply_report["section"], "gamestate");
    assert_eq!(apply_report["section_scan_complete"], true);
    assert_eq!(apply_report["gamestate_integrity_checked"], true);
    assert_eq!(apply_report["complete"], true);
    assert_eq!(
        apply_report["canonical_raw_path"],
        plan_report["target"]["canonical_raw_path"]
    );

    let edited = fs::read(&output).expect("F64 gamestate output should be published");
    let checked_gamestate = verified_binary_gamestate(&edited);
    assert_eq!(checked_gamestate.len(), gamestate.len());
    let original_values = target_values(&gamestate);
    let edited_values = target_values(&checked_gamestate);
    assert_eq!(original_values.len(), 2);
    assert_eq!(edited_values.len(), 2);
    assert_eq!(original_values[0].1, f64_identity(&F64_FIRST_BITS));
    assert_eq!(original_values[1].1, f64_identity(&F64_SECOND_BITS));
    assert_eq!(edited_values[0], original_values[0]);
    assert_eq!(edited_values[1].0, original_values[1].0);
    assert_eq!(edited_values[1].1, f64_identity(&F64_REPLACEMENT_BITS));
    assert_eq!(fs::read(&save).unwrap(), source);
}

#[test]
fn malformed_cross_kind_and_noop_float_arguments_reject_without_artifacts() {
    let dir = TempDir::new();
    let source_metadata = f32_metadata(F32_METADATA_BITS);
    let source = unified_binary_save(&source_metadata, &nested_repeated_gamestate());
    let save = dir.path().join("invalid-float-source.ck3");
    let map = dir.path().join("invalid-float-tokens.txt");
    fs::write(&save, &source).unwrap();
    fs::write(&map, TOKEN_MAP).unwrap();

    let valid_f32 = "f32bits:0ad7a33e";
    for (label, expected, replacement, error) in [
        (
            "short-f32",
            valid_f32,
            "f32bits:001122",
            "f32bits value must contain exactly 8 lowercase hex digits",
        ),
        (
            "nonhex-f32",
            valid_f32,
            "f32bits:0ad7a33z",
            "f32bits value contains invalid hex",
        ),
        (
            "uppercase-f32",
            valid_f32,
            "f32bits:0AD7A33E",
            "f32bits value must use lowercase hex",
        ),
        (
            "short-f64",
            valid_f32,
            "f64bits:001122",
            "f64bits value must contain exactly 16 lowercase hex digits",
        ),
        (
            "nonhex-f64",
            valid_f32,
            "f64bits:a08601000000000g",
            "f64bits value contains invalid hex",
        ),
        (
            "uppercase-f64",
            valid_f32,
            "f64bits:A086010000000000",
            "f64bits value must use lowercase hex",
        ),
        (
            "cross-kind",
            valid_f32,
            "f64bits:a086010000000000",
            "does not match expected kind",
        ),
        (
            "no-op",
            valid_f32,
            valid_f32,
            "replacement is identical to the expected scalar",
        ),
    ] {
        let plan = dir.path().join(format!("{label}.plan.json"));
        assert_failure(
            run(&[
                "plan-scalar",
                "--section",
                "metadata",
                "--token-map",
                path_text(&map),
                "--raw-key",
                "0x4000",
                "--expect",
                expected,
                "--value",
                replacement,
                "--plan",
                path_text(&plan),
                path_text(&save),
            ]),
            error,
        );
        assert!(!plan.exists(), "rejected {label} input published a plan");
    }

    assert_eq!(fs::read(&save).unwrap(), source);
    for entry in fs::read_dir(dir.path()).unwrap() {
        let name = entry.unwrap().file_name().to_string_lossy().into_owned();
        assert!(
            !name.contains(".tmp"),
            "temporary artifact remained after rejected float input: {name}"
        );
    }
}

#[test]
fn compact_fixed5_f64_source_is_rejected_as_noncanonical_without_artifacts() {
    let dir = TempDir::new();
    let source_metadata = compact_fixed5_metadata(5);
    assert_eq!(
        field_values(&source_metadata, METADATA_KEY),
        vec![(
            vec![PathSegment::Field {
                key: RawTokenIdentity::Id {
                    token: METADATA_KEY,
                },
                occurrence: 0,
            }],
            f64_identity(&[5, 0, 0, 0, 0, 0, 0, 0]),
        )],
        "fixture must decode to an F64 identity despite its compact spelling"
    );
    let source = unified_binary_save(&source_metadata, &nested_repeated_gamestate());
    let save = dir.path().join("compact-fixed5-source.ck3");
    let map = dir.path().join("compact-fixed5-tokens.txt");
    let plan = dir.path().join("compact-fixed5.plan.json");
    fs::write(&save, &source).unwrap();
    fs::write(&map, TOKEN_MAP).unwrap();

    assert_failure(
        run(&[
            "plan-scalar",
            "--section",
            "metadata",
            "--token-map",
            path_text(&map),
            "--raw-key",
            "0x4000",
            "--expect",
            "f64bits:0500000000000000",
            "--value",
            "f64bits:0600000000000000",
            "--plan",
            path_text(&plan),
            path_text(&save),
        ]),
        "non-canonical encoding",
    );
    assert!(!plan.exists());

    let legacy_output = dir.path().join("compact-fixed5-legacy-output.ck3");
    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "metadata",
            "--expect",
            "f64bits:0500000000000000",
            "--value",
            "f64bits:0600000000000000",
            "0x4000",
            path_text(&save),
            path_text(&legacy_output),
        ]),
        "non-canonical encoding",
    );
    assert!(!legacy_output.exists());

    let noop_output = dir.path().join("float-noop-legacy-output.ck3");
    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "metadata",
            "--expect",
            "f64bits:0500000000000000",
            "--value",
            "f64bits:0500000000000000",
            "0x4000",
            path_text(&save),
            path_text(&noop_output),
        ]),
        "replacement is identical to the expected scalar",
    );
    assert!(!noop_output.exists());

    assert_eq!(fs::read(&save).unwrap(), source);
    for entry in fs::read_dir(dir.path()).unwrap() {
        let name = entry.unwrap().file_name().to_string_lossy().into_owned();
        assert!(
            !name.contains(".tmp"),
            "temporary artifact remained after compact FIXED5 rejection: {name}"
        );
    }
}

#[test]
fn stale_source_wrong_map_tampered_plan_and_existing_output_never_publish() {
    let dir = TempDir::new();
    let (save, source, map) = write_standard_fixture(&dir);
    let plan = dir.path().join("edit.plan.json");
    create_second_match_plan(&save, &map, &plan);
    let original_plan = fs::read(&plan).unwrap();

    let changed_save = dir.path().join("changed-source.ck3");
    fs::write(
        &changed_save,
        unified_binary_save(&metadata(2), &nested_repeated_gamestate()),
    )
    .unwrap();
    let stale_output = dir.path().join("stale-output.ck3");
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&map),
            "--plan",
            path_text(&plan),
            path_text(&changed_save),
            path_text(&stale_output),
        ]),
        "differ from the edit plan",
    );
    assert!(!stale_output.exists());

    let wrong_map = dir.path().join("wrong-tokens.txt");
    fs::write(
        &wrong_map,
        b"0x1000 changed_root\n0x2000 target\n0x3000 nested\n",
    )
    .unwrap();
    let wrong_map_output = dir.path().join("wrong-map-output.ck3");
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&wrong_map),
            "--plan",
            path_text(&plan),
            path_text(&save),
            path_text(&wrong_map_output),
        ]),
        "token map bytes or SHA-256 do not match",
    );
    assert!(!wrong_map_output.exists());

    let tampered_plan = dir.path().join("tampered.plan.json");
    let mut document: Value = serde_json::from_slice(&original_plan).unwrap();
    document["body"]["replacement"]["bytes_hex"] = Value::String("00".to_owned());
    fs::write(
        &tampered_plan,
        serde_json::to_vec_pretty(&document).unwrap(),
    )
    .unwrap();
    let tampered_output = dir.path().join("tampered-output.ck3");
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&map),
            "--plan",
            path_text(&tampered_plan),
            path_text(&save),
            path_text(&tampered_output),
        ]),
        "plan ID does not match",
    );
    assert!(!tampered_output.exists());

    let existing_output = dir.path().join("existing-output.ck3");
    fs::write(&existing_output, b"keep existing output").unwrap();
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&map),
            "--plan",
            path_text(&plan),
            path_text(&save),
            path_text(&existing_output),
        ]),
        "output already exists",
    );
    assert_eq!(fs::read(&existing_output).unwrap(), b"keep existing output");

    assert_eq!(fs::read(&save).unwrap(), source);
    assert_eq!(fs::read(&plan).unwrap(), original_plan);
    for entry in fs::read_dir(dir.path()).unwrap() {
        let name = entry.unwrap().file_name().to_string_lossy().into_owned();
        assert!(
            !name.contains(".tmp"),
            "temporary artifact remained: {name}"
        );
    }
}

#[test]
fn untrusted_plan_depth_spans_and_unknown_fields_reject_without_panicking() {
    let dir = TempDir::new();
    let (save, source, map) = write_standard_fixture(&dir);
    let plan = dir.path().join("edit.plan.json");
    create_second_match_plan(&save, &map, &plan);
    let original_plan = fs::read(&plan).unwrap();
    let valid_document: Value = serde_json::from_slice(&original_plan).unwrap();

    let mut depth_attack = valid_document.clone();
    depth_attack["body"]["target"]["depth"] = Value::from(u64::MAX);
    let depth_plan = dir.path().join("depth-attack.plan.json");
    fs::write(
        &depth_plan,
        serde_json::to_vec_pretty(&depth_attack).unwrap(),
    )
    .unwrap();
    let depth_output = dir.path().join("depth-attack-output.ck3");
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&map),
            "--plan",
            path_text(&depth_plan),
            path_text(&save),
            path_text(&depth_output),
        ]),
        "planned target depth",
    );
    assert!(!depth_output.exists());

    let mut span_attack = valid_document.clone();
    span_attack["body"]["target"]["spans"]["value"]["start"] = Value::from(100u64);
    span_attack["body"]["target"]["spans"]["value"]["end"] = Value::from(99u64);
    let span_plan = dir.path().join("span-attack.plan.json");
    fs::write(&span_plan, serde_json::to_vec_pretty(&span_attack).unwrap()).unwrap();
    let span_output = dir.path().join("span-attack-output.ck3");
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&map),
            "--plan",
            path_text(&span_plan),
            path_text(&save),
            path_text(&span_output),
        ]),
        "spans are invalid or out of bounds",
    );
    assert!(!span_output.exists());

    let mut unknown_field_attack = valid_document.clone();
    unknown_field_attack["body"]["target"]["attacker_controlled"] = Value::Bool(true);
    let unknown_field_plan = dir.path().join("unknown-field-attack.plan.json");
    fs::write(
        &unknown_field_plan,
        serde_json::to_vec_pretty(&unknown_field_attack).unwrap(),
    )
    .unwrap();
    let unknown_field_output = dir.path().join("unknown-field-attack-output.ck3");
    assert_failure(
        run(&[
            "apply-plan",
            "--token-map",
            path_text(&map),
            "--plan",
            path_text(&unknown_field_plan),
            path_text(&save),
            path_text(&unknown_field_output),
        ]),
        "unknown field",
    );
    assert!(!unknown_field_output.exists());

    let mut nested_raw_attack = valid_document.clone();
    nested_raw_attack["body"]["replacement"]["attacker_controlled"] = Value::Bool(true);
    let mut nested_path_attack = valid_document.clone();
    nested_path_attack["body"]["target"]["canonical_raw_path"][0]["attacker_controlled"] =
        Value::Bool(true);
    let mut nested_span_attack = valid_document;
    nested_span_attack["body"]["target"]["spans"]["value"]["attacker_controlled"] =
        Value::Bool(true);
    for (label, attack) in [
        ("nested-raw-unknown", nested_raw_attack),
        ("nested-path-unknown", nested_path_attack),
        ("nested-span-unknown", nested_span_attack),
    ] {
        let attack_plan = dir.path().join(format!("{label}.plan.json"));
        let attack_output = dir.path().join(format!("{label}-output.ck3"));
        fs::write(&attack_plan, serde_json::to_vec_pretty(&attack).unwrap()).unwrap();
        assert_failure(
            run(&[
                "apply-plan",
                "--token-map",
                path_text(&map),
                "--plan",
                path_text(&attack_plan),
                path_text(&save),
                path_text(&attack_output),
            ]),
            "unknown field",
        );
        assert!(!attack_output.exists());
    }

    assert_eq!(fs::read(&save).unwrap(), source);
    assert_eq!(fs::read(&plan).unwrap(), original_plan);
    for entry in fs::read_dir(dir.path()).unwrap() {
        let name = entry.unwrap().file_name().to_string_lossy().into_owned();
        assert!(
            !name.contains(".tmp"),
            "temporary artifact remained: {name}"
        );
    }
}
