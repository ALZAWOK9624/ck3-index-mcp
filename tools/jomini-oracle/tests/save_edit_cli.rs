use ck3_index_jomini_oracle::{lowercase_hex, sha256_bytes};
use flate2::{Compression, write::DeflateEncoder};
use jomini::{
    Scalar,
    binary::{Token, TokenReader},
    envelope::{JominiFile, JominiFileKind, SaveContentKind},
};
use rawzip::ZipArchive;
use serde_json::Value;
use std::{
    fs,
    io::{Read, Write},
    path::{Path, PathBuf},
    process::{Command, Output},
    sync::atomic::{AtomicU64, Ordering},
};

static NEXT_TEMP: AtomicU64 = AtomicU64::new(0);

fn save_editor() -> Command {
    Command::new(env!("CARGO_BIN_EXE_ck3-index-jomini-edit"))
}

struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        let sequence = NEXT_TEMP.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "ck3-index-jomini-edit-{}-{sequence}",
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
        String::from_utf8_lossy(&output.stderr).contains(needle),
        "{output:?}"
    );
}

fn scalar_field(key: u16, value: Token<'_>) -> Vec<u8> {
    let mut result = Vec::new();
    Token::Id(key).write(&mut result).unwrap();
    Token::Equal.write(&mut result).unwrap();
    value.write(&mut result).unwrap();
    result
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

fn metadata_bounds(save: &[u8]) -> (usize, usize) {
    let metadata_len = usize::from_str_radix(std::str::from_utf8(&save[15..23]).unwrap(), 16)
        .expect("metadata length should be hex");
    (24, 24 + metadata_len)
}

fn raw_zip_payload(save: &[u8], wanted: &[u8]) -> Vec<u8> {
    let (_, zip_start) = metadata_bounds(save);
    let archive = ZipArchive::from_slice(&save[zip_start..]).unwrap();
    for entry in archive.entries() {
        let entry = entry.unwrap();
        if entry.file_path().as_ref() == wanted {
            return archive
                .get_entry(entry.wayfinder())
                .unwrap()
                .data()
                .to_vec();
        }
    }
    panic!("missing ZIP entry {wanted:?}")
}

fn verified_binary_gamestate(save_bytes: &[u8]) -> Vec<u8> {
    let save = JominiFile::from_slice(save_bytes).unwrap();
    let JominiFileKind::Zip(zip) = save.kind() else {
        panic!("fixture must be a ZIP save")
    };
    let SaveContentKind::Binary(mut reader) = zip.gamestate_verified().unwrap() else {
        panic!("fixture must have a binary gamestate")
    };
    let mut gamestate = Vec::new();
    reader.read_to_end(&mut gamestate).unwrap();
    gamestate
}

fn quoted_value(data: &[u8], key: u16) -> Vec<u8> {
    let mut reader = TokenReader::from_slice(data);
    let mut saw_key = false;
    let mut saw_equal = false;
    while let Some(token) = reader.next().unwrap() {
        if saw_equal {
            return match token {
                Token::Quoted(value) => value.as_bytes().to_vec(),
                other => panic!("target value was not quoted: {other:?}"),
            };
        }
        if saw_key && matches!(token, Token::Equal) {
            saw_equal = true;
            continue;
        }
        saw_key = matches!(token, Token::Id(id) if id == key);
    }
    panic!("missing key 0x{key:04x}")
}

fn write_fixture(dir: &TempDir, name: &str, metadata: &[u8]) -> (PathBuf, Vec<u8>) {
    let source = unified_binary_save(metadata, b"game-state-payload");
    let path = dir.path().join(name);
    fs::write(&path, &source).unwrap();
    (path, source)
}

#[test]
fn equal_width_u32_edit_is_copy_on_write_and_preserves_zip_tail() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1234, Token::U32(7));
    let (input, source) = write_fixture(&dir, "input.ck3", &metadata);
    let output = dir.path().join("output.ck3");

    let report = parse_success(run(&[
        "set-scalar",
        "--section",
        "metadata",
        "--expect",
        "u32:7",
        "--value",
        "u32:9",
        "0x1234",
        input.to_str().unwrap(),
        output.to_str().unwrap(),
    ]));

    assert_eq!(report["schema"], "ck3-index-jomini-save-edit/v1");
    assert_eq!(report["key"], "0x1234");
    assert_eq!(report["old"]["value"], 7);
    assert_eq!(report["new"]["value"], 9);
    assert_eq!(report["complete"], true);
    assert_eq!(
        report["source_sha256"],
        lowercase_hex(&sha256_bytes(&source).unwrap())
    );
    assert_eq!(fs::read(&input).unwrap(), source);
    let edited = fs::read(&output).unwrap();
    assert_eq!(
        report["output_sha256"],
        lowercase_hex(&sha256_bytes(&edited).unwrap())
    );
    assert_eq!(edited.len(), source.len());
    let (_, source_metadata_end) = metadata_bounds(&source);
    let (_, edited_metadata_end) = metadata_bounds(&edited);
    assert_eq!(
        &edited[edited_metadata_end..],
        &source[source_metadata_end..]
    );
}

#[test]
fn variable_width_quoted_edit_updates_header_and_preserves_zip_tail() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x2233, Token::Quoted(Scalar::new(b"old")));
    let (input, source) = write_fixture(&dir, "quoted-input.ck3", &metadata);
    let output = dir.path().join("quoted-output.ck3");

    let report = parse_success(run(&[
        "set-scalar",
        "--section",
        "metadata",
        "--expect",
        "quoted:old",
        "--value",
        "quoted:a-longer-value",
        "0x2233",
        input.to_str().unwrap(),
        output.to_str().unwrap(),
    ]));

    let edited = fs::read(&output).unwrap();
    assert_eq!(
        report["output_bytes"].as_u64().unwrap() - report["source_bytes"].as_u64().unwrap(),
        ("a-longer-value".len() - "old".len()) as u64
    );
    let (_, source_metadata_end) = metadata_bounds(&source);
    let (_, edited_metadata_end) = metadata_bounds(&edited);
    assert_eq!(
        edited_metadata_end - source_metadata_end,
        "a-longer-value".len() - "old".len()
    );
    assert_eq!(
        &edited[edited_metadata_end..],
        &source[source_metadata_end..]
    );
    assert_eq!(fs::read(&input).unwrap(), source);
}

#[test]
fn expectation_mismatch_leaves_no_output() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1234, Token::U32(7));
    let (input, source) = write_fixture(&dir, "mismatch-input.ck3", &metadata);
    let output = dir.path().join("mismatch-output.ck3");

    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "metadata",
            "--expect",
            "u32:8",
            "--value",
            "u32:9",
            "0x1234",
            input.to_str().unwrap(),
            output.to_str().unwrap(),
        ]),
        "does not match",
    );
    assert!(!output.exists());
    assert_eq!(fs::read(input).unwrap(), source);
}

#[test]
fn duplicate_key_leaves_no_output() {
    let dir = TempDir::new();
    let mut metadata = scalar_field(0x1234, Token::U32(7));
    metadata.extend_from_slice(&scalar_field(0x1234, Token::U32(7)));
    let (input, source) = write_fixture(&dir, "duplicate-input.ck3", &metadata);
    let output = dir.path().join("duplicate-output.ck3");

    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "metadata",
            "--expect",
            "u32:7",
            "--value",
            "u32:9",
            "0x1234",
            input.to_str().unwrap(),
            output.to_str().unwrap(),
        ]),
        "matched 2 fields",
    );
    assert!(!output.exists());
    assert_eq!(fs::read(input).unwrap(), source);
}

#[test]
fn existing_output_is_never_replaced() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1234, Token::U32(7));
    let (input, source) = write_fixture(&dir, "existing-input.ck3", &metadata);
    let output = dir.path().join("existing-output.ck3");
    fs::write(&output, b"keep me").unwrap();

    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "metadata",
            "--expect",
            "u32:7",
            "--value",
            "u32:9",
            "0x1234",
            input.to_str().unwrap(),
            output.to_str().unwrap(),
        ]),
        "output already exists",
    );
    assert_eq!(fs::read(output).unwrap(), b"keep me");
    assert_eq!(fs::read(input).unwrap(), source);
}

#[test]
fn input_cannot_also_be_the_output() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1234, Token::U32(7));
    let (input, source) = write_fixture(&dir, "same-path.ck3", &metadata);

    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "metadata",
            "--expect",
            "u32:7",
            "--value",
            "u32:9",
            "0x1234",
            input.to_str().unwrap(),
            input.to_str().unwrap(),
        ]),
        "input and output",
    );
    assert_eq!(fs::read(input).unwrap(), source);
}

#[test]
fn gamestate_variable_width_edit_roundtrips_and_preserves_other_raw_payloads() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1001, Token::Quoted(Scalar::new(b"campaign")));
    let mut gamestate = scalar_field(0x2000, Token::Open);
    gamestate.extend_from_slice(&scalar_field(
        0x2345,
        Token::Quoted(Scalar::new(b"old-name")),
    ));
    gamestate.extend_from_slice(&scalar_field(0x2001, Token::U32(8675309)));
    Token::Close.write(&mut gamestate).unwrap();
    let sidecar = b"opaque-sidecar-data-that-must-stay-byte-identical";
    let source = unified_binary_save_with_entries(
        &metadata,
        &[(b"gamestate", &gamestate), (b"sidecar", sidecar)],
    );
    let input = dir.path().join("game-input.ck3");
    let output = dir.path().join("game-output.ck3");
    fs::write(&input, &source).unwrap();
    let untouched_payload = raw_zip_payload(&source, b"sidecar");

    let report = parse_success(run(&[
        "set-scalar",
        "--section",
        "gamestate",
        "--expect",
        "quoted:old-name",
        "--value",
        "quoted:a-much-longer-player-name",
        "0x2345",
        input.to_str().unwrap(),
        output.to_str().unwrap(),
    ]));

    assert_eq!(report["section"], "gamestate");
    assert_eq!(report["span"]["coordinate_space"], "gamestate_uncompressed");
    assert_eq!(report["gamestate_integrity_checked"], true);
    assert_eq!(report["complete"], true);
    assert_eq!(fs::read(&input).unwrap(), source);
    let edited = fs::read(&output).unwrap();
    let (_, source_zip_start) = metadata_bounds(&source);
    let (_, edited_zip_start) = metadata_bounds(&edited);
    assert_eq!(source_zip_start, edited_zip_start);
    assert_eq!(edited[..edited_zip_start], source[..source_zip_start]);
    assert_eq!(raw_zip_payload(&edited, b"sidecar"), untouched_payload);
    assert_eq!(
        quoted_value(&verified_binary_gamestate(&edited), 0x2345),
        b"a-much-longer-player-name"
    );
    assert_eq!(
        report["source_sha256"],
        lowercase_hex(&sha256_bytes(&source).unwrap())
    );
    assert_eq!(
        report["output_sha256"],
        lowercase_hex(&sha256_bytes(&edited).unwrap())
    );
}

#[test]
fn gamestate_refuses_duplicate_mismatch_and_existing_output_without_artifacts() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1001, Token::U32(1));

    let mut duplicate_gamestate = scalar_field(0x3456, Token::U32(7));
    duplicate_gamestate.extend_from_slice(&scalar_field(0x3456, Token::U32(7)));
    let duplicate_source = unified_binary_save(&metadata, &duplicate_gamestate);
    let duplicate_input = dir.path().join("duplicate-game.ck3");
    let duplicate_output = dir.path().join("duplicate-game-output.ck3");
    fs::write(&duplicate_input, &duplicate_source).unwrap();
    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "gamestate",
            "--expect",
            "u32:7",
            "--value",
            "u32:9",
            "0x3456",
            duplicate_input.to_str().unwrap(),
            duplicate_output.to_str().unwrap(),
        ]),
        "matched 2 fields",
    );
    assert!(!duplicate_output.exists());
    assert_eq!(fs::read(&duplicate_input).unwrap(), duplicate_source);

    let gamestate = scalar_field(0x4567, Token::U32(11));
    let mismatch_source = unified_binary_save(&metadata, &gamestate);
    let mismatch_input = dir.path().join("mismatch-game.ck3");
    let mismatch_output = dir.path().join("mismatch-game-output.ck3");
    fs::write(&mismatch_input, &mismatch_source).unwrap();
    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "gamestate",
            "--expect",
            "u32:12",
            "--value",
            "u32:13",
            "0x4567",
            mismatch_input.to_str().unwrap(),
            mismatch_output.to_str().unwrap(),
        ]),
        "does not match",
    );
    assert!(!mismatch_output.exists());
    assert_eq!(fs::read(&mismatch_input).unwrap(), mismatch_source);

    let existing_output = dir.path().join("existing-game-output.ck3");
    fs::write(&existing_output, b"keep this output").unwrap();
    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "gamestate",
            "--expect",
            "u32:11",
            "--value",
            "u32:13",
            "0x4567",
            mismatch_input.to_str().unwrap(),
            existing_output.to_str().unwrap(),
        ]),
        "output already exists",
    );
    assert_eq!(fs::read(existing_output).unwrap(), b"keep this output");
    assert_eq!(fs::read(mismatch_input).unwrap(), mismatch_source);

    let leftovers: Vec<_> = fs::read_dir(dir.path())
        .unwrap()
        .map(|entry| entry.unwrap().file_name())
        .filter(|name| name.to_string_lossy().contains("ck3-index-edit"))
        .collect();
    assert!(
        leftovers.is_empty(),
        "temporary artifacts remained: {leftovers:?}"
    );
}

#[test]
fn gamestate_crc_failure_is_detected_before_output_creation() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1001, Token::U32(1));
    let gamestate = scalar_field(0x5678, Token::U32(7));
    let mut source = unified_binary_save(&metadata, &gamestate);
    let (_, zip_start) = metadata_bounds(&source);
    let central_offset = {
        let archive = ZipArchive::from_slice(&source[zip_start..]).unwrap();
        let mut found = None;
        for entry in archive.entries() {
            let entry = entry.unwrap();
            if entry.file_path().as_ref() == b"gamestate" {
                found = Some(entry.central_directory_offset() as usize);
                break;
            }
        }
        found.unwrap()
    };
    source[zip_start + central_offset + 16] ^= 0x80;
    let input = dir.path().join("bad-crc.ck3");
    let output = dir.path().join("bad-crc-output.ck3");
    fs::write(&input, &source).unwrap();

    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "gamestate",
            "--expect",
            "u32:7",
            "--value",
            "u32:9",
            "0x5678",
            input.to_str().unwrap(),
            output.to_str().unwrap(),
        ]),
        "gamestate",
    );
    assert!(!output.exists());
    assert_eq!(fs::read(input).unwrap(), source);
}

#[test]
fn gamestate_nesting_budget_is_enforced_before_output_creation() {
    let dir = TempDir::new();
    let metadata = scalar_field(0x1001, Token::U32(1));
    let mut gamestate = Vec::new();
    for _ in 0..513 {
        Token::Open.write(&mut gamestate).unwrap();
    }
    gamestate.extend_from_slice(&scalar_field(0x6789, Token::U32(7)));
    for _ in 0..513 {
        Token::Close.write(&mut gamestate).unwrap();
    }
    let source = unified_binary_save(&metadata, &gamestate);
    let input = dir.path().join("too-deep.ck3");
    let output = dir.path().join("too-deep-output.ck3");
    fs::write(&input, &source).unwrap();

    assert_failure(
        run(&[
            "set-scalar",
            "--section",
            "gamestate",
            "--expect",
            "u32:7",
            "--value",
            "u32:9",
            "0x6789",
            input.to_str().unwrap(),
            output.to_str().unwrap(),
        ]),
        "nesting limit",
    );
    assert!(!output.exists());
    assert_eq!(fs::read(input).unwrap(), source);
}
