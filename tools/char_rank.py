"""
Comprehensive CK3 character ranking: base stats + traits + education + intellect
+ physique + personality + culture traditions + faith bonuses + special buildings.

Searches ALL of D:\mod 工程\游戏本体\game\
"""

import re, os, sys, json
from collections import defaultdict

GAME = r'D:\mod 工程\游戏本体\game'
STATS = ['martial', 'diplomacy', 'stewardship', 'intrigue', 'learning', 'prowess']

# ── Education trait bonuses ──
def edu_bonuses():
    d = {}
    for pre, stat in [('martial','martial'),('diplomacy','diplomacy'),
                      ('stewardship','stewardship'),('intrigue','intrigue'),
                      ('learning','learning'),('prowess','prowess')]:
        for lvl, bonus in [(1,1),(2,3),(3,5),(4,7),(5,9)]:
            d[f'education_{pre}_{lvl}'] = {stat: bonus}
            d[f'education_{pre}_{lvl}_2'] = {stat: bonus}  # some mods use _2 suffix
    return d

# ── Intellect trait bonuses ──
def intellect_bonuses():
    d = {}
    for lvl, b in [(1,1),(2,2),(3,3)]:
        d[f'intellect_good_{lvl}'] = {'diplomacy':b,'stewardship':b,'intrigue':b,'learning':b}
        d[f'intellect_bad_{lvl}'] = {'diplomacy':-b,'stewardship':-b,'intrigue':-b,'learning':-b}
    return d

# ── Physique trait bonuses ──
def physique_bonuses():
    d = {}
    for lvl, b in [(1,2),(2,4),(3,6)]:
        d[f'physique_good_{lvl}'] = {'prowess': b}
        d[f'physique_bad_{lvl}'] = {'prowess': -b}
    return d

# ── Personality trait bonuses (CK3 wiki) ──
PERSONALITY = {
    'brave': {'martial':2,'prowess':3,'diplomacy':-1},
    'craven': {'martial':-2,'prowess':-3,'diplomacy':1},
    'calm': {'diplomacy':1,'intrigue':-1},
    'wrathful': {'martial':3,'diplomacy':-1,'intrigue':1},
    'diligent': {'diplomacy':1,'stewardship':1},
    'lazy': {'diplomacy':-1,'stewardship':-1,'prowess':-1},
    'ambitious': {'diplomacy':1,'intrigue':1,'learning':1,'stewardship':-1},
    'content': {'learning':2,'intrigue':-1,'stewardship':1},
    'honest': {'diplomacy':2,'intrigue':-4},
    'deceitful': {'intrigue':4,'diplomacy':-2},
    'just': {'stewardship':2,'learning':1,'intrigue':-3},
    'arbitrary': {'intrigue':3,'stewardship':-2,'learning':-1},
    'patient': {'diplomacy':1,'intrigue':-1},
    'impatient': {'diplomacy':-1,'intrigue':1},
    'humble': {'learning':1,'diplomacy':-1},
    'arrogant': {'diplomacy':1,'learning':-1},
    'zealous': {'martial':2,'learning':-1},
    'cynical': {'intrigue':2,'learning':1},
    'compassionate': {'diplomacy':2,'martial':-1},
    'callous': {'intrigue':2,'diplomacy':-2},
    'sadistic': {'intrigue':2,'prowess':1,'diplomacy':-1},
    'generous': {'diplomacy':2,'stewardship':-1},
    'greedy': {'stewardship':1,'diplomacy':-1,'learning':-1},
    'lustful': {'intrigue':1,'diplomacy':-1},
    'chaste': {'learning':1,'diplomacy':-1},
    'temperate': {'stewardship':1,'diplomacy':-1},
    'gluttonous': {'diplomacy':-1,'stewardship':-1},
    'forgiving': {'diplomacy':1,'intrigue':-2},
    'vengeful': {'intrigue':2,'martial':1,'diplomacy':-2},
    'stubborn': {'stewardship':1,'diplomacy':-1},
    'fickle': {'diplomacy':1,'intrigue':-1,'stewardship':-1},
    'paranoid': {'intrigue':1,'diplomacy':-1},
    'trusting': {'diplomacy':2,'intrigue':-2},
    'gregarious': {'diplomacy':2,'intrigue':-1},
    'shy': {'diplomacy':-2,'intrigue':1},
    'eccentric': {'learning':2,'diplomacy':-1},
}
LIFESTYLE_TRAITS = {
    'administrator': {'diplomacy':1,'stewardship':3},
    'architect': {'stewardship':2,'diplomacy':1},
    'avenger': {'martial':2,'prowess':2,'intrigue':-1},
    'celibate': {'learning':2,'prowess':-2},
    'devoted': {'learning':2,'diplomacy':1,'intrigue':-1},
    'diplomat': {'diplomacy':4},
    'gallant': {'martial':2,'prowess':4,'diplomacy':1},
    'herbalist': {'learning':2,'diplomacy':-1},
    'hunter': {'prowess':2,'martial':1},
    'overseer': {'stewardship':2,'martial':1},
    'scholar': {'learning':3,'diplomacy':1},
    'strategist': {'martial':3,'stewardship':1},
    'theologian': {'learning':3,'diplomacy':1},
    'torturer': {'prowess':1,'intrigue':1,'diplomacy':-1},
    'whole_of_body': {'prowess':1,'learning':1},
    'seducer': {'intrigue':3,'diplomacy':1},
    'schemer': {'intrigue':3,'diplomacy':-1},
    'august': {'diplomacy':2,'stewardship':1},
    'patriarch': {'diplomacy':2,'learning':1},
    'matriarch': {'diplomacy':2,'learning':1},
    'blademaster_1': {'prowess':3}, 'blademaster_2': {'prowess':6}, 'blademaster_3': {'prowess':9},
    'hunter_1': {'prowess':2}, 'hunter_2': {'prowess':4}, 'hunter_3': {'prowess':6},
    'mystic_1': {'learning':1}, 'mystic_2': {'learning':2}, 'mystic_3': {'learning':3},
}
COMMANDER_TRAITS = {
    'aggressive_attacker': {'martial':1,'prowess':2},
    'flexible_leader': {'martial':1,'diplomacy':1},
    'forest_fighter': {'martial':1,'prowess':2},
    'holy_warrior': {'martial':1,'prowess':1},
    'military_engineer': {'martial':1,'stewardship':1},
    'open_terrain_expert': {'martial':1,'prowess':2},
    'organizer': {'martial':1},
    'rough_terrain_expert': {'martial':1,'prowess':2},
    'unyielding_defender': {'martial':1,'prowess':2},
    'winter_soldier': {'martial':1,'prowess':1},
    'jungle_stalker': {'martial':1,'prowess':2},
    'desert_warrior': {'martial':1,'prowess':2},
    'reaver': {'martial':1,'prowess':2},
    'reckless': {'martial':2,'prowess':3,'diplomacy':-1},
    'cautious_leader': {'martial':1,'diplomacy':1},
    'famous_champion': {'prowess':5},
    'vanguard': {'martial':1,'prowess':2},
}


# ── Parse trait bonuses from game files ──
def parse_traits(base_dir):
    bonuses = {}
    td = os.path.join(base_dir, 'common', 'traits')
    if not os.path.isdir(td): return bonuses
    for root, dirs, files in os.walk(td):
        for f in files:
            if not f.endswith('.txt'): continue
            with open(os.path.join(root,f), 'r', encoding='utf-8', errors='replace') as fh:
                txt = fh.read()
            for block in re.split(r'\n\s*\}\s*\n', txt):
                m = re.search(r'(\w+)\s*=\s*\{', block)
                if not m: continue
                tname = m.group(1)
                if tname in bonuses: continue
                b = defaultdict(int)
                for s in STATS:
                    sm = re.search(rf'\b{s}\s*=\s*([-\d]+)', block)
                    if sm: b[s] = int(sm.group(1))
                if any(v != 0 for v in b.values()):
                    bonuses[tname] = dict(b)
    return bonuses

# ── Parse culture traditions ──
def parse_traditions(base_dir):
    """Returns {tradition_name: stat_bonuses}"""
    bonuses = {}
    td = os.path.join(base_dir, 'common', 'culture', 'traditions')
    if not os.path.isdir(td): return bonuses
    for root, dirs, files in os.walk(td):
        for f in files:
            if not f.endswith('.txt'): continue
            with open(os.path.join(root,f), 'r', encoding='utf-8', errors='replace') as fh:
                txt = fh.read()
            for block in re.split(r'\n\s*\}\s*\n', txt):
                m = re.search(r'tradition_(\w+)\s*=\s*\{', block)
                if not m: continue
                tname = f'tradition_{m.group(1)}'
                b = defaultdict(int)
                # Direct modifiers
                for s in STATS:
                    sm = re.search(rf'\b{s}\s*=\s*([-\d]+)', block)
                    if sm: b[s] = int(sm.group(1))
                # Character modifiers
                cm = re.search(r'character_modifier\s*=\s*\{(.*?)\}', block, re.DOTALL)
                if cm:
                    for s in STATS:
                        sm = re.search(rf'\b{s}\s*=\s*([-\d]+)', cm.group(1))
                        if sm: b[s] += int(sm.group(1))
                if any(v != 0 for v in b.values()):
                    bonuses[tname] = dict(b)
    return bonuses

# ── Parse culture -> traditions mapping ──
def parse_culture_traditions(base_dir):
    """Returns {culture_name: [tradition_names]}"""
    ct = defaultdict(list)
    cd = os.path.join(base_dir, 'common', 'culture', 'cultures')
    if not os.path.isdir(cd): return ct
    for root, dirs, files in os.walk(cd):
        for f in files:
            if not f.endswith('.txt'): continue
            with open(os.path.join(root,f), 'r', encoding='utf-8', errors='replace') as fh:
                txt = fh.read()
            for block in re.split(r'\n\s*\}\s*\n', txt):
                # Culture block
                m = re.match(r'\s*(\w+)\s*=\s*\{', block)
                if not m: continue
                cname = m.group(1)
                # Find tradition references
                # Pattern 1: tradition = tradition_xxx
                for tm in re.findall(r'traditions?\s*=\s*\{([^}]+)\}', block, re.DOTALL):
                    for tn in re.findall(r'tradition_(\w+)', tm):
                        ct[cname].append(f'tradition_{tn}')
                # Pattern 2: tradition = xxx (inline)
                for tm in re.findall(r'tradition\s*=\s*tradition_(\w+)', block):
                    ct[cname].append(f'tradition_{tm}')
    return ct

# ── Parse faith bonuses ──
def parse_faiths(base_dir):
    """Returns {faith_name: stat_bonuses} (faiths nested inside religions)"""
    bonuses = {}
    rd = os.path.join(base_dir, 'common', 'religion', 'religions')
    if not os.path.isdir(rd): return bonuses
    for root, dirs, files in os.walk(rd):
        for f in files:
            if not f.endswith('.txt'): continue
            with open(os.path.join(root,f), 'r', encoding='utf-8', errors='replace') as fh:
                txt = fh.read()
            # Find religion blocks that contain faiths
            rel_blocks = re.split(r'\n\s*\}\s*\n', txt)
            for block in rel_blocks:
                # Find faiths = { faith1 = {...} faith2 = {...} }
                fm = re.search(r'faiths?\s*=\s*\{', block)
                if fm:
                    # Find nested faith blocks within this religion
                    idx = fm.start()
                    depth = 0
                    faiths_block = ''
                    for i in range(idx, len(block)):
                        if block[i] == '{': depth += 1
                        elif block[i] == '}':
                            depth -= 1
                            if depth == 0:
                                faiths_block = block[idx:i+1]
                                break
                    # Parse individual faiths
                    for faith_match in re.finditer(r'(\w+)\s*=\s*\{', faiths_block):
                        fname = faith_match.group(1)
                        if fname in ('flag','icon','color','holy_site','doctrine','religious_head'): continue
                        # Find this faith's closing brace
                        fstart = faith_match.end()
                        fd = 1
                        for j in range(fstart, len(faiths_block)):
                            if faiths_block[j] == '{': fd += 1
                            elif faiths_block[j] == '}':
                                fd -= 1
                                if fd == 0:
                                    fblock = faiths_block[fstart:j]
                                    b = defaultdict(int)
                                    for s in STATS:
                                        sm = re.search(rf'\b{s}\s*=\s*([-\d]+)', fblock)
                                        if sm: b[s] = int(sm.group(1))
                                    if any(v != 0 for v in b.values()):
                                        bonuses[fname] = dict(b)
                                    break
    return bonuses

# ── Parse special building bonuses ──
def parse_buildings(base_dir):
    """Returns {building_name: stat_bonuses}"""
    bonuses = {}
    bd = os.path.join(base_dir, 'common', 'buildings')
    if not os.path.isdir(bd): return bonuses
    for root, dirs, files in os.walk(bd):
        for f in files:
            if not f.endswith('.txt'): continue
            with open(os.path.join(root,f), 'r', encoding='utf-8', errors='replace') as fh:
                txt = fh.read()
            for block in re.split(r'\n\s*\}\s*\n', txt):
                m = re.search(r'(\w+)\s*=\s*\{', block)
                if not m: continue
                bname = m.group(1)
                if bname in bonuses: continue
                b = defaultdict(int)
                for s in STATS:
                    sm = re.search(rf'\b{s}\s*=\s*([-\d]+)', block)
                    if sm: b[s] = int(sm.group(1))
                cm = re.search(r'county_modifier\s*=\s*\{(.*?)\}', block, re.DOTALL)
                if cm:
                    for s in STATS:
                        sm = re.search(rf'\b{s}\s*=\s*([-\d]+)', cm.group(1))
                        if sm: b[s] += int(sm.group(1))
                if any(v != 0 for v in b.values()):
                    bonuses[bname] = dict(b)
    return bonuses

# ══════════════════════════════════════════════════════════════════
# MAIN
# ══════════════════════════════════════════════════════════════════

# 1) Build all bonus tables
print("Loading trait bonuses...", file=sys.stderr)
trait_bonuses = {}
for k,v in edu_bonuses().items(): trait_bonuses[k] = v
for k,v in intellect_bonuses().items(): trait_bonuses[k] = v
for k,v in physique_bonuses().items(): trait_bonuses[k] = v
for k,v in PERSONALITY.items(): trait_bonuses[k] = v
for k,v in LIFESTYLE_TRAITS.items(): trait_bonuses[k] = v
for k,v in COMMANDER_TRAITS.items(): trait_bonuses[k] = v
file_traits = parse_traits(GAME)
for k,v in file_traits.items():
    if k not in trait_bonuses: trait_bonuses[k] = v

print(f"  {len(trait_bonuses)} trait bonuses loaded", file=sys.stderr)

print("Loading tradition bonuses...", file=sys.stderr)
trad_bonuses = parse_traditions(GAME)
print(f"  {len(trad_bonuses)} tradition bonuses loaded", file=sys.stderr)

print("Loading culture-tradition mappings...", file=sys.stderr)
culture_trads = parse_culture_traditions(GAME)
print(f"  {len(culture_trads)} cultures with traditions", file=sys.stderr)

print("Loading faith bonuses...", file=sys.stderr)
faith_bonuses = parse_faiths(GAME)
print(f"  {len(faith_bonuses)} faiths with stat bonuses", file=sys.stderr)

print("Loading building bonuses...", file=sys.stderr)
building_bonuses = parse_buildings(GAME)
print(f"  {len(building_bonuses)} buildings with stat bonuses", file=sys.stderr)

# 2) Parse ALL characters
char_dir = os.path.join(GAME, 'history', 'characters')
char_files = []
for root, dirs, files in os.walk(char_dir):
    for f in files:
        if f.endswith('.txt'):
            char_files.append(os.path.join(root, f))

print(f"Parsing {len(char_files)} character files...", file=sys.stderr)

characters = []
for fi, cf in enumerate(char_files):
    with open(cf, 'r', encoding='utf-8', errors='replace') as fh:
        content = fh.read()
    blocks = re.split(r'\n\s*(?=\w+\s*=\s*\{)', content)
    for block in blocks:
        m = re.match(r'(\w+)\s*=\s*\{', block)
        if not m: continue
        name = m.group(1)
        # Skip meta-blocks
        if name in ('effect','trigger','limit','option','if','else','immediate',
                     'hidden_effect','desc','text','title','name','icon','tooltip'): continue
        culture = re.search(r'culture\s*=\s*"?(\w+)"?', block)
        culture = culture.group(1) if culture else '?'
        religion = re.search(r'religion\s*=\s*"?(\w+)"?', block)
        religion = religion.group(1) if religion else '?'
        gov = re.search(r'government\s*=\s*"?(\w+)"?', block)
        gov = gov.group(1) if gov else '?'
        base = {}
        for s in STATS:
            bm = re.search(rf'\b{s}\s*=\s*(\d+)', block)
            base[s] = int(bm.group(1)) if bm else 0
        if not any(base.values()): continue
        traits = re.findall(r'trait\s*=\s*(\w+)', block)
        titles = re.findall(r'title\s*=\s*"?([ekdcb]_\w+)"?', block)

        # Compute total
        total = dict(base)
        detail = {}

        # Trait bonuses
        for t in traits:
            if t in trait_bonuses:
                for s, v in trait_bonuses[t].items():
                    total[s] += v
                    detail.setdefault(t, {})[s] = v

        # Culture traditions
        trad_names = culture_trads.get(culture, [])
        for tn in trad_names:
            if tn in trad_bonuses:
                for s, v in trad_bonuses[tn].items():
                    total[s] += v
                    detail.setdefault(f'culture:{tn}', {})[s] = v

        # Faith bonuses
        if religion in faith_bonuses:
            for s, v in faith_bonuses[religion].items():
                total[s] += v
                detail.setdefault(f'faith:{religion}', {})[s] = v

        # Cap minimum at 0 (CK3 game minimum for displayed stats)
        for s in STATS:
            if total[s] < 0: total[s] = 0

        score = (total['martial']*1.5 + total['diplomacy'] + total['stewardship']*1.3 +
                 total['intrigue'] + total['learning']*1.2 + total['prowess']*0.5)

        characters.append({
            'name': name, 'culture': culture, 'religion': religion, 'gov': gov,
            'total': total, 'score': score, 'base': base,
            'traits': traits, 'titles': titles, 'detail': detail
        })

    if (fi+1) % 50 == 0:
        print(f"  {fi+1}/{len(char_files)} files, {len(characters)} rulers", file=sys.stderr)

characters.sort(key=lambda c: c['score'], reverse=True)

# 3) Output
print(f"\n{'='*120}")
print(f"CK3 VANILLA CHARACTER COMPLETE RANKING (Top 50 of {len(characters)})")
print(f"Base + Traits + Education + Intellect + Physique + Personality")
print(f"+ Culture Traditions + Faith Bonuses")
print(f"={'='*120}")
print(f"{'#':<3} {'Name':<22} {'Culture':<14} {'Faith':<24} {'Gov':<18} {'M':>3}{'D':>3}{'S':>3}{'I':>3}{'L':>3}{'P':>3} {'Score':>5}  Bonus Sources")
print("-"*120)

for i, c in enumerate(characters[:50], 1):
    t = c['total']
    detail_items = []
    for src, bon in c['detail'].items():
        detail_items.append(f"{src}({bon})")
    detail_str = ' | '.join(detail_items[:3])
    if len(detail_items) > 3: detail_str += f' +{len(detail_items)-3}'
    print(f"{i:<3} {c['name']:<22} {c['culture']:<14} {c['religion']:<24} {c['gov']:<18} {t['martial']:>3}{t['diplomacy']:>3}{t['stewardship']:>3}{t['intrigue']:>3}{t['learning']:>3}{t['prowess']:>3} {c['score']:>5.0f}  {detail_str[:105]}")

print(f"\n--- Lifetime stat summary (all {len(characters)} rulers) ---")
for stat in STATS:
    vals = [c['total'][stat] for c in characters]
    print(f"  {stat:<12} avg={sum(vals)/len(vals):.1f}  max={max(vals)}  min={min(vals)}")

print(f"\nTotal bonus sources loaded:")
print(f"  traits={len(trait_bonuses)} traditions={len(trad_bonuses)} cultures={len(culture_trads)} faiths={len(faith_bonuses)} buildings={len(building_bonuses)}")
