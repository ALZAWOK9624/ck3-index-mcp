# Verification status

Verified status:

- Jomini API shape was checked against the published 0.35.0 documentation.
- The dependency is exactly pinned and has `default-features = false`.
- Cargo generated and resolved the committed `Cargo.lock` to Jomini 0.35.0.
- Rust 1.85 was intentionally tried and rejected: Jomini itself uses let-chains
  stabilized in Rust 1.88 and pointer APIs stabilized in Rust 1.87. The package
  therefore declares Rust 1.88 as its actual minimum.
- Rust 1.88.0 compiled the pinned crate and oracle successfully.
- The current stable Rust toolchain also passes `cargo check --all-targets`.
- Five unit tests and three CLI integration tests pass, including byte-for-byte
  verification of both committed goldens.
- `cargo fmt --check` passes.
- `cargo clippy --locked -- -D warnings` passes.
- The committed goldens were captured from the successfully compiled binary;
  none were inferred or hand-authored as upstream output.

Reproduce the verification with:

```text
cargo fmt --manifest-path tools/jomini-oracle/Cargo.toml -- --check
cargo test --manifest-path tools/jomini-oracle/Cargo.toml --locked
cargo clippy --manifest-path tools/jomini-oracle/Cargo.toml --locked -- -D warnings
```

The verification host lacked the MSVC linker, so the successful Windows run used
the official Rust 1.88 GNU toolchain. MinGW cannot reliably link files through a
non-ASCII path; its Cargo target and explicit sysroot were exposed through ASCII
paths for the test. This is a host-toolchain constraint, not a runtime dependency
or production release requirement.

## Exact PowerShell reproduction on this host

The `D:\BOT-WO~1` junction is the host's existing ASCII alias for the workspace.
Both the explicit sysroot and Cargo target must stay on genuinely ASCII paths;
pointing either variable at the ordinary Chinese path reproduces the MinGW linker
failure. Run the following block from a PowerShell session whose account can
write `C:\tmp`:

```powershell
$ErrorActionPreference = 'Stop'
$oracleRepo = 'D:\BOT-WO~1\bot-tmp\ck3-index-agent-phase1'
$rustPortable = 'D:\BOT-WO~1\bot-tmp\rust-portable'
$rustToolchain = Join-Path $rustPortable 'rustup\toolchains\1.88.0-x86_64-pc-windows-gnu'
$oracleTarget = 'C:\tmp\jomini-oracle-target'
$oracleManifest = 'tools\jomini-oracle\Cargo.toml'

$env:CARGO_HOME = Join-Path $rustPortable 'cargo'
$env:RUSTUP_HOME = Join-Path $rustPortable 'rustup'
$env:CARGO_TARGET_DIR = $oracleTarget
$env:RUSTFLAGS = "--sysroot=$rustToolchain"

New-Item -ItemType Directory -Force -Path $oracleTarget | Out-Null
Set-Location -LiteralPath $oracleRepo
$oracleCargo = Join-Path $env:CARGO_HOME 'bin\cargo.exe'

& $oracleCargo +1.88.0-x86_64-pc-windows-gnu fmt --manifest-path $oracleManifest -- --check
if ($LASTEXITCODE -ne 0) { throw 'jomini oracle rustfmt failed' }

& $oracleCargo +1.88.0-x86_64-pc-windows-gnu test --manifest-path $oracleManifest --locked
if ($LASTEXITCODE -ne 0) { throw 'jomini oracle tests failed' }

& $oracleCargo +1.88.0-x86_64-pc-windows-gnu clippy --manifest-path $oracleManifest --locked -- -D warnings
if ($LASTEXITCODE -ne 0) { throw 'jomini oracle clippy failed' }
```

The test command includes the CLI fixture tests and byte-for-byte comparison of
both checked-in Jomini goldens. It does not invoke or modify the Go release build.
