"""Publish compact evidence, without shipping vanilla file bodies or host paths."""
from __future__ import annotations

import argparse
import json
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("report", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    report = json.loads(args.report.read_text(encoding="utf-8-sig"))
    result = {key: value for key, value in report.items() if key != "families"}
    result["evidence_kind"] = "observed_vanilla_fields_not_exhaustive_engine_schema"
    result["families"] = {}
    for name, family in sorted(report["families"].items()):
        item = {key: value for key, value in family.items() if key != "direct_definition_fields"}
        fields = family["direct_definition_fields"]
        item["observed_field_count"] = len(fields)
        if name.startswith("common/") or name == "events":
            item["fields"] = {
                key: {
                    "shapes": field["shapes"],
                    "evidence": f'{field["example_path"]}:{field["example_line"]}',
                }
                for key, field in sorted(fields.items())
            }
        result["families"][name] = item
    # Keep each generated directory on one line so the evidence catalog does
    # not consume GitHub's entire diff line budget before the actual code.
    families = result.pop("families")
    header = json.dumps(result, ensure_ascii=False, indent=2).rstrip()[:-1].rstrip()
    rows = [header + ",\n  \"families\": {"]
    for index, (name, item) in enumerate(families.items()):
        comma = "," if index + 1 < len(families) else ""
        rows.append("    " + json.dumps(name) + ": " + json.dumps(item, ensure_ascii=False, separators=(",", ":")) + comma)
    rows.append("  }\n}\n")
    args.output.write_text("\n".join(rows), encoding="utf-8")
    print(f'{report["files"]} text files; {report["documentation_files"]} documentation files; '
          f'{len(families)} directories; {args.output.stat().st_size} output bytes')


if __name__ == "__main__":
    main()
