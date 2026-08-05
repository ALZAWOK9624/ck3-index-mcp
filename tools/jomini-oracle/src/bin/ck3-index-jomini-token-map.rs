#![forbid(unsafe_code)]

use ck3_index_jomini_oracle::{lowercase_hex, sha256_bytes, sha256_reader};
use jomini::{
    binary::{Token as BinaryToken, TokenReader as BinaryTokenReader},
    envelope::{
        JominiFile, JominiFileKind, SaveContentKind, SaveHeader, SaveHeaderKind, SaveMetadataKind,
    },
    text::{Token as TextToken, TokenReader as TextTokenReader},
};
use serde::Serialize;
use std::{
    collections::{BTreeMap, BTreeSet},
    env,
    ffi::{OsStr, OsString},
    fs::{self, File, OpenOptions},
    io::{self, Read, Seek, SeekFrom, Write},
    path::{Path, PathBuf},
    process::{self, Command, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant},
};

const SCHEMA: &str = "ck3-index-jomini-token-map/v1";
const JOMINI_VERSION: &str = "0.35.0";
const MAX_SOURCE_BYTES: u64 = 16 * 1024 * 1024 * 1024;
const MAX_SECTION_BYTES: u64 = 256 * 1024 * 1024;
const MAX_ORACLE_STDOUT_BYTES: u64 = 64 * 1024 * 1024;
const MAX_ORACLE_STDERR_BYTES: u64 = 1024 * 1024;
const MAX_VERSION_STDOUT_BYTES: u64 = 64 * 1024;
const MIN_PROBE_BYTES: usize = 2 * 1024;
const MAX_RAKALY_WALL_TIME: Duration = Duration::from_secs(60);
static TEMP_SEQUENCE: AtomicU64 = AtomicU64::new(0);

fn main() {
    if let Err(error) = run() {
        eprintln!("jomini-token-map: {error}");
        process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let args: Vec<OsString> = env::args_os().skip(1).collect();
    if args
        .first()
        .is_some_and(|arg| arg == OsStr::new("--help") || arg == OsStr::new("-h"))
    {
        print_help();
        return Ok(());
    }
    if args
        .first()
        .is_some_and(|arg| arg == OsStr::new("--version"))
    {
        println!("ck3-index-jomini-token-map 0.0.0 (jomini {JOMINI_VERSION})");
        return Ok(());
    }

    let command = args
        .first()
        .and_then(|arg| arg.to_str())
        .ok_or_else(|| "expected command 'from-rakaly'".to_owned())?;
    if command != "from-rakaly" {
        return Err(format!(
            "unknown command {command:?}; expected 'from-rakaly'"
        ));
    }
    let args = FromRakalyArgs::parse(&args[1..])?;
    let report = build_from_rakaly(&args)?;
    write_json(&report)
}

fn print_help() {
    println!(
        "ck3-index-jomini-token-map from-rakaly --rakaly EXE --output MAP SAVE\n\
         \n\
         Build a version-matched CK3 binary token map through an explicit Rakaly executable.\n\
         SAVE is read-only and MAP must not already exist."
    );
}

struct FromRakalyArgs {
    rakaly: PathBuf,
    output: PathBuf,
    save: PathBuf,
}

impl FromRakalyArgs {
    fn parse(args: &[OsString]) -> Result<Self, String> {
        let mut rakaly = None;
        let mut output = None;
        let mut positional = Vec::new();
        let mut index = 0;
        let mut options = true;

        while index < args.len() {
            let arg = &args[index];
            if options && arg == OsStr::new("--") {
                options = false;
                index += 1;
                continue;
            }
            if options && arg == OsStr::new("--rakaly") {
                let value = args
                    .get(index + 1)
                    .ok_or_else(|| "--rakaly requires an EXE".to_owned())?;
                rakaly = Some(PathBuf::from(value));
                index += 2;
                continue;
            }
            if options && arg == OsStr::new("--output") {
                let value = args
                    .get(index + 1)
                    .ok_or_else(|| "--output requires a MAP".to_owned())?;
                output = Some(PathBuf::from(value));
                index += 2;
                continue;
            }
            if options && arg.to_string_lossy().starts_with('-') {
                return Err(format!("unknown from-rakaly option {arg:?}"));
            }
            positional.push(PathBuf::from(arg));
            index += 1;
        }

        if positional.len() != 1 {
            return Err("from-rakaly expects exactly one SAVE".to_owned());
        }
        Ok(Self {
            rakaly: rakaly.ok_or_else(|| "from-rakaly requires --rakaly EXE".to_owned())?,
            output: output.ok_or_else(|| "from-rakaly requires --output MAP".to_owned())?,
            save: positional.remove(0),
        })
    }
}

#[derive(Serialize)]
struct TokenMapReport {
    schema: &'static str,
    jomini: &'static str,
    rakaly_version: String,
    source_sha256: String,
    source_bytes: u64,
    unique_ids: usize,
    mappings: usize,
    map_sha256: String,
    map_bytes: usize,
    metadata: SectionReport,
    gamestate: SectionReport,
}

#[derive(Serialize)]
struct SectionReport {
    bytes: usize,
    complete: bool,
    integrity_checked: bool,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
struct FlavorPreamble {
    meta_id: u16,
    version_id: u16,
    version: i32,
}

fn build_from_rakaly(args: &FromRakalyArgs) -> Result<TokenMapReport, String> {
    if args.output.parent().is_none() {
        return Err("MAP must have a parent directory".to_owned());
    }
    if fs::symlink_metadata(&args.output).is_ok() {
        return Err(format!("MAP already exists: {}", args.output.display()));
    }

    let (source_bytes, source_hash) = source_fingerprint(&args.save)?;

    let input = File::open(&args.save)
        .map_err(|error| format!("cannot open SAVE {}: {error}", args.save.display()))?;
    let save = JominiFile::from_file(input)
        .map_err(|error| format!("cannot read SAVE envelope: {error}"))?;
    if !save.header().kind().is_binary() {
        return Err("from-rakaly requires binary CK3 metadata and gamestate".to_owned());
    }
    if matches!(save.header().kind(), SaveHeaderKind::Other(_)) {
        return Err("unsupported SAVE header kind".to_owned());
    }

    let metadata = match save
        .meta()
        .map_err(|error| format!("cannot open metadata: {error}"))?
    {
        SaveMetadataKind::Binary(reader) => read_section(reader, "metadata")?,
        SaveMetadataKind::Text(_) => return Err("SAVE metadata is not binary".to_owned()),
    };
    let preamble = extract_flavor_preamble(&metadata)?;
    let mut observed = collect_ids(&metadata, "metadata")?;

    let gamestate_integrity;
    let gamestate = match save.kind() {
        JominiFileKind::Zip(zip) => {
            gamestate_integrity = true;
            match zip
                .gamestate_verified()
                .map_err(|error| format!("cannot open verified gamestate: {error}"))?
            {
                SaveContentKind::Binary(reader) => read_section(reader, "gamestate")?,
                SaveContentKind::Text(_) => return Err("SAVE gamestate is not binary".to_owned()),
            }
        }
        JominiFileKind::Uncompressed(_) => {
            gamestate_integrity = false;
            let offset = (save.header().header_len() as u64)
                .checked_add(save.header().metadata_len())
                .ok_or_else(|| "gamestate offset overflows u64".to_owned())?;
            let mut input = File::open(&args.save)
                .map_err(|error| format!("cannot reopen SAVE gamestate: {error}"))?;
            input
                .seek(SeekFrom::Start(offset))
                .map_err(|error| format!("cannot seek to gamestate: {error}"))?;
            read_section(input, "gamestate")?
        }
    };
    observed.extend(collect_ids(&gamestate, "gamestate")?);

    let source_after = source_fingerprint(&args.save)?;
    if source_after != (source_bytes, source_hash) {
        return Err("SAVE changed while its token IDs were being collected".to_owned());
    }

    let output_directory = args.output.parent().expect("MAP parent was checked above");
    let (probe_guard, mut probe_file) = create_temp(output_directory, "probe")?;
    let probe = build_probe(save.header(), preamble, &observed)?;
    probe_file
        .write_all(&probe)
        .and_then(|_| probe_file.flush())
        .and_then(|_| probe_file.sync_all())
        .map_err(|error| format!("cannot persist temporary probe: {error}"))?;
    drop(probe_file);

    let version_output = run_command_bounded(
        &args.rakaly,
        &[OsString::from("--version")],
        MAX_VERSION_STDOUT_BYTES,
    )?;
    let rakaly_version = parse_version(&version_output)?;
    let melt_args = vec![
        OsString::from("melt"),
        OsString::from("--to-stdout"),
        OsString::from("--unknown-key"),
        OsString::from("error"),
        OsString::from("--format"),
        OsString::from("ck3"),
        OsString::from("--retain"),
        probe_guard.path.as_os_str().to_owned(),
    ];
    let melted = run_command_bounded(&args.rakaly, &melt_args, MAX_ORACLE_STDOUT_BYTES)?;
    let mappings = parse_melted_mappings(&melted, &observed)?;
    let map = encode_map(&mappings);
    let map_hash = sha256_bytes(&map).map_err(|error| format!("cannot hash MAP: {error}"))?;
    publish_map(&args.output, &map)?;

    Ok(TokenMapReport {
        schema: SCHEMA,
        jomini: JOMINI_VERSION,
        rakaly_version,
        source_sha256: lowercase_hex(&source_hash),
        source_bytes,
        unique_ids: observed.len(),
        mappings: mappings.len(),
        map_sha256: lowercase_hex(&map_hash),
        map_bytes: map.len(),
        metadata: SectionReport {
            bytes: metadata.len(),
            complete: true,
            integrity_checked: false,
        },
        gamestate: SectionReport {
            bytes: gamestate.len(),
            complete: true,
            integrity_checked: gamestate_integrity,
        },
    })
}

fn source_fingerprint(path: &Path) -> Result<(u64, [u8; 32]), String> {
    let file = File::open(path)
        .map_err(|error| format!("cannot open SAVE {}: {error}", path.display()))?;
    let source_bytes = file
        .metadata()
        .map_err(|error| format!("cannot stat SAVE {}: {error}", path.display()))?
        .len();
    if source_bytes > MAX_SOURCE_BYTES {
        return Err(format!(
            "SAVE is {source_bytes} bytes, exceeding the {MAX_SOURCE_BYTES}-byte limit"
        ));
    }
    let source_hash = sha256_reader(file, source_bytes)
        .map_err(|error| format!("cannot hash SAVE {}: {error}", path.display()))?;
    Ok((source_bytes, source_hash))
}

fn read_section<R: Read>(reader: R, name: &str) -> Result<Vec<u8>, String> {
    let mut data = Vec::new();
    reader
        .take(MAX_SECTION_BYTES + 1)
        .read_to_end(&mut data)
        .map_err(|error| format!("cannot completely read {name}: {error}"))?;
    if data.len() as u64 > MAX_SECTION_BYTES {
        return Err(format!(
            "{name} exceeds the {MAX_SECTION_BYTES}-byte decompressed limit"
        ));
    }
    Ok(data)
}

fn collect_ids(data: &[u8], name: &str) -> Result<BTreeSet<u16>, String> {
    let mut reader = BinaryTokenReader::from_slice(data);
    let mut identifiers = BTreeSet::new();
    let mut depth = 0usize;
    while let Some(token) = {
        let position = reader.position();
        reader
            .next()
            .map_err(|error| format!("cannot fully parse {name} at byte {position}: {error}"))?
    } {
        match token {
            BinaryToken::Id(id) => {
                identifiers.insert(id);
            }
            BinaryToken::Open => depth = depth.saturating_add(1),
            BinaryToken::Close => {
                depth = depth
                    .checked_sub(1)
                    .ok_or_else(|| format!("{name} contains an unmatched close container"))?;
            }
            _ => {}
        }
    }
    if depth != 0 {
        return Err(format!("{name} ends with {depth} unclosed container(s)"));
    }
    Ok(identifiers)
}

fn extract_flavor_preamble(metadata: &[u8]) -> Result<FlavorPreamble, String> {
    let mut reader = BinaryTokenReader::from_slice(metadata);
    let meta_id = match reader
        .next()
        .map_err(|error| format!("invalid metadata preamble: {error}"))?
    {
        Some(BinaryToken::Id(id)) => id,
        _ => return Err("metadata must begin with Id(meta)".to_owned()),
    };
    expect_binary(
        &mut reader,
        BinaryToken::Equal,
        "Id(meta) must be followed by '='",
    )?;
    expect_binary(
        &mut reader,
        BinaryToken::Open,
        "Id(meta)= must open a container",
    )?;
    let version_id = match reader
        .next()
        .map_err(|error| format!("invalid metadata preamble: {error}"))?
    {
        Some(BinaryToken::Id(id)) => id,
        _ => return Err("metadata meta block must begin with Id(version)".to_owned()),
    };
    expect_binary(
        &mut reader,
        BinaryToken::Equal,
        "Id(version) must be followed by '='",
    )?;
    let version = match reader
        .next()
        .map_err(|error| format!("invalid metadata preamble: {error}"))?
    {
        Some(BinaryToken::I32(value)) => value,
        _ => return Err("Id(version)= must have an I32 value".to_owned()),
    };
    Ok(FlavorPreamble {
        meta_id,
        version_id,
        version,
    })
}

fn expect_binary(
    reader: &mut BinaryTokenReader<'_>,
    expected: BinaryToken<'_>,
    message: &str,
) -> Result<(), String> {
    let actual = reader
        .next()
        .map_err(|error| format!("invalid metadata preamble: {error}"))?;
    if actual == Some(expected) {
        Ok(())
    } else {
        Err(message.to_owned())
    }
}

fn build_probe(
    source_header: &SaveHeader,
    preamble: FlavorPreamble,
    observed: &BTreeSet<u16>,
) -> Result<Vec<u8>, String> {
    let mut header = source_header.clone();
    header.set_kind(SaveHeaderKind::Binary);
    header.set_metadata_len(0);
    let mut output = Vec::new();
    header
        .write(&mut output)
        .map_err(|error| format!("cannot write probe header: {error}"))?;

    BinaryToken::Id(preamble.meta_id)
        .write(&mut output)
        .map_err(io_string)?;
    BinaryToken::Equal.write(&mut output).map_err(io_string)?;
    BinaryToken::Open.write(&mut output).map_err(io_string)?;
    write_version_field(&mut output, preamble)?;

    let mapping_bytes = observed
        .len()
        .checked_mul(10)
        .ok_or_else(|| "probe size overflow".to_owned())?;
    while output
        .len()
        .checked_add(2 + mapping_bytes)
        .ok_or_else(|| "probe size overflow".to_owned())?
        < MIN_PROBE_BYTES
    {
        write_version_field(&mut output, preamble)?;
    }
    BinaryToken::Close.write(&mut output).map_err(io_string)?;
    for &id in observed {
        BinaryToken::Id(id).write(&mut output).map_err(io_string)?;
        BinaryToken::Equal.write(&mut output).map_err(io_string)?;
        BinaryToken::U32(u32::from(id))
            .write(&mut output)
            .map_err(io_string)?;
    }
    Ok(output)
}

fn write_version_field(output: &mut Vec<u8>, preamble: FlavorPreamble) -> Result<(), String> {
    BinaryToken::Id(preamble.version_id)
        .write(&mut *output)
        .map_err(io_string)?;
    BinaryToken::Equal.write(&mut *output).map_err(io_string)?;
    BinaryToken::I32(preamble.version)
        .write(output)
        .map_err(io_string)
}

fn io_string(error: io::Error) -> String {
    error.to_string()
}

fn parse_melted_mappings(
    melted: &[u8],
    observed: &BTreeSet<u16>,
) -> Result<BTreeMap<u16, String>, String> {
    let save = JominiFile::from_slice(melted)
        .map_err(|error| format!("Rakaly stdout is not a Jomini envelope: {error}"))?;
    if !save.header().kind().is_text() {
        return Err("Rakaly stdout envelope is not text".to_owned());
    }
    let gamestate = save
        .gamestate()
        .map_err(|error| format!("cannot open Rakaly stdout gamestate: {error}"))?;
    let mut text = Vec::new();
    gamestate
        .take(MAX_ORACLE_STDOUT_BYTES + 1)
        .read_to_end(&mut text)
        .map_err(|error| format!("cannot read Rakaly stdout gamestate: {error}"))?;
    if text.len() as u64 > MAX_ORACLE_STDOUT_BYTES {
        return Err("Rakaly stdout gamestate exceeds its limit".to_owned());
    }
    parse_text_mappings(&text, observed)
}

fn parse_text_mappings(
    text: &[u8],
    observed: &BTreeSet<u16>,
) -> Result<BTreeMap<u16, String>, String> {
    enum State {
        Key,
        Operator(Vec<u8>),
        Value(Vec<u8>),
    }

    let mut reader = TextTokenReader::from_slice(text);
    let mut state = State::Key;
    let mut depth = 0usize;
    let mut result = BTreeMap::new();
    let mut names = BTreeSet::new();

    while let Some(token) = {
        let position = reader.position();
        reader
            .next()
            .map_err(|error| format!("cannot parse melted text at byte {position}: {error}"))?
    } {
        match token {
            TextToken::Open => {
                depth = depth.saturating_add(1);
                state = State::Key;
            }
            TextToken::Close => {
                depth = depth
                    .checked_sub(1)
                    .ok_or_else(|| "melted text has an unmatched close container".to_owned())?;
                state = State::Key;
            }
            TextToken::Operator(operator) if depth == 0 => {
                let State::Operator(key) = state else {
                    return Err("melted top-level operator has no key".to_owned());
                };
                if operator.symbol() != "=" {
                    return Err("melted mapping uses a non-equality operator".to_owned());
                }
                state = State::Value(key);
            }
            TextToken::Quoted(value) | TextToken::Unquoted(value) if depth == 0 => match state {
                State::Key => state = State::Operator(value.as_bytes().to_vec()),
                State::Operator(_) => {
                    return Err("melted top-level field is missing an operator".to_owned());
                }
                State::Value(key) => {
                    let raw_value = std::str::from_utf8(value.as_bytes())
                        .map_err(|_| "melted U32 value is not UTF-8".to_owned())?;
                    let numeric = raw_value
                        .parse::<u32>()
                        .map_err(|_| format!("melted mapping value {raw_value:?} is not U32"))?;
                    let id = u16::try_from(numeric)
                        .map_err(|_| format!("melted mapping value {numeric} exceeds u16"))?;
                    if !observed.contains(&id) {
                        return Err(format!(
                            "melted output contains unexpected mapping value {id}"
                        ));
                    }
                    let name = validate_map_name(&key)?;
                    if result.insert(id, name.clone()).is_some() {
                        return Err(format!("melted output maps 0x{id:04x} more than once"));
                    }
                    if !names.insert(name.clone()) {
                        return Err(format!("melted output repeats token name {name:?}"));
                    }
                    state = State::Key;
                }
            },
            _ => {}
        }
    }
    if depth != 0 {
        return Err(format!(
            "melted text ends with {depth} unclosed container(s)"
        ));
    }
    if !matches!(state, State::Key) {
        return Err("melted text ends with an incomplete top-level field".to_owned());
    }
    if result.len() != observed.len() {
        let missing = observed
            .iter()
            .filter(|id| !result.contains_key(id))
            .map(|id| format!("0x{id:04x}"))
            .collect::<Vec<_>>()
            .join(", ");
        return Err(format!("Rakaly omitted observed token ids: {missing}"));
    }
    Ok(result)
}

fn validate_map_name(bytes: &[u8]) -> Result<String, String> {
    let name = std::str::from_utf8(bytes)
        .map_err(|_| "melted token name is not valid UTF-8".to_owned())?;
    if name.is_empty()
        || name
            .chars()
            .any(|character| character.is_whitespace() || character.is_control())
    {
        return Err(format!(
            "melted token name {name:?} contains invalid whitespace"
        ));
    }
    Ok(name.to_owned())
}

fn encode_map(mappings: &BTreeMap<u16, String>) -> Vec<u8> {
    let mut output = Vec::new();
    for (id, name) in mappings {
        output.extend_from_slice(format!("0x{id:04x} {name}\n").as_bytes());
    }
    output
}

fn parse_version(output: &[u8]) -> Result<String, String> {
    let value = std::str::from_utf8(output)
        .map_err(|_| "Rakaly --version output is not UTF-8".to_owned())?
        .trim();
    if value.is_empty() || value.chars().any(char::is_control) {
        return Err("Rakaly --version returned an invalid version string".to_owned());
    }
    Ok(value.to_owned())
}

fn run_command_bounded(
    executable: &Path,
    args: &[OsString],
    max_stdout: u64,
) -> Result<Vec<u8>, String> {
    let mut child = Command::new(executable)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| format!("cannot start {}: {error}", executable.display()))?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| "cannot capture Rakaly stdout".to_owned())?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| "cannot capture Rakaly stderr".to_owned())?;
    let stdout_thread = thread::spawn(move || read_bounded_stream(stdout, max_stdout));
    let stderr_thread = thread::spawn(move || read_bounded_stream(stderr, MAX_ORACLE_STDERR_BYTES));
    let started = Instant::now();
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) if started.elapsed() < MAX_RAKALY_WALL_TIME => {
                thread::sleep(Duration::from_millis(10));
            }
            Ok(None) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(format!(
                    "Rakaly exceeded the {}-second wall-clock limit",
                    MAX_RAKALY_WALL_TIME.as_secs()
                ));
            }
            Err(error) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(format!("cannot poll Rakaly: {error}"));
            }
        }
    };
    let stdout = stdout_thread
        .join()
        .map_err(|_| "Rakaly stdout reader panicked".to_owned())??;
    let stderr = stderr_thread
        .join()
        .map_err(|_| "Rakaly stderr reader panicked".to_owned())??;
    if !status.success() {
        let detail = String::from_utf8_lossy(&stderr);
        return Err(format!("Rakaly exited with {status}: {}", detail.trim()));
    }
    Ok(stdout)
}

fn read_bounded_stream<R: Read>(reader: R, limit: u64) -> Result<Vec<u8>, String> {
    let mut output = Vec::new();
    reader
        .take(limit + 1)
        .read_to_end(&mut output)
        .map_err(|error| format!("cannot read child output: {error}"))?;
    if output.len() as u64 > limit {
        return Err(format!("child output exceeds the {limit}-byte limit"));
    }
    Ok(output)
}

struct TempGuard {
    path: PathBuf,
}

impl Drop for TempGuard {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}

fn create_temp(directory: &Path, purpose: &str) -> Result<(TempGuard, File), String> {
    if !directory.is_dir() {
        return Err(format!(
            "output directory does not exist: {}",
            directory.display()
        ));
    }
    for _ in 0..128 {
        let sequence = TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let path = directory.join(format!(
            ".ck3-index-token-map-{purpose}-{}-{sequence}.tmp",
            process::id()
        ));
        match OpenOptions::new().write(true).create_new(true).open(&path) {
            Ok(file) => return Ok((TempGuard { path }, file)),
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(format!("cannot create temporary {purpose}: {error}")),
        }
    }
    Err(format!("cannot allocate a unique temporary {purpose} file"))
}

fn publish_map(output: &Path, data: &[u8]) -> Result<(), String> {
    let directory = output
        .parent()
        .ok_or_else(|| "MAP must have a parent directory".to_owned())?;
    let (temporary, mut file) = create_temp(directory, "map")?;
    file.write_all(data)
        .and_then(|_| file.flush())
        .and_then(|_| file.sync_all())
        .map_err(|error| format!("cannot persist temporary MAP: {error}"))?;
    drop(file);
    let reread = fs::read(&temporary.path)
        .map_err(|error| format!("cannot reread temporary MAP: {error}"))?;
    if reread != data {
        return Err("temporary MAP reread differs from intended bytes".to_owned());
    }
    hard_link_no_clobber(&temporary.path, output)?;
    match fs::read(output) {
        Ok(published) if published == data => Ok(()),
        Ok(_) => remove_failed_publication(output, "published MAP differs from intended bytes"),
        Err(error) => {
            remove_failed_publication(output, &format!("cannot reread published MAP: {error}"))
        }
    }
}

fn hard_link_no_clobber(source: &Path, output: &Path) -> Result<(), String> {
    fs::hard_link(source, output).map_err(|error| {
        if error.kind() == io::ErrorKind::AlreadyExists {
            format!("MAP already exists: {}", output.display())
        } else {
            format!("cannot publish MAP without clobbering: {error}")
        }
    })
}

fn remove_failed_publication(output: &Path, reason: &str) -> Result<(), String> {
    match fs::remove_file(output) {
        Ok(()) => Err(reason.to_owned()),
        Err(error) => Err(format!(
            "{reason}; additionally cannot remove failed MAP: {error}"
        )),
    }
}

fn write_json<T: Serialize>(value: &T) -> Result<(), String> {
    let mut bytes = serde_json::to_vec_pretty(value)
        .map_err(|error| format!("cannot serialize report: {error}"))?;
    bytes.push(b'\n');
    io::stdout()
        .lock()
        .write_all(&bytes)
        .map_err(|error| format!("cannot write report: {error}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn header(kind: SaveHeaderKind) -> SaveHeader {
        let mut header = SaveHeader::from_slice(b"SAV01010000000000000000\n").unwrap();
        header.set_kind(kind);
        header.set_metadata_len(0);
        header
    }

    fn text_envelope(body: &[u8]) -> Vec<u8> {
        let mut output = Vec::new();
        header(SaveHeaderKind::Text).write(&mut output).unwrap();
        output.extend_from_slice(body);
        output
    }

    #[test]
    fn probe_is_a_readable_binary_jomini_envelope() {
        let observed = BTreeSet::from([0x1000, 0x2000]);
        let probe = build_probe(
            &header(SaveHeaderKind::UnifiedBinary),
            FlavorPreamble {
                meta_id: 0x3155,
                version_id: 0x058f,
                version: 15,
            },
            &observed,
        )
        .unwrap();
        assert!(probe.len() >= MIN_PROBE_BYTES);
        let save = JominiFile::from_slice(&probe).unwrap();
        assert_eq!(save.header().kind(), SaveHeaderKind::Binary);
        assert_eq!(save.header().metadata_len(), 0);
        let mut game = save.gamestate().unwrap();
        let mut body = Vec::new();
        game.read_to_end(&mut body).unwrap();
        let mut reader = BinaryTokenReader::from_slice(&body);
        let mut count = 0;
        while reader.next().unwrap().is_some() {
            count += 1;
        }
        assert!(count > 6);
    }

    #[test]
    fn melted_parser_requires_complete_unique_mappings() {
        let observed = BTreeSet::from([0x1000, 0x2000]);
        let valid = text_envelope(b"meta_data={ save_game_version=15 } alpha=4096 beta=8192");
        let mappings = parse_melted_mappings(&valid, &observed).unwrap();
        assert_eq!(mappings.get(&0x1000).map(String::as_str), Some("alpha"));
        assert_eq!(mappings.get(&0x2000).map(String::as_str), Some("beta"));

        let missing = text_envelope(b"alpha=4096");
        assert!(parse_melted_mappings(&missing, &observed).is_err());
        let duplicate = text_envelope(b"alpha=4096 alpha_again=4096 beta=8192");
        assert!(parse_melted_mappings(&duplicate, &observed).is_err());
        let invalid = text_envelope(b"\"bad name\"=4096 beta=8192");
        assert!(parse_melted_mappings(&invalid, &observed).is_err());
    }

    #[test]
    fn hard_link_publication_never_clobbers() {
        let directory = env::temp_dir().join(format!(
            "ck3-index-token-map-test-{}-{}",
            process::id(),
            TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&directory).unwrap();
        let source = directory.join("source");
        let output = directory.join("output");
        fs::write(&source, b"new").unwrap();
        fs::write(&output, b"old").unwrap();
        assert!(hard_link_no_clobber(&source, &output).is_err());
        assert_eq!(fs::read(&output).unwrap(), b"old");
        fs::remove_file(source).unwrap();
        fs::remove_file(output).unwrap();
        fs::remove_dir(directory).unwrap();
    }
}
