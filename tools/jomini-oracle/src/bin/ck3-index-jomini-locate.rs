#![forbid(unsafe_code)]

use ck3_index_jomini_oracle::{
    ByteSpan, KeyView, PathSegment, StructuralEventKind, StructuralValue, lowercase_hex,
    sha256_bytes, walk_binary_with_resolver,
};
use jomini::{
    binary::{BasicTokenResolver, Token, TokenReader, TokenResolver},
    envelope::{JominiFile, JominiFileKind, SaveContentKind, SaveHeaderKind, SaveMetadataKind},
};
use serde::Serialize;
use std::{
    collections::BTreeSet,
    env,
    ffi::{OsStr, OsString},
    fs::File,
    io::{self, BufWriter, Read, Write},
    path::{Path, PathBuf},
    process,
};

const SCHEMA: &str = "ck3-index-jomini-save-locate-key/v1";
const JOMINI_VERSION: &str = "0.35.0";
const DEFAULT_LIMIT: usize = 100;
const MAX_LIMIT: usize = 1000;
const DEFAULT_MAX_BYTES: u64 = 64 * 1024 * 1024;
const MAX_SECTION_BYTES: u64 = 256 * 1024 * 1024;
const TOKEN_MAP_MAX_BYTES: u64 = 16 * 1024 * 1024;
const TOKEN_MAP_MAX_LINE_BYTES: usize = 4 * 1024;
const TOKEN_NAME_MAX_BYTES: usize = 256;
const MAX_TOKENS: u64 = 5_000_000;
const MAX_DEPTH: usize = 512;
const MAX_ESTIMATED_WALK_BYTES: u64 = 512 * 1024 * 1024;
const MAX_SOURCE_BYTES: u64 = 512 * 1024 * 1024;
const ESTIMATED_EVENT_BYTES: u64 = 128;
const ESTIMATED_PATH_SEGMENT_BYTES: u64 = 96;
const ESTIMATED_DYNAMIC_STRING_MULTIPLIER: u64 = 2;

fn main() {
    if let Err(error) = run() {
        eprintln!("jomini-locate: {error}");
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
        println!("ck3-index-jomini-locate 0.0.0 (jomini {JOMINI_VERSION})");
        return Ok(());
    }
    if command != OsStr::new("locate-key") {
        return Err("expected command 'locate-key'".to_owned());
    }
    if args.get(1).is_some_and(|arg| arg == OsStr::new("--help")) {
        print_locate_help();
        return Ok(());
    }

    let args = LocateArgs::parse(&args[1..])?;
    let report = locate(&args)?;
    write_json(&report)
}

fn print_help() {
    println!(
        "ck3-index-jomini-locate locate-key [OPTIONS] KEY SAVE\n\
         \n\
         Fully validate and structurally locate exact binary save field keys.\n\
         Use 'locate-key --help' for arguments."
    );
}

fn print_locate_help() {
    println!(
        "ck3-index-jomini-locate locate-key [OPTIONS] KEY SAVE\n\
         \n\
         KEY is exactly 0xNNNN, or an exact resolver name with --token-map.\n\
         \n\
         Options:\n\
           --section metadata|gamestate   Section to walk (default: gamestate)\n\
           --token-map FILE               BasicTokenResolver token map\n\
           --limit N                      Returned matches, 1..1000 (default: 100)\n\
           --max-bytes N                  Complete section cap (default: 67108864; max: 268435456)\n\
         The complete source is read once into a bounded snapshot (max: 536870912 bytes)."
    );
}

fn write_json<T: Serialize>(value: &T) -> Result<(), String> {
    let mut bytes = serde_json::to_vec_pretty(value)
        .map_err(|error| format!("cannot serialize report: {error}"))?;
    bytes.push(b'\n');
    let stdout = io::stdout();
    let mut writer = BufWriter::new(stdout.lock());
    writer
        .write_all(&bytes)
        .and_then(|_| writer.flush())
        .map_err(|error| format!("cannot write report: {error}"))
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
enum RequestedSection {
    Metadata,
    Gamestate,
}

struct LocateArgs {
    section: RequestedSection,
    token_map: Option<PathBuf>,
    limit: usize,
    max_bytes: u64,
    key: String,
    path: PathBuf,
}

impl LocateArgs {
    fn parse(args: &[OsString]) -> Result<Self, String> {
        let mut section = RequestedSection::Gamestate;
        let mut token_map = None;
        let mut limit = DEFAULT_LIMIT;
        let mut max_bytes = DEFAULT_MAX_BYTES;
        let mut positional = Vec::new();
        let mut parse_options = true;
        let mut index = 0;

        while index < args.len() {
            let arg = &args[index];
            if parse_options && arg == OsStr::new("--") {
                parse_options = false;
                index += 1;
                continue;
            }
            if parse_options && arg == OsStr::new("--section") {
                let value = unicode_option(args, index + 1, "--section")?;
                section = match value {
                    "metadata" => RequestedSection::Metadata,
                    "gamestate" => RequestedSection::Gamestate,
                    _ => return Err("--section must be metadata or gamestate".to_owned()),
                };
                index += 2;
                continue;
            }
            if parse_options && arg == OsStr::new("--token-map") {
                token_map = Some(PathBuf::from(
                    args.get(index + 1)
                        .ok_or_else(|| "--token-map requires a FILE".to_owned())?,
                ));
                index += 2;
                continue;
            }
            if parse_options && arg == OsStr::new("--limit") {
                limit = unicode_option(args, index + 1, "--limit")?
                    .parse()
                    .map_err(|_| "--limit requires an integer".to_owned())?;
                if !(1..=MAX_LIMIT).contains(&limit) {
                    return Err(format!("--limit must be between 1 and {MAX_LIMIT}"));
                }
                index += 2;
                continue;
            }
            if parse_options && arg == OsStr::new("--max-bytes") {
                max_bytes = unicode_option(args, index + 1, "--max-bytes")?
                    .parse()
                    .map_err(|_| "--max-bytes requires an integer".to_owned())?;
                if !(1..=MAX_SECTION_BYTES).contains(&max_bytes) {
                    return Err(format!(
                        "--max-bytes must be between 1 and {MAX_SECTION_BYTES}"
                    ));
                }
                index += 2;
                continue;
            }
            if parse_options && arg.to_string_lossy().starts_with('-') {
                return Err(format!("unknown locate-key option {arg:?}"));
            }
            positional.push(arg.clone());
            index += 1;
        }

        if positional.len() != 2 {
            return Err("locate-key expects KEY and save FILE".to_owned());
        }
        let key = positional[0]
            .to_str()
            .ok_or_else(|| "KEY must be valid Unicode".to_owned())?
            .to_owned();
        if key.is_empty() {
            return Err("KEY must not be empty".to_owned());
        }

        Ok(Self {
            section,
            token_map,
            limit,
            max_bytes,
            key,
            path: PathBuf::from(&positional[1]),
        })
    }
}

fn unicode_option<'a>(args: &'a [OsString], index: usize, name: &str) -> Result<&'a str, String> {
    args.get(index)
        .and_then(|value| value.to_str())
        .ok_or_else(|| format!("{name} requires a Unicode value"))
}

#[derive(Serialize)]
struct LocateReport {
    schema: &'static str,
    jomini: &'static str,
    source: SourceReport,
    header: HeaderReport,
    container: &'static str,
    section: SectionReport,
    query: QueryReport,
    token_map: TokenMapReport,
    limits: LimitsReport,
    total_events: usize,
    all_match_count: usize,
    truncated: bool,
    matches: Vec<MatchReport>,
}

#[derive(Serialize, PartialEq, Eq)]
struct SourceReport {
    bytes: u64,
    sha256: String,
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
struct SectionReport {
    name: RequestedSection,
    encoding: &'static str,
    coordinate_space: &'static str,
    bytes: usize,
    sha256: String,
    integrity_checked: bool,
    save_file_span: Option<ByteSpan>,
}

#[derive(Serialize)]
struct QueryReport {
    key: String,
    raw_token: Option<String>,
    resolved_name: Option<String>,
    limit: usize,
    token_map_configured: bool,
}

#[derive(Serialize)]
struct TokenMapReport {
    configured: bool,
    bytes: Option<usize>,
    sha256: Option<String>,
    coverage: TokenMapCoverageReport,
}

#[derive(Serialize)]
struct TokenMapCoverageReport {
    observed_identifier_occurrences: u64,
    observed_unique_identifiers: usize,
    resolved_unique_identifiers: usize,
    unresolved_unique_identifiers: usize,
    complete_for_section: bool,
}

#[derive(Serialize)]
struct LimitsReport {
    requested_max_section_bytes: u64,
    hard_max_section_bytes: u64,
    observed_section_bytes: usize,
    max_tokens: u64,
    max_depth: usize,
    max_estimated_walk_bytes: u64,
    observed_tokens: u64,
    observed_max_depth: usize,
    estimated_event_candidates: u64,
    estimated_path_segments: u64,
    estimated_dynamic_string_bytes: u64,
    estimated_walk_bytes: u64,
}

struct PreflightResult {
    report: LimitsReport,
    observed_identifiers: BTreeSet<u16>,
    observed_identifier_occurrences: u64,
}

struct LoadedTokenMap {
    resolver: BasicTokenResolver,
    bytes: usize,
    sha256: String,
}

#[derive(Serialize)]
struct MatchReport {
    coordinate_space: &'static str,
    canonical_path: Vec<PathSegment>,
    depth: usize,
    key_span: ByteSpan,
    equal_span: ByteSpan,
    value_span: ByteSpan,
    save_file_spans: Option<SaveFileMatchSpans>,
    key: KeyView,
    value: StructuralValue,
}

#[derive(Serialize)]
struct SaveFileMatchSpans {
    key_span: ByteSpan,
    equal_span: ByteSpan,
    value_span: ByteSpan,
}

struct ReadSection {
    data: Vec<u8>,
    integrity_checked: bool,
    coordinate_space: &'static str,
    save_file_span: Option<ByteSpan>,
}

fn locate(args: &LocateArgs) -> Result<LocateReport, String> {
    // Hash, envelope parsing, section extraction and structural walking must all
    // observe exactly the same bytes.  A single bounded snapshot also prevents
    // path replacement from producing a report assembled from multiple files.
    let source = read_bounded_source(&args.path)?;
    let source_report = SourceReport {
        bytes: source.len() as u64,
        sha256: lowercase_hex(
            &sha256_bytes(&source).map_err(|error| format!("cannot hash source save: {error}"))?,
        ),
    };
    let token_map = load_token_resolver(args.token_map.as_deref())?;
    let resolver = token_map.as_ref().map(|map| &map.resolver);
    let query = Query::parse(&args.key, resolver)?;

    let save = JominiFile::from_slice(source.as_slice())
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
    let container = match save.kind() {
        JominiFileKind::Zip(_) => "zip",
        JominiFileKind::Uncompressed(_) => "uncompressed",
    };

    let selected = read_section(&save, args, &source)?;
    let section = &selected.data;
    let section_sha256 = lowercase_hex(
        &sha256_bytes(section).map_err(|error| format!("cannot hash selected section: {error}"))?,
    );
    let preflight = preflight_binary(section, args.max_bytes, resolver)?;
    let resolved_unique_identifiers = preflight
        .observed_identifiers
        .iter()
        .filter(|token| {
            resolver
                .and_then(|resolver| resolver.resolve(**token))
                .is_some()
        })
        .count();
    let unresolved_unique_identifiers = preflight
        .observed_identifiers
        .len()
        .checked_sub(resolved_unique_identifiers)
        .expect("resolved identifiers are a subset of observed identifiers");
    let complete_for_section = unresolved_unique_identifiers == 0;
    if matches!(&query, Query::Resolved(_)) && !complete_for_section {
        return Err(format!(
            "named KEY requires complete token-map coverage for the selected section; {unresolved_unique_identifiers} of {} observed identifier(s) are unresolved",
            preflight.observed_identifiers.len()
        ));
    }
    let document = walk_binary_with_resolver(
        section,
        resolver.map(|resolver| resolver as &dyn TokenResolver),
    )
    .map_err(|error| format!("cannot structurally walk selected section: {error}"))?;

    let mut matches = Vec::new();
    let mut all_match_count = 0usize;
    for event in &document.events {
        if event.kind != StructuralEventKind::Field {
            continue;
        }
        let key = event
            .key
            .as_ref()
            .expect("field events always contain a key");
        if !query.matches(key) {
            continue;
        }
        all_match_count = all_match_count
            .checked_add(1)
            .ok_or_else(|| "match count overflowed usize".to_owned())?;
        if matches.len() < args.limit {
            let save_file_spans = selected
                .save_file_span
                .map(|physical| save_file_match_spans(physical, event))
                .transpose()?;
            matches.push(MatchReport {
                coordinate_space: selected.coordinate_space,
                canonical_path: event.path.clone(),
                depth: event.depth,
                key_span: event.key_span.expect("field events have a key span"),
                equal_span: event.equal_span.expect("field events have an equal span"),
                value_span: event.value_span,
                save_file_spans,
                key: key.clone(),
                value: event.value.clone(),
            });
        }
    }

    Ok(LocateReport {
        schema: SCHEMA,
        jomini: JOMINI_VERSION,
        source: source_report,
        header: header_report,
        container,
        section: SectionReport {
            name: args.section,
            encoding: "binary",
            coordinate_space: selected.coordinate_space,
            bytes: section.len(),
            sha256: section_sha256,
            integrity_checked: selected.integrity_checked,
            save_file_span: selected.save_file_span,
        },
        query: QueryReport {
            key: args.key.clone(),
            raw_token: query.raw_token().map(|token| format!("0x{token:04x}")),
            resolved_name: query.resolved_name().map(str::to_owned),
            limit: args.limit,
            token_map_configured: resolver.is_some(),
        },
        token_map: TokenMapReport {
            configured: token_map.is_some(),
            bytes: token_map.as_ref().map(|map| map.bytes),
            sha256: token_map.as_ref().map(|map| map.sha256.clone()),
            coverage: TokenMapCoverageReport {
                observed_identifier_occurrences: preflight.observed_identifier_occurrences,
                observed_unique_identifiers: preflight.observed_identifiers.len(),
                resolved_unique_identifiers,
                unresolved_unique_identifiers,
                complete_for_section,
            },
        },
        limits: preflight.report,
        total_events: document.events.len(),
        all_match_count,
        truncated: all_match_count > args.limit,
        matches,
    })
}

fn read_bounded_source(path: &Path) -> Result<Vec<u8>, String> {
    let file =
        File::open(path).map_err(|error| format!("cannot open {}: {error}", path.display()))?;
    let hinted_len = file
        .metadata()
        .map_err(|error| format!("cannot inspect {}: {error}", path.display()))?
        .len();
    if hinted_len > MAX_SOURCE_BYTES {
        return Err(format!(
            "source save exceeds the {MAX_SOURCE_BYTES}-byte snapshot limit"
        ));
    }
    let capacity = usize::try_from(hinted_len.min(1024 * 1024))
        .map_err(|_| "source snapshot capacity does not fit usize".to_owned())?;
    let mut source = Vec::with_capacity(capacity);
    file.take(MAX_SOURCE_BYTES + 1)
        .read_to_end(&mut source)
        .map_err(|error| format!("cannot completely snapshot {}: {error}", path.display()))?;
    if source.len() as u64 > MAX_SOURCE_BYTES {
        return Err(format!(
            "source save exceeds the {MAX_SOURCE_BYTES}-byte snapshot limit"
        ));
    }
    Ok(source)
}

enum Query {
    Raw(u16),
    Resolved(String),
}

impl Query {
    fn parse(value: &str, resolver: Option<&BasicTokenResolver>) -> Result<Self, String> {
        if value.starts_with("0x") || value.starts_with("0X") {
            let hex = &value[2..];
            if hex.len() != 4 || !hex.bytes().all(|byte| byte.is_ascii_hexdigit()) {
                return Err("raw KEY must use the exact form 0xNNNN".to_owned());
            }
            return u16::from_str_radix(hex, 16)
                .map(Self::Raw)
                .map_err(|_| "raw KEY must use the exact form 0xNNNN".to_owned());
        }
        if resolver.is_none() {
            return Err("named KEY requires --token-map".to_owned());
        }
        Ok(Self::Resolved(value.to_owned()))
    }

    fn matches(&self, key: &KeyView) -> bool {
        match self {
            Self::Raw(expected) => matches!(
                key.raw,
                ck3_index_jomini_oracle::RawTokenIdentity::Id { token } if token == *expected
            ),
            Self::Resolved(expected) => key.resolved.as_deref() == Some(expected.as_str()),
        }
    }

    fn raw_token(&self) -> Option<u16> {
        match self {
            Self::Raw(value) => Some(*value),
            Self::Resolved(_) => None,
        }
    }

    fn resolved_name(&self) -> Option<&str> {
        match self {
            Self::Raw(_) => None,
            Self::Resolved(value) => Some(value),
        }
    }
}

fn read_section<R: rawzip::ReaderAt>(
    save: &JominiFile<R>,
    args: &LocateArgs,
    source: &[u8],
) -> Result<ReadSection, String> {
    match args.section {
        RequestedSection::Metadata => match save
            .meta()
            .map_err(|error| format!("cannot open metadata: {error}"))?
        {
            SaveMetadataKind::Binary(reader) => {
                let data = read_complete(reader, args.max_bytes, "metadata")?;
                let save_file_span = inline_metadata_span(save, source, &data)?;
                Ok(ReadSection {
                    data,
                    integrity_checked: false,
                    coordinate_space: "metadata_uncompressed",
                    save_file_span,
                })
            }
            SaveMetadataKind::Text(_) => {
                Err("selected metadata section is text, not binary".to_owned())
            }
        },
        RequestedSection::Gamestate => match save.kind() {
            JominiFileKind::Zip(zip) => match zip
                .gamestate_verified()
                .map_err(|error| format!("cannot open gamestate: {error}"))?
            {
                SaveContentKind::Binary(reader) => {
                    let data = read_complete(reader, args.max_bytes, "gamestate")?;
                    Ok(ReadSection {
                        data,
                        integrity_checked: true,
                        coordinate_space: "gamestate_uncompressed",
                        save_file_span: None,
                    })
                }
                SaveContentKind::Text(_) => {
                    Err("selected gamestate section is text, not binary".to_owned())
                }
            },
            JominiFileKind::Uncompressed(_) => {
                if !save.header().kind().is_binary() {
                    return Err("selected gamestate section is text, not binary".to_owned());
                }
                let offset = (save.header().header_len() as u64)
                    .checked_add(save.header().metadata_len())
                    .ok_or_else(|| "gamestate offset overflows u64".to_owned())?;
                let offset = usize::try_from(offset)
                    .map_err(|_| "gamestate offset does not fit this platform".to_owned())?;
                let gamestate = source
                    .get(offset..)
                    .ok_or_else(|| "declared metadata extends beyond the save body".to_owned())?;
                let data = read_complete(gamestate, args.max_bytes, "gamestate")?;
                Ok(ReadSection {
                    data,
                    integrity_checked: false,
                    coordinate_space: "gamestate_uncompressed",
                    save_file_span: Some(ByteSpan {
                        start: offset,
                        end: source.len(),
                    }),
                })
            }
        },
    }
}

fn inline_metadata_span<R: rawzip::ReaderAt>(
    save: &JominiFile<R>,
    source: &[u8],
    metadata: &[u8],
) -> Result<Option<ByteSpan>, String> {
    if !matches!(
        save.header().kind(),
        SaveHeaderKind::Binary | SaveHeaderKind::UnifiedBinary
    ) {
        return Ok(None);
    }
    let start = save.header().header_len();
    let declared = usize::try_from(save.header().metadata_len())
        .map_err(|_| "declared metadata length does not fit this platform".to_owned())?;
    let end = start
        .checked_add(declared)
        .ok_or_else(|| "inline metadata span overflows usize".to_owned())?;
    let inline = source.get(start..end);
    if inline == Some(metadata) {
        return Ok(Some(ByteSpan { start, end }));
    }
    // Jomini deliberately accepts ZIPs whose legacy Binary header is
    // misleading.  In that case metadata may live in the ZIP and therefore
    // has no contiguous save-file coordinate even though it decoded cleanly.
    if save.header().kind() == SaveHeaderKind::Binary
        && matches!(save.kind(), JominiFileKind::Zip(_))
    {
        return Ok(None);
    }
    Err("decoded metadata differs from the declared inline source snapshot".to_owned())
}

fn save_file_match_spans(
    section_span: ByteSpan,
    event: &ck3_index_jomini_oracle::StructuralEvent,
) -> Result<SaveFileMatchSpans, String> {
    let key_span = event.key_span.expect("field events have a key span");
    let equal_span = event.equal_span.expect("field events have an equal span");
    Ok(SaveFileMatchSpans {
        key_span: shift_section_span(section_span, key_span)?,
        equal_span: shift_section_span(section_span, equal_span)?,
        value_span: shift_section_span(section_span, event.value_span)?,
    })
}

fn shift_section_span(section_span: ByteSpan, local: ByteSpan) -> Result<ByteSpan, String> {
    let start = section_span
        .start
        .checked_add(local.start)
        .ok_or_else(|| "save-file span start overflows usize".to_owned())?;
    let end = section_span
        .start
        .checked_add(local.end)
        .ok_or_else(|| "save-file span end overflows usize".to_owned())?;
    if start > end || end > section_span.end {
        return Err("section-local span falls outside its save-file span".to_owned());
    }
    Ok(ByteSpan { start, end })
}

fn read_complete<R: Read>(reader: R, max_bytes: u64, name: &str) -> Result<Vec<u8>, String> {
    let capacity = usize::try_from(max_bytes.min(1024 * 1024))
        .map_err(|_| "section byte cap does not fit usize".to_owned())?;
    let mut reader = reader.take(max_bytes + 1);
    let mut data = Vec::with_capacity(capacity);
    reader
        .read_to_end(&mut data)
        .map_err(|error| format!("cannot completely read {name}: {error}"))?;
    if data.len() as u64 > max_bytes {
        return Err(format!(
            "{name} exceeds the configured {max_bytes}-byte complete-read cap"
        ));
    }
    Ok(data)
}

#[derive(Clone, Copy, Default)]
struct ScalarAllocation {
    raw_string_bytes: u64,
    resolved_string_bytes: u64,
}

struct MemoryEstimate {
    events: u64,
    path_segments: u64,
    dynamic_string_bytes: u64,
}

impl MemoryEstimate {
    fn new() -> Self {
        Self {
            events: 0,
            path_segments: 0,
            dynamic_string_bytes: 0,
        }
    }

    fn record_event(
        &mut self,
        depth: usize,
        ancestor_path_raw_string_bytes: u64,
        key: Option<ScalarAllocation>,
        value: Option<ScalarAllocation>,
    ) -> Result<u64, String> {
        self.events = self
            .events
            .checked_add(1)
            .ok_or_else(|| "event estimate overflowed u64".to_owned())?;
        self.path_segments = self
            .path_segments
            .checked_add(depth as u64 + 1)
            .ok_or_else(|| "path estimate overflowed u64".to_owned())?;

        let key_raw = key.map_or(0, |allocation| allocation.raw_string_bytes);
        let path_raw = ancestor_path_raw_string_bytes
            .checked_add(key_raw)
            .ok_or_else(|| "path string estimate overflowed u64".to_owned())?;
        let key_stored = key.map_or(0, |allocation| {
            allocation
                .raw_string_bytes
                .saturating_add(allocation.resolved_string_bytes)
        });
        let value_stored = value.map_or(0, |allocation| {
            allocation
                .raw_string_bytes
                .saturating_add(allocation.resolved_string_bytes)
        });
        let string_contents = path_raw
            .checked_add(key_stored)
            .and_then(|bytes| bytes.checked_add(value_stored))
            .ok_or_else(|| "dynamic string estimate overflowed u64".to_owned())?;
        let conservative_allocation = string_contents
            .checked_mul(ESTIMATED_DYNAMIC_STRING_MULTIPLIER)
            .ok_or_else(|| "dynamic string allocation estimate overflowed u64".to_owned())?;
        self.dynamic_string_bytes = self
            .dynamic_string_bytes
            .checked_add(conservative_allocation)
            .ok_or_else(|| "dynamic string estimate overflowed u64".to_owned())?;
        Ok(path_raw)
    }
}

fn preflight_binary(
    source: &[u8],
    requested_max_bytes: u64,
    resolver: Option<&BasicTokenResolver>,
) -> Result<PreflightResult, String> {
    let mut reader = TokenReader::from_slice(source);
    let mut tokens = 0u64;
    let mut depth = 0usize;
    let mut observed_max_depth = 0usize;
    let mut observed_identifier_occurrences = 0u64;
    let mut observed_identifiers = BTreeSet::new();
    let mut pending_scalar = None::<ScalarAllocation>;
    let mut pending_field = None::<ScalarAllocation>;
    let mut ancestor_path_raw_string_bytes = [0u64; MAX_DEPTH + 1];
    let mut memory = MemoryEstimate::new();

    loop {
        let offset = reader.position();
        let Some(token) = reader
            .next()
            .map_err(|error| format!("cannot preflight binary token at byte {offset}: {error}"))?
        else {
            break;
        };
        tokens = tokens
            .checked_add(1)
            .ok_or_else(|| "token count overflowed u64".to_owned())?;
        if tokens > MAX_TOKENS {
            return Err(format!(
                "selected section exceeds the {MAX_TOKENS}-token walk limit"
            ));
        }
        if let Token::Id(token) = token {
            observed_identifier_occurrences = observed_identifier_occurrences
                .checked_add(1)
                .ok_or_else(|| "identifier occurrence count overflowed u64".to_owned())?;
            observed_identifiers.insert(token);
        }
        match token {
            Token::Open => {
                if pending_field.is_none()
                    && let Some(value) = pending_scalar.take()
                {
                    memory.record_event(
                        depth,
                        ancestor_path_raw_string_bytes[depth],
                        None,
                        Some(value),
                    )?;
                }
                let child_path_raw = if let Some(key) = pending_field.take() {
                    memory.record_event(
                        depth,
                        ancestor_path_raw_string_bytes[depth],
                        Some(key),
                        None,
                    )?
                } else {
                    memory.record_event(
                        depth,
                        ancestor_path_raw_string_bytes[depth],
                        None,
                        None,
                    )?;
                    ancestor_path_raw_string_bytes[depth]
                };
                depth += 1;
                observed_max_depth = observed_max_depth.max(depth);
                if depth > MAX_DEPTH {
                    return Err(format!(
                        "selected section exceeds the {MAX_DEPTH}-level depth limit"
                    ));
                }
                ancestor_path_raw_string_bytes[depth] = child_path_raw;
            }
            Token::Close => {
                if pending_field.is_some() {
                    return Err("selected section contains a field without a value".to_owned());
                }
                if let Some(value) = pending_scalar.take() {
                    memory.record_event(
                        depth,
                        ancestor_path_raw_string_bytes[depth],
                        None,
                        Some(value),
                    )?;
                }
                if depth == 0 {
                    return Err("selected section contains an unmatched close container".to_owned());
                }
                depth -= 1;
            }
            Token::Equal => {
                if pending_field.is_some() {
                    return Err("selected section contains a repeated equality operator".to_owned());
                }
                pending_field = Some(pending_scalar.take().ok_or_else(|| {
                    "selected section contains an equality operator without a field key".to_owned()
                })?);
            }
            scalar => {
                let allocation = scalar_allocation(scalar, resolver);
                if let Some(key) = pending_field.take() {
                    memory.record_event(
                        depth,
                        ancestor_path_raw_string_bytes[depth],
                        Some(key),
                        Some(allocation),
                    )?;
                } else {
                    if let Some(previous) = pending_scalar.take() {
                        memory.record_event(
                            depth,
                            ancestor_path_raw_string_bytes[depth],
                            None,
                            Some(previous),
                        )?;
                    }
                    pending_scalar = Some(allocation);
                }
            }
        }
    }
    if pending_field.is_some() {
        return Err("selected section ends with a field without a value".to_owned());
    }
    if let Some(value) = pending_scalar.take() {
        memory.record_event(
            depth,
            ancestor_path_raw_string_bytes[depth],
            None,
            Some(value),
        )?;
    }
    if depth != 0 {
        return Err(format!(
            "selected section has {depth} unclosed container(s)"
        ));
    }

    let estimated_walk_bytes = (source.len() as u64)
        .checked_add(memory.events.saturating_mul(ESTIMATED_EVENT_BYTES))
        .and_then(|value| {
            value.checked_add(
                memory
                    .path_segments
                    .saturating_mul(ESTIMATED_PATH_SEGMENT_BYTES),
            )
        })
        .and_then(|value| value.checked_add(memory.dynamic_string_bytes))
        .ok_or_else(|| "estimated structural walk memory overflowed u64".to_owned())?;
    if estimated_walk_bytes > MAX_ESTIMATED_WALK_BYTES {
        return Err(format!(
            "selected section's estimated structural walk memory ({estimated_walk_bytes} bytes) exceeds the {MAX_ESTIMATED_WALK_BYTES}-byte limit"
        ));
    }

    Ok(PreflightResult {
        report: LimitsReport {
            requested_max_section_bytes: requested_max_bytes,
            hard_max_section_bytes: MAX_SECTION_BYTES,
            observed_section_bytes: source.len(),
            max_tokens: MAX_TOKENS,
            max_depth: MAX_DEPTH,
            max_estimated_walk_bytes: MAX_ESTIMATED_WALK_BYTES,
            observed_tokens: tokens,
            observed_max_depth,
            estimated_event_candidates: memory.events,
            estimated_path_segments: memory.path_segments,
            estimated_dynamic_string_bytes: memory.dynamic_string_bytes,
            estimated_walk_bytes,
        },
        observed_identifiers,
        observed_identifier_occurrences,
    })
}

fn scalar_allocation(token: Token<'_>, resolver: Option<&BasicTokenResolver>) -> ScalarAllocation {
    match token {
        Token::Id(token) => ScalarAllocation {
            raw_string_bytes: 0,
            resolved_string_bytes: resolver
                .and_then(|resolver| resolver.resolve(token))
                .map_or(0, |name| name.len() as u64),
        },
        Token::Quoted(value) | Token::Unquoted(value) => ScalarAllocation {
            raw_string_bytes: (value.as_bytes().len() as u64).saturating_mul(2),
            resolved_string_bytes: 0,
        },
        Token::Lookup(index) => ScalarAllocation {
            raw_string_bytes: 0,
            resolved_string_bytes: resolver
                .and_then(|resolver| resolver.lookup(index))
                .map_or(0, |name| name.len() as u64),
        },
        Token::F32(_) => ScalarAllocation {
            raw_string_bytes: 8,
            resolved_string_bytes: 0,
        },
        Token::F64(_) => ScalarAllocation {
            raw_string_bytes: 16,
            resolved_string_bytes: 0,
        },
        Token::Open | Token::Close | Token::Equal => {
            unreachable!("structural operators are handled before scalar allocation")
        }
        _ => ScalarAllocation::default(),
    }
}

fn load_token_resolver(path: Option<&Path>) -> Result<Option<LoadedTokenMap>, String> {
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
    let resolver = BasicTokenResolver::from_text_lines(&data[..])
        .map_err(|error| format!("cannot parse token map {}: {error}", path.display()))?;
    let sha256 = lowercase_hex(
        &sha256_bytes(&data).map_err(|error| format!("cannot hash token map: {error}"))?,
    );
    Ok(Some(LoadedTokenMap {
        resolver,
        bytes: data.len(),
        sha256,
    }))
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
    let kind = save.header().kind();
    if matches!(kind, SaveHeaderKind::Other(_)) {
        return Err(format!(
            "unsupported save header kind 0x{:02x}",
            kind.value()
        ));
    }
    if matches!(save.kind(), JominiFileKind::Uncompressed(_))
        && matches!(
            kind,
            SaveHeaderKind::UnifiedText
                | SaveHeaderKind::UnifiedBinary
                | SaveHeaderKind::SplitText
                | SaveHeaderKind::SplitBinary
        )
    {
        return Err(format!(
            "save header kind {} requires a ZIP container, but no valid ZIP was found",
            header_kind_name(kind)
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use jomini::{Scalar, binary::Token as BinaryToken};

    fn encode(tokens: &[BinaryToken<'_>]) -> Vec<u8> {
        let mut bytes = Vec::new();
        for token in tokens {
            token.write(&mut bytes).unwrap();
        }
        bytes
    }

    #[test]
    fn raw_query_requires_four_hex_digits() {
        assert!(matches!(
            Query::parse("0x1234", None),
            Ok(Query::Raw(0x1234))
        ));
        assert!(Query::parse("0x1", None).is_err());
        assert!(Query::parse("name", None).is_err());
    }

    #[test]
    fn preflight_observes_nested_tokens() {
        let source = encode(&[
            BinaryToken::Id(0x10),
            BinaryToken::Equal,
            BinaryToken::Open,
            BinaryToken::Id(0x20),
            BinaryToken::Equal,
            BinaryToken::U32(1),
            BinaryToken::Close,
        ]);
        let preflight = preflight_binary(&source, DEFAULT_MAX_BYTES, None).unwrap();
        assert_eq!(preflight.report.observed_tokens, 7);
        assert_eq!(preflight.report.observed_max_depth, 1);
        assert_eq!(preflight.observed_identifier_occurrences, 2);
        assert_eq!(preflight.observed_identifiers.len(), 2);
    }

    #[test]
    fn preflight_rejects_amplified_text_path_clones() {
        let text_key = vec![b'x'; 32 * 1024];
        let mut source = Vec::new();
        BinaryToken::Quoted(Scalar::new(&text_key))
            .write(&mut source)
            .unwrap();
        BinaryToken::Equal.write(&mut source).unwrap();
        BinaryToken::Open.write(&mut source).unwrap();
        for value in 0..8192 {
            BinaryToken::U32(value).write(&mut source).unwrap();
        }
        BinaryToken::Close.write(&mut source).unwrap();

        let error = preflight_binary(&source, DEFAULT_MAX_BYTES, None)
            .err()
            .expect("amplified path strings should exceed the memory budget");
        assert!(
            error.contains("estimated structural walk memory"),
            "{error}"
        );
    }
}
