#!/usr/bin/env python3
"""Exercise the installed independent checker: reject, repair, recheck, receipt."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading


def verify(stage: Path, platform: str) -> dict:
    stage = stage.resolve()
    if platform == "windows-x64":
        command = [r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe",
                   "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
                   str(stage / "scripts/start-ck3-check.ps1")]
    else:
        command = ["/bin/sh", str(stage / "scripts/start-ck3-check.sh")]
    with tempfile.TemporaryDirectory(prefix="ck3-check-smoke-") as raw:
        cwd = Path(raw)
        virtual_path = "common/scripted_triggers/proposal.txt"
        canary = cwd / virtual_path
        canary.parent.mkdir(parents=True)
        canary.write_text("} } } # disk content must never be checked", encoding="utf8")
        original = canary.read_bytes()
        environment = os.environ.copy()
        environment["CK3_INDEX_CONFIG"] = str(cwd / "missing-config.toml")
        with tempfile.TemporaryFile(mode="w+t", encoding="utf8") as stderr:
            process = subprocess.Popen(command, cwd=cwd, env=environment, stdin=subprocess.PIPE,
                                       stdout=subprocess.PIPE, stderr=stderr, text=True, encoding="utf8")
            timer = threading.Timer(45, process.kill)
            timer.start()
            sequence = 0

            def rpc(method, params, *, rejected=False):
                nonlocal sequence
                sequence += 1
                process.stdin.write(json.dumps(dict(jsonrpc="2.0", id=sequence, method=method, params=params)) + "\n")
                process.stdin.flush()
                line = process.stdout.readline()
                if not line:
                    stderr.seek(0)
                    raise AssertionError("checker ended without a response: " + stderr.read())
                response = json.loads(line)
                assert response["id"] == sequence, response
                if rejected:
                    assert "error" in response or response["result"].get("isError"), response
                    return response
                assert "error" not in response, response
                return response["result"]

            def check(text, *, path=virtual_path, passed):
                result = rpc("tools/call", dict(name="ck3_check", arguments={"files": [{"path": path, "content": text}]}))
                assert not result.get("isError") and result["content"] == [], result
                body = result["structuredContent"]
                assert body["passed"] is passed and body["database_used"] is False, body
                file = body["files"][0]
                assert file["content_sha256"] == hashlib.sha256(text.encode("utf8")).hexdigest(), file
                assert file["coverage"]["references"] == file["coverage"]["runtime"] == "not_checked", file
                return body

            try:
                rpc("initialize", dict(protocolVersion="2025-03-26", capabilities={}, clientInfo=dict(name="check-release", version="1")))
                process.stdin.write(json.dumps(dict(jsonrpc="2.0", method="notifications/initialized")) + "\n")
                process.stdin.flush()
                names = {tool["name"] for tool in rpc("tools/list", {})["tools"]}
                assert names == {"ck3_check", "ck3_check_rules"}, names
                bad = check("proposal = { AND = { add_gold = 5 } }\n", passed=False)
                assert any(d["code"] == "effect_in_trigger" for d in bad["files"][0]["diagnostics"]), bad
                good = check("proposal = { is_alive = yes }\n# checked 中文\n", passed=True)
                changed = check("proposal = { is_alive = yes }\n}", passed=False)
                assert good["files"][0]["content_sha256"] != changed["files"][0]["content_sha256"]
                check("types Example { type box = widget { size = { 10 20 } } }", path="gui/example.gui", passed=True)
                check('l_english:\n example:0 "Example"\n', path="localization/english/example_l_english.yml", passed=True)
                for files in ([{"path": virtual_path}], [{"path": "../private.txt", "content": ""}]):
                    rpc("tools/call", dict(name="ck3_check", arguments={"files": files}), rejected=True)
                rules = rpc("tools/call", dict(name="ck3_check_rules", arguments={"key": "add_gold"}))
                assert not rules.get("isError") and rules["content"] == [], rules
                assert canary.read_bytes() == original
                assert sorted(p.relative_to(cwd).as_posix() for p in cwd.rglob("*") if p.is_file()) == [virtual_path]
            finally:
                process.stdin.close()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
                timer.cancel()
            assert process.returncode == 0, process.returncode
    return {"repair_cycle": "failed -> passed -> failed after edit", "dialects": ["pdx", "gui", "localization"],
            "missing_content_and_unsafe_path": "rejected", "content_receipts": "verified",
            "config": "missing", "database_used": False, "filesystem": "unchanged", "payload": "structured only"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stage", type=Path, required=True)
    parser.add_argument("--platform", choices=["windows-x64", "linux-x64"], required=True)
    args = parser.parse_args()
    print(json.dumps(verify(args.stage, args.platform), ensure_ascii=False, indent=2))
