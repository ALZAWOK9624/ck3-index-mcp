"""Extract scope-to-scope transitions from ck3-tiger targets.rs."""

import re, os, sys

BASE = os.path.dirname(os.path.abspath(__file__))
TIER = os.path.join(BASE, "..", "external", "ck3-tiger", "src", "ck3", "tables")

# The scope constants from scope_data.gen.go
SCOPE_TO_GO = {
    "Accolade": "ScopeAccolade", "AccoladeType": "ScopeAccoladeType",
    "Activity": "ScopeActivity", "ActivityType": "ScopeActivityType",
    "AgentSlot": "ScopeAgentSlot", "Army": "ScopeArmy",
    "Artifact": "ScopeArtifact", "CasusBelli": "ScopeCasusBelli",
    "CasusBelliType": "ScopeCasusBelliType", "Character": "ScopeCharacter",
    "Combat": "ScopeCombat", "CombatSide": "ScopeCombatSide",
    "Confederation": "ScopeConfederation", "ConfederationType": "ScopeConfederationType",
    "CouncilTask": "ScopeCouncilTask", "CourtPosition": "ScopeCourtPosition",
    "CourtPositionType": "ScopeCourtPositionType", "Culture": "ScopeCulture",
    "CultureInnovation": "ScopeCultureInnovation",
    "CulturePillar": "ScopeCulturePillar",
    "CultureTradition": "ScopeCultureTradition", "Decision": "ScopeDecision",
    "Doctrine": "ScopeDoctrine", "Domicile": "ScopeDomicile",
    "Dynasty": "ScopeDynasty", "DynastyHouse": "ScopeDynastyHouse",
    "Epidemic": "ScopeEpidemic", "EpidemicType": "ScopeEpidemicType",
    "Faction": "ScopeFaction", "Faith": "ScopeFaith",
    "GeographicalRegion": "ScopeGeographicalRegion",
    "GovernmentType": "ScopeGovernmentType",
    "GreatHolyWar": "ScopeGreatHolyWar", "GreatProject": "ScopeGreatProject",
    "GreatProjectType": "ScopeGreatProjectType",
    "HoldingType": "ScopeHoldingType", "HolyOrder": "ScopeHolyOrder",
    "HouseAspiration": "ScopeHouseAspiration",
    "HouseRelation": "ScopeHouseRelation",
    "HouseRelationLevel": "ScopeHouseRelationLevel",
    "HouseRelationType": "ScopeHouseRelationType",
    "Inspiration": "ScopeInspiration", "LandedTitle": "ScopeTitle",
    "Legend": "ScopeLegend", "LegendType": "ScopeLegendType",
    "MercenaryCompany": "ScopeMercenaryCompany",
    "ProjectContribution": "ScopeProjectContribution",
    "Province": "ScopeProvince", "Regiment": "ScopeRegiment",
    "Religion": "ScopeReligion", "Scheme": "ScopeScheme",
    "Secret": "ScopeSecret", "Situation": "ScopeSituation",
    "SituationParticipantGroup": "ScopeSituationParticipantGroup",
    "SituationSubRegion": "ScopeSituationSubRegion",
    "StoryCycle": "ScopeStoryCycle", "Struggle": "ScopeStruggle",
    "TaskContract": "ScopeTaskContract",
    "TaskContractType": "ScopeTaskContractType",
    "TaxSlot": "ScopeTaxSlot",
    "TitleAndVassalChange": "ScopeTitleAndVassalChange",
    "Trait": "ScopeTrait", "TravelPlan": "ScopeTravelPlan",
    "VassalContract": "ScopeVassalContract",
    "VassalObligationLevel": "ScopeVassalObligationLevel",
    "War": "ScopeWar",
    "all": "ScopeAllScopes", "all_but_none": "ScopeAllScopes",
    "Bool": "ScopeValue", "Flag": "ScopeValue", "Value": "ScopeValue",
    "None": "ScopeValue",
}


def parse_scopes(expr):
    base = re.search(r"Scopes::([A-Za-z0-9_]+)", expr)
    if not base:
        return ["ScopeAllScopes"]
    base_name = SCOPE_TO_GO.get(base.group(1))
    out = [] if base_name in (None, "ScopeAllScopes") else [base_name]
    all_scopes = base_name == "ScopeAllScopes"
    for op, raw_name in re.findall(
        r"\.(union|minus)\(\s*Scopes::([A-Za-z0-9_]+)\s*\)", expr
    ):
        name = SCOPE_TO_GO.get(raw_name)
        if not name or name == "ScopeAllScopes":
            continue
        if op == "union" and name not in out:
            out.append(name)
        elif op == "minus":
            if all_scopes:
                raise RuntimeError(
                    f"cannot safely generate all-scopes subtraction yet: {expr}"
                )
            out = [item for item in out if item != name]
    if all_scopes:
        return ["ScopeAllScopes"]
    return out or ["ScopeAllScopes"]


def go_scope_expr(scopes):
    parts = scopes or ["ScopeAllScopes"]
    if len(parts) == 1:
        return parts[0]
    return f"scopeUnion({', '.join(parts)})"


def extract(filepath, const_name):
    with open(filepath, "r", encoding="utf-8") as f:
        c = f.read()
    idx = c.find(f"const {const_name}")
    idx = c.find("= &[", idx)
    if idx < 0:
        return ([], [])
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

    pat = re.compile(r'\(\s*\n?\s*(Scopes::[^,]+?),\s*\n?\s*"([^"]+?)",\s*\n?\s*(Scopes::[^,)]+)')
    from_scopes = []
    to_scopes = []
    for m in pat.finditer(block):
        in_scope, name, out_scope = m.groups()
        in_s = parse_scopes(in_scope)
        out_s = parse_scopes(out_scope)
        from_scopes.append((name, in_s))
        to_scopes.append((name, out_s))
    return from_scopes, to_scopes


def extract_scope_prefix(filepath):
    with open(filepath, "r", encoding="utf-8") as f:
        c = f.read()
    start = c.find("const SCOPE_PREFIX")
    end = c.find("const SCOPE_TO_SCOPE_REMOVED", start)
    if start < 0 or end < 0:
        return ([], [])
    block = c[start:end]
    pat = re.compile(r'\(\s*\n?\s*(Scopes::[^,]+?),\s*\n?\s*"([^"]+?)",\s*\n?\s*(Scopes::[^,)]+)')
    from_scopes = []
    to_scopes = []
    for m in pat.finditer(block):
        in_scope, name, out_scope = m.groups()
        from_scopes.append((name, parse_scopes(in_scope)))
        to_scopes.append((name, parse_scopes(out_scope)))
    return from_scopes, to_scopes


def merge_entries(entries):
    merged = {}
    for name, scopes in entries:
        target = merged.setdefault(name, [])
        for scope in scopes:
            if scope not in target:
                target.append(scope)
    return list(merged.items())


def main():
    target_path = os.path.join(TIER, "targets.rs")
    from_s, to_s = extract(target_path, "SCOPE_TO_SCOPE")
    prefix_from, prefix_to = extract_scope_prefix(target_path)
    from_s = merge_entries(from_s + prefix_from)
    to_s = merge_entries(to_s + prefix_to)

    lines = []
    lines.append("// Code generated by tools/extract_targets.py; DO NOT EDIT.")
    lines.append(f"// Source: ck3-tiger targets.rs ({len(from_s)} scope transitions)")
    lines.append("")
    lines.append("package indexer")
    lines.append("")
    lines.append("// scopeTransitions maps event target names to their required input scope.")
    lines.append("var scopeTransitionsIn = map[string]TigerScope{")
    for key, scopes in sorted(from_s):
        val = go_scope_expr(scopes)
        lines.append(f'\t"{key}": {val},')
    lines.append("}")
    lines.append("")
    lines.append("// scopeTransitionsOut maps event target names to their output scope.")
    lines.append("var scopeTransitionsOut = map[string]TigerScope{")
    for key, scopes in sorted(to_s):
        val = go_scope_expr(scopes)
        lines.append(f'\t"{key}": {val},')
    lines.append("}")
    lines.append("")
    lines.append(f"// Generated {len(from_s)} scope transition entries")

    rendered = "\n".join(lines) + "\n"
    if len(sys.argv) > 1:
        with open(sys.argv[1], "w", encoding="utf-8", newline="\n") as f:
            f.write(rendered)
    else:
        print(rendered, end="")


if __name__ == "__main__":
    main()
