#!/usr/bin/env python3
"""Run the staged ck3-index plugin through its real MCP launcher."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


class SmokeError(RuntimeError):
    pass


def request_lines(version: str) -> str:
    requests = [
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-11-25",
                "capabilities": {},
                "clientInfo": {"name": "ck3-index-release", "version": version},
            },
        },
        {
            "jsonrpc": "2.0",
            "method": "notifications/initialized",
            "params": {},
        },
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
        {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "tools/call",
            "params": {"name": "ck3_health", "arguments": {}},
        },
        {
            "jsonrpc": "2.0",
            "id": 4,
            "method": "tools/call",
            "params": {
                "name": "map_asset_audit",
                "arguments": {"operation": "rivers", "limit": 2},
            },
        },
    ]
    return "".join(json.dumps(item, separators=(",", ":")) + "\n" for item in requests)


def launcher_command(stage: Path, platform: str) -> list[str]:
    if platform == "windows-x64":
        return [
            r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe",
            "-NoProfile",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            str(stage / "scripts" / "start-ck3-index.ps1"),
        ]
    if platform == "linux-x64":
        return ["/bin/sh", str(stage / "scripts" / "start-ck3-index.sh")]
    raise SmokeError(f"unsupported platform: {platform}")


def invoke(
    stage: Path,
    platform: str,
    config: Path,
    payload: str,
) -> list[dict]:
    environment = os.environ.copy()
    environment["CK3_INDEX_CONFIG"] = str(config)
    try:
        completed = subprocess.run(
            launcher_command(stage, platform),
            cwd=stage,
            env=environment,
            input=payload,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="strict",
            check=False,
            timeout=120,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise SmokeError(f"staged launcher failed: {exc}") from exc
    if completed.returncode != 0:
        raise SmokeError(
            f"staged launcher exited {completed.returncode}: {completed.stderr.strip()}"
        )
    responses: list[dict] = []
    for line in completed.stdout.splitlines():
        if not line.strip():
            continue
        try:
            response = json.loads(line)
        except json.JSONDecodeError as exc:
            raise SmokeError(f"launcher emitted non-JSON stdout: {line[:200]!r}") from exc
        if not isinstance(response, dict):
            raise SmokeError("launcher emitted a non-object JSON-RPC response")
        responses.append(response)
    return responses


def responses_by_id(responses: list[dict], expected_ids: set[int]) -> dict[int, dict]:
    by_id: dict[int, dict] = {}
    for response in responses:
        response_id = response.get("id")
        if type(response_id) is not int or response_id not in expected_ids:
            raise SmokeError(
                f"MCP server returned an unexpected response id: {response_id!r}"
            )
        if response_id in by_id:
            raise SmokeError(
                f"MCP server returned duplicate response id {response_id}"
            )
        by_id[response_id] = response
    missing = expected_ids.difference(by_id)
    if missing:
        raise SmokeError(
                f"MCP server omitted response id(s): {sorted(missing)}"
        )
    return by_id


def binary_version(plugin_version: str) -> str:
    return plugin_version.split("+", 1)[0]


def validate_standard(
    responses: list[dict],
    version: str,
    expected_gis_sha256: str,
    expected_standard_tools: int,
) -> None:
    if len(responses) != 4:
        raise SmokeError(f"MCP server returned {len(responses)} responses, expected 4")
    by_id = responses_by_id(responses, {1, 2, 3, 4})
    initialize = by_id[1].get("result") or {}
    if initialize.get("protocolVersion") != "2025-11-25":
        raise SmokeError("staged server negotiated an unexpected MCP protocol")
    if (initialize.get("serverInfo") or {}).get("version") != version:
        raise SmokeError("staged server version does not match VERSION")
    tools = (by_id[2].get("result") or {}).get("tools") or []
    if len(tools) != expected_standard_tools:
        raise SmokeError(
            f"MCP server advertised {len(tools)} tools, expected {expected_standard_tools}"
        )
    names = [item.get("name") for item in tools if isinstance(item, dict)]
    if len(names) != len(set(names)):
        raise SmokeError("MCP server advertises duplicate tool names")
    for required in ("ck3_health", "ck3_package", "map_physical_context", "map_route", "map_render"):
        if required not in names:
            raise SmokeError(f"MCP server is missing canonical tool {required}")

    health = by_id[3].get("result") or {}
    if health.get("isError"):
        raise SmokeError("staged ck3_health returned isError")
    health_content = health.get("structuredContent") or {}
    gis = health_content.get("gis") or {}
    if not gis.get("available") or gis.get("sha256") != expected_gis_sha256:
        raise SmokeError("staged ck3_health did not verify the bundled GIS sidecar")

    audit = by_id[4].get("result") or {}
    if audit.get("isError"):
        raise SmokeError("staged map_asset_audit returned isError")
    if (audit.get("structuredContent") or {}).get("intent") != "map_asset_audit":
        raise SmokeError("staged map_asset_audit returned an unexpected contract")


def verify(args: argparse.Namespace) -> dict[str, object]:
    stage = Path(args.stage).resolve()
    config = Path(args.config).resolve()
    if not stage.is_dir() or not config.is_file():
        raise SmokeError("stage or config path is missing")
    manifest = json.loads(
        (stage / ".codex-plugin" / "plugin.json").read_text(encoding="utf-8")
    )
    version = str(manifest.get("version") or "")
    expected_server_version = binary_version(version)
    wbt = json.loads(
        (stage / "third_party" / "whitebox-tools-v2.4.0.json").read_text(
            encoding="utf-8"
        )
    )
    expected_hash = wbt["platforms"][args.platform]["binary_sha256"]

    standard = invoke(
        stage,
        args.platform,
        config,
        request_lines(version),
    )
    validate_standard(
        standard,
        expected_server_version,
        expected_hash,
        args.expected_tools,
    )
    return {
        "version": version,
        "binary_version": expected_server_version,
        "platform": args.platform,
        "tools": args.expected_tools,
        "gis_sha256": expected_hash,
        "status": "ready",
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", required=True)
    parser.add_argument("--platform", required=True, choices=("windows-x64", "linux-x64"))
    parser.add_argument("--config", required=True)
    parser.add_argument("--expected-tools", type=int, required=True)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    try:
        result = verify(parse_args(argv or sys.argv[1:]))
    except (OSError, KeyError, ValueError, json.JSONDecodeError, SmokeError) as exc:
        print(f"release MCP smoke: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
