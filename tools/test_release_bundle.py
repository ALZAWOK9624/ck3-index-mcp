#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("build_release_bundle.py")
SPEC = importlib.util.spec_from_file_location("build_release_bundle", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value), encoding="utf-8")


class ReleaseBundleTests(unittest.TestCase):
    def test_archive_is_deterministic_and_rooted(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            stage = root / "stage"
            (stage / "scripts").mkdir(parents=True)
            (stage / "scripts" / "start.sh").write_text("#!/bin/sh\n", encoding="utf-8")
            (stage / "payload.txt").write_text("stable\n", encoding="utf-8")
            first = root / "first.zip"
            second = root / "second.zip"
            first_hash = release.create_deterministic_zip(stage, first, "linux-x64")
            second_hash = release.create_deterministic_zip(stage, second, "linux-x64")
            self.assertEqual(first_hash, second_hash)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with __import__("zipfile").ZipFile(first) as archive:
                self.assertEqual(
                    archive.namelist(),
                    ["ck3-index/payload.txt", "ck3-index/scripts/start.sh"],
                )

    def test_private_repository_path_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            repo = Path(raw) / "repo"
            stage = Path(raw) / "stage"
            repo.mkdir()
            stage.mkdir()
            (stage / "leak.txt").write_text(str(repo), encoding="utf-8")
            with self.assertRaisesRegex(release.ReleaseError, "PRIVATE_PATH_LEAK"):
                release.scan_private_path_leaks(repo, stage)

    def test_release_output_must_resolve_inside_repository(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            repo = (root / "repo").resolve()
            outside = (root / "outside").resolve()
            repo.mkdir()
            outside.mkdir()
            release.require_repository_output(repo, repo / "cache" / "release.zip")
            with self.assertRaisesRegex(
                release.ReleaseError, "RELEASE_OUTPUT_OUTSIDE_REPOSITORY"
            ):
                release.require_repository_output(repo, outside / "release.zip")

            link = repo / "linked-output"
            try:
                link.symlink_to(outside, target_is_directory=True)
            except OSError:
                return
            with self.assertRaisesRegex(
                release.ReleaseError, "RELEASE_OUTPUT_OUTSIDE_REPOSITORY"
            ):
                release.require_repository_output(
                    repo.resolve(), (link / "release.zip").resolve()
                )

    def test_runtime_database_and_cache_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            stage = Path(raw) / "stage"
            (stage / "cache").mkdir(parents=True)
            (stage / "cache" / "index.sqlite").write_bytes(b"sqlite")
            with self.assertRaisesRegex(release.ReleaseError, "FORBIDDEN_RELEASE_PATH"):
                release.validate_stage_paths(stage)
            stage = Path(raw) / "stage-junk"
            stage.mkdir()
            (stage / ".DS_Store").write_bytes(b"junk")
            with self.assertRaisesRegex(release.ReleaseError, "FORBIDDEN_RELEASE_FILE"):
                release.validate_stage_paths(stage)

    def test_portable_settings_and_single_binary_are_enforced(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            stage = Path(raw) / "stage"
            version = "1.2.3-rc.1"
            sidecar = stage / "sidecar" / "whitebox_tools.exe"
            binary = stage / "bin" / f"ck3-index-v{version}.exe"
            launcher = stage / "scripts" / "start-ck3-index.ps1"
            skill = stage / "skills" / "ck3-coding" / "SKILL.md"
            for path, content in (
                (sidecar, b"whitebox"),
                (binary, b"ck3-index"),
                (launcher, b"launcher"),
                (skill, b"skill"),
                (stage / "third_party" / "WHITEBOXTOOLS_LICENSE.txt", b"license"),
            ):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(content)
            write_json(stage / ".codex-plugin" / "plugin.json", {"version": version})
            write_json(
                stage / ".mcp.json",
                {
                    "mcpServers": {
                        "ck3_index": {
                            "command": r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe",
                            "cwd": ".",
                        }
                    }
                },
            )
            write_json(stage / "config" / "settings.json", {"version": 1, "config_path": ""})
            write_json(
                stage / "third_party" / "whitebox-tools-v2.4.0.json",
                {
                    "version": "2.4.0",
                    "platforms": {
                        "windows-x64": {
                            "binary_sha256": hashlib.sha256(b"whitebox").hexdigest()
                        }
                    },
                },
            )

            resolved, _, _ = release.validate_platform_contract(
                stage, "windows-x64", version
            )
            self.assertEqual(resolved, binary)

            write_json(
                stage / "config" / "settings.json",
                {"version": 1, "config_path": r"C:\private\index.toml"},
            )
            with self.assertRaisesRegex(release.ReleaseError, "PORTABLE_CONFIG_REQUIRED"):
                release.validate_platform_contract(stage, "windows-x64", version)

            write_json(stage / "config" / "settings.json", {"version": 1, "config_path": ""})
            (stage / "bin" / "ck3-index-v0.0.1.exe").write_bytes(b"old")
            with self.assertRaisesRegex(release.ReleaseError, "STALE_RELEASE_BINARY"):
                release.validate_platform_contract(stage, "windows-x64", version)


if __name__ == "__main__":
    unittest.main()
