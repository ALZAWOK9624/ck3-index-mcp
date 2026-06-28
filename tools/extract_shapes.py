"""Extract trigger/effect value shapes from ck3-tiger into Go data.

Usage: python extract_shapes.py > ../internal/indexer/shape_data.gen.go
"""

import re, os

BASE = os.path.dirname(os.path.abspath(__file__))
TIER = os.path.join(BASE, "..", "external", "ck3-tiger", "src", "ck3", "tables")

# Shape categories for LLM consumption
SHAPE_CATEGORY = {
    "Boolean": ("boolean", "yes or no"),
    "Yes": ("boolean", "yes (no effect)"),
    "CompareValue": ("compare", "numeric value with comparison (>= > = < <=)"),
    "ScriptValue": ("script_value", "integer or script value expression"),
    "NonNegativeValue": ("script_value", "non-negative integer"),
    "Integer": ("integer", "integer value"),
    "Color": ("string", "color value (hex or hsv)"),
    "Identifier": ("string", "identifier string"),
}

def parse_scope_arg(expr):
    """Scopes::X -> scope constant name"""
    s = expr.replace("Scopes::", "").strip()
    m = {
        "Character": "character", "LandedTitle": "title", "Province": "province",
        "Faith": "faith", "Culture": "culture", "Dynasty": "dynasty",
        "Artifact": "artifact", "War": "war", "Accolade": "accolade",
        "Activity": "activity", "Army": "army", "Scheme": "scheme",
        "Secret": "secret", "Struggle": "struggle", "Combat": "combat",
        "CombatSide": "combat_side", "Religion": "religion",
        "HolyOrder": "holy_order", "Legend": "legend", "Domicile": "domicile",
        "Faction": "faction", "House": "dynasty_house",
        "StoryCycle": "story_cycle", "Regiment": "regiment",
        "CasusBelli": "casus_belli", "CourtPosition": "court_position",
        "MercenaryCompany": "mercenary_company", "Army": "army",
    }
    return m.get(s, s.lower())


def parse_item_arg(expr):
    """Item::X -> item key type name"""
    s = expr.replace("Item::", "").strip()
    return s.lower()


def parse_field(field_expr):
    """Parse a sub-field like ("type", Item(Item::AccoladeType)) into {"key":..., "shape":...}"""
    m = re.match(r'\(\s*"([^"]+)"\s*,\s*(.+)', field_expr)
    if not m:
        return None
    key = m.group(1)
    val = m.group(2)
    shape, desc = parse_one_shape(val)
    return {"key": key, "shape": shape, "desc": desc}


def parse_one_shape(expr, depth=0):
    """Parse a single Trigger/Effect variant expression into (shape_name, description)."""
    expr = expr.strip()
    # Remove stray trailing closing parentheses
    while expr.endswith(')') and expr.count('(') < expr.count(')'):
        # Check if this trailing paren is balanced by an inner paren
        # Only strip if there's an unmatched )
        balanced = 0
        for ch in expr:
            if ch == '(': balanced += 1
            elif ch == ')': balanced -= 1
        if balanced < 0:
            expr = expr[:-1].strip()
        else:
            break

    # Handle parenthesized expressions
    if expr.endswith(')'):
        paren_start = expr.find('(')
        if paren_start > 0:
            variant = expr[:paren_start].strip()
            inner = expr[paren_start+1:-1].strip()
            if variant in ("Scope", "ScopeOkThis"):
                s = parse_scope_arg(inner)
                return ("scope_target", f"scope target ({s})")
            if variant == "Item":
                s = parse_item_arg(inner)
                return ("item_key", f"key reference ({s})")
            if variant in ("ScopeOrItem",):
                parts = split_top_level(inner)
                s = parse_scope_arg(parts[0]) if parts else "?"
                i = parse_item_arg(parts[1]) if len(parts) > 1 else "?"
                return ("scope_or_item", f"scope ({s}) or key ({i})")
            if variant == "Block":
                fields = parse_block_fields(inner)
                desc = "block: " + ", ".join(f"{f['key']}={f['desc']}" for f in fields)
                return ("block", desc)
            if variant in ("BlockOrCompareValue",):
                parts = split_top_level(inner)
                item_part = parse_one_shape(parts[0]) if parts else ("?", "?")
                return ("block_or_compare", f"{item_part[1]} or compare value")
            if variant == "Target":
                parts = split_top_level(inner)
                key = parts[0].strip('"') if parts else "?"
                scopes = parse_scope_arg(parts[1]) if len(parts) > 1 else "?"
                return ("block_target", f"block: key={key} scope target ({scopes})")
            if variant == "TargetValue":
                parts = split_top_level(inner)
                key = parts[0].strip('"') if parts else "?"
                scopes = parse_scope_arg(parts[1]) if len(parts) > 1 else "?"
                vkey = parts[2].strip('"') if len(parts) > 2 else "value"
                return ("block_target_value", f"block: {key}=scope({scopes}), {vkey}=script_value")
            if variant == "ItemTarget":
                parts = split_top_level(inner)
                ikey = parts[0].strip('"') if parts else "?"
                item_typ = parse_item_arg(parts[1]) if len(parts) > 1 else "?"
                tkey = parts[2].strip('"') if len(parts) > 2 else "target"
                scopes = parse_scope_arg(parts[3]) if len(parts) > 3 else "?"
                return ("block_item_target", f"block: {ikey}=item({item_typ}), {tkey}=scope({scopes})")
            if variant == "ItemValue":
                parts = split_top_level(inner)
                key = parts[0].strip('"') if parts else "?"
                item_typ = parse_item_arg(parts[1]) if len(parts) > 1 else "?"
                vkey = parts[2].strip('"') if len(parts) > 2 else "value"
                return ("block_item_value", f"block: {key}=item({item_typ}), {vkey}=script_value")
            if variant == "Choice":
                choices = [c.strip('" ') for c in inner.split(',')]
                return ("choice", f"one of: {', '.join(choices[:8])}")
            if variant in ("Vb", "Vbc", "Vbv", "Vv"):
                return ("special", f"validated by {inner}")
            if variant == "Removed":
                parts = split_top_level(inner)
                ver = parts[0].strip('"') if parts else "?"
                return ("removed", f"removed in {ver}")
            if variant in ("Desc", "Timespan", "Control", "ControlOrLabel"):
                return ("block", f"{variant.lower()} block")
            if variant == "String":
                return ("string", "text string")
        else:
            # Single-word variant like Boolean, CompareValue, etc.
            pass

    # Simple variants
    if expr in SHAPE_CATEGORY:
        return SHAPE_CATEGORY[expr]
    if expr.startswith("Scope("):
        inner = expr[len("Scope("):-1]
        s = parse_scope_arg(inner)
        return ("scope_target", f"scope target ({s})")
    if expr.startswith("Item("):
        inner = expr[len("Item("):-1]
        s = parse_item_arg(inner)
        return ("item_key", f"key reference ({s})")
    if expr in ("Boolean",):
        return ("boolean", "yes or no")

    # Try to parse as nested expression
    for variant, desc in [
        ("Scope(", "scope target"), ("Item(", "key reference"),
        ("ScopeOrItem(", "scope or item"), ("Block(", "block"),
        ("Target(", "block target"), ("TargetValue(", "block with target+value"),
        ("ItemTarget(", "block with item+target"),
        ("BlockOrCompareValue(", "block or compare"),
        ("Choice(", "choice list"), ("Vb(", "special"), ("Vbc(", "special"),
        ("Vbv(", "special"), ("Vv(", "special"), ("Removed(", "removed"),
        ("Control", "control block"), ("ControlOrLabel", "control or label block"),
        ("Desc", "description block"), ("Timespan", "timespan block"),
        ("Unchecked", "unchecked"), ("UncheckedTodo", "unchecked (TODO)"),
        ("Special", "special"),
    ]:
        if expr.startswith(variant):
            return (variant.strip('(').lower(), desc)

    return ("special", expr[:60])


def split_top_level(s):
    """Split by commas not inside parens/brackets/quotes."""
    parts = []
    current = ""
    depth = 0
    in_quote = False
    for ch in s:
        if ch == '"' and (not current or current[-1] != '\\'):
            in_quote = not in_quote
        if in_quote:
            current += ch
            continue
        if ch in '([{':
            depth += 1
            current += ch
        elif ch in ')]}':
            depth -= 1
            current += ch
        elif ch == ',' and depth == 0:
            parts.append(current.strip())
            current = ""
        else:
            current += ch
    if current.strip():
        parts.append(current.strip())
    return parts


def parse_block_fields(expr):
    """Parse &[...] field list into list of dicts."""
    # Strip outer &[...]
    expr = expr.strip()
    # Look for '[' and match
    if not expr.startswith('&['):
        return []
    # Find the matching ']'
    depth = 0
    end = 0
    for i, ch in enumerate(expr):
        if ch == '[':
            depth += 1
        elif ch == ']':
            depth -= 1
            if depth == 0:
                end = i
                break
    inner = expr[2:end]
    fields = []
    for field_str in split_top_level(inner):
        f = parse_field(field_str)
        if f:
            fields.append(f)
    return fields


def extract_with_shapes(filepath, const_name):
    """Extract (name, scopes, shape_name, shape_desc) tuples from a Rust const."""
    with open(filepath, "r", encoding="utf-8") as f:
        c = f.read()
    idx = c.find(f"const {const_name}")
    idx = c.find("= &[", idx)
    if idx < 0:
        return []
    idx = c.find("[", idx)
    depth = 0
    start = idx
    for i in range(idx, len(c)):
        if c[i] == '[':
            depth += 1
        elif c[i] == ']':
            depth -= 1
            if depth == 0:
                block = c[start:i + 1]
                break

    # Match all three fields: (Scopes, "name", ShapeExpression)
    # The shape expression can be complex (nested parens)
    # Strategy: find each top-level tuple, extract fields 1 and 3
    entries = []
    # Simple approach: use regex to find (Scopes::X, "name", ... then parse the rest
    # Actually, let me find each top-level tuple by tracking paren depth

    i = 0
    while i < len(block):
        # Find opening paren of a tuple
        tup_start = block.find("(", i)
        if tup_start < 0:
            break
        # Check if this is the start of a data entry (not a nested expression)
        # Data entries start with (Scopes::
        if not block[tup_start+1:].strip().startswith("Scopes::"):
            i = tup_start + 1
            continue
        # Find matching closing paren
        pdepth = 0
        j = tup_start
        while j < len(block):
            if block[j] == '(':
                pdepth += 1
            elif block[j] == ')':
                pdepth -= 1
                if pdepth == 0:
                    entry_text = block[tup_start:j+1]
                    break
            j += 1
        if pdepth != 0:
            break
        entries.append(entry_text)
        i = j + 1

    # Parse each entry
    results = []
    seen = set()
    SCOPE_PAT = re.compile(r'\(\s*\n?\s*(Scopes::[^,]+)')
    NAME_PAT = re.compile(r'"([^"]+)"')
    for entry in entries:
        sm = SCOPE_PAT.search(entry)
        if not sm:
            continue
        scope_expr = sm.group(1)
        # Find the name (second field, quoted)
        idx2 = sm.end()
        nms = list(NAME_PAT.finditer(entry[idx2:]))
        if not nms:
            continue
        name = nms[0].group(1)
        if name in seen:
            continue
        seen.add(name)
        # The third field starts after the name + comma
        third_start = idx2 + nms[0].end() + 1  # skip closing quote and comma
        shape_expr = entry[third_start:].strip()
        # Remove trailing ), and stray closing parens
        while shape_expr.endswith("),") or shape_expr.endswith(")") :
            if shape_expr.endswith("),"):
                shape_expr = shape_expr[:-2].strip()
            else:
                shape_expr = shape_expr[:-1].strip()
        shape_name, shape_desc = parse_one_shape(shape_expr)

        # Parse scopes
        scopes = []
        s = scope_expr.replace("Scopes::", " ")
        for p in re.split(r'\.union\(|\.minus\(|\)', s):
            p = p.strip()
            if p in ("all", "all_but_none"):
                scopes = ["ScopeAllScopes"]
                break
        if not scopes:
            scopes = ["ScopeAllScopes"]

        results.append((name, "|".join(scopes), shape_name, shape_desc))

    return results


def main():
    trig_shapes = extract_with_shapes(os.path.join(TIER, "triggers.rs"), "TRIGGER")
    eff_shapes = extract_with_shapes(os.path.join(TIER, "effects.rs"), "SCOPE_EFFECT")

    lines = []
    lines.append("// Code generated by tools/extract_shapes.py; DO NOT EDIT.")
    lines.append(f"// Source: ck3-tiger {len(trig_shapes)} triggers, {len(eff_shapes)} effects")
    lines.append("")
    lines.append("package indexer")
    lines.append("")
    lines.append("// ShapeDesc describes what value a trigger or effect key expects.")
    lines.append("type ShapeDesc struct {")
    lines.append("\tShape string `json:\"shape\"`")
    lines.append("\tDesc  string `json:\"desc\"`")
    lines.append("}")
    lines.append("")

    lines.append("var tigerShapeData = map[string]ShapeDesc{")
    merged = {}
    for name, scopes, shape, desc in trig_shapes + eff_shapes:
        kind = "trigger/effect"
        merged[name] = (shape, desc)
    for name, (shape, desc) in sorted(merged.items()):
        lines.append(f'\t"{name}": {{Shape: "{shape}", Desc: `{desc}`}},')
    lines.append("}")
    lines.append("")

    lines.append(f"// Generated {len(merged)} shape entries")

    for line in lines:
        print(line)


if __name__ == "__main__":
    main()
