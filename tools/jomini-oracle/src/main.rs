#![forbid(unsafe_code)]

use jomini::{Scalar, TextTape, TextToken};
use std::env;
use std::ffi::{OsStr, OsString};
use std::fs;
use std::io::{self, BufWriter, Read, Write};
use std::path::PathBuf;
use std::process;

const ORACLE_SCHEMA: &str = "ck3-index-jomini-text-tape/v1";
const JOMINI_VERSION: &str = "0.35.0";

fn main() {
    if let Err(error) = run() {
        eprintln!("jomini-oracle: {error}");
        process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let mut args = env::args_os().skip(1);
    let input_arg = args.next();
    if args.next().is_some() {
        return Err("expected at most one input path; use '-' or no argument for stdin".to_owned());
    }

    if input_arg.as_deref() == Some(OsStr::new("--help")) {
        print_help();
        return Ok(());
    }
    if input_arg.as_deref() == Some(OsStr::new("--version")) {
        println!("ck3-index-jomini-oracle 0.0.0 (jomini {JOMINI_VERSION})");
        return Ok(());
    }

    let input = read_input(input_arg)?;
    let tape =
        TextTape::from_slice(input.as_slice()).map_err(|error| format!("parse failed: {error}"))?;

    let stdout = io::stdout();
    let mut out = BufWriter::new(stdout.lock());
    write_document(&mut out, &tape).map_err(|error| format!("write failed: {error}"))?;
    out.flush()
        .map_err(|error| format!("flush failed: {error}"))
}

fn print_help() {
    println!(
        "ck3-index-jomini-oracle [FILE|-]\n\
         \n\
         Parse Clausewitz plaintext with jomini {JOMINI_VERSION} and write a stable\n\
         JSON representation of its TextTape tokens. With no FILE, or with '-',\n\
         input is read from stdin."
    );
}

fn read_input(input_arg: Option<OsString>) -> Result<Vec<u8>, String> {
    match input_arg {
        None => read_stdin(),
        Some(arg) if arg.as_os_str() == OsStr::new("-") => read_stdin(),
        Some(arg) => {
            let path = PathBuf::from(arg);
            fs::read(&path).map_err(|error| format!("cannot read {}: {error}", path.display()))
        }
    }
}

fn read_stdin() -> Result<Vec<u8>, String> {
    let mut data = Vec::new();
    io::stdin()
        .read_to_end(&mut data)
        .map_err(|error| format!("cannot read stdin: {error}"))?;
    Ok(data)
}

fn write_document<W: Write>(out: &mut W, tape: &TextTape<'_>) -> io::Result<()> {
    writeln!(out, "{{")?;
    writeln!(out, "  \"schema\": \"{ORACLE_SCHEMA}\",")?;
    writeln!(out, "  \"jomini\": \"{JOMINI_VERSION}\",")?;
    writeln!(out, "  \"utf8_bom\": {},", tape.utf8_bom())?;
    writeln!(out, "  \"tokens\": [")?;

    let tokens = tape.tokens();
    for (index, token) in tokens.iter().enumerate() {
        write!(out, "    ")?;
        write_token(out, index, token)?;
        if index + 1 != tokens.len() {
            write!(out, ",")?;
        }
        writeln!(out)?;
    }

    writeln!(out, "  ]")?;
    writeln!(out, "}}")
}

fn write_token<W: Write>(out: &mut W, index: usize, token: &TextToken<'_>) -> io::Result<()> {
    match token {
        TextToken::Array { end, mixed } => write!(
            out,
            "{{\"index\":{index},\"kind\":\"array\",\"end\":{end},\"mixed\":{mixed}}}"
        ),
        TextToken::Object { end, mixed } => write!(
            out,
            "{{\"index\":{index},\"kind\":\"object\",\"end\":{end},\"mixed\":{mixed}}}"
        ),
        TextToken::MixedContainer => {
            write!(out, "{{\"index\":{index},\"kind\":\"mixed_container\"}}")
        }
        TextToken::Unquoted(scalar) => write_scalar_token(out, index, "unquoted", *scalar),
        TextToken::Quoted(scalar) => write_scalar_token(out, index, "quoted", *scalar),
        TextToken::Parameter(scalar) => write_scalar_token(out, index, "parameter", *scalar),
        TextToken::UndefinedParameter(scalar) => {
            write_scalar_token(out, index, "undefined_parameter", *scalar)
        }
        TextToken::Operator(operator) => {
            write!(out, "{{\"index\":{index},\"kind\":\"operator\",\"name\":")?;
            write_json_string(out, operator.name())?;
            write!(out, ",\"symbol\":")?;
            write_json_string(out, operator.symbol())?;
            write!(out, "}}")
        }
        TextToken::End(start) => write!(
            out,
            "{{\"index\":{index},\"kind\":\"end\",\"start\":{start}}}"
        ),
        TextToken::Header(scalar) => write_scalar_token(out, index, "header", *scalar),
    }
}

fn write_scalar_token<W: Write>(
    out: &mut W,
    index: usize,
    kind: &str,
    scalar: Scalar<'_>,
) -> io::Result<()> {
    write!(out, "{{\"index\":{index},\"kind\":")?;
    write_json_string(out, kind)?;
    write_scalar_fields(out, scalar)?;
    write!(out, "}}")
}

fn write_scalar_fields<W: Write>(out: &mut W, scalar: Scalar<'_>) -> io::Result<()> {
    let bytes = scalar.as_bytes();
    write!(out, ",\"bytes_hex\":\"")?;
    for byte in bytes {
        write!(out, "{byte:02x}")?;
    }
    write!(out, "\",\"utf8\":")?;
    match std::str::from_utf8(bytes) {
        Ok(text) => write_json_string(out, text),
        Err(_) => write!(out, "null"),
    }
}

fn write_json_string<W: Write>(out: &mut W, value: &str) -> io::Result<()> {
    write!(out, "\"")?;
    for character in value.chars() {
        match character {
            '"' => write!(out, "\\\"")?,
            '\\' => write!(out, "\\\\")?,
            '\u{08}' => write!(out, "\\b")?,
            '\u{0c}' => write!(out, "\\f")?,
            '\n' => write!(out, "\\n")?,
            '\r' => write!(out, "\\r")?,
            '\t' => write!(out, "\\t")?,
            '\u{00}'..='\u{1f}' => write!(out, "\\u{:04x}", character as u32)?,
            _ => write!(out, "{character}")?,
        }
    }
    write!(out, "\"")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scalar_pair_has_stable_json() {
        let tape = TextTape::from_slice(b"foo=bar").expect("fixture should parse");
        let mut output = Vec::new();
        write_document(&mut output, &tape).expect("document should render");

        let expected = concat!(
            "{\n",
            "  \"schema\": \"ck3-index-jomini-text-tape/v1\",\n",
            "  \"jomini\": \"0.35.0\",\n",
            "  \"utf8_bom\": false,\n",
            "  \"tokens\": [\n",
            "    {\"index\":0,\"kind\":\"unquoted\",\"bytes_hex\":\"666f6f\",\"utf8\":\"foo\"},\n",
            "    {\"index\":1,\"kind\":\"unquoted\",\"bytes_hex\":\"626172\",\"utf8\":\"bar\"}\n",
            "  ]\n",
            "}\n"
        );
        assert_eq!(String::from_utf8(output).unwrap(), expected);
    }

    #[test]
    fn json_strings_are_escaped_without_changing_unicode() {
        let mut output = Vec::new();
        write_json_string(&mut output, "quote=\" slash=\\ line=\n 检索")
            .expect("string should render");
        assert_eq!(
            String::from_utf8(output).unwrap(),
            "\"quote=\\\" slash=\\\\ line=\\n 检索\""
        );
    }

    #[test]
    fn invalid_utf8_keeps_exact_bytes() {
        let mut output = Vec::new();
        write_scalar_fields(&mut output, Scalar::new(&[0xff])).expect("scalar should render");
        assert_eq!(
            String::from_utf8(output).unwrap(),
            ",\"bytes_hex\":\"ff\",\"utf8\":null"
        );
    }

    #[test]
    fn non_equal_operator_is_named_and_symbolized() {
        let tape = TextTape::from_slice(b"limit >= 3").expect("fixture should parse");
        let mut output = Vec::new();
        write_document(&mut output, &tape).expect("document should render");
        let output = String::from_utf8(output).unwrap();
        assert!(output.contains("\"name\":\"GREATER_THAN_EQUAL\""));
        assert!(output.contains("\"symbol\":\">=\""));
    }

    #[test]
    fn utf8_bom_is_reported() {
        let tape = TextTape::from_slice(b"\xef\xbb\xbffoo=bar").expect("fixture should parse");
        let mut output = Vec::new();
        write_document(&mut output, &tape).expect("document should render");
        assert!(
            String::from_utf8(output)
                .unwrap()
                .contains("\"utf8_bom\": true")
        );
    }
}
