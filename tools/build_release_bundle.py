#!/usr/bin/env python3
"""Validate and build a deterministic ck3-index plugin release archive.

The platform release scripts assemble a staging directory first. This helper
then adds the licenses for Go modules that are actually linked into the
ck3-index binary, rejects common private/build artifacts, writes a
content-only release manifest and SHA256SUMS, and emits a deterministic ZIP.

It deliberately does not choose a license for ck3-index. Public release is
blocked until the repository contains a project LICENSE. The explicit
--allow-missing-project-license switch exists only for clearly marked local
release candidates.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile


VERSION_RE = re.compile(r"^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$")
LICENSE_RE = re.compile(r"^(?:LICENSE|COPYING|NOTICE)(?:[._-].*)?$", re.IGNORECASE)
TEXT_SUFFIXES = {
    "",
    ".json",
    ".md",
    ".ps1",
    ".sh",
    ".txt",
    ".yml",
    ".yaml",
}
FORBIDDEN_COMPONENTS = {
    ".git",
    ".gocache",
    "__pycache__",
    "artifacts",
    "cache",
    "external",
    "tmp",
    "vendor",
}
FORBIDDEN_SUFFIXES = {
    ".7z",
    ".db",
    ".log",
    ".rar",
    ".sqlite",
    ".sqlite-shm",
    ".sqlite-wal",
    ".tar",
    ".tmp",
    ".zip",
}
FORBIDDEN_NAMES = {".DS_Store", "Thumbs.db", "desktop.ini"}
FORBIDDEN_NAMES_LOWER = {value.lower() for value in FORBIDDEN_NAMES}
ALLOWED_TOP_LEVEL = {
    ".codex-plugin",
    ".mcp.json",
    "LICENSE",
    "RELEASE_MANIFEST.json",
    "SHA256SUMS",
    "THIRD_PARTY_NOTICES.md",
    "bin",
    "config",
    "scripts",
    "sidecar",
    "skills",
    "third_party",
}


class ReleaseError(RuntimeError):
    """A stable release-gate failure."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_json(path: Path) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ReleaseError(f"invalid JSON file {path.name}: {exc}") from exc
    if not isinstance(value, dict):
        raise ReleaseError(f"JSON root must be an object: {path.name}")
    return value


def write_text(path: Path, content: str) -> None:
    path.write_text(content.replace("\r\n", "\n"), encoding="utf-8", newline="\n")


def relative_files(root: Path) -> list[Path]:
    files: list[Path] = []
    for path in root.rglob("*"):
        if path.is_symlink():
            raise ReleaseError(f"release stage contains a symlink: {path.relative_to(root)}")
        if path.is_file():
            files.append(path)
    return sorted(files, key=lambda item: item.relative_to(root).as_posix())


def decode_json_stream(raw: str) -> list[dict]:
    decoder = json.JSONDecoder()
    values: list[dict] = []
    offset = 0
    while offset < len(raw):
        while offset < len(raw) and raw[offset].isspace():
            offset += 1
        if offset >= len(raw):
            break
        value, offset = decoder.raw_decode(raw, offset)
        if isinstance(value, dict):
            values.append(value)
    return values


def effective_modules(repo: Path) -> list[tuple[str, str, Path]]:
    try:
        completed = subprocess.run(
            # The release builder is deliberately usable from an exported
            # source tree, not only from a Git checkout.  Without this flag
            # newer Go versions try to stamp VCS state and reject that valid
            # non-repository layout.
            ["go", "list", "-buildvcs=false", "-deps", "-json", "."],
            cwd=repo,
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        detail = getattr(exc, "stderr", "") or str(exc)
        raise ReleaseError(f"GO_DEPENDENCY_ENUMERATION_FAILED: {detail.strip()}") from exc

    modules: dict[tuple[str, str], Path] = {}
    for package in decode_json_stream(completed.stdout):
        module = package.get("Module")
        if not isinstance(module, dict) or module.get("Main"):
            continue
        effective = module.get("Replace")
        if not isinstance(effective, dict):
            effective = module
        path = str(effective.get("Path") or "").strip()
        version = str(effective.get("Version") or "").strip()
        directory = str(effective.get("Dir") or "").strip()
        if not path or not version or not directory:
            raise ReleaseError(f"unsupported local or incomplete module replacement: {module!r}")
        key = (path, version)
        resolved = Path(directory).resolve()
        previous = modules.get(key)
        if previous is not None and previous != resolved:
            raise ReleaseError(f"module resolves to multiple directories: {path}@{version}")
        modules[key] = resolved
    return [(path, version, modules[(path, version)]) for path, version in sorted(modules)]


def safe_module_directory(module: str, version: str) -> str:
    value = f"{module}@{version}".replace("/", "__")
    value = re.sub(r"[^0-9A-Za-z._@+-]", "-", value)
    if not value or value in {".", ".."}:
        raise ReleaseError(f"cannot derive license directory for {module}@{version}")
    return value


def collect_go_module_licenses(repo: Path, stage: Path) -> list[dict[str, object]]:
    destination = stage / "third_party" / "go-modules"
    if destination.exists():
        shutil.rmtree(destination)
    destination.mkdir(parents=True)

    result: list[dict[str, object]] = []
    for module, version, directory in effective_modules(repo):
        if not directory.is_dir():
            raise ReleaseError(f"module directory is missing: {module}@{version}")
        licenses = sorted(
            path for path in directory.iterdir() if path.is_file() and LICENSE_RE.match(path.name)
        )
        if not licenses:
            raise ReleaseError(f"GO_MODULE_LICENSE_MISSING: {module}@{version}")
        module_dir = destination / safe_module_directory(module, version)
        module_dir.mkdir()
        copied: list[str] = []
        for source in licenses:
            target = module_dir / source.name
            shutil.copyfile(source, target)
            copied.append(target.relative_to(stage).as_posix())
        result.append({"module": module, "version": version, "license_files": copied})
    return result


def project_license(repo: Path) -> Path | None:
    for name in ("LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING", "COPYING.txt"):
        candidate = repo / name
        if candidate.is_file():
            return candidate
    return None


def write_third_party_notices(stage: Path, modules: list[dict[str, object]]) -> None:
    lines = [
        "# Third-Party Notices",
        "",
        "This distribution includes third-party software. Each component remains",
        "subject to its own license; the corresponding license text is bundled at",
        "the relative path listed below.",
        "",
        "## WhiteboxTools Open Core",
        "",
        "- Version: 2.4.0",
        "- Project: https://github.com/jblindsay/whitebox-tools",
        "- License: MIT",
        "- License text: `third_party/WHITEBOXTOOLS_LICENSE.txt`",
        "",
        "## Go modules linked into ck3-index",
        "",
        "| Module | Version | License files |",
        "|---|---|---|",
    ]
    for module in modules:
        files = ", ".join(f"`{value}`" for value in module["license_files"])
        lines.append(f"| `{module['module']}` | `{module['version']}` | {files} |")
    lines.extend(
        [
            "",
            "This notice is generated from `go list -deps` during the release build.",
            "Modules listed in go.mod but not linked into the release binary are not",
            "included in this binary-distribution inventory.",
            "",
        ]
    )
    write_text(stage / "THIRD_PARTY_NOTICES.md", "\n".join(lines))


def validate_platform_contract(stage: Path, platform: str, version: str) -> tuple[Path, Path, dict]:
    manifest_path = stage / ".codex-plugin" / "plugin.json"
    mcp_path = stage / ".mcp.json"
    settings_path = stage / "config" / "settings.json"
    wbt_manifest_path = stage / "third_party" / "whitebox-tools-v2.4.0.json"
    wbt_license_path = stage / "third_party" / "WHITEBOXTOOLS_LICENSE.txt"
    for required in (
        manifest_path,
        mcp_path,
        settings_path,
        wbt_manifest_path,
        wbt_license_path,
        stage / "skills" / "ck3-coding" / "SKILL.md",
    ):
        if not required.is_file():
            raise ReleaseError(f"required release file is missing: {required.relative_to(stage)}")

    manifest = read_json(manifest_path)
    if manifest.get("version") != version:
        raise ReleaseError("plugin manifest version does not match VERSION")
    settings = read_json(settings_path)
    if settings.get("version") != 1 or settings.get("config_path") != "":
        raise ReleaseError(
            "PORTABLE_CONFIG_REQUIRED: staged config/settings.json must have version=1 and an empty config_path"
        )

    mcp = read_json(mcp_path)
    server = (mcp.get("mcpServers") or {}).get("ck3_index")
    if not isinstance(server, dict) or server.get("cwd") != ".":
        raise ReleaseError(".mcp.json must define ck3_index with cwd='.'")

    if platform == "windows-x64":
        expected_binary = stage / "bin" / f"ck3-index-v{version}.exe"
        sidecar = stage / "sidecar" / "whitebox_tools.exe"
        launcher = stage / "scripts" / "start-ck3-index.ps1"
        if server.get("command") != r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe":
            raise ReleaseError("Windows .mcp.json does not use the pinned PowerShell launcher")
    elif platform == "linux-x64":
        expected_binary = stage / "bin" / f"ck3-index-v{version}"
        sidecar = stage / "sidecar" / "whitebox_tools"
        launcher = stage / "scripts" / "start-ck3-index.sh"
        if server.get("command") != "/bin/sh":
            raise ReleaseError("Linux .mcp.json does not use /bin/sh")
    else:
        raise ReleaseError(f"unsupported platform: {platform}")

    if not expected_binary.is_file():
        raise ReleaseError(f"versioned ck3-index binary is missing: {expected_binary.name}")
    if not sidecar.is_file():
        raise ReleaseError(f"WhiteboxTools executable is missing: {sidecar.relative_to(stage)}")
    if not launcher.is_file():
        raise ReleaseError(f"platform launcher is missing: {launcher.relative_to(stage)}")

    release_binaries = [
        path
        for path in (stage / "bin").iterdir()
        if path.is_file() and path.name.startswith("ck3-index-v")
    ]
    if release_binaries != [expected_binary]:
        names = ", ".join(sorted(path.name for path in release_binaries))
        raise ReleaseError(f"STALE_RELEASE_BINARY: expected only {expected_binary.name}, found {names}")

    wbt_manifest = read_json(wbt_manifest_path)
    platform_entry = (wbt_manifest.get("platforms") or {}).get(platform)
    if (
        wbt_manifest.get("version") != "2.4.0"
        or not isinstance(platform_entry, dict)
        or not re.fullmatch(r"[0-9a-f]{64}", str(platform_entry.get("binary_sha256") or ""))
    ):
        raise ReleaseError("WhiteboxTools manifest lacks the expected platform hash")
    if sha256_file(sidecar) != platform_entry["binary_sha256"]:
        raise ReleaseError("WhiteboxTools executable SHA-256 does not match the pinned manifest")
    return expected_binary, sidecar, wbt_manifest


def validate_stage_paths(stage: Path) -> None:
    for path in relative_files(stage):
        relative = path.relative_to(stage)
        parts_lower = {part.lower() for part in relative.parts}
        forbidden = parts_lower & FORBIDDEN_COMPONENTS
        if forbidden:
            raise ReleaseError(f"FORBIDDEN_RELEASE_PATH: {relative.as_posix()}")
        lower_name = path.name.lower()
        if lower_name in FORBIDDEN_NAMES_LOWER:
            raise ReleaseError(f"FORBIDDEN_RELEASE_FILE: {relative.as_posix()}")
        if any(lower_name.endswith(suffix) for suffix in FORBIDDEN_SUFFIXES):
            raise ReleaseError(f"FORBIDDEN_RELEASE_FILE: {relative.as_posix()}")
        top = relative.parts[0]
        if top not in ALLOWED_TOP_LEVEL and not top.startswith("LICENSE"):
            raise ReleaseError(f"unexpected release top-level entry: {top}")


def local_path_spellings(path: Path) -> set[str]:
    spellings = {str(path), os.path.abspath(path)}
    if os.name == "nt":
        try:
            import ctypes

            buffer = ctypes.create_unicode_buffer(32768)
            length = ctypes.windll.kernel32.GetShortPathNameW(str(path), buffer, len(buffer))
            if 0 < length < len(buffer):
                spellings.add(buffer.value)
        except (AttributeError, OSError):
            pass
    return {value for value in spellings if value}


def scan_private_path_leaks(repo: Path, stage: Path) -> None:
    needles: set[bytes] = set()
    candidates = {repo.resolve(), Path.home().resolve()}
    for variable in ("HOME", "USERPROFILE"):
        value = os.environ.get(variable)
        if value:
            candidates.add(Path(value))
    for candidate in candidates:
        for spelling in local_path_spellings(candidate):
            for value in (spelling, spelling.replace("\\", "/")):
                if value:
                    needles.add(value.encode("utf-8"))
                    needles.add(value.lower().encode("utf-8"))
    for path in relative_files(stage):
        if path.suffix.lower() not in TEXT_SUFFIXES:
            continue
        data = path.read_bytes()
        lowered = data.lower()
        for needle in needles:
            if needle in data or needle in lowered:
                raise ReleaseError(
                    f"PRIVATE_PATH_LEAK: {path.relative_to(stage).as_posix()} contains a local path"
                )


def write_release_metadata(
    stage: Path,
    version: str,
    platform: str,
    modules: list[dict[str, object]],
    release_ready: bool,
    blockers: list[str],
    binary: Path,
    sidecar: Path,
) -> None:
    manifest_path = stage / "RELEASE_MANIFEST.json"
    sums_path = stage / "SHA256SUMS"
    manifest_path.unlink(missing_ok=True)
    sums_path.unlink(missing_ok=True)

    inventory = []
    for path in relative_files(stage):
        relative = path.relative_to(stage).as_posix()
        inventory.append(
            {
                "path": relative,
                "size": path.stat().st_size,
                "sha256": sha256_file(path),
                "archive_mode": format(zip_mode(PurePosixPath(relative), platform), "04o"),
            }
        )
    manifest = {
        "schema_version": 1,
        "product": "ck3-index Codex plugin",
        "version": version,
        "platform": platform,
        "release_ready": release_ready,
        "blockers": blockers,
        "binary": binary.relative_to(stage).as_posix(),
        "whitebox_tools_binary": sidecar.relative_to(stage).as_posix(),
        "go_modules": [
            {"module": item["module"], "version": item["version"]} for item in modules
        ],
        "inventory_excludes": ["RELEASE_MANIFEST.json", "SHA256SUMS"],
        "files": inventory,
    }
    write_text(
        manifest_path,
        json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
    )

    lines = []
    for path in relative_files(stage):
        if path == sums_path:
            continue
        relative = path.relative_to(stage).as_posix()
        lines.append(f"{sha256_file(path)}  {relative}")
    write_text(sums_path, "\n".join(lines) + "\n")


def zip_mode(relative: PurePosixPath, platform: str) -> int:
    value = relative.as_posix()
    if value.endswith(".sh"):
        return 0o755
    if platform == "linux-x64" and (
        value.startswith("bin/ck3-index-v")
        or value in {"sidecar/whitebox_runner", "sidecar/whitebox_tools"}
        or (
            value.startswith("sidecar/plugins/")
            and PurePosixPath(value).suffix == ""
        )
    ):
        return 0o755
    return 0o644


def create_deterministic_zip(stage: Path, output: Path, platform: str) -> str:
    stage = stage.resolve()
    output = output.resolve()
    if stage == output or stage in output.parents:
        raise ReleaseError("release archive must be outside the staging directory")
    output.parent.mkdir(parents=True, exist_ok=True)
    handle, temporary_name = tempfile.mkstemp(
        prefix=output.name + ".", suffix=".tmp", dir=output.parent
    )
    os.close(handle)
    temporary = Path(temporary_name)
    try:
        with zipfile.ZipFile(
            temporary,
            mode="w",
            compression=zipfile.ZIP_DEFLATED,
            compresslevel=9,
            allowZip64=True,
        ) as archive:
            for path in relative_files(stage):
                relative = PurePosixPath("ck3-index") / PurePosixPath(
                    path.relative_to(stage).as_posix()
                )
                info = zipfile.ZipInfo(relative.as_posix(), date_time=(1980, 1, 1, 0, 0, 0))
                info.create_system = 3
                info.compress_type = zipfile.ZIP_DEFLATED
                info.external_attr = zip_mode(PurePosixPath(path.relative_to(stage).as_posix()), platform) << 16
                info.flag_bits |= 0x800
                with path.open("rb") as source, archive.open(info, "w", force_zip64=True) as target:
                    shutil.copyfileobj(source, target, length=1024 * 1024)
        os.replace(temporary, output)
    finally:
        temporary.unlink(missing_ok=True)
    return sha256_file(output)


def require_repository_output(repo: Path, output: Path) -> None:
    try:
        output.relative_to(repo)
    except ValueError as exc:
        raise ReleaseError(
            "RELEASE_OUTPUT_OUTSIDE_REPOSITORY: local release archives must remain inside the repository"
        ) from exc
    if output == repo:
        raise ReleaseError("release archive path must name a file below the repository")


def build(args: argparse.Namespace) -> dict[str, object]:
    repo = Path(args.repo).resolve()
    stage = Path(args.stage).resolve()
    output = Path(args.output).resolve()
    if not repo.is_dir() or not stage.is_dir():
        raise ReleaseError("repository or staging directory does not exist")
    require_repository_output(repo, output)

    version = (repo / "VERSION").read_text(encoding="utf-8").strip()
    if not VERSION_RE.fullmatch(version):
        raise ReleaseError(f"VERSION is not a release semver: {version!r}")

    modules = collect_go_module_licenses(repo, stage)
    write_third_party_notices(stage, modules)

    license_path = project_license(repo)
    blockers: list[str] = []
    if license_path is None:
        if not args.allow_missing_project_license:
            raise ReleaseError(
                "PROJECT_LICENSE_MISSING: choose and add the ck3-index project license before public release"
            )
        blockers.append("PROJECT_LICENSE_MISSING")
        (stage / "LICENSE").unlink(missing_ok=True)
    else:
        shutil.copyfile(license_path, stage / "LICENSE")

    binary, sidecar, _ = validate_platform_contract(stage, args.platform, version)
    validate_stage_paths(stage)
    scan_private_path_leaks(repo, stage)
    write_release_metadata(
        stage,
        version,
        args.platform,
        modules,
        release_ready=not blockers,
        blockers=blockers,
        binary=binary,
        sidecar=sidecar,
    )
    validate_stage_paths(stage)
    scan_private_path_leaks(repo, stage)
    archive_sha256 = create_deterministic_zip(stage, output, args.platform)
    return {
        "version": version,
        "platform": args.platform,
        "release_ready": not blockers,
        "blockers": blockers,
        "archive": str(output),
        "archive_size": output.stat().st_size,
        "archive_sha256": archive_sha256,
        "stage_file_count": len(relative_files(stage)),
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", required=True)
    parser.add_argument("--stage", required=True)
    parser.add_argument("--platform", required=True, choices=("windows-x64", "linux-x64"))
    parser.add_argument("--output", required=True)
    parser.add_argument(
        "--allow-missing-project-license",
        action="store_true",
        help="build a visibly blocked local RC; never use this for a public release",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    try:
        result = build(parse_args(argv or sys.argv[1:]))
    except (OSError, ReleaseError, json.JSONDecodeError) as exc:
        print(f"release bundle: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
