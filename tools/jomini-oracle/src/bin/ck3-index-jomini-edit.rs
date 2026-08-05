#![forbid(unsafe_code)]

use ck3_index_jomini_oracle::{
    ByteSpan, EnvelopeLimits, PathSegment, RawTokenIdentity, RebuildStrategy, SaveEnvelope,
    SaveLayout, SaveSection, StructuralBudget, StructuralEvent, StructuralEventKind,
    StructuralValue, TextRepresentation, envelope, lowercase_hex, sha256_bytes,
    walk_binary_with_budget, zip_rebuild::ZipRebuildLimits,
};
use jomini::{
    Scalar,
    binary::{BasicTokenResolver, Token, TokenReader, TokenResolver},
};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{
    collections::BTreeSet,
    env,
    ffi::{OsStr, OsString},
    fs::{self, OpenOptions},
    io::{self, BufWriter, Read, Seek, SeekFrom, Write},
    ops::Range,
    path::{Path, PathBuf},
    process,
    time::{SystemTime, UNIX_EPOCH},
};

const EDIT_SCHEMA: &str = "ck3-index-jomini-save-edit/v1";
const PLAN_SCHEMA: &str = "ck3-index-jomini-save-edit-plan/v1";
const PLAN_SCHEMA_V2: &str = "ck3-index-jomini-save-edit-plan/v2";
const PLAN_REPORT_SCHEMA: &str = "ck3-index-jomini-save-edit-plan-report/v2";
const APPLY_REPORT_SCHEMA: &str = "ck3-index-jomini-save-edit-apply/v2";
const PATH_FORMAT: &str = "ck3-index-jomini-raw-path/v1";
const TOOL_VERSION: &str = "0.0.0";
const JOMINI_VERSION: &str = "0.35.0";
const ZIP_REBUILD_PROFILE: &str =
    "ck3-index-jomini-zip-rebuild/v1;rawzip=0.5.1;flate2=1.1.9;backend=zlib-rs";

fn main() {
    if let Err(error) = run() {
        eprintln!("jomini-edit: {error}");
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
        println!("ck3-index-jomini-edit {TOOL_VERSION} (jomini {JOMINI_VERSION})");
        return Ok(());
    }

    match command.to_str() {
        Some("set-scalar") => {
            if args.get(1).is_some_and(|x| x == OsStr::new("--help")) {
                print_set_scalar_help();
                return Ok(());
            }
            let edit = EditArgs::parse(&args[1..])?;
            let report = edit_save(&edit)?;
            write_json(&report)
        }
        Some("plan-scalar") => {
            if args.get(1).is_some_and(|x| x == OsStr::new("--help")) {
                print_plan_scalar_help();
                return Ok(());
            }
            let plan = PlanArgs::parse(&args[1..])?;
            let report = plan_scalar_edit(&plan)?;
            write_json(&report)
        }
        Some("apply-plan") => {
            if args.get(1).is_some_and(|x| x == OsStr::new("--help")) {
                print_apply_plan_help();
                return Ok(());
            }
            let apply = ApplyArgs::parse(&args[1..])?;
            let report = apply_scalar_plan(&apply)?;
            write_json(&report)
        }
        Some(other) => Err(format!(
            "unknown command {other:?}; expected 'set-scalar', 'plan-scalar', or 'apply-plan'"
        )),
        None => Err("command must be valid Unicode".to_owned()),
    }
}

fn print_help() {
    println!(
        "ck3-index-jomini-edit <COMMAND> [OPTIONS]\n\
         \n\
         Copy-on-write edits for narrowly supported CK3 save fields.\n\
         \n\
         Commands:\n\
           plan-scalar ...  Validate and publish a hash-bound exact-path edit plan\n\
           apply-plan ...   Revalidate and apply an exact-path edit plan\n\
           set-scalar ...   Legacy unique-key scalar replacement"
    );
}

fn print_plan_scalar_help() {
    println!(
        "ck3-index-jomini-edit plan-scalar --section metadata|gamestate \\\n         --token-map MAP --raw-key 0xNNNN [--match-index N | --path-file FILE] \\\n         [--plan-format v1|v2] --expect KIND:VALUE --value KIND:VALUE \\\n         --plan PLAN SAVE\n\
         \n\
         The target is selected either by source-order match index or by the\n\
         canonical raw path in a {PATH_FORMAT} file; the two are mutually\n\
         exclusive and produce identical plans for the same field. If RAW_KEY\n\
         occurs more than once, one of them is required. Plans default to v2,\n\
         which records the save layout, section storage, ZIP manifest,\n\
         unmodified-region hashes, and the output rebuild strategy; v1 is\n\
         accepted only for unified_binary_zip saves. PLAN must not exist.\n\
         f32bits/f64bits values are exact lowercase little-endian token bytes,\n\
         not decimal floating-point values. The source save is never modified."
    );
}

fn print_apply_plan_help() {
    println!(
        "ck3-index-jomini-edit apply-plan --token-map MAP --plan PLAN SAVE OUTPUT\n\
         \n\
         Accepts v1 and v2 plans. SAVE and MAP must byte-for-byte match the\n\
         hashes bound into PLAN, and SAVE's layout must be the one PLAN was\n\
         written for. The target is reparsed by canonical raw path; recorded\n\
         offsets are audit gates only. A v2 plan additionally re-verifies every\n\
         unmodified region against both the source and the rebuilt output.\n\
         OUTPUT must not exist."
    );
}

fn print_set_scalar_help() {
    println!(
        "ck3-index-jomini-edit set-scalar --section metadata|gamestate \\\n         --expect KIND:VALUE --value KIND:VALUE RAW_KEY INPUT OUTPUT\n\
         \n\
         RAW_KEY must be a 16-bit token such as 0x1234 and globally unique in\n\
         the selected section. Supported scalar kinds: quoted, unquoted, bool,\n\
         u32, i32, u64, i64, f32bits, and f64bits. Float bits are exact\n\
         lowercase little-endian token bytes, not decimal values. The\n\
         replacement must have the same kind as the expected value. This legacy\n\
         command handles unified_binary_zip saves only; use plan-scalar and\n\
         apply-plan for every other layout. OUTPUT must not exist.\n\
         Editing is bounded to a 128 MiB source and a 64 MiB section."
    );
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

struct EditArgs {
    section: Section,
    key: u16,
    input: PathBuf,
    output: PathBuf,
    expected: ScalarValue,
    replacement: ScalarValue,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
enum Section {
    Metadata,
    Gamestate,
}

impl Section {
    const fn name(self) -> &'static str {
        match self {
            Self::Metadata => "metadata",
            Self::Gamestate => "gamestate",
        }
    }

    const fn coordinate_space(self) -> &'static str {
        match self {
            Self::Metadata => "metadata_uncompressed",
            Self::Gamestate => "gamestate_uncompressed",
        }
    }

    const fn logical(self) -> SaveSection {
        match self {
            Self::Metadata => SaveSection::Metadata,
            Self::Gamestate => SaveSection::Gamestate,
        }
    }

    const fn other(self) -> Self {
        match self {
            Self::Metadata => Self::Gamestate,
            Self::Gamestate => Self::Metadata,
        }
    }
}

impl EditArgs {
    fn parse(args: &[OsString]) -> Result<Self, String> {
        let mut section = None::<String>;
        let mut expected = None::<ScalarValue>;
        let mut replacement = None::<ScalarValue>;
        let mut positional = Vec::<OsString>::new();
        let mut index = 0usize;
        while index < args.len() {
            let arg = &args[index];
            if arg == OsStr::new("--section") {
                section = Some(option_value(args, &mut index, "--section")?);
            } else if arg == OsStr::new("--expect") {
                expected = Some(parse_scalar(&option_value(args, &mut index, "--expect")?)?);
            } else if arg == OsStr::new("--value") {
                replacement = Some(parse_scalar(&option_value(args, &mut index, "--value")?)?);
            } else if arg.to_string_lossy().starts_with('-') {
                return Err(format!("unknown option {:?}", arg.to_string_lossy()));
            } else {
                positional.push(arg.clone());
            }
            index += 1;
        }

        let section = match section.as_deref() {
            Some("metadata") => Section::Metadata,
            Some("gamestate") => Section::Gamestate,
            Some(_) => return Err("--section must be metadata or gamestate".to_owned()),
            None => return Err("--section metadata|gamestate is required".to_owned()),
        };
        let expected = expected.ok_or_else(|| "--expect KIND:VALUE is required".to_owned())?;
        let replacement = replacement.ok_or_else(|| "--value KIND:VALUE is required".to_owned())?;
        validate_scalar_change(&expected, &replacement)?;
        if positional.len() != 3 {
            return Err("set-scalar expects RAW_KEY INPUT OUTPUT after its options".to_owned());
        }
        let key_text = positional[0]
            .to_str()
            .ok_or_else(|| "RAW_KEY must be valid Unicode".to_owned())?;
        let key = parse_raw_key(key_text)?;
        Ok(Self {
            section,
            key,
            input: PathBuf::from(&positional[1]),
            output: PathBuf::from(&positional[2]),
            expected,
            replacement,
        })
    }
}

/// Which plan envelope `plan-scalar` publishes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PlanFormat {
    /// The layout-aware default.
    V2,
    /// The unified-binary-only compatibility envelope.
    V1,
}

struct PlanArgs {
    section: Section,
    token_map: PathBuf,
    key: u16,
    match_index: Option<usize>,
    path_file: Option<PathBuf>,
    plan_format: PlanFormat,
    input: PathBuf,
    plan: PathBuf,
    expected: ScalarValue,
    replacement: ScalarValue,
}

impl PlanArgs {
    fn parse(args: &[OsString]) -> Result<Self, String> {
        let mut section = None::<Section>;
        let mut token_map = None::<PathBuf>;
        let mut key = None::<u16>;
        let mut match_index = None::<usize>;
        let mut path_file = None::<PathBuf>;
        let mut plan_format = None::<PlanFormat>;
        let mut plan = None::<PathBuf>;
        let mut expected = None::<ScalarValue>;
        let mut replacement = None::<ScalarValue>;
        let mut positional = Vec::<OsString>::new();
        let mut index = 0usize;
        while index < args.len() {
            let arg = &args[index];
            if arg == OsStr::new("--section") {
                if section.is_some() {
                    return Err("--section was provided more than once".to_owned());
                }
                section = Some(parse_section(&option_value(
                    args,
                    &mut index,
                    "--section",
                )?)?);
            } else if arg == OsStr::new("--token-map") {
                if token_map.is_some() {
                    return Err("--token-map was provided more than once".to_owned());
                }
                index += 1;
                token_map = Some(PathBuf::from(
                    args.get(index)
                        .ok_or_else(|| "--token-map requires a FILE".to_owned())?,
                ));
            } else if arg == OsStr::new("--raw-key") {
                if key.is_some() {
                    return Err("--raw-key was provided more than once".to_owned());
                }
                key = Some(parse_raw_key(&option_value(
                    args,
                    &mut index,
                    "--raw-key",
                )?)?);
            } else if arg == OsStr::new("--match-index") {
                if match_index.is_some() {
                    return Err("--match-index was provided more than once".to_owned());
                }
                match_index = Some(
                    option_value(args, &mut index, "--match-index")?
                        .parse()
                        .map_err(|_| "--match-index requires a non-negative integer".to_owned())?,
                );
            } else if arg == OsStr::new("--path-file") {
                if path_file.is_some() {
                    return Err("--path-file was provided more than once".to_owned());
                }
                index += 1;
                path_file = Some(PathBuf::from(
                    args.get(index)
                        .ok_or_else(|| "--path-file requires a FILE".to_owned())?,
                ));
            } else if arg == OsStr::new("--plan-format") {
                if plan_format.is_some() {
                    return Err("--plan-format was provided more than once".to_owned());
                }
                plan_format = Some(parse_plan_format(&option_value(
                    args,
                    &mut index,
                    "--plan-format",
                )?)?);
            } else if arg == OsStr::new("--expect") {
                if expected.is_some() {
                    return Err("--expect was provided more than once".to_owned());
                }
                expected = Some(parse_scalar(&option_value(args, &mut index, "--expect")?)?);
            } else if arg == OsStr::new("--value") {
                if replacement.is_some() {
                    return Err("--value was provided more than once".to_owned());
                }
                replacement = Some(parse_scalar(&option_value(args, &mut index, "--value")?)?);
            } else if arg == OsStr::new("--plan") {
                if plan.is_some() {
                    return Err("--plan was provided more than once".to_owned());
                }
                index += 1;
                plan = Some(PathBuf::from(
                    args.get(index)
                        .ok_or_else(|| "--plan requires a FILE".to_owned())?,
                ));
            } else if arg.to_string_lossy().starts_with('-') {
                return Err(format!("unknown option {:?}", arg.to_string_lossy()));
            } else {
                positional.push(arg.clone());
            }
            index += 1;
        }

        if positional.len() != 1 {
            return Err("plan-scalar expects exactly one SAVE after its options".to_owned());
        }
        let expected = expected.ok_or_else(|| "--expect KIND:VALUE is required".to_owned())?;
        let replacement = replacement.ok_or_else(|| "--value KIND:VALUE is required".to_owned())?;
        validate_scalar_change(&expected, &replacement)?;
        if match_index.is_some() && path_file.is_some() {
            return Err(
                "--match-index and --path-file select the same target and are mutually \
                        exclusive"
                    .to_owned(),
            );
        }
        Ok(Self {
            section: section
                .ok_or_else(|| "--section metadata|gamestate is required".to_owned())?,
            token_map: token_map.ok_or_else(|| "--token-map MAP is required".to_owned())?,
            key: key.ok_or_else(|| "--raw-key 0xNNNN is required".to_owned())?,
            match_index,
            path_file,
            plan_format: plan_format.unwrap_or(PlanFormat::V2),
            input: PathBuf::from(&positional[0]),
            plan: plan.ok_or_else(|| "--plan PLAN is required".to_owned())?,
            expected,
            replacement,
        })
    }
}

fn parse_plan_format(value: &str) -> Result<PlanFormat, String> {
    match value {
        "v1" => Ok(PlanFormat::V1),
        "v2" => Ok(PlanFormat::V2),
        _ => Err("--plan-format must be v1 or v2".to_owned()),
    }
}

struct ApplyArgs {
    token_map: PathBuf,
    plan: PathBuf,
    input: PathBuf,
    output: PathBuf,
}

impl ApplyArgs {
    fn parse(args: &[OsString]) -> Result<Self, String> {
        let mut token_map = None::<PathBuf>;
        let mut plan = None::<PathBuf>;
        let mut positional = Vec::<OsString>::new();
        let mut index = 0usize;
        while index < args.len() {
            let arg = &args[index];
            if arg == OsStr::new("--token-map") {
                if token_map.is_some() {
                    return Err("--token-map was provided more than once".to_owned());
                }
                index += 1;
                token_map = Some(PathBuf::from(
                    args.get(index)
                        .ok_or_else(|| "--token-map requires a FILE".to_owned())?,
                ));
            } else if arg == OsStr::new("--plan") {
                if plan.is_some() {
                    return Err("--plan was provided more than once".to_owned());
                }
                index += 1;
                plan = Some(PathBuf::from(
                    args.get(index)
                        .ok_or_else(|| "--plan requires a FILE".to_owned())?,
                ));
            } else if arg.to_string_lossy().starts_with('-') {
                return Err(format!("unknown option {:?}", arg.to_string_lossy()));
            } else {
                positional.push(arg.clone());
            }
            index += 1;
        }
        if positional.len() != 2 {
            return Err("apply-plan expects SAVE and OUTPUT after its options".to_owned());
        }
        Ok(Self {
            token_map: token_map.ok_or_else(|| "--token-map MAP is required".to_owned())?,
            plan: plan.ok_or_else(|| "--plan PLAN is required".to_owned())?,
            input: PathBuf::from(&positional[0]),
            output: PathBuf::from(&positional[1]),
        })
    }
}

fn parse_section(value: &str) -> Result<Section, String> {
    match value {
        "metadata" => Ok(Section::Metadata),
        "gamestate" => Ok(Section::Gamestate),
        _ => Err("--section must be metadata or gamestate".to_owned()),
    }
}

fn validate_scalar_change(expected: &ScalarValue, replacement: &ScalarValue) -> Result<(), String> {
    if expected.kind() != replacement.kind() {
        return Err(format!(
            "replacement kind {} does not match expected kind {}",
            replacement.kind(),
            expected.kind()
        ));
    }
    if expected == replacement {
        return Err("replacement is identical to the expected scalar".to_owned());
    }
    Ok(())
}

fn option_value(args: &[OsString], index: &mut usize, name: &str) -> Result<String, String> {
    *index += 1;
    args.get(*index)
        .ok_or_else(|| format!("{name} requires a value"))?
        .to_str()
        .map(str::to_owned)
        .ok_or_else(|| format!("{name} value must be valid Unicode"))
}

fn parse_raw_key(value: &str) -> Result<u16, String> {
    if value.len() != 6 || !value.starts_with("0x") {
        return Err("RAW_KEY must have the exact form 0xNNNN".to_owned());
    }
    u16::from_str_radix(&value[2..], 16).map_err(|_| "RAW_KEY contains invalid hex".to_owned())
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum ScalarValue {
    Quoted(Vec<u8>),
    Unquoted(Vec<u8>),
    Bool(bool),
    U32(u32),
    I32(i32),
    U64(u64),
    I64(i64),
    F32Bits([u8; 4]),
    F64Bits([u8; 8]),
}

impl ScalarValue {
    fn kind(&self) -> &'static str {
        match self {
            Self::Quoted(_) => "quoted",
            Self::Unquoted(_) => "unquoted",
            Self::Bool(_) => "bool",
            Self::U32(_) => "u32",
            Self::I32(_) => "i32",
            Self::U64(_) => "u64",
            Self::I64(_) => "i64",
            Self::F32Bits(_) => "f32bits",
            Self::F64Bits(_) => "f64bits",
        }
    }

    fn encode(&self) -> Result<Vec<u8>, String> {
        let mut output = Vec::new();
        match self {
            Self::Quoted(value) => Token::Quoted(Scalar::new(value)).write(&mut output),
            Self::Unquoted(value) => Token::Unquoted(Scalar::new(value)).write(&mut output),
            Self::Bool(value) => Token::Bool(*value).write(&mut output),
            Self::U32(value) => Token::U32(*value).write(&mut output),
            Self::I32(value) => Token::I32(*value).write(&mut output),
            Self::U64(value) => Token::U64(*value).write(&mut output),
            Self::I64(value) => Token::I64(*value).write(&mut output),
            Self::F32Bits(value) => Token::F32(*value).write(&mut output),
            Self::F64Bits(value) => Token::F64(*value).write(&mut output),
        }
        .map_err(|error| format!("cannot encode replacement scalar: {error}"))?;
        Ok(output)
    }

    fn report(&self) -> ScalarReport {
        let value = match self {
            Self::Quoted(value) | Self::Unquoted(value) => {
                Value::String(String::from_utf8_lossy(value).into_owned())
            }
            Self::Bool(value) => Value::Bool(*value),
            Self::U32(value) => Value::from(*value),
            Self::I32(value) => Value::from(*value),
            Self::U64(value) => Value::String(value.to_string()),
            Self::I64(value) => Value::String(value.to_string()),
            Self::F32Bits(value) => Value::String(encode_hex(value)),
            Self::F64Bits(value) => Value::String(encode_hex(value)),
        };
        ScalarReport {
            kind: self.kind(),
            value,
        }
    }

    fn raw_identity(&self) -> RawTokenIdentity {
        match self {
            Self::Quoted(value) => RawTokenIdentity::Text {
                representation: TextRepresentation::Quoted,
                bytes_hex: encode_hex(value),
            },
            Self::Unquoted(value) => RawTokenIdentity::Text {
                representation: TextRepresentation::Unquoted,
                bytes_hex: encode_hex(value),
            },
            Self::Bool(value) => RawTokenIdentity::Bool { value: *value },
            Self::U32(value) => RawTokenIdentity::U32 { value: *value },
            Self::I32(value) => RawTokenIdentity::I32 { value: *value },
            Self::U64(value) => RawTokenIdentity::U64 { value: *value },
            Self::I64(value) => RawTokenIdentity::I64 { value: *value },
            Self::F32Bits(value) => RawTokenIdentity::F32 {
                bits_hex: encode_hex(value),
            },
            Self::F64Bits(value) => RawTokenIdentity::F64 {
                bits_hex: encode_hex(value),
            },
        }
    }

    fn from_raw_identity(raw: &RawTokenIdentity) -> Result<Self, String> {
        match raw {
            RawTokenIdentity::Text {
                representation,
                bytes_hex,
            } => {
                let bytes = decode_hex(bytes_hex)?;
                if bytes.len() > u16::MAX as usize {
                    return Err("planned text scalar exceeds 65535 encoded bytes".to_owned());
                }
                Ok(match representation {
                    TextRepresentation::Quoted => Self::Quoted(bytes),
                    TextRepresentation::Unquoted => Self::Unquoted(bytes),
                })
            }
            RawTokenIdentity::Bool { value } => Ok(Self::Bool(*value)),
            RawTokenIdentity::U32 { value } => Ok(Self::U32(*value)),
            RawTokenIdentity::I32 { value } => Ok(Self::I32(*value)),
            RawTokenIdentity::U64 { value } => Ok(Self::U64(*value)),
            RawTokenIdentity::I64 { value } => Ok(Self::I64(*value)),
            RawTokenIdentity::F32 { bits_hex } => {
                decode_fixed_bits::<4>(bits_hex, "planned f32 bits").map(Self::F32Bits)
            }
            RawTokenIdentity::F64 { bits_hex } => {
                decode_fixed_bits::<8>(bits_hex, "planned f64 bits").map(Self::F64Bits)
            }
            _ => Err("plan contains a scalar kind unsupported by set_scalar".to_owned()),
        }
    }
}

fn encode_hex(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut output = String::with_capacity(bytes.len() * 2);
    for &byte in bytes {
        output.push(char::from(HEX[(byte >> 4) as usize]));
        output.push(char::from(HEX[(byte & 0x0f) as usize]));
    }
    output
}

fn decode_hex(value: &str) -> Result<Vec<u8>, String> {
    if value.len() % 2 != 0 || !value.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err("plan text bytes_hex must contain an even number of hex digits".to_owned());
    }
    if value.bytes().any(|byte| byte.is_ascii_uppercase()) {
        return Err("plan text bytes_hex must use lowercase hex".to_owned());
    }
    let mut output = Vec::with_capacity(value.len() / 2);
    for pair in value.as_bytes().chunks_exact(2) {
        let pair = std::str::from_utf8(pair).expect("ASCII hex is valid UTF-8");
        output.push(
            u8::from_str_radix(pair, 16)
                .map_err(|_| "plan text bytes_hex contains invalid hex".to_owned())?,
        );
    }
    Ok(output)
}

fn decode_fixed_bits<const N: usize>(value: &str, name: &str) -> Result<[u8; N], String> {
    let expected_digits = N
        .checked_mul(2)
        .ok_or_else(|| format!("{name} width overflows usize"))?;
    if value.len() != expected_digits {
        return Err(format!(
            "{name} must contain exactly {expected_digits} lowercase hex digits"
        ));
    }
    if !value.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err(format!("{name} contains invalid hex"));
    }
    if value.bytes().any(|byte| byte.is_ascii_uppercase()) {
        return Err(format!("{name} must use lowercase hex"));
    }
    let mut output = [0u8; N];
    for (index, pair) in value.as_bytes().chunks_exact(2).enumerate() {
        let pair = std::str::from_utf8(pair).expect("ASCII hex is valid UTF-8");
        output[index] =
            u8::from_str_radix(pair, 16).map_err(|_| format!("{name} contains invalid hex"))?;
    }
    Ok(output)
}

fn parse_scalar(value: &str) -> Result<ScalarValue, String> {
    let (kind, raw) = value
        .split_once(':')
        .ok_or_else(|| "scalar must have the form KIND:VALUE".to_owned())?;
    match kind {
        "quoted" | "unquoted" => {
            let bytes = raw.as_bytes().to_vec();
            if bytes.len() > u16::MAX as usize {
                return Err(format!("{kind} value exceeds 65535 encoded bytes"));
            }
            Ok(if kind == "quoted" {
                ScalarValue::Quoted(bytes)
            } else {
                ScalarValue::Unquoted(bytes)
            })
        }
        "bool" => match raw {
            "true" => Ok(ScalarValue::Bool(true)),
            "false" => Ok(ScalarValue::Bool(false)),
            _ => Err("bool value must be true or false".to_owned()),
        },
        "u32" => raw
            .parse()
            .map(ScalarValue::U32)
            .map_err(|_| "invalid u32 value".to_owned()),
        "i32" => raw
            .parse()
            .map(ScalarValue::I32)
            .map_err(|_| "invalid i32 value".to_owned()),
        "u64" => raw
            .parse()
            .map(ScalarValue::U64)
            .map_err(|_| "invalid u64 value".to_owned()),
        "i64" => raw
            .parse()
            .map(ScalarValue::I64)
            .map_err(|_| "invalid i64 value".to_owned()),
        "f32bits" => decode_fixed_bits::<4>(raw, "f32bits value").map(ScalarValue::F32Bits),
        "f64bits" => decode_fixed_bits::<8>(raw, "f64bits value").map(ScalarValue::F64Bits),
        _ => Err(format!("unsupported scalar kind {kind:?}")),
    }
}

#[derive(Clone)]
struct Match {
    start: usize,
    end: usize,
    value: ScalarValue,
}

const MAX_EDIT_SCAN_TOKENS: usize = 8_000_000;
const MAX_EDIT_SCAN_DEPTH: usize = 512;
const MAX_EDIT_SCAN_EVENTS: u64 = 2_000_000;
const MAX_EDIT_PATH_SEGMENTS: u64 = 16_000_000;
const MAX_EDIT_DYNAMIC_BYTES: u64 = 384 * 1024 * 1024;

fn scan_binary_section(section: &str, data: &[u8], key: u16) -> Result<Vec<Match>, String> {
    let section_kind = match section {
        "metadata" => Section::Metadata,
        "gamestate" => Section::Gamestate,
        _ => return Err(format!("unsupported section name {section:?}")),
    };
    let document = structurally_walk(section_kind, data)?;
    let mut matches = Vec::new();
    for event in raw_key_candidates(&document.events, key) {
        let value = ScalarValue::from_raw_identity(event_scalar(event)?).map_err(|error| {
            format!("target key 0x{key:04x} has an unsupported scalar: {error}")
        })?;
        matches.push(Match {
            start: event.value_span.start,
            end: event.value_span.end,
            value,
        });
    }
    Ok(matches)
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct EditPlan {
    schema: String,
    plan_id: String,
    body: PlanBody,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct PlanBody {
    operation: String,
    producer: ProducerBinding,
    source: SourceBinding,
    section: SectionBinding,
    token_map: TokenMapBinding,
    selection: SelectionBinding,
    target: TargetBinding,
    replacement: RawTokenIdentity,
    predicted_output: PredictedOutputBinding,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct ProducerBinding {
    tool: String,
    tool_version: String,
    jomini_version: String,
    path_format: String,
    zip_rebuild_profile: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct SourceBinding {
    bytes: u64,
    sha256: String,
    container: String,
    header: HeaderBinding,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct HeaderBinding {
    version: u16,
    kind: String,
    kind_code: u16,
    header_bytes: usize,
    declared_metadata_bytes: u64,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct SectionBinding {
    name: Section,
    encoding: String,
    coordinate_space: String,
    bytes: u64,
    sha256: String,
    integrity_checked: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct TokenMapBinding {
    bytes: u64,
    sha256: String,
    coverage: CoverageBinding,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct CoverageBinding {
    observed_identifier_occurrences: u64,
    observed_unique_identifiers: u64,
    resolved_unique_identifiers: u64,
    unresolved_unique_identifiers: u64,
    complete_for_section: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct SelectionBinding {
    query_raw_key: RawTokenIdentity,
    raw_key_match_count: u64,
    selected_match_index: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct TargetBinding {
    canonical_raw_path: Vec<PathSegment>,
    depth: u64,
    raw_key: RawTokenIdentity,
    spans: TargetSpans,
    expected: RawTokenIdentity,
    expected_token_bytes: u64,
    expected_token_sha256: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct TargetSpans {
    key: ByteSpan,
    equal: ByteSpan,
    value: ByteSpan,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct PredictedOutputBinding {
    save_bytes: u64,
    save_sha256: String,
    section_bytes: u64,
    section_sha256: String,
}

/// A layout-aware edit plan.
///
/// v2 adds everything v1 had to assume about a `unified_binary` ZIP: which
/// layout the save actually has, where each section is stored, the archive
/// manifest, the byte regions the edit promises not to change, and how the
/// output is reconstructed.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct EditPlanV2 {
    schema: String,
    plan_id: String,
    body: PlanBodyV2,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct PlanBodyV2 {
    operation: String,
    producer: ProducerBinding,
    source: SourceBindingV2,
    section: SectionBinding,
    token_map: TokenMapBinding,
    selection: SelectionBinding,
    target: TargetBinding,
    replacement: RawTokenIdentity,
    unmodified_regions: Vec<RegionBinding>,
    rebuild: RebuildBinding,
    predicted_output: PredictedOutputBinding,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct SourceBindingV2 {
    bytes: u64,
    sha256: String,
    layout: String,
    container: String,
    header: HeaderBinding,
    storage: StorageBinding,
    zip: Option<ZipBinding>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct StorageBinding {
    metadata: String,
    metadata_zip_entry: Option<String>,
    gamestate: String,
    gamestate_zip_entry: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct ZipBinding {
    archive_offset: u64,
    archive_bytes: u64,
    archive_sha256: String,
    entries: Vec<ZipEntryBinding>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct ZipEntryBinding {
    name: String,
    compression_method: u16,
    crc32: String,
    compressed_bytes: u64,
    uncompressed_bytes: u64,
}

/// One byte region the edit must reproduce exactly.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct RegionBinding {
    id: String,
    space: String,
    bytes: u64,
    sha256: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct RebuildBinding {
    strategy: String,
    zip_entry: Option<String>,
    header_metadata_len_before: u64,
    header_metadata_len_after: u64,
    header_metadata_len_rewritten: bool,
}

#[derive(Serialize)]
struct FileFingerprint {
    bytes: u64,
    sha256: String,
}

#[derive(Serialize)]
struct PlanReport {
    schema: &'static str,
    plan_schema: String,
    plan_id: String,
    plan_file: FileFingerprint,
    layout: &'static str,
    source: SourceBinding,
    section: SectionBinding,
    token_map: TokenMapBinding,
    selection: SelectionBinding,
    /// How the caller identified the target; never part of the plan body, so
    /// path and index selection of the same field produce identical plans.
    selected_by: &'static str,
    target: TargetBinding,
    replacement: RawTokenIdentity,
    unmodified_regions: Vec<RegionBinding>,
    rebuild: RebuildBinding,
    predicted_output: PredictedOutputBinding,
    complete: bool,
}

#[derive(Serialize)]
struct ApplyReport {
    schema: &'static str,
    plan_schema: String,
    plan_id: String,
    plan_file: FileFingerprint,
    layout: &'static str,
    source: SourceBinding,
    source_section: SectionBinding,
    token_map: TokenMapBinding,
    output: FileFingerprint,
    output_section: FileFingerprint,
    section: Section,
    coordinate_space: &'static str,
    canonical_raw_path: Vec<PathSegment>,
    old: RawTokenIdentity,
    new: RawTokenIdentity,
    source_value_span: ByteSpan,
    output_value_span: ByteSpan,
    rebuild: RebuildBinding,
    unmodified_regions: Vec<RegionBinding>,
    unmodified_regions_verified: bool,
    section_scan_complete: bool,
    gamestate_integrity_checked: bool,
    complete: bool,
}

#[derive(Serialize)]
struct EditReport {
    schema: &'static str,
    section: Section,
    source_bytes: u64,
    source_sha256: String,
    output_bytes: u64,
    output_sha256: String,
    key: String,
    old: ScalarReport,
    new: ScalarReport,
    span: SpanReport,
    section_scan_complete: bool,
    gamestate_integrity_checked: bool,
    complete: bool,
}

#[derive(Serialize)]
struct ScalarReport {
    kind: &'static str,
    value: Value,
}

#[derive(Serialize)]
struct SpanReport {
    coordinate_space: &'static str,
    source_start: u64,
    source_end: u64,
    output_start: u64,
    output_end: u64,
}

struct PreparedPathEdit {
    bytes: Vec<u8>,
    section: Vec<u8>,
    output_value_span: ByteSpan,
    envelope: SaveEnvelope,
}

struct LoadedTokenMap {
    resolver: BasicTokenResolver,
    bytes: Vec<u8>,
    sha256: String,
}

fn plan_scalar_edit(args: &PlanArgs) -> Result<PlanReport, String> {
    let plan_path = checked_output_path(&args.input, &args.plan)?;
    let source = read_bounded_source(&args.input)?;
    let envelope = analyze_source(&source)?;
    let layout = envelope.layout();
    if args.plan_format == PlanFormat::V1 && layout != SaveLayout::UnifiedBinaryZip {
        return Err(format!(
            "--plan-format v1 only describes unified_binary_zip saves; this save is {}",
            layout.name()
        ));
    }
    let source_binding = bind_source(&source, &envelope)?;
    let token_map = load_required_token_map(&args.token_map)?;
    let (section, section_binding) = bind_section(&source, &envelope, args.section)?;
    let coverage = token_map_coverage(&section, &token_map.resolver)?;
    require_complete_coverage(&coverage)?;
    let token_map_binding = TokenMapBinding {
        bytes: token_map.bytes.len() as u64,
        sha256: token_map.sha256.clone(),
        coverage,
    };
    let document = structurally_walk(args.section, &section)?;
    let candidates = raw_key_candidates(&document.events, args.key);
    let match_count = candidates.len();
    let (selected_index, selected_by) = match &args.path_file {
        Some(path_file) => {
            let requested = load_raw_path_file(path_file, args.section, args.key)?;
            (
                select_by_canonical_path(&candidates, &requested)?,
                "canonical_raw_path",
            )
        }
        None => (
            select_match_index(match_count, args.match_index, args.key)?,
            "match_index",
        ),
    };
    let selected = candidates[selected_index].clone();
    let target = bind_target(&selected, args.key, &section, &args.expected)?;
    drop(candidates);
    drop(document);
    let unmodified_regions = compute_regions(&source, &envelope, args.section)?;
    let prepared = prepare_path_edit(
        &source,
        &envelope,
        args.section,
        &section,
        &selected,
        &args.replacement,
    )?;
    let output_regions = compute_regions(&prepared.bytes, &prepared.envelope, args.section)?;
    require_regions(&output_regions, &unmodified_regions, "predicted output")?;
    let rebuild = rebuild_binding(&envelope, args.section, prepared.section.len())?;
    let predicted_output = PredictedOutputBinding {
        save_bytes: prepared.bytes.len() as u64,
        save_sha256: hash_bytes(&prepared.bytes, "predicted output save")?,
        section_bytes: prepared.section.len() as u64,
        section_sha256: hash_bytes(&prepared.section, "predicted output section")?,
    };
    let selection = SelectionBinding {
        query_raw_key: RawTokenIdentity::Id { token: args.key },
        raw_key_match_count: match_count as u64,
        selected_match_index: selected_index as u64,
    };
    let producer = ProducerBinding {
        tool: "ck3-index-jomini-edit".to_owned(),
        tool_version: TOOL_VERSION.to_owned(),
        jomini_version: JOMINI_VERSION.to_owned(),
        path_format: PATH_FORMAT.to_owned(),
        zip_rebuild_profile: ZIP_REBUILD_PROFILE.to_owned(),
    };

    let (plan_schema, plan_id, plan_bytes) = match args.plan_format {
        PlanFormat::V1 => {
            let body = PlanBody {
                operation: "set_scalar".to_owned(),
                producer,
                source: source_binding.clone(),
                section: section_binding.clone(),
                token_map: token_map_binding.clone(),
                selection: selection.clone(),
                target: target.clone(),
                replacement: args.replacement.raw_identity(),
                predicted_output: predicted_output.clone(),
            };
            validate_plan_body(&body)?;
            let plan_id = plan_id(&body)?;
            let plan = EditPlan {
                schema: PLAN_SCHEMA.to_owned(),
                plan_id: plan_id.clone(),
                body,
            };
            (PLAN_SCHEMA.to_owned(), plan_id, serialize_plan(&plan)?)
        }
        PlanFormat::V2 => {
            let body = PlanBodyV2 {
                operation: "set_scalar".to_owned(),
                producer,
                source: bind_source_v2(&source, &envelope)?,
                section: section_binding.clone(),
                token_map: token_map_binding.clone(),
                selection: selection.clone(),
                target: target.clone(),
                replacement: args.replacement.raw_identity(),
                unmodified_regions: unmodified_regions.clone(),
                rebuild: rebuild.clone(),
                predicted_output: predicted_output.clone(),
            };
            validate_plan_body_v2(&body)?;
            let plan_id = plan_id_v2(&body)?;
            let plan = EditPlanV2 {
                schema: PLAN_SCHEMA_V2.to_owned(),
                plan_id: plan_id.clone(),
                body,
            };
            (PLAN_SCHEMA_V2.to_owned(), plan_id, serialize_plan(&plan)?)
        }
    };
    let plan_sha256 = hash_bytes(&plan_bytes, "edit plan")?;
    write_new_file(&plan_path, &plan_bytes)?;

    Ok(PlanReport {
        schema: PLAN_REPORT_SCHEMA,
        plan_schema,
        plan_id,
        plan_file: FileFingerprint {
            bytes: plan_bytes.len() as u64,
            sha256: plan_sha256,
        },
        layout: layout.name(),
        source: source_binding,
        section: section_binding,
        token_map: token_map_binding,
        selection,
        selected_by,
        target,
        replacement: args.replacement.raw_identity(),
        unmodified_regions,
        rebuild,
        predicted_output,
        complete: true,
    })
}

fn serialize_plan<T: Serialize>(plan: &T) -> Result<Vec<u8>, String> {
    let mut bytes = serde_json::to_vec_pretty(plan)
        .map_err(|error| format!("cannot serialize edit plan: {error}"))?;
    bytes.push(b'\n');
    if bytes.len() as u64 > MAX_PLAN_BYTES {
        return Err(format!("edit plan exceeds the {MAX_PLAN_BYTES}-byte limit"));
    }
    Ok(bytes)
}

/// A parsed plan of either supported envelope version.
enum LoadedPlan {
    V1(Box<EditPlan>),
    V2(Box<EditPlanV2>),
}

#[derive(Deserialize)]
struct SchemaProbe {
    schema: String,
}

fn load_plan(plan_bytes: &[u8]) -> Result<LoadedPlan, String> {
    let probe: SchemaProbe = serde_json::from_slice(plan_bytes)
        .map_err(|error| format!("cannot read the edit plan schema: {error}"))?;
    match probe.schema.as_str() {
        PLAN_SCHEMA => {
            let plan: EditPlan = serde_json::from_slice(plan_bytes)
                .map_err(|error| format!("cannot parse strict edit plan JSON: {error}"))?;
            validate_plan(&plan)?;
            Ok(LoadedPlan::V1(Box::new(plan)))
        }
        PLAN_SCHEMA_V2 => {
            let plan: EditPlanV2 = serde_json::from_slice(plan_bytes)
                .map_err(|error| format!("cannot parse strict edit plan JSON: {error}"))?;
            validate_plan_v2(&plan)?;
            Ok(LoadedPlan::V2(Box::new(plan)))
        }
        other => Err(format!("unsupported edit plan schema {other:?}")),
    }
}

/// The version-independent view an application pass needs.
struct PlanView<'a> {
    schema: &'a str,
    plan_id: &'a str,
    section: &'a SectionBinding,
    token_map: &'a TokenMapBinding,
    selection: &'a SelectionBinding,
    target: &'a TargetBinding,
    replacement: &'a RawTokenIdentity,
    predicted_output: &'a PredictedOutputBinding,
    source_v1: Option<&'a SourceBinding>,
    source_v2: Option<&'a SourceBindingV2>,
    unmodified_regions: Option<&'a [RegionBinding]>,
    rebuild: Option<&'a RebuildBinding>,
}

impl LoadedPlan {
    fn view(&self) -> PlanView<'_> {
        match self {
            Self::V1(plan) => PlanView {
                schema: &plan.schema,
                plan_id: &plan.plan_id,
                section: &plan.body.section,
                token_map: &plan.body.token_map,
                selection: &plan.body.selection,
                target: &plan.body.target,
                replacement: &plan.body.replacement,
                predicted_output: &plan.body.predicted_output,
                source_v1: Some(&plan.body.source),
                source_v2: None,
                unmodified_regions: None,
                rebuild: None,
            },
            Self::V2(plan) => PlanView {
                schema: &plan.schema,
                plan_id: &plan.plan_id,
                section: &plan.body.section,
                token_map: &plan.body.token_map,
                selection: &plan.body.selection,
                target: &plan.body.target,
                replacement: &plan.body.replacement,
                predicted_output: &plan.body.predicted_output,
                source_v1: None,
                source_v2: Some(&plan.body.source),
                unmodified_regions: Some(&plan.body.unmodified_regions),
                rebuild: Some(&plan.body.rebuild),
            },
        }
    }
}

fn apply_scalar_plan(args: &ApplyArgs) -> Result<ApplyReport, String> {
    let output = checked_output_path(&args.input, &args.output)?;
    let plan_bytes = read_bounded_file(&args.plan, MAX_PLAN_BYTES, "edit plan")?;
    let plan_file = FileFingerprint {
        bytes: plan_bytes.len() as u64,
        sha256: hash_bytes(&plan_bytes, "edit plan")?,
    };
    let loaded = load_plan(&plan_bytes)?;
    let plan = loaded.view();

    let token_map = load_required_token_map(&args.token_map)?;
    if token_map.bytes.len() as u64 != plan.token_map.bytes
        || token_map.sha256 != plan.token_map.sha256
    {
        return Err("token map bytes or SHA-256 do not match the edit plan".to_owned());
    }
    let source = read_bounded_source(&args.input)?;
    let envelope = analyze_source(&source)?;
    // The layout gate runs before the byte hashes so a plan carried across
    // layouts fails with the reason that actually matters.
    require_planned_layout(&plan, &envelope)?;
    let source_binding = bind_source(&source, &envelope)?;
    if let Some(planned) = plan.source_v1 {
        require_source_binding(&source_binding, planned)?;
    }
    if let Some(planned) = plan.source_v2 {
        require_source_binding_v2(&bind_source_v2(&source, &envelope)?, planned)?;
    }
    let (section, section_binding) = bind_section(&source, &envelope, plan.section.name)?;
    require_section_binding(&section_binding, plan.section)?;
    let coverage = token_map_coverage(&section, &token_map.resolver)?;
    require_complete_coverage(&coverage)?;
    if coverage != plan.token_map.coverage {
        return Err("selected-section token-map coverage differs from the edit plan".to_owned());
    }

    let document = structurally_walk(plan.section.name, &section)?;
    let query_key = raw_id(&plan.selection.query_raw_key, "selection query key")?;
    let candidates = raw_key_candidates(&document.events, query_key);
    if candidates.len() as u64 != plan.selection.raw_key_match_count {
        return Err("raw-key match count differs from the edit plan".to_owned());
    }
    let selected_index = usize::try_from(plan.selection.selected_match_index)
        .map_err(|_| "selected match index does not fit this platform".to_owned())?;
    let selected_path = candidates
        .get(selected_index)
        .map(|event| event.path.clone())
        .ok_or_else(|| "selected match index is outside the reparsed candidate list".to_owned())?;
    if selected_path != plan.target.canonical_raw_path {
        return Err(
            "selected source-order match no longer has the planned canonical path".to_owned(),
        );
    }
    let exact_matches: Vec<&StructuralEvent> = document
        .events
        .iter()
        .filter(|event| event.path == plan.target.canonical_raw_path)
        .collect();
    let selected = match exact_matches.as_slice() {
        [event] => (*event).clone(),
        [] => return Err("planned canonical raw path was not found".to_owned()),
        many => {
            return Err(format!(
                "planned canonical raw path matched {} events; exactly one is required",
                many.len()
            ));
        }
    };
    validate_target(&selected, &section, plan.target)?;
    drop(exact_matches);
    drop(candidates);
    drop(document);

    let source_regions = compute_regions(&source, &envelope, plan.section.name)?;
    if let Some(planned) = plan.unmodified_regions {
        require_regions(&source_regions, planned, "source")?;
    }
    let replacement = ScalarValue::from_raw_identity(plan.replacement)?;
    let prepared = prepare_path_edit(
        &source,
        &envelope,
        plan.section.name,
        &section,
        &selected,
        &replacement,
    )?;
    let output_regions = compute_regions(&prepared.bytes, &prepared.envelope, plan.section.name)?;
    require_regions(&output_regions, &source_regions, "rebuilt output")?;
    let rebuild = rebuild_binding(&envelope, plan.section.name, prepared.section.len())?;
    if let Some(planned) = plan.rebuild
        && &rebuild != planned
    {
        return Err("the reparsed rebuild strategy differs from the edit plan".to_owned());
    }
    let actual_prediction = PredictedOutputBinding {
        save_bytes: prepared.bytes.len() as u64,
        save_sha256: hash_bytes(&prepared.bytes, "candidate output save")?,
        section_bytes: prepared.section.len() as u64,
        section_sha256: hash_bytes(&prepared.section, "candidate output section")?,
    };
    require_prediction(&actual_prediction, plan.predicted_output)?;

    let report = ApplyReport {
        schema: APPLY_REPORT_SCHEMA,
        plan_schema: plan.schema.to_owned(),
        plan_id: plan.plan_id.to_owned(),
        plan_file,
        layout: envelope.layout().name(),
        source: source_binding,
        source_section: section_binding,
        token_map: plan.token_map.clone(),
        output: FileFingerprint {
            bytes: actual_prediction.save_bytes,
            sha256: actual_prediction.save_sha256.clone(),
        },
        output_section: FileFingerprint {
            bytes: actual_prediction.section_bytes,
            sha256: actual_prediction.section_sha256,
        },
        section: plan.section.name,
        coordinate_space: plan.section.name.coordinate_space(),
        canonical_raw_path: plan.target.canonical_raw_path.clone(),
        old: plan.target.expected.clone(),
        new: plan.replacement.clone(),
        source_value_span: plan.target.spans.value,
        output_value_span: prepared.output_value_span,
        rebuild,
        unmodified_regions: output_regions,
        unmodified_regions_verified: true,
        section_scan_complete: true,
        gamestate_integrity_checked: gamestate_integrity_checked(&envelope),
        complete: true,
    };
    write_new_file(&output, &prepared.bytes)?;
    Ok(report)
}

/// True when the gamestate was consumed through a CRC/size verifier.
///
/// Every edit reads both sections, so this depends on where the gamestate is
/// stored rather than on which section is being edited: an inline gamestate has
/// no CRC to check, a ZIP-stored one always does.
fn gamestate_integrity_checked(envelope: &SaveEnvelope) -> bool {
    envelope
        .storage(SaveSection::Gamestate)
        .zip_entry()
        .is_some()
}

fn require_planned_layout(plan: &PlanView<'_>, envelope: &SaveEnvelope) -> Result<(), String> {
    let planned = match (plan.source_v1, plan.source_v2) {
        (Some(_), _) => SaveLayout::UnifiedBinaryZip.name(),
        (_, Some(source)) => source.layout.as_str(),
        _ => return Err("edit plan carries no source binding".to_owned()),
    };
    if planned != envelope.layout().name() {
        return Err(format!(
            "edit plan targets a {planned} save, but this save is {}",
            envelope.layout().name()
        ));
    }
    Ok(())
}

fn hash_bytes(bytes: &[u8], name: &str) -> Result<String, String> {
    sha256_bytes(bytes)
        .map(|digest| lowercase_hex(&digest))
        .map_err(|error| format!("cannot hash {name}: {error}"))
}

/// Resolves and validates the save layout before anything else touches it.
fn analyze_source(source: &[u8]) -> Result<SaveEnvelope, String> {
    envelope::analyze(source, edit_limits()).map_err(|error| error.to_string())
}

fn edit_limits() -> EnvelopeLimits {
    EnvelopeLimits {
        max_section_bytes: MAX_GAMESTATE_EDIT_BYTES,
        zip: edit_zip_limits(),
    }
}

fn header_binding(envelope: &SaveEnvelope) -> HeaderBinding {
    let header = envelope.header();
    HeaderBinding {
        version: header.version(),
        kind: header_kind_name(envelope.layout()).to_owned(),
        kind_code: header.kind().value(),
        header_bytes: header.header_len(),
        declared_metadata_bytes: header.metadata_len(),
    }
}

const fn header_kind_name(layout: SaveLayout) -> &'static str {
    match layout {
        SaveLayout::BinaryUncompressed => "binary",
        SaveLayout::UnifiedBinaryZip => "unified_binary",
        SaveLayout::SplitBinaryZip => "split_binary",
    }
}

fn bind_source(source: &[u8], envelope: &SaveEnvelope) -> Result<SourceBinding, String> {
    Ok(SourceBinding {
        bytes: source.len() as u64,
        sha256: hash_bytes(source, "source save")?,
        container: envelope.layout().container().to_owned(),
        header: header_binding(envelope),
    })
}

fn bind_source_v2(source: &[u8], envelope: &SaveEnvelope) -> Result<SourceBindingV2, String> {
    let layout = envelope.layout();
    let storage = StorageBinding {
        metadata: layout.storage(SaveSection::Metadata).name().to_owned(),
        metadata_zip_entry: layout
            .storage(SaveSection::Metadata)
            .zip_entry()
            .map(str::to_owned),
        gamestate: layout.storage(SaveSection::Gamestate).name().to_owned(),
        gamestate_zip_entry: layout
            .storage(SaveSection::Gamestate)
            .zip_entry()
            .map(str::to_owned),
    };
    let zip = match envelope.zip_start() {
        None => None,
        Some(start) => {
            let archive = source
                .get(start..)
                .ok_or_else(|| "the archive offset lies outside the save".to_owned())?;
            let mut entries = Vec::with_capacity(envelope.zip_entries().len());
            for entry in envelope.zip_entries() {
                entries.push(ZipEntryBinding {
                    name: zip_entry_name(&entry.name)?,
                    compression_method: entry.compression_method,
                    crc32: format!("0x{:08x}", entry.crc32),
                    compressed_bytes: entry.compressed_bytes,
                    uncompressed_bytes: entry.uncompressed_bytes,
                });
            }
            Some(ZipBinding {
                archive_offset: start as u64,
                archive_bytes: archive.len() as u64,
                archive_sha256: hash_bytes(archive, "embedded ZIP")?,
                entries,
            })
        }
    };
    Ok(SourceBindingV2 {
        bytes: source.len() as u64,
        sha256: hash_bytes(source, "source save")?,
        layout: layout.name().to_owned(),
        container: layout.container().to_owned(),
        header: header_binding(envelope),
        storage,
        zip,
    })
}

const MAX_ZIP_ENTRY_NAME_BYTES: usize = 255;

/// Renders a ZIP entry name for a plan, refusing anything JSON cannot carry
/// unambiguously.
fn zip_entry_name(raw: &[u8]) -> Result<String, String> {
    if raw.is_empty() || raw.len() > MAX_ZIP_ENTRY_NAME_BYTES {
        return Err("ZIP entry name is empty or exceeds the supported length".to_owned());
    }
    let name = std::str::from_utf8(raw)
        .map_err(|_| "ZIP entry name is not valid UTF-8".to_owned())?
        .to_owned();
    if name.chars().any(char::is_control) {
        return Err("ZIP entry name contains control characters".to_owned());
    }
    Ok(name)
}

fn bind_section(
    source: &[u8],
    envelope: &SaveEnvelope,
    section: Section,
) -> Result<(Vec<u8>, SectionBinding), String> {
    let read = envelope::read_section(source, envelope, section.logical(), edit_limits())
        .map_err(|error| error.to_string())?;
    // Both sections are validated on every edit, so a damaged gamestate can
    // never be published as a side effect of a metadata-only plan.
    if section == Section::Metadata && envelope.layout() != SaveLayout::BinaryUncompressed {
        drop(
            envelope::read_section(source, envelope, SaveSection::Gamestate, edit_limits())
                .map_err(|error| error.to_string())?,
        );
    }
    let binding = SectionBinding {
        name: section,
        encoding: "binary".to_owned(),
        coordinate_space: section.coordinate_space().to_owned(),
        bytes: read.bytes.len() as u64,
        sha256: hash_bytes(&read.bytes, "selected section")?,
        integrity_checked: read.integrity_checked,
    };
    Ok((read.bytes, binding))
}

/// Hashes every byte region the edit promises to reproduce exactly.
///
/// The list is deterministic: header bytes outside the metadata-length field,
/// the section that is not being edited, every archive entry other than the one
/// carrying the edit, and — for an inline-metadata edit of a ZIP layout — the
/// whole archive tail.
fn compute_regions(
    data: &[u8],
    envelope: &SaveEnvelope,
    edited: Section,
) -> Result<Vec<RegionBinding>, String> {
    let mut regions = Vec::new();
    let [prefix, suffix] = envelope.header_immutable_spans();
    for (id, span) in [("header_prefix", prefix), ("header_suffix", suffix)] {
        regions.push(byte_region(data, id, "save_file", span)?);
    }

    let other = edited.other();
    let read = envelope::read_section(data, envelope, other.logical(), edit_limits())
        .map_err(|error| error.to_string())?;
    regions.push(RegionBinding {
        id: format!("section:{}", other.name()),
        space: other.coordinate_space().to_owned(),
        bytes: read.bytes.len() as u64,
        sha256: hash_bytes(&read.bytes, "unmodified section")?,
    });

    if let Some(zip_start) = envelope.zip_start() {
        let edited_entry = envelope.storage(edited.logical()).zip_entry();
        for entry in envelope.zip_entries() {
            let name = zip_entry_name(&entry.name)?;
            if edited_entry == Some(name.as_str()) {
                continue;
            }
            let start = zip_start
                .checked_add(usize::try_from(entry.compressed_data_start).unwrap_or(usize::MAX))
                .ok_or_else(|| "ZIP entry offset overflows".to_owned())?;
            let end = zip_start
                .checked_add(usize::try_from(entry.compressed_data_end).unwrap_or(usize::MAX))
                .ok_or_else(|| "ZIP entry offset overflows".to_owned())?;
            regions.push(byte_region(
                data,
                &format!("zip_entry_compressed:{name}"),
                "zip_entry_compressed",
                start..end,
            )?);
        }
        if envelope.storage(edited.logical()).zip_entry().is_none() {
            // An inline-metadata edit of a ZIP layout must leave the entire
            // archive byte-for-byte identical, not merely logically equal.
            regions.push(byte_region(
                data,
                "zip_archive",
                "save_file",
                zip_start..data.len(),
            )?);
        }
    }
    Ok(regions)
}

fn byte_region(
    data: &[u8],
    id: &str,
    space: &str,
    span: Range<usize>,
) -> Result<RegionBinding, String> {
    let bytes = data
        .get(span)
        .ok_or_else(|| format!("region {id} lies outside the save"))?;
    Ok(RegionBinding {
        id: id.to_owned(),
        space: space.to_owned(),
        bytes: bytes.len() as u64,
        sha256: hash_bytes(bytes, "unmodified region")?,
    })
}

fn require_regions(
    actual: &[RegionBinding],
    expected: &[RegionBinding],
    stage: &str,
) -> Result<(), String> {
    if actual.len() != expected.len() {
        return Err(format!(
            "{stage} lists {} unmodified regions; {} were required",
            actual.len(),
            expected.len()
        ));
    }
    for (actual, expected) in actual.iter().zip(expected) {
        if actual != expected {
            return Err(format!(
                "{stage} region {:?} does not match its required bytes",
                expected.id
            ));
        }
    }
    Ok(())
}

fn rebuild_binding(
    envelope: &SaveEnvelope,
    section: Section,
    edited_section_bytes: usize,
) -> Result<RebuildBinding, String> {
    let strategy = envelope::rebuild_strategy(envelope.layout(), section.logical());
    let before = envelope.header().metadata_len();
    let after = if strategy == RebuildStrategy::SpliceInlineMetadata {
        u64::try_from(edited_section_bytes)
            .map_err(|_| "edited metadata length does not fit".to_owned())?
    } else {
        before
    };
    Ok(RebuildBinding {
        strategy: strategy.name().to_owned(),
        zip_entry: strategy.zip_entry().map(str::to_owned),
        header_metadata_len_before: before,
        header_metadata_len_after: after,
        header_metadata_len_rewritten: after != before,
    })
}

fn structurally_walk(
    section: Section,
    bytes: &[u8],
) -> Result<ck3_index_jomini_oracle::StructuralDocument, String> {
    if bytes.len() as u64 > MAX_GAMESTATE_EDIT_BYTES {
        return Err(format!(
            "{} exceeds the {MAX_GAMESTATE_EDIT_BYTES}-byte structural edit limit",
            section.name()
        ));
    }
    walk_binary_with_budget(
        bytes,
        StructuralBudget {
            max_source_bytes: MAX_GAMESTATE_EDIT_BYTES,
            max_tokens: MAX_EDIT_SCAN_TOKENS as u64,
            max_depth: MAX_EDIT_SCAN_DEPTH,
            max_events: MAX_EDIT_SCAN_EVENTS,
            max_path_segments: MAX_EDIT_PATH_SEGMENTS,
            max_dynamic_bytes: MAX_EDIT_DYNAMIC_BYTES,
        },
    )
    .map_err(|error| {
        format!(
            "cannot structurally walk {} within edit and nesting limits: {error}",
            section.name()
        )
    })
}

fn raw_key_candidates(events: &[StructuralEvent], key: u16) -> Vec<&StructuralEvent> {
    events
        .iter()
        .filter(|event| {
            event.kind == StructuralEventKind::Field
                && event
                    .key
                    .as_ref()
                    .is_some_and(|view| view.raw == RawTokenIdentity::Id { token: key })
        })
        .collect()
}

const MAX_PATH_FILE_BYTES: u64 = 64 * 1024;

/// The strict on-disk form of a canonical raw path.
///
/// It exists so a caller can paste the `canonical_raw_path` a read or locate
/// report already produced instead of counting source-order occurrences.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawPathFile {
    format: String,
    section: Section,
    canonical_raw_path: Vec<PathSegment>,
}

fn load_raw_path_file(path: &Path, section: Section, key: u16) -> Result<Vec<PathSegment>, String> {
    let bytes = read_bounded_file(path, MAX_PATH_FILE_BYTES, "raw path file")?;
    let file: RawPathFile = serde_json::from_slice(&bytes)
        .map_err(|error| format!("cannot parse strict raw path JSON: {error}"))?;
    if file.format != PATH_FORMAT {
        return Err(format!(
            "unsupported raw path format {:?}; expected {PATH_FORMAT}",
            file.format
        ));
    }
    if file.section != section {
        return Err(format!(
            "raw path file selects {} but --section is {}",
            file.section.name(),
            section.name()
        ));
    }
    if file.canonical_raw_path.is_empty() || file.canonical_raw_path.len() > MAX_EDIT_SCAN_DEPTH + 1
    {
        return Err("raw path is empty or deeper than the supported nesting limit".to_owned());
    }
    match file.canonical_raw_path.last() {
        Some(PathSegment::Field {
            key: RawTokenIdentity::Id { token },
            ..
        }) if *token == key => {}
        _ => {
            return Err(format!("raw path does not end at --raw-key 0x{key:04x}"));
        }
    }
    Ok(file.canonical_raw_path)
}

/// Converts a canonical raw path into the source-order index of the same field.
///
/// Going through the index keeps path-selected and index-selected plans
/// byte-identical for the same target.
fn select_by_canonical_path(
    candidates: &[&StructuralEvent],
    requested: &[PathSegment],
) -> Result<usize, String> {
    let mut found = None;
    for (index, event) in candidates.iter().enumerate() {
        if event.path == requested {
            if found.is_some() {
                return Err(
                    "the requested canonical raw path matches more than one field".to_owned(),
                );
            }
            found = Some(index);
        }
    }
    found.ok_or_else(|| "the requested canonical raw path was not found".to_owned())
}

fn select_match_index(
    match_count: usize,
    requested: Option<usize>,
    key: u16,
) -> Result<usize, String> {
    match (match_count, requested) {
        (0, _) => Err(format!("target key 0x{key:04x} was not found")),
        (1, None) => Ok(0),
        (many, None) => Err(format!(
            "target key 0x{key:04x} matched {many} fields; --match-index is required"
        )),
        (many, Some(index)) if index >= many => Err(format!(
            "--match-index {index} is outside the {many} source-order match(es)"
        )),
        (_, Some(index)) => Ok(index),
    }
}

fn bind_target(
    event: &StructuralEvent,
    key: u16,
    section: &[u8],
    expected: &ScalarValue,
) -> Result<TargetBinding, String> {
    let raw = event_scalar(event)?;
    if raw != &expected.raw_identity() {
        return Err(format!(
            "expected {} value does not match the selected stored scalar",
            expected.kind()
        ));
    }
    let encoded = event
        .value_span
        .get(section)
        .ok_or_else(|| "selected value span lies outside the section".to_owned())?;
    let expected_encoded = expected.encode()?;
    if encoded != expected_encoded {
        return Err(
            "selected scalar has a non-canonical encoding unsupported by v1 plans".to_owned(),
        );
    }
    let key_span = event
        .key_span
        .ok_or_else(|| "selected field has no key span".to_owned())?;
    let equal_span = event
        .equal_span
        .ok_or_else(|| "selected field has no equal span".to_owned())?;
    Ok(TargetBinding {
        canonical_raw_path: event.path.clone(),
        depth: event.depth as u64,
        raw_key: RawTokenIdentity::Id { token: key },
        spans: TargetSpans {
            key: key_span,
            equal: equal_span,
            value: event.value_span,
        },
        expected: raw.clone(),
        expected_token_bytes: encoded.len() as u64,
        expected_token_sha256: hash_bytes(encoded, "selected scalar token")?,
    })
}

fn event_scalar(event: &StructuralEvent) -> Result<&RawTokenIdentity, String> {
    if event.kind != StructuralEventKind::Field {
        return Err("planned target is not a field".to_owned());
    }
    match &event.value {
        StructuralValue::Scalar { raw, .. } => Ok(raw),
        StructuralValue::Container => Err("planned target is a container, not a scalar".to_owned()),
    }
}

fn validate_target(
    event: &StructuralEvent,
    section: &[u8],
    target: &TargetBinding,
) -> Result<(), String> {
    if event.path != target.canonical_raw_path {
        return Err("reparsed target path differs from the edit plan".to_owned());
    }
    if event.depth as u64 != target.depth {
        return Err("reparsed target depth differs from the edit plan".to_owned());
    }
    let key = event
        .key
        .as_ref()
        .ok_or_else(|| "reparsed target field has no key".to_owned())?;
    if key.raw != target.raw_key {
        return Err("reparsed target raw key differs from the edit plan".to_owned());
    }
    let spans = TargetSpans {
        key: event
            .key_span
            .ok_or_else(|| "reparsed target has no key span".to_owned())?,
        equal: event
            .equal_span
            .ok_or_else(|| "reparsed target has no equal span".to_owned())?,
        value: event.value_span,
    };
    if spans != target.spans {
        return Err("reparsed target spans differ from the edit plan".to_owned());
    }
    if event_scalar(event)? != &target.expected {
        return Err("reparsed target scalar differs from the edit plan".to_owned());
    }
    let encoded = event
        .value_span
        .get(section)
        .ok_or_else(|| "reparsed target value span lies outside the section".to_owned())?;
    if encoded.len() as u64 != target.expected_token_bytes
        || hash_bytes(encoded, "reparsed target scalar")? != target.expected_token_sha256
    {
        return Err("reparsed target token bytes differ from the edit plan".to_owned());
    }
    let expected = ScalarValue::from_raw_identity(&target.expected)?;
    if expected.encode()? != encoded {
        return Err("reparsed target scalar encoding is not canonical".to_owned());
    }
    Ok(())
}

fn prepare_path_edit(
    source: &[u8],
    envelope: &SaveEnvelope,
    section_kind: Section,
    section: &[u8],
    event: &StructuralEvent,
    replacement: &ScalarValue,
) -> Result<PreparedPathEdit, String> {
    let source_span = event.value_span;
    let old = ScalarValue::from_raw_identity(event_scalar(event)?)?;
    validate_scalar_change(&old, replacement)?;
    let old_encoded = source_span
        .get(section)
        .ok_or_else(|| "target value span lies outside the section".to_owned())?;
    if old.encode()? != old_encoded {
        return Err(
            "target scalar has a non-canonical encoding unsupported by v1 plans".to_owned(),
        );
    }
    let replacement_bytes = replacement.encode()?;
    let new_section_len = section
        .len()
        .checked_sub(source_span.len())
        .and_then(|len| len.checked_add(replacement_bytes.len()))
        .ok_or_else(|| "edited section length overflow".to_owned())?;
    if new_section_len as u64 > MAX_GAMESTATE_EDIT_BYTES {
        return Err(format!(
            "edited section exceeds the {MAX_GAMESTATE_EDIT_BYTES}-byte limit"
        ));
    }
    let mut edited_section = Vec::with_capacity(new_section_len);
    edited_section.extend_from_slice(&section[..source_span.start]);
    edited_section.extend_from_slice(&replacement_bytes);
    edited_section.extend_from_slice(&section[source_span.end..]);
    let expected_output_span = ByteSpan {
        start: source_span.start,
        end: source_span.start + replacement_bytes.len(),
    };

    let edited = envelope::rebuild_save(
        source,
        envelope,
        section_kind.logical(),
        &edited_section,
        edit_limits(),
    )
    .map_err(|error| error.to_string())?;
    let output_envelope = analyze_source(&edited)?;
    let (checked_section, _) = bind_section(&edited, &output_envelope, section_kind)?;
    if checked_section != edited_section {
        return Err("self-check section differs after save rebuild".to_owned());
    }
    let checked_document = structurally_walk(section_kind, &checked_section)?;
    let matches: Vec<&StructuralEvent> = checked_document
        .events
        .iter()
        .filter(|candidate| candidate.path == event.path)
        .collect();
    let checked = match matches.as_slice() {
        [checked] => *checked,
        [] => return Err("edited output no longer contains the target path".to_owned()),
        many => {
            return Err(format!(
                "edited output contains {} copies of the target path",
                many.len()
            ));
        }
    };
    if event_scalar(checked)? != &replacement.raw_identity() {
        return Err("edited output target does not contain the replacement scalar".to_owned());
    }
    if checked.value_span != expected_output_span {
        return Err("edited output target span differs from the predicted splice".to_owned());
    }
    if checked
        .value_span
        .get(&checked_section)
        .ok_or_else(|| "edited output target span lies outside the section".to_owned())?
        != replacement_bytes
    {
        return Err("edited output token bytes differ from the requested replacement".to_owned());
    }
    Ok(PreparedPathEdit {
        bytes: edited,
        section: checked_section,
        output_value_span: checked.value_span,
        envelope: output_envelope,
    })
}

fn load_required_token_map(path: &Path) -> Result<LoadedTokenMap, String> {
    let bytes = read_bounded_file(path, TOKEN_MAP_MAX_BYTES, "token map")?;
    if bytes.is_empty() {
        return Err("token map must not be empty".to_owned());
    }
    for (index, line) in bytes.split_inclusive(|byte| *byte == b'\n').enumerate() {
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
    let resolver = BasicTokenResolver::from_text_lines(&bytes[..])
        .map_err(|error| format!("cannot parse token map {}: {error}", path.display()))?;
    let sha256 = hash_bytes(&bytes, "token map")?;
    Ok(LoadedTokenMap {
        resolver,
        bytes,
        sha256,
    })
}

fn token_map_coverage(
    section: &[u8],
    resolver: &BasicTokenResolver,
) -> Result<CoverageBinding, String> {
    let mut reader = TokenReader::from_slice(section);
    let mut occurrences = 0u64;
    let mut tokens = 0u64;
    let mut identifiers = BTreeSet::<u16>::new();
    loop {
        let offset = reader.position();
        let Some(token) = reader
            .next()
            .map_err(|error| format!("cannot scan token-map coverage at byte {offset}: {error}"))?
        else {
            break;
        };
        tokens = tokens
            .checked_add(1)
            .ok_or_else(|| "section token count overflow".to_owned())?;
        if tokens > MAX_EDIT_SCAN_TOKENS as u64 {
            return Err(format!(
                "selected section exceeds the {MAX_EDIT_SCAN_TOKENS}-token edit limit"
            ));
        }
        if let Token::Id(id) = token {
            occurrences = occurrences
                .checked_add(1)
                .ok_or_else(|| "identifier occurrence count overflow".to_owned())?;
            identifiers.insert(id);
        }
    }
    let resolved = identifiers
        .iter()
        .filter(|id| resolver.resolve(**id).is_some())
        .count() as u64;
    let observed = identifiers.len() as u64;
    let unresolved = observed
        .checked_sub(resolved)
        .expect("resolved identifiers are a subset of observed identifiers");
    Ok(CoverageBinding {
        observed_identifier_occurrences: occurrences,
        observed_unique_identifiers: observed,
        resolved_unique_identifiers: resolved,
        unresolved_unique_identifiers: unresolved,
        complete_for_section: unresolved == 0,
    })
}

fn require_complete_coverage(coverage: &CoverageBinding) -> Result<(), String> {
    if !coverage.complete_for_section || coverage.unresolved_unique_identifiers != 0 {
        return Err(format!(
            "token map is incomplete for the selected section: {} of {} identifier(s) unresolved",
            coverage.unresolved_unique_identifiers, coverage.observed_unique_identifiers
        ));
    }
    if coverage.resolved_unique_identifiers != coverage.observed_unique_identifiers {
        return Err("token-map coverage counters are inconsistent".to_owned());
    }
    Ok(())
}

fn plan_id(body: &PlanBody) -> Result<String, String> {
    let canonical = serde_json::to_vec(body)
        .map_err(|error| format!("cannot canonically serialize edit plan body: {error}"))?;
    Ok(format!(
        "sha256:{}",
        hash_bytes(&canonical, "edit plan body")?
    ))
}

fn plan_id_v2(body: &PlanBodyV2) -> Result<String, String> {
    let canonical = serde_json::to_vec(body)
        .map_err(|error| format!("cannot canonicalize edit plan body: {error}"))?;
    Ok(format!(
        "sha256:{}",
        hash_bytes(&canonical, "edit plan body")?
    ))
}

fn validate_plan(plan: &EditPlan) -> Result<(), String> {
    if plan.schema != PLAN_SCHEMA {
        return Err(format!("unsupported edit plan schema {:?}", plan.schema));
    }
    validate_plan_body(&plan.body)?;
    let expected_id = plan_id(&plan.body)?;
    if plan.plan_id != expected_id {
        return Err("edit plan ID does not match its typed body".to_owned());
    }
    Ok(())
}

fn validate_plan_v2(plan: &EditPlanV2) -> Result<(), String> {
    if plan.schema != PLAN_SCHEMA_V2 {
        return Err(format!("unsupported edit plan schema {:?}", plan.schema));
    }
    validate_plan_body_v2(&plan.body)?;
    let expected_id = plan_id_v2(&plan.body)?;
    if plan.plan_id != expected_id {
        return Err("edit plan ID does not match its typed body".to_owned());
    }
    Ok(())
}

/// Validates everything a v2 plan claims about the save's shape.
///
/// The layout table is closed, so an unknown layout, a storage claim that the
/// layout contradicts, or a rebuild strategy from a different layout is refused
/// before any save is opened.
fn validate_plan_body_v2(body: &PlanBodyV2) -> Result<(), String> {
    validate_common_plan_body(
        &body.operation,
        &body.producer,
        body.source.bytes,
        &body.source.sha256,
        &body.section,
        &body.token_map,
        &body.selection,
        &body.target,
        &body.replacement,
        &body.predicted_output,
    )?;

    let layout = match body.source.layout.as_str() {
        "binary_uncompressed" => SaveLayout::BinaryUncompressed,
        "unified_binary_zip" => SaveLayout::UnifiedBinaryZip,
        "split_binary_zip" => SaveLayout::SplitBinaryZip,
        other => return Err(format!("edit plan names an unsupported layout {other:?}")),
    };
    if body.source.container != layout.container()
        || body.source.header.kind != header_kind_name(layout)
        || body.source.header.header_bytes == 0
    {
        return Err("edit plan header and container disagree with its layout".to_owned());
    }
    let expected_storage = StorageBinding {
        metadata: layout.storage(SaveSection::Metadata).name().to_owned(),
        metadata_zip_entry: layout
            .storage(SaveSection::Metadata)
            .zip_entry()
            .map(str::to_owned),
        gamestate: layout.storage(SaveSection::Gamestate).name().to_owned(),
        gamestate_zip_entry: layout
            .storage(SaveSection::Gamestate)
            .zip_entry()
            .map(str::to_owned),
    };
    if body.source.storage != expected_storage {
        return Err("edit plan storage locations do not match its layout".to_owned());
    }
    if body.section.integrity_checked
        != layout
            .storage(body.section.name.logical())
            .zip_entry()
            .is_some()
    {
        return Err("planned section integrity flag is inconsistent with its storage".to_owned());
    }

    match (&body.source.zip, layout) {
        (None, SaveLayout::BinaryUncompressed) => {}
        (Some(_), SaveLayout::BinaryUncompressed) => {
            return Err("an uncompressed layout must not carry a ZIP manifest".to_owned());
        }
        (None, _) => return Err("a ZIP layout must carry its archive manifest".to_owned()),
        (Some(zip), _) => validate_zip_binding(zip, layout)?,
    }

    let strategy = envelope::rebuild_strategy(layout, body.section.name.logical());
    if body.rebuild.strategy != strategy.name()
        || body.rebuild.zip_entry.as_deref() != strategy.zip_entry()
    {
        return Err("edit plan rebuild strategy does not match its layout".to_owned());
    }
    let rewrites_header = strategy == RebuildStrategy::SpliceInlineMetadata;
    if body.rebuild.header_metadata_len_before != body.source.header.declared_metadata_bytes {
        return Err("edit plan rebuild disagrees with the declared metadata length".to_owned());
    }
    if rewrites_header {
        if body.rebuild.header_metadata_len_before != body.section.bytes {
            return Err("edit plan inline metadata length differs from its section".to_owned());
        }
        if body.rebuild.header_metadata_len_after != body.predicted_output.section_bytes {
            return Err("edit plan predicted metadata length is inconsistent".to_owned());
        }
    } else if body.rebuild.header_metadata_len_after != body.rebuild.header_metadata_len_before {
        return Err("only an inline-metadata edit may change the header length".to_owned());
    }
    if body.rebuild.header_metadata_len_rewritten
        != (body.rebuild.header_metadata_len_after != body.rebuild.header_metadata_len_before)
    {
        return Err("edit plan header-rewrite flag is inconsistent".to_owned());
    }

    validate_regions(&body.unmodified_regions)?;
    Ok(())
}

fn validate_zip_binding(zip: &ZipBinding, layout: SaveLayout) -> Result<(), String> {
    validate_digest(&zip.archive_sha256, "embedded ZIP SHA-256")?;
    if zip.archive_bytes == 0 || zip.archive_bytes > MAX_SOURCE_EDIT_BYTES {
        return Err("planned archive size is outside the supported limits".to_owned());
    }
    if zip.entries.is_empty() || zip.entries.len() > edit_zip_limits().max_entries {
        return Err("planned archive entry count is outside the supported limits".to_owned());
    }
    let mut names = BTreeSet::new();
    for entry in &zip.entries {
        if !names.insert(entry.name.as_str()) {
            return Err(format!(
                "planned archive lists {:?} more than once",
                entry.name
            ));
        }
        drop(zip_entry_name(entry.name.as_bytes())?);
        if entry.compression_method != 0 && entry.compression_method != 8 {
            return Err(format!(
                "planned archive entry {:?} uses an unsupported compression method",
                entry.name
            ));
        }
        if entry.crc32.len() != 10 || !entry.crc32.starts_with("0x") {
            return Err(format!(
                "planned archive entry {:?} has a malformed CRC32",
                entry.name
            ));
        }
        if !entry.crc32[2..]
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
        {
            return Err(format!(
                "planned archive entry {:?} has a malformed CRC32",
                entry.name
            ));
        }
    }
    if !names.contains(envelope::GAMESTATE_ENTRY) {
        return Err("planned archive has no gamestate entry".to_owned());
    }
    let has_meta = names.contains(envelope::META_ENTRY);
    match layout {
        SaveLayout::SplitBinaryZip if !has_meta => {
            return Err("a split_binary_zip plan must list a meta entry".to_owned());
        }
        SaveLayout::UnifiedBinaryZip if has_meta => {
            return Err("a unified_binary_zip plan must not list a meta entry".to_owned());
        }
        _ => {}
    }
    Ok(())
}

const MAX_PLAN_REGIONS: usize = 128;
const MAX_REGION_ID_BYTES: usize = 512;

fn validate_regions(regions: &[RegionBinding]) -> Result<(), String> {
    if regions.len() < 3 || regions.len() > MAX_PLAN_REGIONS {
        return Err("planned unmodified-region count is outside the supported limits".to_owned());
    }
    let mut ids = BTreeSet::new();
    for region in regions {
        if region.id.is_empty()
            || region.id.len() > MAX_REGION_ID_BYTES
            || region.id.chars().any(char::is_control)
        {
            return Err("planned unmodified-region id is empty or malformed".to_owned());
        }
        if !ids.insert(region.id.as_str()) {
            return Err(format!(
                "planned unmodified regions repeat the id {:?}",
                region.id
            ));
        }
        if !matches!(
            region.space.as_str(),
            "save_file"
                | "zip_entry_compressed"
                | "metadata_uncompressed"
                | "gamestate_uncompressed"
        ) {
            return Err(format!(
                "planned unmodified region {:?} names an unsupported coordinate space",
                region.id
            ));
        }
        if region.bytes > MAX_SOURCE_EDIT_BYTES {
            return Err(format!(
                "planned unmodified region {:?} is larger than the edit envelope",
                region.id
            ));
        }
        validate_digest(&region.sha256, "unmodified region SHA-256")?;
    }
    Ok(())
}

fn validate_plan_body(body: &PlanBody) -> Result<(), String> {
    validate_common_plan_body(
        &body.operation,
        &body.producer,
        body.source.bytes,
        &body.source.sha256,
        &body.section,
        &body.token_map,
        &body.selection,
        &body.target,
        &body.replacement,
        &body.predicted_output,
    )?;
    if body.source.container != "zip"
        || body.source.header.kind != header_kind_name(SaveLayout::UnifiedBinaryZip)
        || body.source.header.kind_code != UNIFIED_BINARY_KIND_CODE
        || body.source.header.header_bytes == 0
    {
        return Err("edit plan is not bound to a unified_binary ZIP save".to_owned());
    }
    if body.section.integrity_checked != matches!(body.section.name, Section::Gamestate) {
        return Err("planned section integrity flag is inconsistent".to_owned());
    }
    Ok(())
}

const UNIFIED_BINARY_KIND_CODE: u16 = 3;

/// The checks every plan envelope shares, independent of the save's layout.
#[allow(clippy::too_many_arguments)]
fn validate_common_plan_body(
    operation: &str,
    producer: &ProducerBinding,
    source_bytes: u64,
    source_sha256: &str,
    section: &SectionBinding,
    token_map: &TokenMapBinding,
    selection: &SelectionBinding,
    target: &TargetBinding,
    replacement_raw: &RawTokenIdentity,
    predicted_output: &PredictedOutputBinding,
) -> Result<(), String> {
    if operation != "set_scalar" {
        return Err("edit plan operation must be set_scalar".to_owned());
    }
    if producer.tool != "ck3-index-jomini-edit"
        || producer.tool_version != TOOL_VERSION
        || producer.jomini_version != JOMINI_VERSION
        || producer.path_format != PATH_FORMAT
        || producer.zip_rebuild_profile != ZIP_REBUILD_PROFILE
    {
        return Err("edit plan producer or path contract is incompatible".to_owned());
    }
    validate_digest(source_sha256, "source SHA-256")?;
    validate_digest(&section.sha256, "section SHA-256")?;
    validate_digest(&token_map.sha256, "token-map SHA-256")?;
    validate_digest(&target.expected_token_sha256, "expected token SHA-256")?;
    validate_digest(&predicted_output.save_sha256, "predicted save SHA-256")?;
    validate_digest(
        &predicted_output.section_sha256,
        "predicted section SHA-256",
    )?;
    if source_bytes > MAX_SOURCE_EDIT_BYTES || source_bytes == 0 {
        return Err("planned source size is outside the supported edit envelope".to_owned());
    }
    if section.encoding != "binary"
        || section.coordinate_space != section.name.coordinate_space()
        || section.bytes > MAX_GAMESTATE_EDIT_BYTES
    {
        return Err(
            "planned section encoding, coordinate space, or size is unsupported".to_owned(),
        );
    }
    if token_map.bytes == 0 || token_map.bytes > TOKEN_MAP_MAX_BYTES {
        return Err("planned token-map size is outside the supported limit".to_owned());
    }
    require_complete_coverage(&token_map.coverage)?;
    let query_key = raw_id(&selection.query_raw_key, "selection query key")?;
    let target_key = raw_id(&target.raw_key, "target raw key")?;
    if query_key != target_key {
        return Err("selection query key differs from target raw key".to_owned());
    }
    if selection.raw_key_match_count == 0
        || selection.selected_match_index >= selection.raw_key_match_count
    {
        return Err("planned source-order selection counters are invalid".to_owned());
    }
    let target_depth = usize::try_from(target.depth)
        .map_err(|_| "planned target depth does not fit this platform".to_owned())?;
    let expected_path_len = target_depth
        .checked_add(1)
        .ok_or_else(|| "planned target depth overflows path length".to_owned())?;
    if target.canonical_raw_path.is_empty()
        || target.canonical_raw_path.len() > MAX_EDIT_SCAN_DEPTH + 1
        || expected_path_len != target.canonical_raw_path.len()
    {
        return Err("planned canonical path length or depth is invalid".to_owned());
    }
    match target.canonical_raw_path.last() {
        Some(PathSegment::Field { key, .. }) if key == &target.raw_key => {}
        _ => return Err("planned canonical path does not end at the target raw key".to_owned()),
    }
    validate_spans(&target.spans, section.bytes)?;
    let expected = ScalarValue::from_raw_identity(&target.expected)?;
    let replacement = ScalarValue::from_raw_identity(replacement_raw)?;
    validate_scalar_change(&expected, &replacement)?;
    let expected_bytes = expected.encode()?;
    if expected_bytes.len() as u64 != target.expected_token_bytes
        || hash_bytes(&expected_bytes, "planned expected scalar")? != target.expected_token_sha256
    {
        return Err("planned expected scalar bytes or SHA-256 are inconsistent".to_owned());
    }
    if target.spans.value.len() as u64 != target.expected_token_bytes {
        return Err("planned expected token length differs from its value span".to_owned());
    }
    if predicted_output.save_bytes == 0
        || predicted_output.save_bytes > MAX_SOURCE_EDIT_BYTES
        || predicted_output.section_bytes > MAX_GAMESTATE_EDIT_BYTES
    {
        return Err("predicted output sizes are outside the supported limits".to_owned());
    }
    Ok(())
}

fn validate_spans(spans: &TargetSpans, section_bytes: u64) -> Result<(), String> {
    let end = usize::try_from(section_bytes)
        .map_err(|_| "planned section size does not fit this platform".to_owned())?;
    if spans.key.start >= spans.key.end
        || spans.key.end != spans.equal.start
        || spans.equal.start >= spans.equal.end
        || spans.equal.end != spans.value.start
        || spans.value.start >= spans.value.end
        || spans.value.end > end
    {
        return Err("planned key/equal/value spans are invalid or out of bounds".to_owned());
    }
    Ok(())
}

fn validate_digest(value: &str, name: &str) -> Result<(), String> {
    if value.len() != 64
        || !value.bytes().all(|byte| byte.is_ascii_hexdigit())
        || value.bytes().any(|byte| byte.is_ascii_uppercase())
    {
        return Err(format!("{name} must be exactly 64 lowercase hex digits"));
    }
    Ok(())
}

fn raw_id(raw: &RawTokenIdentity, name: &str) -> Result<u16, String> {
    match raw {
        RawTokenIdentity::Id { token } => Ok(*token),
        _ => Err(format!("{name} must be a raw 16-bit identifier")),
    }
}

fn require_source_binding_v2(
    actual: &SourceBindingV2,
    planned: &SourceBindingV2,
) -> Result<(), String> {
    if actual.bytes != planned.bytes || actual.sha256 != planned.sha256 {
        return Err(
            "source save bytes, SHA-256, container, or header differ from the edit plan".to_owned(),
        );
    }
    if actual.layout != planned.layout
        || actual.container != planned.container
        || actual.header != planned.header
        || actual.storage != planned.storage
    {
        return Err("source layout, header, or storage differ from the edit plan".to_owned());
    }
    if actual.zip != planned.zip {
        return Err("embedded ZIP manifest entries differ from the edit plan".to_owned());
    }
    Ok(())
}

fn require_source_binding(actual: &SourceBinding, planned: &SourceBinding) -> Result<(), String> {
    if actual != planned {
        return Err(
            "source save bytes, SHA-256, container, or header differ from the edit plan".to_owned(),
        );
    }
    Ok(())
}

fn require_section_binding(
    actual: &SectionBinding,
    planned: &SectionBinding,
) -> Result<(), String> {
    if actual != planned {
        return Err(
            "selected section bytes, SHA-256, or envelope metadata differ from the edit plan"
                .to_owned(),
        );
    }
    Ok(())
}

fn require_prediction(
    actual: &PredictedOutputBinding,
    planned: &PredictedOutputBinding,
) -> Result<(), String> {
    if actual != planned {
        return Err("candidate output does not match the plan's dry-run prediction".to_owned());
    }
    Ok(())
}

fn read_bounded_file(path: &Path, max_bytes: u64, name: &str) -> Result<Vec<u8>, String> {
    let file = fs::File::open(path)
        .map_err(|error| format!("cannot open {name} {}: {error}", path.display()))?;
    let hinted = file
        .metadata()
        .map_err(|error| format!("cannot inspect {name} {}: {error}", path.display()))?
        .len();
    if hinted > max_bytes {
        return Err(format!("{name} exceeds the {max_bytes}-byte limit"));
    }
    let mut bytes = Vec::with_capacity(usize::try_from(hinted).unwrap_or(0));
    file.take(max_bytes + 1)
        .read_to_end(&mut bytes)
        .map_err(|error| format!("cannot fully read {name} {}: {error}", path.display()))?;
    if bytes.len() as u64 > max_bytes {
        return Err(format!("{name} exceeds the {max_bytes}-byte limit"));
    }
    Ok(bytes)
}

fn edit_save(args: &EditArgs) -> Result<EditReport, String> {
    let output = checked_output_path(&args.input, &args.output)?;
    let source = read_bounded_source(&args.input)?;
    let envelope = analyze_source(&source)?;
    if envelope.layout() != SaveLayout::UnifiedBinaryZip {
        return Err(format!(
            "legacy set-scalar only supports unified_binary_zip saves; this save is {}; \
             use plan-scalar/apply-plan instead",
            envelope.layout().name()
        ));
    }
    let source_sha256 = hash_bytes(&source, "source save")?;
    let (section, _) = bind_section(&source, &envelope, args.section)?;
    let found = unique_expected_match(args.section.name(), &section, args)?;
    let replacement = args.replacement.encode()?;
    let mut edited_section =
        Vec::with_capacity(section.len() - (found.end - found.start) + replacement.len());
    edited_section.extend_from_slice(&section[..found.start]);
    edited_section.extend_from_slice(&replacement);
    edited_section.extend_from_slice(&section[found.end..]);

    let edited = envelope::rebuild_save(
        &source,
        &envelope,
        args.section.logical(),
        &edited_section,
        edit_limits(),
    )
    .map_err(|error| error.to_string())?;
    let output_envelope = analyze_source(&edited)?;
    let (checked_section, _) = bind_section(&edited, &output_envelope, args.section)?;
    if checked_section != edited_section {
        return Err("self-check section differs after save rebuild".to_owned());
    }
    verify_replacement(
        args.section.name(),
        &checked_section,
        args.key,
        &args.replacement,
    )?;
    let output_sha256 = hash_bytes(&edited, "edited save")?;
    write_new_file(&output, &edited)?;

    // Metadata offsets stay in whole-file coordinates for compatibility with
    // the original report; a ZIP gamestate has no meaningful file offset.
    let (coordinate_space, base) = match args.section {
        Section::Metadata => (
            "save_file",
            envelope
                .inline_span(SaveSection::Metadata)
                .map_or(0, |span| span.start),
        ),
        Section::Gamestate => ("gamestate_uncompressed", 0),
    };
    let scalar_start = base + found.start;
    Ok(EditReport {
        schema: EDIT_SCHEMA,
        section: args.section,
        source_bytes: source.len() as u64,
        source_sha256,
        output_bytes: edited.len() as u64,
        output_sha256,
        key: format!("0x{:04x}", args.key),
        old: found.value.report(),
        new: args.replacement.report(),
        span: SpanReport {
            coordinate_space,
            source_start: scalar_start as u64,
            source_end: (scalar_start + (found.end - found.start)) as u64,
            output_start: scalar_start as u64,
            output_end: (scalar_start + replacement.len()) as u64,
        },
        section_scan_complete: true,
        gamestate_integrity_checked: true,
        complete: true,
    })
}

fn unique_expected_match(section: &str, data: &[u8], args: &EditArgs) -> Result<Match, String> {
    let matches = scan_binary_section(section, data, args.key)?;
    let found = match matches.as_slice() {
        [] => return Err(format!("target key 0x{:04x} was not found", args.key)),
        [found] => found.clone(),
        many => {
            return Err(format!(
                "target key 0x{:04x} matched {} fields; exactly one is required",
                args.key,
                many.len()
            ));
        }
    };
    if found.value != args.expected {
        return Err(format!(
            "expected {} does not match stored {} value",
            args.expected.kind(),
            found.value.kind()
        ));
    }
    let encoded = data
        .get(found.start..found.end)
        .ok_or_else(|| "selected scalar span lies outside the section".to_owned())?;
    if found.value.encode()? != encoded {
        return Err(
            "selected scalar has a non-canonical encoding unsupported by set-scalar".to_owned(),
        );
    }
    Ok(found)
}

fn verify_replacement(
    section: &str,
    data: &[u8],
    key: u16,
    replacement: &ScalarValue,
) -> Result<(), String> {
    let checked = scan_binary_section(section, data, key)?;
    if checked.len() != 1 || checked[0].value != *replacement {
        return Err("self-check did not find exactly the requested replacement".to_owned());
    }
    Ok(())
}

const MAX_SOURCE_EDIT_BYTES: u64 = 128 * 1024 * 1024;
const MAX_GAMESTATE_EDIT_BYTES: u64 = 64 * 1024 * 1024;
const MAX_PLAN_BYTES: u64 = 1024 * 1024;
const TOKEN_MAP_MAX_BYTES: u64 = 16 * 1024 * 1024;
const TOKEN_MAP_MAX_LINE_BYTES: usize = 4 * 1024;
const TOKEN_NAME_MAX_BYTES: usize = 256;

fn edit_zip_limits() -> ZipRebuildLimits {
    ZipRebuildLimits {
        max_entries: 32,
        max_total_compressed_bytes: MAX_SOURCE_EDIT_BYTES,
        max_total_uncompressed_bytes: MAX_GAMESTATE_EDIT_BYTES,
    }
}

fn read_bounded_source(path: &Path) -> Result<Vec<u8>, String> {
    let file =
        fs::File::open(path).map_err(|error| format!("cannot open {}: {error}", path.display()))?;
    let hinted_len = file
        .metadata()
        .map_err(|error| format!("cannot inspect {}: {error}", path.display()))?
        .len();
    if hinted_len > MAX_SOURCE_EDIT_BYTES {
        return Err(format!(
            "source save exceeds the {MAX_SOURCE_EDIT_BYTES}-byte edit limit"
        ));
    }
    let mut source = Vec::with_capacity(usize::try_from(hinted_len).unwrap_or(0));
    file.take(MAX_SOURCE_EDIT_BYTES + 1)
        .read_to_end(&mut source)
        .map_err(|error| format!("cannot fully read {}: {error}", path.display()))?;
    if source.len() as u64 > MAX_SOURCE_EDIT_BYTES {
        return Err(format!(
            "source save exceeds the {MAX_SOURCE_EDIT_BYTES}-byte edit limit"
        ));
    }
    Ok(source)
}

fn checked_output_path(input: &Path, output: &Path) -> Result<PathBuf, String> {
    let input = fs::canonicalize(input)
        .map_err(|error| format!("cannot resolve input {}: {error}", input.display()))?;
    let file_name = output
        .file_name()
        .ok_or_else(|| "output must name a file".to_owned())?;
    let parent = output.parent().unwrap_or_else(|| Path::new("."));
    let parent = fs::canonicalize(parent).map_err(|error| {
        format!(
            "cannot resolve output directory {}: {error}",
            parent.display()
        )
    })?;
    let output = parent.join(file_name);
    if input == output {
        return Err("input and output resolve to the same file".to_owned());
    }
    if fs::symlink_metadata(&output).is_ok() {
        return Err(format!("output already exists: {}", output.display()));
    }
    Ok(output)
}

struct TempOutput {
    path: PathBuf,
    armed: bool,
}

impl Drop for TempOutput {
    fn drop(&mut self) {
        if self.armed {
            let _ = fs::remove_file(&self.path);
        }
    }
}

fn write_new_file(output: &Path, data: &[u8]) -> Result<(), String> {
    let parent = output
        .parent()
        .ok_or_else(|| "output has no parent directory".to_owned())?;
    let file_name = output.file_name().and_then(OsStr::to_str).unwrap_or("save");
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    let mut created = None;
    for attempt in 0..100u32 {
        let path = parent.join(format!(
            ".{file_name}.ck3-index-edit-{}-{nonce}-{attempt}.tmp",
            process::id()
        ));
        match OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)
        {
            Ok(file) => {
                created = Some((path, file));
                break;
            }
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(format!("cannot create temporary output: {error}")),
        }
    }
    let (path, mut file) = created.ok_or_else(|| "cannot reserve a temporary output".to_owned())?;
    let mut guard = TempOutput { path, armed: true };
    file.write_all(data)
        .and_then(|_| file.flush())
        .and_then(|_| file.sync_all())
        .map_err(|error| format!("cannot durably write temporary output: {error}"))?;
    file.seek(SeekFrom::Start(0))
        .map_err(|error| format!("cannot rewind temporary output: {error}"))?;
    let read_limit = u64::try_from(data.len())
        .unwrap_or(u64::MAX)
        .checked_add(1)
        .unwrap_or(u64::MAX);
    let mut written = Vec::with_capacity(data.len());
    (&mut file)
        .take(read_limit)
        .read_to_end(&mut written)
        .map_err(|error| format!("cannot reread temporary output handle: {error}"))?;
    if written != data {
        return Err("temporary output differs from the verified edit buffer".to_owned());
    }
    // Keep the verified handle alive until the no-clobber hard link succeeds.
    // The containing directory is part of the trusted local execution boundary.
    fs::hard_link(&guard.path, output).map_err(|error| {
        format!("cannot publish output without replacing an existing path: {error}")
    })?;
    drop(file);
    if fs::remove_file(&guard.path).is_ok() {
        guard.armed = false;
    }
    Ok(())
}
