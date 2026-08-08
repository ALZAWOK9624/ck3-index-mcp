#![forbid(unsafe_code)]

use jomini::{
    TextTape, TextToken,
    binary::{
        BasicTokenResolver, Token as BinaryToken, TokenReader as BinaryTokenReader, TokenResolver,
    },
    envelope::{JominiFile, JominiFileKind, SaveContentKind, SaveHeaderKind, SaveMetadataKind},
    text::{Operator, Token as TextStreamToken, TokenReader as TextTokenReader},
};
use serde::Serialize;
use std::{
    cell::Cell,
    collections::BTreeMap,
    env,
    ffi::{OsStr, OsString},
    fs::{self, File},
    io::{self, BufWriter, Read, Seek, SeekFrom, Write},
    path::{Path, PathBuf},
    process,
    rc::Rc,
};

const JOMINI_VERSION: &str = "0.35.0";
const OVERVIEW_SCHEMA: &str = "ck3-index-jomini-save-overview/v1";
const FIND_SCHEMA: &str = "ck3-index-jomini-save-find-key/v1";
const TOKEN_IDS_SCHEMA: &str = "ck3-index-jomini-save-token-ids/v1";
const METADATA_INSPECTION_LIMIT: usize = 1024 * 1024;
const METADATA_SAMPLE_LIMIT: usize = 32;
const SCALAR_PREVIEW_LIMIT: usize = 256;
const STREAM_BUFFER_BYTES: usize = 1024 * 1024;
const DEFAULT_MATCH_LIMIT: usize = 100;
const MAX_MATCH_LIMIT: usize = 1000;
const DEFAULT_SCAN_MAX_BYTES: u64 = 256 * 1024 * 1024;
const MAX_SCAN_MAX_BYTES: u64 = 16 * 1024 * 1024 * 1024;
const TOKEN_MAP_MAX_BYTES: u64 = 16 * 1024 * 1024;
const TOKEN_MAP_MAX_LINE_BYTES: usize = 4 * 1024;
const TOKEN_NAME_MAX_BYTES: usize = 256;

fn main() {
    if let Err(error) = run() {
        eprintln!("jomini-save: {error}");
        process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let args: Vec<OsString> = env::args_os().skip(1).collect();
    let Some(command) = args.first() else {
        print_help();
        return Ok(());
    };

    if command == OsStr::new("--help") || command == OsStr::new("-h") {
        print_help();
        return Ok(());
    }
    if command == OsStr::new("--version") {
        println!("ck3-index-jomini-save 0.0.0 (jomini {JOMINI_VERSION})");
        return Ok(());
    }

    match command.to_str() {
        Some("overview") => {
            if args.get(1).is_some_and(|x| x == OsStr::new("--help")) {
                print_overview_help();
                return Ok(());
            }
            let path = one_path(&args[1..], "overview")?;
            let report = build_overview(&path)?;
            write_json(&report)
        }
        Some("find-key") => {
            if args.get(1).is_some_and(|x| x == OsStr::new("--help")) {
                print_find_help();
                return Ok(());
            }
            let find = FindArgs::parse(&args[1..])?;
            let report = build_find_report(&find)?;
            write_json(&report)
        }
        Some("token-ids") => {
            if args.get(1).is_some_and(|x| x == OsStr::new("--help")) {
                print_token_ids_help();
                return Ok(());
            }
            let token_ids = TokenIdsArgs::parse(&args[1..])?;
            let report = build_token_ids_report(&token_ids)?;
            write_json(&report)
        }
        Some(other) => Err(format!(
            "unknown command {other:?}; expected 'overview', 'find-key', or 'token-ids'"
        )),
        None => Err("command must be valid Unicode".to_owned()),
    }
}

fn print_help() {
    println!(
        "ck3-index-jomini-save <COMMAND> [OPTIONS]\n\
         \n\
         Read modern Paradox save envelopes without modifying them.\n\
         \n\
         Commands:\n\
           overview FILE   Inspect the envelope and bounded metadata\n\
           find-key ...    Stream the selected section for an exact field key\n\
           token-ids ...   Count every binary identifier token in a section\n\
         \n\
         Use '<COMMAND> --help' for command-specific arguments."
    );
}

fn print_token_ids_help() {
    println!(
        "ck3-index-jomini-save token-ids [OPTIONS] FILE\n\
         \n\
         Stream binary save content and count every raw identifier token.\n\
         This is useful for constructing a version-matched external token map.\n\
         \n\
         Options:\n\
           --section metadata|gamestate|both   Section to scan (default: both)\n\
           --max-bytes N                       Decompressed cap per section (default: 268435456)"
    );
}

fn print_overview_help() {
    println!(
        "ck3-index-jomini-save overview FILE\n\
         \n\
         Report the actual container, header, encoding, bounded metadata summary,\n\
         and gamestate size hint. The gamestate is not scanned by this command."
    );
}

fn print_find_help() {
    println!(
        "ck3-index-jomini-save find-key [OPTIONS] KEY FILE\n\
         \n\
         Stream save content and report exact field-key matches.\n\
         \n\
         Options:\n\
           --section metadata|gamestate|both   Section to scan (default: gamestate)\n\
           --token-map FILE                    0x1234 field_name mapping for binary saves\n\
           --limit N                           Matches per section, 1..1000 (default: 100)\n\
           --max-bytes N                       Decompressed scan cap (default: 268435456)"
    );
}

fn one_path(args: &[OsString], command: &str) -> Result<PathBuf, String> {
    if args.len() != 1 {
        return Err(format!("{command} expects exactly one save FILE"));
    }
    Ok(PathBuf::from(&args[0]))
}

fn write_json<T: Serialize>(value: &T) -> Result<(), String> {
    let mut document = serde_json::to_vec_pretty(value)
        .map_err(|error| format!("cannot serialize report: {error}"))?;
    document.push(b'\n');

    let stdout = io::stdout();
    let mut out = BufWriter::new(stdout.lock());
    out.write_all(&document)
        .and_then(|_| out.flush())
        .map_err(|error| format!("cannot write report: {error}"))
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
enum EncodingKind {
    Text,
    Binary,
}

#[derive(Serialize)]
struct OverviewReport {
    schema: &'static str,
    jomini: &'static str,
    source_bytes: u64,
    container: &'static str,
    header: HeaderReport,
    metadata: MetadataReport,
    gamestate: GamestateReport,
}

#[derive(Serialize)]
struct HeaderReport {
    version: u16,
    kind: &'static str,
    kind_code: u16,
    header_bytes: usize,
    declared_metadata_bytes: u64,
}

#[derive(Serialize)]
struct MetadataReport {
    encoding: EncodingKind,
    declared_bytes: u64,
    inspected_bytes: usize,
    inspection_limit_bytes: usize,
    truncated: bool,
    token_resolver_required: bool,
    text: Option<TextMetadataSummary>,
}

#[derive(Serialize)]
struct GamestateReport {
    encoding: EncodingKind,
    uncompressed_bytes_hint: u64,
    scanned: bool,
    integrity_checked: bool,
    token_resolver_required: bool,
}

#[derive(Serialize)]
struct TextMetadataSummary {
    tokens: usize,
    top_level_fields: usize,
    scalar_fields: usize,
    samples: Vec<TextFieldSample>,
    samples_truncated: bool,
}

#[derive(Serialize)]
struct TextFieldSample {
    key: ScalarView,
    operator: OperatorView,
    value: ScalarView,
}

#[derive(Clone, Serialize)]
struct ScalarView {
    representation: &'static str,
    bytes: usize,
    preview_hex: String,
    utf8: Option<String>,
    truncated: bool,
}

#[derive(Clone, Serialize)]
struct OperatorView {
    name: String,
    symbol: String,
}

fn build_overview(path: &Path) -> Result<OverviewReport, String> {
    let source_bytes = fs::metadata(path)
        .map_err(|error| format!("cannot stat {}: {error}", path.display()))?
        .len();
    let input =
        File::open(path).map_err(|error| format!("cannot open {}: {error}", path.display()))?;
    let save = JominiFile::from_file(input)
        .map_err(|error| format!("cannot read save envelope: {error}"))?;
    validate_header_container(&save)?;

    let header = save.header();
    let header_report = HeaderReport {
        version: header.version(),
        kind: header_kind_name(header.kind()),
        kind_code: header.kind().value(),
        header_bytes: header.header_len(),
        declared_metadata_bytes: header.metadata_len(),
    };
    let (container, gamestate_hint) = match save.kind() {
        JominiFileKind::Zip(zip) => ("zip", zip.gamestate_uncompressed_hint()),
        JominiFileKind::Uncompressed(_) => {
            let body_bytes = source_bytes
                .checked_sub(header.header_len() as u64)
                .ok_or_else(|| "save is shorter than its header".to_owned())?;
            let gamestate_bytes = body_bytes
                .checked_sub(header.metadata_len())
                .ok_or_else(|| "declared metadata extends beyond the save body".to_owned())?;
            ("uncompressed", gamestate_bytes)
        }
    };

    let metadata = save
        .meta()
        .map_err(|error| format!("cannot open metadata: {error}"))?;
    let (metadata_encoding, metadata_bytes, metadata_truncated) = match metadata {
        SaveMetadataKind::Text(reader) => {
            let (data, truncated) = read_bounded(reader, METADATA_INSPECTION_LIMIT)
                .map_err(|error| format!("cannot read metadata: {error}"))?;
            (EncodingKind::Text, data, truncated)
        }
        SaveMetadataKind::Binary(reader) => {
            let (data, truncated) = read_bounded(reader, METADATA_INSPECTION_LIMIT)
                .map_err(|error| format!("cannot read metadata: {error}"))?;
            (EncodingKind::Binary, data, truncated)
        }
    };

    let metadata_text = if metadata_encoding == EncodingKind::Text && !metadata_truncated {
        Some(summarize_text_metadata(&metadata_bytes)?)
    } else {
        None
    };

    let gamestate_encoding = match save
        .gamestate()
        .map_err(|error| format!("cannot open gamestate: {error}"))?
    {
        SaveContentKind::Text(_) => EncodingKind::Text,
        SaveContentKind::Binary(_) => EncodingKind::Binary,
    };

    Ok(OverviewReport {
        schema: OVERVIEW_SCHEMA,
        jomini: JOMINI_VERSION,
        source_bytes,
        container,
        header: header_report,
        metadata: MetadataReport {
            encoding: metadata_encoding,
            declared_bytes: header.metadata_len(),
            inspected_bytes: metadata_bytes.len(),
            inspection_limit_bytes: METADATA_INSPECTION_LIMIT,
            truncated: metadata_truncated,
            token_resolver_required: metadata_encoding == EncodingKind::Binary,
            text: metadata_text,
        },
        gamestate: GamestateReport {
            encoding: gamestate_encoding,
            uncompressed_bytes_hint: gamestate_hint,
            scanned: false,
            integrity_checked: false,
            token_resolver_required: gamestate_encoding == EncodingKind::Binary,
        },
    })
}

fn read_bounded<R: Read>(reader: R, limit: usize) -> io::Result<(Vec<u8>, bool)> {
    let mut reader = reader.take(limit as u64 + 1);
    let mut data = Vec::with_capacity(limit.min(64 * 1024));
    reader.read_to_end(&mut data)?;
    let truncated = data.len() > limit;
    data.truncate(limit);
    Ok((data, truncated))
}

fn summarize_text_metadata(data: &[u8]) -> Result<TextMetadataSummary, String> {
    let tape = TextTape::from_slice(data)
        .map_err(|error| format!("cannot parse plaintext metadata: {error}"))?;
    let reader = tape.utf8_reader();
    let top_level_fields = reader.fields_len();
    let mut scalar_fields = 0usize;
    let mut samples = Vec::new();

    for (key, operator, value) in reader.fields() {
        let Ok(value_scalar) = value.read_scalar() else {
            continue;
        };
        scalar_fields += 1;
        if samples.len() >= METADATA_SAMPLE_LIMIT {
            continue;
        }

        samples.push(TextFieldSample {
            key: scalar_view(
                key.read_scalar().as_bytes(),
                text_token_representation(key.token()),
            ),
            operator: operator_view(operator),
            value: scalar_view(
                value_scalar.as_bytes(),
                text_token_representation(value.token()),
            ),
        });
    }

    Ok(TextMetadataSummary {
        tokens: tape.tokens().len(),
        top_level_fields,
        scalar_fields,
        samples,
        samples_truncated: scalar_fields > METADATA_SAMPLE_LIMIT,
    })
}

fn text_token_representation(token: &TextToken<'_>) -> &'static str {
    match token {
        TextToken::Quoted(_) => "quoted",
        TextToken::Unquoted(_) => "unquoted",
        TextToken::Parameter(_) => "parameter",
        TextToken::UndefinedParameter(_) => "undefined_parameter",
        TextToken::Header(_) => "header",
        TextToken::Array { .. } => "array",
        TextToken::Object { .. } => "object",
        TextToken::MixedContainer => "mixed_container",
        TextToken::Operator(_) => "operator",
        TextToken::End(_) => "end",
    }
}

fn scalar_view(bytes: &[u8], representation: &'static str) -> ScalarView {
    let preview_len = bytes.len().min(SCALAR_PREVIEW_LIMIT);
    let preview = &bytes[..preview_len];
    ScalarView {
        representation,
        bytes: bytes.len(),
        preview_hex: bytes_hex(preview),
        utf8: std::str::from_utf8(preview).ok().map(str::to_owned),
        truncated: bytes.len() > preview_len,
    }
}

fn bytes_hex(bytes: &[u8]) -> String {
    use std::fmt::Write as _;

    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(output, "{byte:02x}");
    }
    output
}

fn operator_view(operator: Option<Operator>) -> OperatorView {
    match operator {
        Some(operator) => OperatorView {
            name: operator.name().to_owned(),
            symbol: operator.symbol().to_owned(),
        },
        None => OperatorView {
            name: "EQUAL".to_owned(),
            symbol: "=".to_owned(),
        },
    }
}

fn header_kind_name(kind: SaveHeaderKind) -> &'static str {
    match kind {
        SaveHeaderKind::Text => "text",
        SaveHeaderKind::Binary => "binary",
        SaveHeaderKind::UnifiedText => "unified_text",
        SaveHeaderKind::UnifiedBinary => "unified_binary",
        SaveHeaderKind::SplitText => "split_text",
        SaveHeaderKind::SplitBinary => "split_binary",
        SaveHeaderKind::Other(_) => "other",
    }
}

fn validate_header_container<R>(save: &JominiFile<R>) -> Result<(), String> {
    let header_kind = save.header().kind();
    if matches!(header_kind, SaveHeaderKind::Other(_)) {
        return Err(format!(
            "unsupported save header kind 0x{:02x}",
            header_kind.value()
        ));
    }
    if matches!(save.kind(), JominiFileKind::Uncompressed(_))
        && matches!(
            header_kind,
            SaveHeaderKind::UnifiedText
                | SaveHeaderKind::UnifiedBinary
                | SaveHeaderKind::SplitText
                | SaveHeaderKind::SplitBinary
        )
    {
        return Err(format!(
            "save header kind {} requires a ZIP container, but no valid ZIP was found",
            header_kind_name(header_kind)
        ));
    }
    Ok(())
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
enum RequestedSection {
    Metadata,
    Gamestate,
    Both,
}

struct FindArgs {
    section: RequestedSection,
    token_map: Option<PathBuf>,
    limit: usize,
    max_bytes: u64,
    key: String,
    path: PathBuf,
}

#[derive(Clone, Copy)]
struct ScanOptions<'a> {
    query: &'a str,
    raw_token: Option<u16>,
    resolver: Option<&'a BasicTokenResolver>,
    match_limit: usize,
    max_bytes: u64,
    integrity_on_complete: bool,
}

impl FindArgs {
    fn parse(args: &[OsString]) -> Result<Self, String> {
        let mut section = RequestedSection::Gamestate;
        let mut token_map = None;
        let mut limit = DEFAULT_MATCH_LIMIT;
        let mut max_bytes = DEFAULT_SCAN_MAX_BYTES;
        let mut positional = Vec::new();
        let mut options = true;
        let mut index = 0usize;

        while index < args.len() {
            let arg = &args[index];
            if options && arg == OsStr::new("--") {
                options = false;
                index += 1;
                continue;
            }
            if options && arg == OsStr::new("--section") {
                let value = args
                    .get(index + 1)
                    .and_then(|x| x.to_str())
                    .ok_or_else(|| "--section requires a Unicode value".to_owned())?;
                section = match value {
                    "metadata" | "meta" => RequestedSection::Metadata,
                    "gamestate" | "game" => RequestedSection::Gamestate,
                    "both" => RequestedSection::Both,
                    _ => {
                        return Err("--section must be metadata, gamestate, or both".to_owned());
                    }
                };
                index += 2;
                continue;
            }
            if options && (arg == OsStr::new("--token-map") || arg == OsStr::new("--tokens")) {
                let value = args
                    .get(index + 1)
                    .ok_or_else(|| "--token-map requires a FILE".to_owned())?;
                token_map = Some(PathBuf::from(value));
                index += 2;
                continue;
            }
            if options && arg == OsStr::new("--limit") {
                let value = args
                    .get(index + 1)
                    .and_then(|x| x.to_str())
                    .ok_or_else(|| "--limit requires an integer".to_owned())?;
                limit = value
                    .parse::<usize>()
                    .map_err(|_| "--limit requires an integer".to_owned())?;
                if !(1..=MAX_MATCH_LIMIT).contains(&limit) {
                    return Err(format!("--limit must be between 1 and {MAX_MATCH_LIMIT}"));
                }
                index += 2;
                continue;
            }
            if options && arg == OsStr::new("--max-bytes") {
                let value = args
                    .get(index + 1)
                    .and_then(|x| x.to_str())
                    .ok_or_else(|| "--max-bytes requires an integer".to_owned())?;
                max_bytes = value
                    .parse::<u64>()
                    .map_err(|_| "--max-bytes requires an integer".to_owned())?;
                if !(1..=MAX_SCAN_MAX_BYTES).contains(&max_bytes) {
                    return Err(format!(
                        "--max-bytes must be between 1 and {MAX_SCAN_MAX_BYTES}"
                    ));
                }
                index += 2;
                continue;
            }
            if options && arg.to_string_lossy().starts_with('-') {
                return Err(format!("unknown find-key option {arg:?}"));
            }
            positional.push(arg.clone());
            index += 1;
        }

        if positional.len() != 2 {
            return Err("find-key expects KEY and save FILE".to_owned());
        }
        let key = positional[0]
            .to_str()
            .ok_or_else(|| "KEY must be valid Unicode".to_owned())?
            .to_owned();
        if key.is_empty() {
            return Err("KEY must not be empty".to_owned());
        }

        Ok(FindArgs {
            section,
            token_map,
            limit,
            max_bytes,
            key,
            path: PathBuf::from(&positional[1]),
        })
    }
}

struct TokenIdsArgs {
    section: RequestedSection,
    max_bytes: u64,
    path: PathBuf,
}

impl TokenIdsArgs {
    fn parse(args: &[OsString]) -> Result<Self, String> {
        let mut section = RequestedSection::Both;
        let mut max_bytes = DEFAULT_SCAN_MAX_BYTES;
        let mut positional = Vec::new();
        let mut options = true;
        let mut index = 0usize;

        while index < args.len() {
            let arg = &args[index];
            if options && arg == OsStr::new("--") {
                options = false;
                index += 1;
                continue;
            }
            if options && arg == OsStr::new("--section") {
                let value = args
                    .get(index + 1)
                    .and_then(|x| x.to_str())
                    .ok_or_else(|| "--section requires a Unicode value".to_owned())?;
                section = match value {
                    "metadata" | "meta" => RequestedSection::Metadata,
                    "gamestate" | "game" => RequestedSection::Gamestate,
                    "both" => RequestedSection::Both,
                    _ => return Err("--section must be metadata, gamestate, or both".to_owned()),
                };
                index += 2;
                continue;
            }
            if options && arg == OsStr::new("--max-bytes") {
                let value = args
                    .get(index + 1)
                    .and_then(|x| x.to_str())
                    .ok_or_else(|| "--max-bytes requires an integer".to_owned())?;
                max_bytes = value
                    .parse::<u64>()
                    .map_err(|_| "--max-bytes requires an integer".to_owned())?;
                if !(1..=MAX_SCAN_MAX_BYTES).contains(&max_bytes) {
                    return Err(format!(
                        "--max-bytes must be between 1 and {MAX_SCAN_MAX_BYTES}"
                    ));
                }
                index += 2;
                continue;
            }
            if options && arg.to_string_lossy().starts_with('-') {
                return Err(format!("unknown token-ids option {arg:?}"));
            }
            positional.push(arg.clone());
            index += 1;
        }

        if positional.len() != 1 {
            return Err("token-ids expects exactly one save FILE".to_owned());
        }
        Ok(TokenIdsArgs {
            section,
            max_bytes,
            path: PathBuf::from(&positional[0]),
        })
    }
}

#[derive(Serialize)]
struct FindReport {
    schema: &'static str,
    jomini: &'static str,
    query: FindQueryReport,
    sections: Vec<SectionSearchResult>,
}

#[derive(Serialize)]
struct FindQueryReport {
    key: String,
    raw_token: Option<String>,
    section: RequestedSection,
    limit: usize,
    max_decompressed_bytes: u64,
    token_map_configured: bool,
}

#[derive(Serialize)]
struct TokenIdsReport {
    schema: &'static str,
    jomini: &'static str,
    query: TokenIdsQueryReport,
    sections: Vec<TokenIdSectionResult>,
    unique_identifiers: Vec<TokenIdCount>,
}

#[derive(Serialize)]
struct TokenIdsQueryReport {
    section: RequestedSection,
    max_decompressed_bytes_per_section: u64,
}

#[derive(Serialize)]
struct TokenIdSectionResult {
    name: &'static str,
    encoding: EncodingKind,
    bytes_scanned: usize,
    decompressed_bytes_read: u64,
    lookahead_bytes_read: u8,
    complete: bool,
    stop_reason: Option<StopReason>,
    integrity_checked: bool,
    identifier_occurrences: u64,
    unique_identifiers: usize,
    identifiers: Vec<TokenIdCount>,
}

#[derive(Clone, Serialize)]
struct TokenIdCount {
    token: String,
    count: u64,
}

#[derive(Clone, Copy, Serialize)]
#[serde(rename_all = "snake_case")]
enum StopReason {
    MatchLimit,
    ByteLimit,
}

#[derive(Serialize)]
struct SectionSearchResult {
    name: &'static str,
    encoding: EncodingKind,
    bytes_scanned: usize,
    decompressed_bytes_read: u64,
    lookahead_bytes_read: u8,
    complete: bool,
    stop_reason: Option<StopReason>,
    integrity_checked: bool,
    syntax_checked: bool,
    unresolved_identifier_keys: usize,
    truncated: bool,
    matches: Vec<KeyMatch>,
}

#[derive(Serialize)]
struct KeyMatch {
    key_end_offset: usize,
    depth: usize,
    key: TokenView,
    operator: OperatorView,
    value: TokenView,
}

#[derive(Clone, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
enum TokenView {
    Container,
    HeaderContainer {
        header: ScalarView,
    },
    Text {
        scalar: ScalarView,
    },
    Identifier {
        token: String,
        resolved: Option<String>,
    },
    U32 {
        value: u32,
    },
    U64 {
        value: u64,
    },
    I32 {
        value: i32,
    },
    I64 {
        value: i64,
    },
    Bool {
        value: bool,
    },
    F32 {
        bits_hex: String,
    },
    F64 {
        bits_hex: String,
    },
    Lookup {
        index: u32,
        resolved: Option<String>,
    },
    Rgb {
        red: u32,
        green: u32,
        blue: u32,
        alpha: Option<u32>,
    },
}

fn build_find_report(args: &FindArgs) -> Result<FindReport, String> {
    let input = File::open(&args.path)
        .map_err(|error| format!("cannot open {}: {error}", args.path.display()))?;
    let save = JominiFile::from_file(input)
        .map_err(|error| format!("cannot read save envelope: {error}"))?;
    validate_header_container(&save)?;
    let resolver = load_token_resolver(args.token_map.as_deref())?;
    let raw_token = parse_raw_token_query(&args.key);
    let uncompressed_gamestate_offset = (save.header().header_len() as u64)
        .checked_add(save.header().metadata_len())
        .ok_or_else(|| "gamestate offset overflows u64".to_owned())?;
    let scan_options = |integrity_on_complete| ScanOptions {
        query: &args.key,
        raw_token,
        resolver: resolver.as_ref(),
        match_limit: args.limit,
        max_bytes: args.max_bytes,
        integrity_on_complete,
    };
    let mut sections = Vec::new();

    if matches!(
        args.section,
        RequestedSection::Metadata | RequestedSection::Both
    ) {
        let metadata = save
            .meta()
            .map_err(|error| format!("cannot open metadata: {error}"))?;
        let result = match metadata {
            SaveMetadataKind::Text(reader) => {
                scan_text_key("metadata", reader, scan_options(false))?
            }
            SaveMetadataKind::Binary(reader) => {
                scan_binary_key("metadata", reader, scan_options(false))?
            }
        };
        sections.push(result);
    }

    if matches!(
        args.section,
        RequestedSection::Gamestate | RequestedSection::Both
    ) {
        let result = match save.kind() {
            JominiFileKind::Zip(zip) => {
                let gamestate = zip
                    .gamestate_verified()
                    .map_err(|error| format!("cannot open gamestate: {error}"))?;
                scan_content_key(gamestate, scan_options(true))?
            }
            JominiFileKind::Uncompressed(_) => scan_uncompressed_gamestate(
                &args.path,
                uncompressed_gamestate_offset,
                save.header().kind().is_binary(),
                scan_options(false),
            )?,
        };
        sections.push(result);
    }

    Ok(FindReport {
        schema: FIND_SCHEMA,
        jomini: JOMINI_VERSION,
        query: FindQueryReport {
            key: args.key.clone(),
            raw_token: raw_token.map(|token| format!("0x{token:04x}")),
            section: args.section,
            limit: args.limit,
            max_decompressed_bytes: args.max_bytes,
            token_map_configured: resolver.is_some(),
        },
        sections,
    })
}

fn build_token_ids_report(args: &TokenIdsArgs) -> Result<TokenIdsReport, String> {
    let input = File::open(&args.path)
        .map_err(|error| format!("cannot open {}: {error}", args.path.display()))?;
    let save = JominiFile::from_file(input)
        .map_err(|error| format!("cannot read save envelope: {error}"))?;
    validate_header_container(&save)?;
    let uncompressed_gamestate_offset = (save.header().header_len() as u64)
        .checked_add(save.header().metadata_len())
        .ok_or_else(|| "gamestate offset overflows u64".to_owned())?;
    let mut sections = Vec::new();

    if matches!(
        args.section,
        RequestedSection::Metadata | RequestedSection::Both
    ) {
        let metadata = save
            .meta()
            .map_err(|error| format!("cannot open metadata: {error}"))?;
        let result = match metadata {
            SaveMetadataKind::Binary(reader) => {
                scan_binary_token_ids("metadata", reader, args.max_bytes, false)?
            }
            SaveMetadataKind::Text(_) => {
                return Err("token-ids requires binary metadata".to_owned());
            }
        };
        sections.push(result);
    }

    if matches!(
        args.section,
        RequestedSection::Gamestate | RequestedSection::Both
    ) {
        let result = match save.kind() {
            JominiFileKind::Zip(zip) => match zip
                .gamestate_verified()
                .map_err(|error| format!("cannot open gamestate: {error}"))?
            {
                SaveContentKind::Binary(reader) => {
                    scan_binary_token_ids("gamestate", reader, args.max_bytes, true)?
                }
                SaveContentKind::Text(_) => {
                    return Err("token-ids requires a binary gamestate".to_owned());
                }
            },
            JominiFileKind::Uncompressed(_) => scan_uncompressed_binary_token_ids(
                &args.path,
                uncompressed_gamestate_offset,
                save.header().kind().is_binary(),
                args.max_bytes,
            )?,
        };
        sections.push(result);
    }

    let mut merged = BTreeMap::<String, u64>::new();
    for section in &sections {
        for item in &section.identifiers {
            *merged.entry(item.token.clone()).or_default() += item.count;
        }
    }
    let unique_identifiers = merged
        .into_iter()
        .map(|(token, count)| TokenIdCount { token, count })
        .collect();

    Ok(TokenIdsReport {
        schema: TOKEN_IDS_SCHEMA,
        jomini: JOMINI_VERSION,
        query: TokenIdsQueryReport {
            section: args.section,
            max_decompressed_bytes_per_section: args.max_bytes,
        },
        sections,
        unique_identifiers,
    })
}

fn scan_uncompressed_binary_token_ids(
    path: &Path,
    offset: u64,
    binary: bool,
    max_bytes: u64,
) -> Result<TokenIdSectionResult, String> {
    if !binary {
        return Err("token-ids requires a binary gamestate".to_owned());
    }
    let source_bytes = fs::metadata(path)
        .map_err(|error| format!("cannot stat {}: {error}", path.display()))?
        .len();
    if offset > source_bytes {
        return Err("declared metadata extends beyond the save body".to_owned());
    }
    let mut reader =
        File::open(path).map_err(|error| format!("cannot reopen {}: {error}", path.display()))?;
    reader
        .seek(SeekFrom::Start(offset))
        .map_err(|error| format!("cannot seek to gamestate: {error}"))?;
    scan_binary_token_ids("gamestate", reader, max_bytes, false)
}

fn load_token_resolver(path: Option<&Path>) -> Result<Option<BasicTokenResolver>, String> {
    let Some(path) = path else {
        return Ok(None);
    };
    let file = File::open(path)
        .map_err(|error| format!("cannot open token map {}: {error}", path.display()))?;
    let mut data = Vec::with_capacity(64 * 1024);
    file.take(TOKEN_MAP_MAX_BYTES + 1)
        .read_to_end(&mut data)
        .map_err(|error| format!("cannot read token map {}: {error}", path.display()))?;
    if data.len() as u64 > TOKEN_MAP_MAX_BYTES {
        return Err(format!(
            "token map exceeds the {TOKEN_MAP_MAX_BYTES}-byte maximum"
        ));
    }

    for (index, line) in data.split_inclusive(|byte| *byte == b'\n').enumerate() {
        let line_number = index + 1;
        if line.len() > TOKEN_MAP_MAX_LINE_BYTES {
            return Err(format!(
                "token map line {line_number} exceeds {TOKEN_MAP_MAX_LINE_BYTES} bytes"
            ));
        }
        let split = line
            .iter()
            .position(|byte| *byte == b' ')
            .ok_or_else(|| format!("token map line {line_number} has no token/name separator"))?;
        let mut name = &line[split + 1..];
        while name.last().is_some_and(|byte| byte.is_ascii_whitespace()) {
            name = &name[..name.len() - 1];
        }
        if name.len() > TOKEN_NAME_MAX_BYTES {
            return Err(format!(
                "token map name on line {line_number} exceeds {TOKEN_NAME_MAX_BYTES} bytes"
            ));
        }
    }

    BasicTokenResolver::from_text_lines(&data[..])
        .map(Some)
        .map_err(|error| format!("cannot parse token map {}: {error}", path.display()))
}

fn parse_raw_token_query(query: &str) -> Option<u16> {
    let hex = query
        .strip_prefix("0x")
        .or_else(|| query.strip_prefix("0X"))?;
    if hex.is_empty() || hex.len() > 4 || !hex.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return None;
    }
    u16::from_str_radix(hex, 16).ok()
}

fn scan_content_key<R: Read>(
    content: SaveContentKind<R>,
    options: ScanOptions<'_>,
) -> Result<SectionSearchResult, String> {
    match content {
        SaveContentKind::Text(reader) => scan_text_key("gamestate", reader, options),
        SaveContentKind::Binary(reader) => scan_binary_key("gamestate", reader, options),
    }
}

fn scan_uncompressed_gamestate(
    path: &Path,
    offset: u64,
    binary: bool,
    options: ScanOptions<'_>,
) -> Result<SectionSearchResult, String> {
    let source_bytes = fs::metadata(path)
        .map_err(|error| format!("cannot stat {}: {error}", path.display()))?
        .len();
    if offset > source_bytes {
        return Err("declared metadata extends beyond the save body".to_owned());
    }

    let mut reader =
        File::open(path).map_err(|error| format!("cannot reopen {}: {error}", path.display()))?;
    reader
        .seek(SeekFrom::Start(offset))
        .map_err(|error| format!("cannot seek to gamestate: {error}"))?;
    if binary {
        scan_binary_key("gamestate", reader, options)
    } else {
        scan_text_key("gamestate", reader, options)
    }
}

#[derive(Clone)]
struct ReadBudgetState {
    decompressed_bytes_read: Rc<Cell<u64>>,
    lookahead_bytes_read: Rc<Cell<u8>>,
    limit_hit: Rc<Cell<bool>>,
}

impl ReadBudgetState {
    fn new() -> Self {
        ReadBudgetState {
            decompressed_bytes_read: Rc::new(Cell::new(0)),
            lookahead_bytes_read: Rc::new(Cell::new(0)),
            limit_hit: Rc::new(Cell::new(false)),
        }
    }
}

struct ReadBudget<R> {
    inner: R,
    remaining: u64,
    lookahead_done: bool,
    state: ReadBudgetState,
}

impl<R> ReadBudget<R> {
    fn new(inner: R, max_bytes: u64) -> (Self, ReadBudgetState) {
        let state = ReadBudgetState::new();
        (
            ReadBudget {
                inner,
                remaining: max_bytes,
                lookahead_done: false,
                state: state.clone(),
            },
            state,
        )
    }
}

impl<R: Read> Read for ReadBudget<R> {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        if buffer.is_empty() {
            return Ok(0);
        }
        if self.remaining == 0 {
            if self.lookahead_done {
                return Ok(0);
            }
            self.lookahead_done = true;
            let mut probe = [0u8; 1];
            let read = self.inner.read(&mut probe)?;
            self.state.lookahead_bytes_read.set(
                self.state
                    .lookahead_bytes_read
                    .get()
                    .saturating_add(read as u8),
            );
            self.state.limit_hit.set(read != 0);
            return Ok(0);
        }

        let requested = self.remaining.min(buffer.len() as u64) as usize;
        let read = self.inner.read(&mut buffer[..requested])?;
        self.remaining -= read as u64;
        self.state.decompressed_bytes_read.set(
            self.state
                .decompressed_bytes_read
                .get()
                .saturating_add(read as u64),
        );
        Ok(read)
    }
}

fn scan_binary_token_ids<R: Read>(
    name: &'static str,
    reader: R,
    max_bytes: u64,
    integrity_on_complete: bool,
) -> Result<TokenIdSectionResult, String> {
    let (reader, budget) = ReadBudget::new(reader, max_bytes);
    let mut reader = BinaryTokenReader::from_reader_with_buf(reader, vec![0; STREAM_BUFFER_BYTES]);
    let mut depth = 0usize;
    let mut counts = BTreeMap::<u16, u64>::new();
    let mut identifier_occurrences = 0u64;
    let mut stop_reason = None;

    loop {
        let token = match reader.next() {
            Ok(Some(token)) => token,
            Ok(None) => {
                if budget.limit_hit.get() {
                    stop_reason = Some(StopReason::ByteLimit);
                }
                break;
            }
            Err(error) => {
                if budget.limit_hit.get() {
                    stop_reason = Some(StopReason::ByteLimit);
                    break;
                }
                let position = reader.position();
                return Err(format!(
                    "cannot scan {name} binary token ids at byte {position}: {error}"
                ));
            }
        };

        match token {
            BinaryToken::Open => depth += 1,
            BinaryToken::Close => {
                if depth == 0 {
                    return Err(format!(
                        "cannot scan {name} binary token ids: unmatched close container"
                    ));
                }
                depth -= 1;
            }
            BinaryToken::Id(token) => {
                identifier_occurrences = identifier_occurrences.saturating_add(1);
                let count = counts.entry(token).or_default();
                *count = count.saturating_add(1);
            }
            _ => {}
        }
    }

    if stop_reason.is_none() && depth != 0 {
        return Err(format!(
            "cannot scan {name} binary token ids: {depth} container(s) remain open"
        ));
    }
    let complete = stop_reason.is_none();
    let identifiers = counts
        .into_iter()
        .map(|(token, count)| TokenIdCount {
            token: format!("0x{token:04x}"),
            count,
        })
        .collect::<Vec<_>>();

    Ok(TokenIdSectionResult {
        name,
        encoding: EncodingKind::Binary,
        bytes_scanned: reader.position(),
        decompressed_bytes_read: budget.decompressed_bytes_read.get(),
        lookahead_bytes_read: budget.lookahead_bytes_read.get(),
        complete,
        stop_reason,
        integrity_checked: complete && integrity_on_complete,
        identifier_occurrences,
        unique_identifiers: identifiers.len(),
        identifiers,
    })
}

struct PendingTextKey {
    key_end_offset: usize,
    depth: usize,
    key: TokenView,
    matches: bool,
}

struct PendingTextValue {
    key: PendingTextKey,
    operator: OperatorView,
    value: ScalarView,
}

fn record_text_match(
    matches: &mut Vec<KeyMatch>,
    key: PendingTextKey,
    operator: OperatorView,
    value: TokenView,
) {
    if key.matches {
        matches.push(KeyMatch {
            key_end_offset: key.key_end_offset,
            depth: key.depth,
            key: key.key,
            operator,
            value,
        });
    }
}

fn scan_text_key<R: Read>(
    name: &'static str,
    reader: R,
    options: ScanOptions<'_>,
) -> Result<SectionSearchResult, String> {
    let (reader, budget) = ReadBudget::new(reader, options.max_bytes);
    let mut reader = TextTokenReader::from_reader_with_buf(reader, vec![0; STREAM_BUFFER_BYTES]);
    let mut depth = 0usize;
    let mut pending_key = None::<PendingTextKey>;
    let mut awaiting = None::<(PendingTextKey, OperatorView)>;
    let mut pending_value = None::<PendingTextValue>;
    let mut matches = Vec::new();
    let mut stop_reason = None;

    loop {
        let token = match reader.next() {
            Ok(Some(token)) => token,
            Ok(None) => {
                if budget.limit_hit.get() {
                    stop_reason = Some(StopReason::ByteLimit);
                }
                break;
            }
            Err(error) => {
                if budget.limit_hit.get() {
                    stop_reason = Some(StopReason::ByteLimit);
                    break;
                }
                let position = reader.position();
                return Err(format!(
                    "cannot scan {name} text at byte {position}: {error}"
                ));
            }
        };
        match token {
            TextStreamToken::Open => {
                pending_key = None;
                if let Some(value) = pending_value.take() {
                    if value.value.representation != "unquoted" {
                        return Err(format!(
                            "cannot scan {name} text: quoted scalar cannot head a container"
                        ));
                    }
                    let header = value.value;
                    record_text_match(
                        &mut matches,
                        value.key,
                        value.operator,
                        TokenView::HeaderContainer { header },
                    );
                } else if let Some((key, operator)) = awaiting.take() {
                    record_text_match(&mut matches, key, operator, TokenView::Container);
                }
                depth += 1;
            }
            TextStreamToken::Close => {
                pending_key = None;
                if let Some(value) = pending_value.take() {
                    record_text_match(
                        &mut matches,
                        value.key,
                        value.operator,
                        TokenView::Text {
                            scalar: value.value,
                        },
                    );
                }
                if awaiting.is_some() {
                    return Err(format!(
                        "cannot scan {name} text: field has no value before close container"
                    ));
                }
                if depth == 0 {
                    return Err(format!(
                        "cannot scan {name} text: unmatched close container"
                    ));
                }
                depth -= 1;
            }
            TextStreamToken::Operator(operator) => {
                if pending_value.is_some() || awaiting.is_some() {
                    return Err(format!(
                        "cannot scan {name} text: repeated operator or operator after field value"
                    ));
                }
                let key = pending_key
                    .take()
                    .ok_or_else(|| format!("cannot scan {name} text: operator has no field key"))?;
                awaiting = Some((key, operator_view(Some(operator))));
            }
            TextStreamToken::Quoted(scalar) | TextStreamToken::Unquoted(scalar) => {
                let representation = if matches!(token, TextStreamToken::Quoted(_)) {
                    "quoted"
                } else {
                    "unquoted"
                };
                if let Some(value) = pending_value.take() {
                    record_text_match(
                        &mut matches,
                        value.key,
                        value.operator,
                        TokenView::Text {
                            scalar: value.value,
                        },
                    );
                }
                if let Some((key, operator)) = awaiting.take() {
                    pending_value = Some(PendingTextValue {
                        key,
                        operator,
                        value: scalar_view(scalar.as_bytes(), representation),
                    });
                    pending_key = None;
                } else {
                    let matches_query = scalar.as_bytes() == options.query.as_bytes();
                    let key = TokenView::Text {
                        scalar: scalar_view(scalar.as_bytes(), representation),
                    };
                    let key_end_offset = reader.position();
                    pending_key = Some(PendingTextKey {
                        key_end_offset,
                        depth,
                        key,
                        matches: matches_query,
                    });
                }
            }
        }

        if matches.len() > options.match_limit {
            stop_reason = Some(StopReason::MatchLimit);
            break;
        }
    }

    if stop_reason.is_none() {
        if let Some(value) = pending_value.take() {
            record_text_match(
                &mut matches,
                value.key,
                value.operator,
                TokenView::Text {
                    scalar: value.value,
                },
            );
        }
        if awaiting.is_some() {
            return Err(format!("cannot scan {name} text: field has no value"));
        }
        if depth != 0 {
            return Err(format!(
                "cannot scan {name} text: {depth} container(s) remain open"
            ));
        }
        if matches.len() > options.match_limit {
            stop_reason = Some(StopReason::MatchLimit);
        }
    }
    let bytes_scanned = reader.position();
    let truncated = matches.len() > options.match_limit;
    matches.truncate(options.match_limit);
    let complete = stop_reason.is_none();

    Ok(SectionSearchResult {
        name,
        encoding: EncodingKind::Text,
        bytes_scanned,
        decompressed_bytes_read: budget.decompressed_bytes_read.get(),
        lookahead_bytes_read: budget.lookahead_bytes_read.get(),
        complete,
        stop_reason,
        integrity_checked: complete && options.integrity_on_complete,
        syntax_checked: false,
        unresolved_identifier_keys: 0,
        truncated,
        matches,
    })
}

struct PendingBinaryKey {
    key_end_offset: usize,
    depth: usize,
    key: TokenView,
    matches: bool,
    unresolved_identifier: bool,
}

fn scan_binary_key<R: Read>(
    name: &'static str,
    reader: R,
    options: ScanOptions<'_>,
) -> Result<SectionSearchResult, String> {
    let (reader, budget) = ReadBudget::new(reader, options.max_bytes);
    let mut reader = BinaryTokenReader::from_reader_with_buf(reader, vec![0; STREAM_BUFFER_BYTES]);
    let mut depth = 0usize;
    let mut pending = None::<PendingBinaryKey>;
    let mut awaiting = None::<PendingBinaryKey>;
    let mut unresolved_identifier_keys = 0usize;
    let mut matches = Vec::new();
    let mut stop_reason = None;

    loop {
        let token = match reader.next() {
            Ok(Some(token)) => token,
            Ok(None) => {
                if budget.limit_hit.get() {
                    stop_reason = Some(StopReason::ByteLimit);
                }
                break;
            }
            Err(error) => {
                if budget.limit_hit.get() {
                    stop_reason = Some(StopReason::ByteLimit);
                    break;
                }
                let position = reader.position();
                return Err(format!(
                    "cannot scan {name} binary at byte {position}: {error}"
                ));
            }
        };
        match token {
            BinaryToken::Open => {
                pending = None;
                if let Some(key) = awaiting.take() {
                    if key.matches {
                        matches.push(KeyMatch {
                            key_end_offset: key.key_end_offset,
                            depth: key.depth,
                            key: key.key,
                            operator: operator_view(None),
                            value: TokenView::Container,
                        });
                    }
                }
                depth += 1;
            }
            BinaryToken::Close => {
                pending = None;
                if awaiting.is_some() {
                    return Err(format!(
                        "cannot scan {name} binary: field has no value before close container"
                    ));
                }
                if depth == 0 {
                    return Err(format!(
                        "cannot scan {name} binary: unmatched close container"
                    ));
                }
                depth -= 1;
            }
            BinaryToken::Equal => {
                if awaiting.is_some() {
                    return Err(format!(
                        "cannot scan {name} binary: repeated equality operator"
                    ));
                }
                let key = pending.take().ok_or_else(|| {
                    format!("cannot scan {name} binary: equality operator has no field key")
                })?;
                if key.unresolved_identifier {
                    unresolved_identifier_keys += 1;
                }
                awaiting = Some(key);
            }
            other => {
                if let Some(key) = awaiting.take() {
                    if key.matches {
                        matches.push(KeyMatch {
                            key_end_offset: key.key_end_offset,
                            depth: key.depth,
                            key: key.key,
                            operator: operator_view(None),
                            value: binary_token_view(other, options.resolver),
                        });
                    }
                    pending = None;
                } else {
                    let mut key = binary_key_candidate(
                        other,
                        options.query,
                        options.raw_token,
                        options.resolver,
                    );
                    let key_end_offset = reader.position();
                    if let Some(key) = key.as_mut() {
                        key.key_end_offset = key_end_offset;
                        key.depth = depth;
                    }
                    pending = key;
                }
            }
        }

        if matches.len() > options.match_limit {
            stop_reason = Some(StopReason::MatchLimit);
            break;
        }
    }

    if stop_reason.is_none() {
        if awaiting.is_some() {
            return Err(format!("cannot scan {name} binary: field has no value"));
        }
        if depth != 0 {
            return Err(format!(
                "cannot scan {name} binary: {depth} container(s) remain open"
            ));
        }
    }
    let bytes_scanned = reader.position();
    let truncated = matches.len() > options.match_limit;
    matches.truncate(options.match_limit);
    let complete = stop_reason.is_none();

    Ok(SectionSearchResult {
        name,
        encoding: EncodingKind::Binary,
        bytes_scanned,
        decompressed_bytes_read: budget.decompressed_bytes_read.get(),
        lookahead_bytes_read: budget.lookahead_bytes_read.get(),
        complete,
        stop_reason,
        integrity_checked: complete && options.integrity_on_complete,
        syntax_checked: false,
        unresolved_identifier_keys,
        truncated,
        matches,
    })
}

fn binary_key_candidate(
    token: BinaryToken<'_>,
    query: &str,
    raw_token: Option<u16>,
    resolver: Option<&BasicTokenResolver>,
) -> Option<PendingBinaryKey> {
    let (key, matches, unresolved_identifier) = match token {
        BinaryToken::Id(token) => {
            let resolved = resolver
                .and_then(|resolver| resolver.resolve(token))
                .map(str::to_owned);
            let matches = raw_token == Some(token) || resolved.as_deref() == Some(query);
            (
                TokenView::Identifier {
                    token: format!("0x{token:04x}"),
                    resolved: resolved.clone(),
                },
                matches,
                resolved.is_none(),
            )
        }
        BinaryToken::Quoted(scalar) => (
            TokenView::Text {
                scalar: scalar_view(scalar.as_bytes(), "quoted"),
            },
            scalar.as_bytes() == query.as_bytes(),
            false,
        ),
        BinaryToken::Unquoted(scalar) => (
            TokenView::Text {
                scalar: scalar_view(scalar.as_bytes(), "unquoted"),
            },
            scalar.as_bytes() == query.as_bytes(),
            false,
        ),
        BinaryToken::U32(value) => (TokenView::U32 { value }, value.to_string() == query, false),
        BinaryToken::U64(value) => (TokenView::U64 { value }, value.to_string() == query, false),
        BinaryToken::I32(value) => (TokenView::I32 { value }, value.to_string() == query, false),
        BinaryToken::I64(value) => (TokenView::I64 { value }, value.to_string() == query, false),
        BinaryToken::Bool(value) => (TokenView::Bool { value }, value.to_string() == query, false),
        BinaryToken::Lookup(index) => {
            let resolved = resolver
                .and_then(|resolver| resolver.lookup(index))
                .map(str::to_owned);
            let matches = resolved.as_deref() == Some(query) || index.to_string() == query;
            (TokenView::Lookup { index, resolved }, matches, false)
        }
        BinaryToken::F32(bits) => (
            TokenView::F32 {
                bits_hex: bytes_hex(&bits),
            },
            false,
            false,
        ),
        BinaryToken::F64(bits) => (
            TokenView::F64 {
                bits_hex: bytes_hex(&bits),
            },
            false,
            false,
        ),
        BinaryToken::Rgb(rgb) => (
            TokenView::Rgb {
                red: rgb.r,
                green: rgb.g,
                blue: rgb.b,
                alpha: rgb.a,
            },
            false,
            false,
        ),
        BinaryToken::Open | BinaryToken::Close | BinaryToken::Equal => return None,
    };

    Some(PendingBinaryKey {
        key_end_offset: 0,
        depth: 0,
        key,
        matches,
        unresolved_identifier,
    })
}

fn binary_token_view(token: BinaryToken<'_>, resolver: Option<&BasicTokenResolver>) -> TokenView {
    match token {
        BinaryToken::Open => TokenView::Container,
        BinaryToken::Close | BinaryToken::Equal => TokenView::Text {
            scalar: scalar_view(b"", "invalid"),
        },
        BinaryToken::Id(token) => TokenView::Identifier {
            token: format!("0x{token:04x}"),
            resolved: resolver
                .and_then(|resolver| resolver.resolve(token))
                .map(str::to_owned),
        },
        BinaryToken::Quoted(scalar) => TokenView::Text {
            scalar: scalar_view(scalar.as_bytes(), "quoted"),
        },
        BinaryToken::Unquoted(scalar) => TokenView::Text {
            scalar: scalar_view(scalar.as_bytes(), "unquoted"),
        },
        BinaryToken::U32(value) => TokenView::U32 { value },
        BinaryToken::U64(value) => TokenView::U64 { value },
        BinaryToken::I32(value) => TokenView::I32 { value },
        BinaryToken::I64(value) => TokenView::I64 { value },
        BinaryToken::Bool(value) => TokenView::Bool { value },
        BinaryToken::F32(bits) => TokenView::F32 {
            bits_hex: bytes_hex(&bits),
        },
        BinaryToken::F64(bits) => TokenView::F64 {
            bits_hex: bytes_hex(&bits),
        },
        BinaryToken::Lookup(index) => TokenView::Lookup {
            index,
            resolved: resolver
                .and_then(|resolver| resolver.lookup(index))
                .map(str::to_owned),
        },
        BinaryToken::Rgb(rgb) => TokenView::Rgb {
            red: rgb.r,
            green: rgb.g,
            blue: rgb.b,
            alpha: rgb.a,
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;

    fn options<'a>(
        query: &'a str,
        raw_token: Option<u16>,
        resolver: Option<&'a BasicTokenResolver>,
        max_bytes: u64,
    ) -> ScanOptions<'a> {
        ScanOptions {
            query,
            raw_token,
            resolver,
            match_limit: 10,
            max_bytes,
            integrity_on_complete: false,
        }
    }

    #[test]
    fn raw_token_query_is_bounded() {
        assert_eq!(parse_raw_token_query("field"), None);
        assert_eq!(parse_raw_token_query("0x2d82"), Some(0x2d82));
        assert_eq!(parse_raw_token_query("0x10000"), None);
        assert_eq!(parse_raw_token_query("0xnope"), None);
    }

    #[test]
    fn plaintext_find_key_keeps_depth_and_value_kind() {
        let input = b"foo=bar nested={ foo={ answer=42 } }";
        let report =
            scan_text_key("gamestate", &input[..], options("foo", None, None, 1024)).unwrap();
        assert_eq!(report.matches.len(), 2);
        assert_eq!(report.matches[0].depth, 0);
        assert!(matches!(report.matches[0].value, TokenView::Text { .. }));
        assert_eq!(report.matches[1].depth, 1);
        assert!(matches!(report.matches[1].value, TokenView::Container));
    }

    #[test]
    fn plaintext_headered_container_is_not_reported_as_a_scalar() {
        for header_name in ["rgb", "hsv", "LIST"] {
            let input = format!("color={header_name} {{ 1 2 3 }}");
            let report = scan_text_key(
                "gamestate",
                input.as_bytes(),
                options("color", None, None, 1024),
            )
            .unwrap();
            assert_eq!(report.matches.len(), 1);
            assert!(matches!(
                &report.matches[0].value,
                TokenView::HeaderContainer { header }
                    if header.utf8.as_deref() == Some(header_name)
            ));
        }

        let scalar = scan_text_key(
            "gamestate",
            &b"color=rgb"[..],
            options("color", None, None, 1024),
        )
        .unwrap();
        assert!(matches!(scalar.matches[0].value, TokenView::Text { .. }));
        let header_is_not_a_key = scan_text_key(
            "gamestate",
            &b"rgb { 1 }"[..],
            options("rgb", None, None, 1024),
        )
        .unwrap();
        assert!(header_is_not_a_key.matches.is_empty());
        assert!(
            scan_text_key(
                "gamestate",
                &b"color=\"rgb\" { 1 }"[..],
                options("color", None, None, 1024),
            )
            .is_err()
        );
    }

    #[test]
    fn incomplete_text_fields_are_rejected() {
        assert!(
            scan_text_key(
                "gamestate",
                &b"target="[..],
                options("target", None, None, 1024),
            )
            .is_err()
        );
        assert!(
            scan_text_key(
                "gamestate",
                &b"root={ other= }"[..],
                options("target", None, None, 1024),
            )
            .is_err()
        );

        let comparison = scan_text_key(
            "gamestate",
            &b"target==42"[..],
            options("target", None, None, 1024),
        )
        .unwrap();
        assert_eq!(comparison.matches[0].operator.symbol, "==");
        assert!(
            scan_text_key(
                "gamestate",
                &b"target = = 42"[..],
                options("target", None, None, 1024),
            )
            .is_err()
        );
        assert!(
            scan_text_key(
                "gamestate",
                &b"other="[..],
                options("target", None, None, 1024),
            )
            .is_err()
        );
    }

    #[test]
    fn text_scan_cap_is_explicit_and_exact_length_can_complete() {
        let capped = scan_text_key(
            "gamestate",
            &b"a=b\npadding=value"[..],
            options("missing", None, None, 4),
        )
        .unwrap();
        assert!(!capped.complete);
        assert!(matches!(capped.stop_reason, Some(StopReason::ByteLimit)));
        assert!(capped.bytes_scanned <= 4);
        assert!(capped.decompressed_bytes_read <= 4);
        assert_eq!(capped.lookahead_bytes_read, 1);

        let inside_token = scan_text_key(
            "gamestate",
            &b"key=abcdefghijklmnopqrstuvwxyz"[..],
            options("missing", None, None, 8),
        )
        .unwrap();
        assert!(!inside_token.complete);
        assert_eq!(inside_token.decompressed_bytes_read, 8);
        assert_eq!(inside_token.lookahead_bytes_read, 1);

        let exact = scan_text_key("gamestate", &b"a=b"[..], options("a", None, None, 3)).unwrap();
        assert!(exact.complete);
        assert_eq!(exact.matches.len(), 1);
    }

    #[test]
    fn long_0x_text_key_is_not_forced_into_binary_token_syntax() {
        let input = b"0x10000=yes 0xnope=other";
        assert_eq!(parse_raw_token_query("0x10000"), None);
        assert_eq!(parse_raw_token_query("0xnope"), None);
        let report = scan_text_key(
            "gamestate",
            &input[..],
            options("0x10000", None, None, 1024),
        )
        .unwrap();
        assert_eq!(report.matches.len(), 1);
        let report =
            scan_text_key("gamestate", &input[..], options("0xnope", None, None, 1024)).unwrap();
        assert_eq!(report.matches.len(), 1);
    }

    #[test]
    fn binary_find_key_uses_a_matching_token_map() {
        let mut input = Vec::new();
        BinaryToken::Id(0x2000).write(&mut input).unwrap();
        BinaryToken::Equal.write(&mut input).unwrap();
        BinaryToken::U32(42).write(&mut input).unwrap();
        let resolver = BasicTokenResolver::from_text_lines(Cursor::new(b"0x2000 answer\n"))
            .expect("token map should parse");
        let report = scan_binary_key(
            "gamestate",
            &input[..],
            options("answer", None, Some(&resolver), 1024),
        )
        .unwrap();
        assert_eq!(report.matches.len(), 1);
        assert_eq!(report.unresolved_identifier_keys, 0);
        assert!(matches!(
            report.matches[0].value,
            TokenView::U32 { value: 42 }
        ));
    }

    #[test]
    fn incomplete_and_repeated_binary_equality_are_rejected() {
        let mut trailing = Vec::new();
        BinaryToken::Id(0x2000).write(&mut trailing).unwrap();
        BinaryToken::Equal.write(&mut trailing).unwrap();
        assert!(
            scan_binary_key(
                "gamestate",
                &trailing[..],
                options("0x2000", Some(0x2000), None, 1024),
            )
            .is_err()
        );

        BinaryToken::Equal.write(&mut trailing).unwrap();
        BinaryToken::U32(42).write(&mut trailing).unwrap();
        assert!(
            scan_binary_key(
                "gamestate",
                &trailing[..],
                options("0x2000", Some(0x2000), None, 1024),
            )
            .is_err()
        );

        let mut container = Vec::new();
        BinaryToken::Id(0x2000).write(&mut container).unwrap();
        BinaryToken::Equal.write(&mut container).unwrap();
        BinaryToken::Open.write(&mut container).unwrap();
        BinaryToken::Close.write(&mut container).unwrap();
        let report = scan_binary_key(
            "gamestate",
            &container[..],
            options("0x2000", Some(0x2000), None, 1024),
        )
        .unwrap();
        assert!(matches!(report.matches[0].value, TokenView::Container));

        let mut rgb = Vec::new();
        BinaryToken::Id(0x2000).write(&mut rgb).unwrap();
        BinaryToken::Equal.write(&mut rgb).unwrap();
        BinaryToken::Rgb(jomini::binary::Rgb {
            r: 1,
            g: 2,
            b: 3,
            a: None,
        })
        .write(&mut rgb)
        .unwrap();
        let report = scan_binary_key(
            "gamestate",
            &rgb[..],
            options("0x2000", Some(0x2000), None, 1024),
        )
        .unwrap();
        assert!(matches!(report.matches[0].value, TokenView::Rgb { .. }));
    }

    #[test]
    fn metadata_scalar_preview_is_bounded() {
        let value = "x".repeat(SCALAR_PREVIEW_LIMIT + 10);
        let input = format!("name=\"{value}\"");
        let summary = summarize_text_metadata(input.as_bytes()).unwrap();
        assert_eq!(summary.samples.len(), 1);
        assert!(summary.samples[0].value.truncated);
        assert_eq!(summary.samples[0].value.bytes, SCALAR_PREVIEW_LIMIT + 10);
    }
}
