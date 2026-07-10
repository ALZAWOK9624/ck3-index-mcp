"""Extract and merge ALL scope data from ck3-tiger into a single Go file."""

import re, os

BASE = os.path.dirname(os.path.abspath(__file__))
TIER = os.path.join(BASE, "..", "external", "ck3-tiger", "src", "ck3", "tables")

SCOPE_TO_GO = {
    "Accolade": "ScopeAccolade", "AccoladeType": "ScopeAccoladeType",
    "Activity": "ScopeActivity", "ActivityType": "ScopeActivityType",
    "AgentSlot": "ScopeAgentSlot", "Army": "ScopeArmy",
    "Artifact": "ScopeArtifact", "CasusBelli": "ScopeCasusBelli",
    "CasusBelliType": "ScopeCasusBelliType", "Character": "ScopeCharacter",
    "CharacterMemory": "ScopeCharacterMemory", "Combat": "ScopeCombat",
    "CombatSide": "ScopeCombatSide", "Confederation": "ScopeConfederation",
    "ConfederationType": "ScopeConfederationType",
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
    pat = re.compile(r'\(\s*\n?\s*(Scopes::[^,]+?),\s*\n?\s*"([^"]+)"')
    out = []
    seen = set()
    for m in pat.finditer(block):
        scope_expr, name = m.groups()
        if name in seen:
            continue
        seen.add(name)
        out.append((name, parse_scopes(scope_expr)))
    return out


def extract_iterators(filepath, const_name):
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
    inmap = []
    outmap = []
    seen = set()
    for m in pat.finditer(block):
        in_scope, name, out_scope = m.groups()
        if name in seen:
            continue
        seen.add(name)
        in_s = parse_scopes(in_scope)
        out_s = parse_scopes(out_scope)
        for prefix in ["every_", "any_", "random_", "ordered_"]:
            full = prefix + name
            inmap.append((full, in_s))
            outmap.append((full, out_s))
    return inmap, outmap


def main():
    triggers = extract(os.path.join(TIER, "triggers.rs"), "TRIGGER")
    effects = extract(os.path.join(TIER, "effects.rs"), "SCOPE_EFFECT")
    iter_in, iter_out = extract_iterators(os.path.join(TIER, "iterators.rs"), "ITERATOR")

    # Collect all scope constants from all data
    all_scopes = set()
    all_scopes.add("ScopeValue")
    for _, scopes in triggers + effects:
        all_scopes.update(scopes)
    for _, scopes in iter_in + iter_out:
        all_scopes.update(scopes)
    all_scopes.discard("ScopeAllScopes")
    sorted_scopes = sorted(all_scopes)

    lines = []
    lines.append("// Code generated by tools/extract_all_scopes.py; DO NOT EDIT.")
    lines.append(f"// Source: ck3-tiger {len(triggers)} triggers, {len(effects)} effects, {len(iter_in)//4} iterators")
    lines.append("")
    lines.append("package indexer")
    lines.append("")
    lines.append("// TigerScope is a lossless 128-bit scope set. CK3 currently has more")
    lines.append("// than 64 distinct scope types, so a uint64 mask silently aliases types.")
    lines.append("type TigerScope struct {")
    lines.append("\tLow  uint64")
    lines.append("\tHigh uint64")
    lines.append("}")
    lines.append("")
    lines.append("var tigerScopesByName = map[string]TigerScope{")
    for s in sorted_scopes:
        name = s.removeprefix("Scope")
        snake = re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower()
        lines.append(f'\t"{snake}": {s},')
    lines.append("}")
    lines.append("")
    lines.append("func scopeBit(bit int) TigerScope {")
    lines.append("\tif bit < 64 {")
    lines.append("\t\treturn TigerScope{Low: uint64(1) << bit}")
    lines.append("\t}")
    lines.append("\treturn TigerScope{High: uint64(1) << (bit - 64)}")
    lines.append("}")
    lines.append("")
    lines.append("func scopeUnion(scopes ...TigerScope) TigerScope {")
    lines.append("\tvar out TigerScope")
    lines.append("\tfor _, scope := range scopes {")
    lines.append("\t\tout.Low |= scope.Low")
    lines.append("\t\tout.High |= scope.High")
    lines.append("\t}")
    lines.append("\treturn out")
    lines.append("}")
    lines.append("")
    lines.append("func (scope TigerScope) IsZero() bool { return scope.Low == 0 && scope.High == 0 }")
    lines.append("func (scope TigerScope) Intersects(other TigerScope) bool {")
    lines.append("\treturn scope.Low&other.Low != 0 || scope.High&other.High != 0")
    lines.append("}")
    lines.append("")
    lines.append("var (")
    for i, s in enumerate(sorted_scopes):
        lines.append(f"\t{s:<34} = scopeBit({i})")
    lines.append("\tScopeAllScopes = TigerScope{Low: ^uint64(0), High: ^uint64(0)}")
    lines.append(")")
    lines.append("")
    lines.append("var tigerScopeNames = []struct {")
    lines.append("\tName  string")
    lines.append("\tScope TigerScope")
    lines.append("}{")
    for s in sorted_scopes:
        name = s.removeprefix("Scope")
        snake = re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower()
        lines.append(f'\t{{Name: "{snake}", Scope: {s}}},')
    lines.append("}")
    lines.append("")

    def mkmap(title, name, entries):
        lines.append(f"// {title}")
        lines.append(f"var {name} = map[string]TigerScope{{")
        for key, scopes in sorted(entries):
            lines.append(f'\t"{key}": {go_scope_expr(scopes)},')
        lines.append("}")
        lines.append("")

    mkmap("Trigger scope map", "tigerTriggerScopes", triggers)
    mkmap("Effect scope map", "tigerEffectScopes", effects)
    mkmap("Iterator input scope map", "iteratorScopeIn", iter_in)
    mkmap("Iterator output scope map", "iteratorScopeOut", iter_out)

    for line in lines:
        print(line)


if __name__ == "__main__":
    main()
