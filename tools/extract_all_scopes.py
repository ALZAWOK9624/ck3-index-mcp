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
    s = expr.replace("Scopes::", " ")
    parts = re.split(r'\.union\(|\.minus\(|\)', s)
    out = []
    for p in parts:
        p = p.strip()
        v = SCOPE_TO_GO.get(p)
        if v and v not in out and v != "ScopeAllScopes" and v != "ScopeValue":
            out.append(v)
    return out or ["ScopeAllScopes"]


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

    bit_map = {}
    for i, s in enumerate(sorted_scopes):
        if s == "ScopeValue":
            continue
        shift = i + 1
        if shift >= 64:
            shift = 63
        bit_map[s] = 1 << shift
    bit_map["ScopeValue"] = 1 << 0

    lines = []
    lines.append("// Code generated by tools/extract_all_scopes.py; DO NOT EDIT.")
    lines.append(f"// Source: ck3-tiger {len(triggers)} triggers, {len(effects)} effects, {len(iter_in)//4} iterators")
    lines.append("")
    lines.append("package indexer")
    lines.append("")
    lines.append("type TigerScope uint64")
    lines.append("")
    lines.append("const (")
    for s in sorted_scopes:
        if s == "ScopeValue":
            continue  # handled explicitly below
        lines.append(f"\t{s:<34} TigerScope = {bit_map[s]}")
    lines.append(f"\tScopeValue         TigerScope = 1 << 0")
    lines.append(f"\tScopeAllScopes    TigerScope = ^TigerScope(0)")
    lines.append(")")
    lines.append("")

    def mkmap(title, name, entries):
        lines.append(f"// {title}")
        lines.append(f"var {name} = map[string]TigerScope{{")
        for key, scopes in sorted(entries):
            parts = scopes if scopes else ["ScopeAllScopes"]
            lines.append(f'\t"{key}": {" | ".join(parts)},')
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
