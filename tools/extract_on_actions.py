"""Extract static on_action evidence from ck3-tiger's on_action.rs.

The generated Go table deliberately preserves the old membership map while
adding a read-only structural projection of direct declarations, aliases,
named bindings, and list bindings.  It is not a validator input: Tiger is a
versioned static source that can lag the engine logs.
"""

import argparse
import json
import os
import re


BASE = os.path.dirname(os.path.abspath(__file__))
TIER = os.path.join(BASE, "..", "external", "ck3-tiger", "src", "ck3", "tables")

TOP_ASSIGNMENT = re.compile(r"^\s*([A-Za-z0-9_]+)\s*=\s*(\{|[A-Za-z0-9_]+)\s*$")
BINDING_ASSIGNMENT = re.compile(r"^\s*([A-Za-z0-9_]+)\s*=\s*([^\s{}#]+)\s*$")
LIST_ASSIGNMENT = re.compile(
    r"^\s*list\s*=\s*\{\s*([A-Za-z0-9_]+)\s*=\s*([^\s{}#]+)\s*\}\s*$"
)
VERSION = re.compile(r"LAST UPDATED CK3 VERSION\s+([0-9][0-9A-Za-z.\-]*)")

# ck3-tiger's table uses a compact set of literal values.  Enumerating known
# scope-like values turns a newly introduced ordinary token into a generator
# failure rather than silently misclassifying it as a scope.
KNOWN_SCOPE_TYPES = {
    "accolade",
    "army",
    "artifact",
    "casus_belli",
    "character",
    "combat",
    "combat_side",
    "council_task",
    "culture",
    "domicile",
    "dynasty",
    "dynasty_house",
    "faith",
    "ghw",
    "government_type",
    "holy_order",
    "house_relation",
    "landed_title",
    "mercenary_company",
    "province",
    "scheme",
    "struggle",
    "travel_plan",
    "war",
}
PRIMITIVE_TYPES = {
    "none": "TigerOnActionValueKindNone",
    "flag": "TigerOnActionValueKindFlag",
    "value": "TigerOnActionValueKindValue",
    "bool": "TigerOnActionValueKindBool",
}
REVIEW_MARKER = re.compile(r"\b(?:todo|undocumented|may be unset)\b", re.IGNORECASE)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", help="write generated Go to this UTF-8 path instead of stdout")
    args = parser.parse_args()
    source_path = os.path.join(TIER, "on_action.rs")
    with open(source_path, "r", encoding="utf-8") as f:
        source = f.read()
    text = raw_string_contents(source)
    version_match = VERSION.search(source)
    if not version_match:
        raise ValueError("could not determine ck3-tiger on_action table version")
    version = version_match.group(1)
    direct, aliases = parse_contracts(text)
    validate_aliases(direct, aliases)
    generated = emit(direct, aliases, version)
    if args.output:
        with open(args.output, "w", encoding="utf-8", newline="\n") as output:
            output.write(generated)
    else:
        print(generated)


def raw_string_contents(source):
    """Return the contents of the ON_ACTION_SCOPES Rust raw string."""
    idx = source.find("ON_ACTION_SCOPES")
    if idx < 0:
        raise ValueError("ON_ACTION_SCOPES is missing")
    start = source.find('"', idx) + 1
    if start == 0:
        raise ValueError("ON_ACTION_SCOPES string does not start")
    end = source.find('";', start)
    if end < 0:
        raise ValueError("ON_ACTION_SCOPES string does not terminate")
    return source[start:end]


def parse_contracts(text):
    """Parse the intentionally small, line-oriented Tiger table grammar."""
    direct = {}
    aliases = {}
    depth = 0
    current = None
    for line_number, raw_line in enumerate(text.splitlines(), start=1):
        code = pdx_code(raw_line)
        stripped = code.strip()
        if depth == 0:
            if stripped:
                match = TOP_ASSIGNMENT.match(code)
                if not match:
                    raise ValueError(
                        f"unsupported top-level on_action declaration at line {line_number}: {raw_line!r}"
                    )
                name, value = match.groups()
                if name in direct or name in aliases:
                    raise ValueError(f"duplicate on_action declaration {name!r} at line {line_number}")
                if value == "{":
                    direct[name] = {
                        "root": None,
                        "named": [],
                        "lists": [],
                        "review": has_review_marker(raw_line),
                    }
                    current = name
                else:
                    aliases[name] = value
        elif depth == 1 and current:
            if stripped and stripped != "}":
                list_match = LIST_ASSIGNMENT.match(code)
                binding_match = BINDING_ASSIGNMENT.match(code)
                if list_match:
                    name, value = list_match.groups()
                    direct[current]["lists"].append(
                        make_binding(name, value, raw_line, line_number)
                    )
                elif binding_match:
                    name, value = binding_match.groups()
                    if name == "list":
                        raise ValueError(
                            f"unsupported non-block list binding in {current!r} at line {line_number}"
                        )
                    binding = make_binding(name, value, raw_line, line_number)
                    if name == "root":
                        if direct[current]["root"] is not None:
                            raise ValueError(f"duplicate root in {current!r} at line {line_number}")
                        direct[current]["root"] = binding
                    else:
                        direct[current]["named"].append(binding)
                else:
                    raise ValueError(
                        f"unsupported nested on_action declaration in {current!r} at line {line_number}: {raw_line!r}"
                    )

        depth += brace_delta(code)
        if depth < 0:
            raise ValueError(f"unbalanced braces while extracting on_action.rs at line {line_number}")
        if current and depth == 0:
            current = None
    if depth != 0:
        raise ValueError("unterminated block while extracting on_action.rs")
    if current:
        raise ValueError(f"unterminated on_action block {current!r}")
    for name, seed in direct.items():
        if seed["root"] is None:
            raise ValueError(f"direct on_action {name!r} has no root binding")
    return direct, aliases


def make_binding(name, value, raw_line, line_number):
    return {
        "name": name,
        "static_type": value,
        "kind": classify_value(value, name, line_number),
        "review": has_review_marker(raw_line),
    }


def classify_value(value, name, line_number):
    if value in PRIMITIVE_TYPES:
        return PRIMITIVE_TYPES[value]
    if is_dynamic_value(value):
        return "TigerOnActionValueKindDynamic"
    if value in KNOWN_SCOPE_TYPES:
        return "TigerOnActionValueKindScope"
    raise ValueError(
        f"unrecognized Tiger on_action value {value!r} for {name!r} at line {line_number}; "
        "classify it deliberately before publishing static evidence"
    )


def is_dynamic_value(value):
    return (
        value.startswith("$")
        or value.startswith("[")
        or value.startswith("scope:")
        or value.startswith("var:")
        or (value.startswith("<") and value.endswith(">"))
    )


def has_review_marker(raw_line):
    comment_index = raw_line.find("#")
    return bool(comment_index >= 0 and REVIEW_MARKER.search(raw_line[comment_index + 1 :]))


def validate_aliases(direct, aliases):
    """Fail loudly on dangling aliases or cycles instead of generating a lie."""
    def resolve(name, trail):
        if name in direct:
            return name
        if name not in aliases:
            raise ValueError(f"on_action alias {trail[-1]!r} points to unknown target {name!r}")
        if name in trail:
            cycle = " -> ".join(trail + [name])
            raise ValueError(f"on_action alias cycle: {cycle}")
        return resolve(aliases[name], trail + [name])

    for name, target in aliases.items():
        resolve(target, [name])


def emit(direct, aliases, version):
    on_actions = sorted(set(direct) | set(aliases))
    list_count = sum(len(seed["lists"]) for seed in direct.values())
    lines = [
        "// Code generated by tools/extract_on_actions.py; DO NOT EDIT.",
        f"// Source: ck3-tiger on_action.rs (CK3 {version}; {len(on_actions)} entries)",
        "package indexer",
        "",
        f"const tigerOnActionTableVersion = {go_string(version)}",
        "",
        "var tigerOnActions = map[string]struct{}{",
    ]
    for name in on_actions:
        lines.append(f"\t{go_string(name)}: {{}},")
    lines.extend([
        "}",
        "",
        "var tigerOnActionDirect = map[string]tigerOnActionDirectSeed{",
    ])
    for name in sorted(direct):
        seed = direct[name]
        lines.append(f"\t{go_string(name)}: {{")
        lines.append(f"\t\tRoot: {emit_binding(seed['root'])},")
        emit_binding_slice(lines, "Named", seed["named"])
        emit_binding_slice(lines, "Lists", seed["lists"])
        if seed["review"]:
            lines.append("\t\tReview: true,")
        lines.append("\t},")
    lines.extend([
        "}",
        "",
        "var tigerOnActionAliases = map[string]tigerOnActionAliasSeed{",
    ])
    for name in sorted(aliases):
        lines.append(f"\t{go_string(name)}: {{Target: {go_string(aliases[name])}}},")
    lines.extend([
        "}",
        f"// {len(on_actions)} on_action entries ({len(direct)} direct, {len(aliases)} aliases; {list_count} list bindings)",
    ])
    return "\n".join(lines) + "\n"


def emit_binding_slice(lines, field, bindings):
    if not bindings:
        return
    lines.append(f"\t\t{field}: []tigerOnActionBindingSeed{{")
    for binding in bindings:
        lines.append(f"\t\t\t{emit_binding(binding)},")
    lines.append("\t\t},")


def emit_binding(binding):
    fields = [
        f"Name: {go_string(binding['name'])}",
        f"StaticType: {go_string(binding['static_type'])}",
        f"Kind: {binding['kind']}",
    ]
    if binding["review"]:
        fields.append("Review: true")
    return "tigerOnActionBindingSeed{" + ", ".join(fields) + "}"


def go_string(value):
    return json.dumps(value, ensure_ascii=False)


def pdx_code(line):
    """Return a PDX line without an out-of-string # comment."""
    out = []
    quoted = False
    escaped = False
    for char in line:
        if quoted:
            out.append(char)
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
            continue
        if char == "#":
            break
        if char == '"':
            quoted = True
        out.append(char)
    return "".join(out)


def brace_delta(line):
    """Count braces while ignoring quoted text (comments were stripped first)."""
    delta = 0
    quoted = False
    escaped = False
    for char in line:
        if quoted:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
            continue
        if char == '"':
            quoted = True
        elif char == "{":
            delta += 1
        elif char == "}":
            delta -= 1
    return delta


if __name__ == "__main__":
    main()
