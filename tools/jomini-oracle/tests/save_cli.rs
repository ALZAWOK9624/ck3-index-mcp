use flate2::{Compression, write::DeflateEncoder};
use jomini::binary::Token;
use serde_json::Value;
use std::{
    fs,
    io::Write,
    path::{Path, PathBuf},
    process::{Command, Output},
    sync::atomic::{AtomicU64, Ordering},
};

static NEXT_TEMP: AtomicU64 = AtomicU64::new(0);

fn save_reader() -> Command {
    Command::new(env!("CARGO_BIN_EXE_ck3-index-jomini-save"))
}

struct TempFile(PathBuf);

impl TempFile {
    fn path(&self) -> &Path {
        &self.0
    }
}

impl Drop for TempFile {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

fn temp_file(label: &str, data: &[u8]) -> TempFile {
    let sequence = NEXT_TEMP.fetch_add(1, Ordering::Relaxed);
    let path = std::env::temp_dir().join(format!(
        "ck3-index-jomini-save-{}-{sequence}-{label}",
        std::process::id()
    ));
    fs::write(&path, data).expect("temporary fixture should be writable");
    TempFile(path)
}

fn run(args: &[&str]) -> Output {
    save_reader()
        .args(args)
        .output()
        .expect("save reader should start")
}

fn parse_success(output: Output) -> Value {
    assert!(output.status.success(), "{output:?}");
    assert!(output.stderr.is_empty(), "{output:?}");
    serde_json::from_slice(&output.stdout).expect("stdout should be valid JSON")
}

fn header(kind: u16, metadata_len: usize) -> Vec<u8> {
    let value = format!("SAV01{kind:02x}deadbeef{metadata_len:08x}\n");
    assert_eq!(value.len(), 24);
    value.into_bytes()
}

fn uncompressed_text_save(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    let mut data = header(0, metadata.len());
    data.extend_from_slice(metadata);
    data.extend_from_slice(gamestate);
    data
}

fn uncompressed_binary_save(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    let mut data = header(1, metadata.len());
    data.extend_from_slice(metadata);
    data.extend_from_slice(gamestate);
    data
}

#[test]
fn overview_and_find_key_read_uncompressed_plaintext() {
    let metadata = b"meta=yes\ndate=1066.9.15\n";
    let gamestate = b"target=one\nnested={ target={ value=2 } }\n0x10000=literal\n";
    let fixture = temp_file(
        "plaintext.ck3",
        &uncompressed_text_save(metadata, gamestate),
    );
    let path = fixture.path().to_str().unwrap();

    let overview = parse_success(run(&["overview", path]));
    assert_eq!(overview["schema"], "ck3-index-jomini-save-overview/v1");
    assert_eq!(overview["container"], "uncompressed");
    assert_eq!(overview["header"]["kind"], "text");
    assert_eq!(overview["metadata"]["encoding"], "text");
    assert_eq!(overview["metadata"]["text"]["top_level_fields"], 2);
    assert_eq!(overview["gamestate"]["encoding"], "text");
    assert_eq!(overview["gamestate"]["scanned"], false);
    assert_eq!(
        overview["gamestate"]["uncompressed_bytes_hint"],
        gamestate.len()
    );

    let found = parse_success(run(&["find-key", "target", path]));
    assert_eq!(found["sections"][0]["encoding"], "text");
    assert_eq!(found["sections"][0]["complete"], true);
    assert_eq!(found["sections"][0]["integrity_checked"], false);
    assert_eq!(found["sections"][0]["syntax_checked"], false);
    assert_eq!(found["sections"][0]["matches"].as_array().unwrap().len(), 2);
    assert_eq!(found["sections"][0]["matches"][0]["depth"], 0);
    assert_eq!(found["sections"][0]["matches"][1]["depth"], 1);

    let literal_0x = parse_success(run(&["find-key", "0x10000", path]));
    assert_eq!(
        literal_0x["sections"][0]["matches"]
            .as_array()
            .unwrap()
            .len(),
        1
    );

    let separated = parse_success(run(&["find-key", "--section", "both", "meta", path]));
    assert_eq!(separated["sections"][0]["name"], "metadata");
    assert_eq!(
        separated["sections"][0]["matches"]
            .as_array()
            .unwrap()
            .len(),
        1
    );
    assert_eq!(separated["sections"][1]["name"], "gamestate");
    assert_eq!(
        separated["sections"][1]["matches"]
            .as_array()
            .unwrap()
            .len(),
        0
    );

    let limited = parse_success(run(&["find-key", "--limit", "1", "target", path]));
    assert_eq!(limited["sections"][0]["truncated"], true);
    assert_eq!(limited["sections"][0]["complete"], false);
    assert_eq!(limited["sections"][0]["stop_reason"], "match_limit");
    assert_eq!(
        limited["sections"][0]["matches"].as_array().unwrap().len(),
        1
    );
}

#[test]
fn binary_find_key_requires_matching_token_data_for_names() {
    let mut metadata = Vec::new();
    Token::Id(0x1000).write(&mut metadata).unwrap();
    Token::Equal.write(&mut metadata).unwrap();
    Token::U32(1).write(&mut metadata).unwrap();

    let mut gamestate = Vec::new();
    Token::Id(0x2000).write(&mut gamestate).unwrap();
    Token::Equal.write(&mut gamestate).unwrap();
    Token::U32(42).write(&mut gamestate).unwrap();

    let fixture = temp_file(
        "binary.ck3",
        &uncompressed_binary_save(&metadata, &gamestate),
    );
    let token_map = temp_file("tokens.txt", b"0x1000 meta\n0x2000 target\n");
    let path = fixture.path().to_str().unwrap();

    let overview = parse_success(run(&["overview", path]));
    assert_eq!(overview["header"]["kind"], "binary");
    assert_eq!(overview["metadata"]["token_resolver_required"], true);
    assert_eq!(overview["gamestate"]["token_resolver_required"], true);

    let token_ids = parse_success(run(&["token-ids", "--section", "both", path]));
    assert_eq!(token_ids["schema"], "ck3-index-jomini-save-token-ids/v1");
    assert_eq!(token_ids["sections"][0]["complete"], true);
    assert_eq!(token_ids["sections"][1]["complete"], true);
    assert_eq!(token_ids["unique_identifiers"].as_array().unwrap().len(), 2);
    assert_eq!(token_ids["unique_identifiers"][0]["token"], "0x1000");
    assert_eq!(token_ids["unique_identifiers"][1]["token"], "0x2000");

    let unresolved = parse_success(run(&["find-key", "target", path]));
    assert_eq!(
        unresolved["sections"][0]["matches"]
            .as_array()
            .unwrap()
            .len(),
        0
    );
    assert_eq!(unresolved["sections"][0]["unresolved_identifier_keys"], 1);

    let resolved = parse_success(run(&[
        "find-key",
        "--token-map",
        token_map.path().to_str().unwrap(),
        "target",
        path,
    ]));
    assert_eq!(resolved["query"]["token_map_configured"], true);
    assert_eq!(
        resolved["sections"][0]["matches"][0]["key"]["token"],
        "0x2000"
    );
    assert_eq!(
        resolved["sections"][0]["matches"][0]["value"]["kind"],
        "u32"
    );
    assert_eq!(resolved["sections"][0]["matches"][0]["value"]["value"], 42);
}

#[test]
fn split_zip_is_detected_and_streamed() {
    let metadata = b"meta=yes\ndate=1066.9.15\n";
    let gamestate = b"target=compressed\n";
    let fixture = temp_file("compressed.ck3", &split_text_zip(metadata, gamestate));
    let path = fixture.path().to_str().unwrap();

    let overview = parse_success(run(&["overview", path]));
    assert_eq!(overview["container"], "zip");
    assert_eq!(overview["header"]["kind"], "split_text");
    assert_eq!(overview["metadata"]["text"]["top_level_fields"], 2);
    assert_eq!(
        overview["gamestate"]["uncompressed_bytes_hint"],
        gamestate.len()
    );

    let found = parse_success(run(&["find-key", "target", path]));
    assert_eq!(found["sections"][0]["matches"][0]["depth"], 0);
    assert_eq!(found["sections"][0]["complete"], true);
    assert_eq!(found["sections"][0]["integrity_checked"], true);
    assert_eq!(
        found["sections"][0]["matches"][0]["value"]["scalar"]["utf8"],
        "compressed"
    );
}

#[test]
fn scan_byte_limit_returns_an_explicit_incomplete_report() {
    let fixture = temp_file(
        "scan-cap.ck3",
        &uncompressed_text_save(b"meta=yes\n", b"a=b\npadding=value\n"),
    );
    let output = parse_success(run(&[
        "find-key",
        "--max-bytes",
        "4",
        "missing",
        fixture.path().to_str().unwrap(),
    ]));
    let section = &output["sections"][0];
    assert_eq!(section["complete"], false);
    assert_eq!(section["stop_reason"], "byte_limit");
    assert_eq!(section["integrity_checked"], false);
    assert!(section["bytes_scanned"].as_u64().unwrap() <= 4);
    assert!(section["decompressed_bytes_read"].as_u64().unwrap() <= 4);
    assert_eq!(section["lookahead_bytes_read"], 1);
}

#[test]
fn split_binary_zip_uses_tokens_and_checks_integrity() {
    let mut metadata = Vec::new();
    Token::Id(0x1000).write(&mut metadata).unwrap();
    Token::Equal.write(&mut metadata).unwrap();
    Token::U32(1).write(&mut metadata).unwrap();
    let mut gamestate = Vec::new();
    Token::Id(0x2000).write(&mut gamestate).unwrap();
    Token::Equal.write(&mut gamestate).unwrap();
    Token::U32(42).write(&mut gamestate).unwrap();

    let fixture = temp_file("binary-zip.ck3", &split_binary_zip(&metadata, &gamestate));
    let token_map = temp_file("binary-zip-tokens.txt", b"0x2000 target\n");
    let output = parse_success(run(&[
        "find-key",
        "--token-map",
        token_map.path().to_str().unwrap(),
        "target",
        fixture.path().to_str().unwrap(),
    ]));
    assert_eq!(output["sections"][0]["encoding"], "binary");
    assert_eq!(output["sections"][0]["complete"], true);
    assert_eq!(output["sections"][0]["integrity_checked"], true);
    assert_eq!(output["sections"][0]["matches"][0]["value"]["value"], 42);
}

#[test]
fn corrupt_zip_gamestate_fails_without_partial_json() {
    let fixture = temp_file(
        "bad-crc.ck3",
        &split_text_zip_with_bad_gamestate_crc(b"meta=yes\n", b"target=compressed\n"),
    );
    let output = run(&["find-key", "target", fixture.path().to_str().unwrap()]);

    assert!(!output.status.success(), "{output:?}");
    assert!(output.stdout.is_empty(), "{output:?}");
    assert!(
        String::from_utf8_lossy(&output.stderr).contains("cannot scan gamestate"),
        "{output:?}"
    );
}

#[test]
fn overlong_token_names_are_rejected_before_scanning() {
    let mut gamestate = Vec::new();
    Token::Id(0x2000).write(&mut gamestate).unwrap();
    Token::Equal.write(&mut gamestate).unwrap();
    Token::U32(42).write(&mut gamestate).unwrap();
    let fixture = temp_file(
        "bounded-token-map.ck3",
        &uncompressed_binary_save(&[], &gamestate),
    );
    let token_map = temp_file(
        "overlong-token-map.txt",
        format!("0x2000 {}\n", "x".repeat(257)).as_bytes(),
    );
    let output = run(&[
        "find-key",
        "--token-map",
        token_map.path().to_str().unwrap(),
        "target",
        fixture.path().to_str().unwrap(),
    ]);

    assert!(!output.status.success(), "{output:?}");
    assert!(output.stdout.is_empty(), "{output:?}");
    assert!(
        String::from_utf8_lossy(&output.stderr).contains("exceeds 256 bytes"),
        "{output:?}"
    );
}

#[test]
fn malformed_envelopes_fail_without_partial_json() {
    let mut data = split_text_zip(b"meta=yes\n", b"target=compressed\n");
    for offset in find_all(&data, b"gamestate") {
        data[offset..offset + b"gamestate".len()].copy_from_slice(b"badestate");
    }
    let fixture = temp_file("missing-entry.ck3", &data);
    let output = run(&["overview", fixture.path().to_str().unwrap()]);

    assert!(!output.status.success(), "{output:?}");
    assert!(output.stdout.is_empty(), "{output:?}");
    assert!(
        String::from_utf8_lossy(&output.stderr).contains("gamestate"),
        "{output:?}"
    );

    let mut malformed = header(0, 2048);
    malformed.extend(std::iter::repeat_n(b'x', 1000));
    let fixture = temp_file("metadata-past-eof.ck3", &malformed);
    let output = run(&["find-key", "target", fixture.path().to_str().unwrap()]);
    assert!(!output.status.success(), "{output:?}");
    assert!(output.stdout.is_empty(), "{output:?}");
    assert!(
        String::from_utf8_lossy(&output.stderr).contains("declared metadata"),
        "{output:?}"
    );

    for (label, kind, expected) in [
        ("split-without-zip.ck3", 4, "requires a ZIP container"),
        ("unknown-kind.ck3", 0xff, "unsupported save header kind"),
    ] {
        let mut malformed = header(kind, 0);
        malformed.extend_from_slice(b"target=forged\n");
        malformed.extend(std::iter::repeat_n(b' ', 1000));
        let fixture = temp_file(label, &malformed);
        let output = run(&["find-key", "target", fixture.path().to_str().unwrap()]);
        assert!(!output.status.success(), "{output:?}");
        assert!(output.stdout.is_empty(), "{output:?}");
        assert!(
            String::from_utf8_lossy(&output.stderr).contains(expected),
            "{output:?}"
        );
    }
}

#[derive(Clone)]
struct CentralEntry {
    name: Vec<u8>,
    crc32: u32,
    compressed_size: u32,
    uncompressed_size: u32,
    local_offset: u32,
}

fn split_text_zip(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    split_zip(4, metadata, gamestate, false)
}

fn split_binary_zip(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    split_zip(5, metadata, gamestate, false)
}

fn split_text_zip_with_bad_gamestate_crc(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    split_zip(4, metadata, gamestate, true)
}

fn split_zip(kind: u16, metadata: &[u8], gamestate: &[u8], bad_crc: bool) -> Vec<u8> {
    let mut output = header(kind, 0);
    let mut entries = [
        append_local_entry(&mut output, b"meta", metadata),
        append_local_entry(&mut output, b"gamestate", gamestate),
    ];
    if bad_crc {
        entries[1].crc32 ^= 1;
    }
    let central_offset = output.len() as u32;

    for entry in &entries {
        put_u32(&mut output, 0x0201_4b50);
        put_u16(&mut output, 20);
        put_u16(&mut output, 20);
        put_u16(&mut output, 0);
        put_u16(&mut output, 8);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u32(&mut output, entry.crc32);
        put_u32(&mut output, entry.compressed_size);
        put_u32(&mut output, entry.uncompressed_size);
        put_u16(&mut output, entry.name.len() as u16);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u16(&mut output, 0);
        put_u32(&mut output, 0);
        put_u32(&mut output, entry.local_offset);
        output.extend_from_slice(&entry.name);
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

fn append_local_entry(output: &mut Vec<u8>, name: &[u8], data: &[u8]) -> CentralEntry {
    let local_offset = output.len() as u32;
    let mut encoder = DeflateEncoder::new(Vec::new(), Compression::default());
    encoder.write_all(data).unwrap();
    let compressed = encoder.finish().unwrap();
    let crc32 = crc32(data);

    put_u32(output, 0x0403_4b50);
    put_u16(output, 20);
    put_u16(output, 0);
    put_u16(output, 8);
    put_u16(output, 0);
    put_u16(output, 0);
    put_u32(output, crc32);
    put_u32(output, compressed.len() as u32);
    put_u32(output, data.len() as u32);
    put_u16(output, name.len() as u16);
    put_u16(output, 0);
    output.extend_from_slice(name);
    output.extend_from_slice(&compressed);

    CentralEntry {
        name: name.to_vec(),
        crc32,
        compressed_size: compressed.len() as u32,
        uncompressed_size: data.len() as u32,
        local_offset,
    }
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

fn put_u16(output: &mut Vec<u8>, value: u16) {
    output.extend_from_slice(&value.to_le_bytes());
}

fn put_u32(output: &mut Vec<u8>, value: u32) {
    output.extend_from_slice(&value.to_le_bytes());
}

fn find_all(haystack: &[u8], needle: &[u8]) -> Vec<usize> {
    haystack
        .windows(needle.len())
        .enumerate()
        .filter_map(|(index, value)| (value == needle).then_some(index))
        .collect()
}
