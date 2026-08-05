use std::fs;
use std::io::Write;
use std::path::PathBuf;
use std::process::{Command, Output, Stdio};

fn oracle() -> Command {
    Command::new(env!("CARGO_BIN_EXE_ck3-index-jomini-oracle"))
}

fn run_stdin(input: &[u8]) -> Output {
    let mut child = oracle()
        .arg("-")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("oracle should start");
    child
        .stdin
        .take()
        .expect("stdin should be piped")
        .write_all(input)
        .expect("stdin should be writable");
    child.wait_with_output().expect("oracle should finish")
}

#[test]
fn stdin_and_file_produce_identical_documents() {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("fixtures")
        .join("common")
        .join("structure.clausewitz.txt");
    let input = fs::read(&fixture).expect("fixture should be readable");

    let stdin_output = run_stdin(&input);
    assert!(stdin_output.status.success(), "{stdin_output:?}");

    let file_output = oracle()
        .arg(&fixture)
        .stdin(Stdio::null())
        .output()
        .expect("oracle should read a file");
    assert!(file_output.status.success(), "{file_output:?}");
    assert_eq!(stdin_output.stdout, file_output.stdout);
}

#[test]
fn output_identifies_the_pinned_dependency_and_schema() {
    let output = run_stdin(b"foo=bar");
    assert!(output.status.success(), "{output:?}");
    let stdout = String::from_utf8(output.stdout).expect("output should be UTF-8 JSON");
    assert!(stdout.contains("\"schema\": \"ck3-index-jomini-text-tape/v1\""));
    assert!(stdout.contains("\"jomini\": \"0.35.0\""));
}

#[test]
fn common_fixtures_match_reviewed_jomini_goldens() {
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    for name in ["structure", "operators"] {
        let input = manifest_dir
            .join("fixtures")
            .join("common")
            .join(format!("{name}.clausewitz.txt"));
        let expected = manifest_dir
            .join("fixtures")
            .join("golden")
            .join(format!("{name}.json"));

        let output = oracle()
            .arg(&input)
            .stdin(Stdio::null())
            .output()
            .expect("oracle should read a common fixture");
        assert!(output.status.success(), "{name}: {output:?}");
        let expected = canonical_lf(fs::read(expected).expect("golden should be readable"));
        assert_eq!(output.stdout, expected, "{name}");
    }
}

fn canonical_lf(input: Vec<u8>) -> Vec<u8> {
    let mut output = Vec::with_capacity(input.len());
    let mut index = 0usize;
    while index < input.len() {
        if input.get(index..index + 2) == Some(b"\r\n") {
            output.push(b'\n');
            index += 2;
        } else {
            output.push(input[index]);
            index += 1;
        }
    }
    output
}
