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
        "ck3-index-jomini-locate-{}-{sequence}-{label}",
        std::process::id()
    ));
    fs::write(&path, data).unwrap();
    TempFile(path)
}

fn run(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_ck3-index-jomini-locate"))
        .args(args)
        .output()
        .unwrap()
}

fn parse_success(output: Output) -> Value {
    assert!(output.status.success(), "{output:?}");
    assert!(output.stderr.is_empty(), "{output:?}");
    serde_json::from_slice(&output.stdout).unwrap()
}

fn header(kind: u16, metadata_len: usize) -> Vec<u8> {
    let value = format!("SAV01{kind:02x}deadbeef{metadata_len:08x}\n");
    assert_eq!(value.len(), 24);
    value.into_bytes()
}

fn binary_save(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    let mut data = header(1, metadata.len());
    data.extend_from_slice(metadata);
    data.extend_from_slice(gamestate);
    data
}

fn text_save(metadata: &[u8], gamestate: &[u8]) -> Vec<u8> {
    let mut data = header(0, metadata.len());
    data.extend_from_slice(metadata);
    data.extend_from_slice(gamestate);
    data
}

fn encode(tokens: &[Token<'_>]) -> Vec<u8> {
    let mut bytes = Vec::new();
    for token in tokens {
        token.write(&mut bytes).unwrap();
    }
    bytes
}

#[test]
fn locates_nested_repeated_raw_and_named_keys_after_complete_walk() {
    let metadata = encode(&[Token::Id(0x2000), Token::Equal, Token::U32(9)]);
    let gamestate = encode(&[
        Token::Id(0x1000),
        Token::Equal,
        Token::Open,
        Token::Id(0x2000),
        Token::Equal,
        Token::Id(0x4000),
        Token::Id(0x2000),
        Token::Equal,
        Token::U32(2),
        Token::Id(0x3000),
        Token::Equal,
        Token::Open,
        Token::Id(0x2000),
        Token::Equal,
        Token::U32(3),
        Token::Close,
        Token::Close,
        Token::Id(0x2000),
        Token::Equal,
        Token::U32(4),
    ]);
    let save = temp_file("nested.ck3", &binary_save(&metadata, &gamestate));
    let token_map_bytes = b"0x1000 root\n0x2000 target\n0x3000 nested\n0x4000 value_name\n";
    let tokens = temp_file("tokens.txt", token_map_bytes);
    let save_path = save.path().to_str().unwrap();

    let limited = parse_success(run(&["locate-key", "--limit", "1", "0x2000", save_path]));
    assert_eq!(limited["schema"], "ck3-index-jomini-save-locate-key/v1");
    assert_eq!(limited["all_match_count"], 4);
    assert_eq!(limited["matches"].as_array().unwrap().len(), 1);
    assert_eq!(limited["truncated"], true);
    assert!(limited["total_events"].as_u64().unwrap() > 4);
    assert_eq!(limited["matches"][0]["canonical_path"][1]["occurrence"], 0);
    assert_eq!(limited["matches"][0]["depth"], 1);
    assert_eq!(
        limited["section"]["coordinate_space"],
        "gamestate_uncompressed"
    );
    assert_eq!(
        limited["matches"][0]["coordinate_space"],
        "gamestate_uncompressed"
    );
    let gamestate_start = 24 + metadata.len();
    assert_eq!(
        limited["section"]["save_file_span"]["start"],
        gamestate_start
    );
    assert_eq!(
        limited["matches"][0]["save_file_spans"]["key_span"]["start"],
        gamestate_start + limited["matches"][0]["key_span"]["start"].as_u64().unwrap() as usize
    );
    assert_eq!(limited["matches"][0]["key"]["raw"]["token"], 0x2000);
    assert_eq!(limited["matches"][0]["value"]["kind"], "scalar");
    assert_eq!(
        limited["token_map"]["coverage"]["complete_for_section"],
        false
    );
    assert_eq!(
        limited["token_map"]["coverage"]["observed_identifier_occurrences"],
        7
    );
    assert_eq!(
        limited["token_map"]["coverage"]["observed_unique_identifiers"],
        4
    );

    let named = parse_success(run(&[
        "locate-key",
        "--token-map",
        tokens.path().to_str().unwrap(),
        "target",
        save_path,
    ]));
    assert_eq!(named["all_match_count"], 4);
    assert_eq!(named["matches"][0]["key"]["resolved"], "target");
    assert_eq!(named["matches"][0]["value"]["resolved"], "value_name");
    assert_eq!(named["section"]["bytes"], gamestate.len());
    assert_eq!(named["section"]["integrity_checked"], false);
    assert_eq!(named["token_map"]["bytes"], token_map_bytes.len());
    assert_eq!(named["token_map"]["sha256"].as_str().unwrap().len(), 64);
    assert_eq!(named["token_map"]["coverage"]["complete_for_section"], true);
    assert_eq!(
        named["token_map"]["coverage"]["resolved_unique_identifiers"],
        4
    );
    assert_eq!(
        named["token_map"]["coverage"]["unresolved_unique_identifiers"],
        0
    );

    let meta = parse_success(run(&[
        "locate-key",
        "--section",
        "metadata",
        "0x2000",
        save_path,
    ]));
    assert_eq!(meta["all_match_count"], 1);
    assert_eq!(meta["section"]["name"], "metadata");
    assert_eq!(meta["section"]["coordinate_space"], "metadata_uncompressed");
    assert_eq!(meta["section"]["save_file_span"]["start"], 24);
    assert_eq!(
        meta["section"]["save_file_span"]["end"],
        24 + metadata.len()
    );
    assert_eq!(
        meta["matches"][0]["coordinate_space"],
        "metadata_uncompressed"
    );
    assert_eq!(
        meta["matches"][0]["save_file_spans"]["key_span"]["start"],
        24
    );

    let partial_tokens = temp_file("partial-tokens.txt", b"0x2000 target\n");
    let rejected = run(&[
        "locate-key",
        "--token-map",
        partial_tokens.path().to_str().unwrap(),
        "target",
        save_path,
    ]);
    assert!(!rejected.status.success(), "{rejected:?}");
    assert!(rejected.stdout.is_empty(), "{rejected:?}");
    assert!(
        String::from_utf8_lossy(&rejected.stderr).contains("complete token-map coverage"),
        "{rejected:?}"
    );

    let raw_partial = parse_success(run(&[
        "locate-key",
        "--token-map",
        partial_tokens.path().to_str().unwrap(),
        "0x2000",
        save_path,
    ]));
    assert_eq!(raw_partial["all_match_count"], 4);
    assert_eq!(
        raw_partial["token_map"]["coverage"]["complete_for_section"],
        false
    );
    assert_eq!(
        raw_partial["token_map"]["coverage"]["unresolved_unique_identifiers"],
        3
    );
}

#[test]
fn rejects_byte_cap_and_text_sections_without_json() {
    let gamestate = encode(&[Token::Id(0x2000), Token::Equal, Token::U32(1)]);
    let binary = temp_file("cap.ck3", &binary_save(&[], &gamestate));
    let cap = (gamestate.len() - 1).to_string();
    let capped = run(&[
        "locate-key",
        "--max-bytes",
        &cap,
        "0x2000",
        binary.path().to_str().unwrap(),
    ]);
    assert!(!capped.status.success(), "{capped:?}");
    assert!(capped.stdout.is_empty(), "{capped:?}");
    assert!(
        String::from_utf8_lossy(&capped.stderr).contains("complete-read cap"),
        "{capped:?}"
    );

    let text = temp_file("text.ck3", &text_save(b"meta=yes\n", b"target=yes\n"));
    let rejected = run(&["locate-key", "0x2000", text.path().to_str().unwrap()]);
    assert!(!rejected.status.success(), "{rejected:?}");
    assert!(rejected.stdout.is_empty(), "{rejected:?}");
    assert!(
        String::from_utf8_lossy(&rejected.stderr).contains("text, not binary"),
        "{rejected:?}"
    );
}

#[test]
fn verified_zip_rejects_bad_gamestate_crc() {
    let metadata = encode(&[Token::Id(0x1000), Token::Equal, Token::U32(1)]);
    let gamestate = encode(&[Token::Id(0x2000), Token::Equal, Token::U32(2)]);
    let save = temp_file(
        "bad-crc.ck3",
        &split_binary_zip(&metadata, &gamestate, true),
    );
    let output = run(&["locate-key", "0x2000", save.path().to_str().unwrap()]);
    assert!(!output.status.success(), "{output:?}");
    assert!(output.stdout.is_empty(), "{output:?}");
    assert!(
        String::from_utf8_lossy(&output.stderr).contains("cannot completely read gamestate"),
        "{output:?}"
    );
}

#[test]
fn zipped_sections_report_logical_coordinates_without_fake_file_offsets() {
    let metadata = encode(&[Token::Id(0x1000), Token::Equal, Token::U32(1)]);
    let gamestate = encode(&[Token::Id(0x2000), Token::Equal, Token::U32(2)]);
    let save = temp_file(
        "coordinate-spaces.ck3",
        &split_binary_zip(&metadata, &gamestate, false),
    );
    let path = save.path().to_str().unwrap();

    let game = parse_success(run(&["locate-key", "0x2000", path]));
    assert_eq!(
        game["section"]["coordinate_space"],
        "gamestate_uncompressed"
    );
    assert!(game["section"]["save_file_span"].is_null());
    assert_eq!(
        game["matches"][0]["coordinate_space"],
        "gamestate_uncompressed"
    );
    assert!(game["matches"][0]["save_file_spans"].is_null());

    let meta = parse_success(run(&[
        "locate-key",
        "--section",
        "metadata",
        "0x1000",
        path,
    ]));
    assert_eq!(meta["section"]["coordinate_space"], "metadata_uncompressed");
    assert!(meta["section"]["save_file_span"].is_null());
    assert_eq!(
        meta["matches"][0]["coordinate_space"],
        "metadata_uncompressed"
    );
    assert!(meta["matches"][0]["save_file_spans"].is_null());
}

struct CentralEntry {
    name: Vec<u8>,
    crc32: u32,
    compressed_size: u32,
    uncompressed_size: u32,
    local_offset: u32,
}

fn split_binary_zip(metadata: &[u8], gamestate: &[u8], bad_crc: bool) -> Vec<u8> {
    let mut output = header(5, 0);
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
