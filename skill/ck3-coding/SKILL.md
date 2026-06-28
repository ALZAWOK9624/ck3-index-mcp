---
name: ck3-coding
description: CK3 and Godherja mod coding workflow. Use when editing, reviewing, generating, or validating Crusader Kings III mod scripts, localization, resources, events, traits, decisions, history, GUI, or related ck3-index/MCP diagnostics in this workspace.
---

# CK3 Coding

Use this skill for CK3 mod scripting work. Treat `ck3-index` as the first source of truth for definitions, references, localization, resources, and diagnostics. Treat compiled scope/shape rules as hints that must be checked against indexed examples before risky edits.

## MCP Tools (21 tools)

### Core Index Queries (read before editing)
| Tool | Purpose |
|---|---|
| `inspect_object` | One-stop: definitions, override chain, refs, localization, diagnostics for one id |
| `prepare_edit` | Examples + schema fields + edit context before generating code |
| `preflight_code` | Cheap generation/edit gate: blockers, unresolved refs, localization candidates, diagnostics |
| `diagnose_key` | Is this id an object, loc key, resource, reference, or diagnostic clue? |
| `query_object` | Active definitions |
| `query_object_types` | All indexed object types and counts |
| `find_refs` | Incoming/outgoing references (source-aware, resolved/unresolved flags) |
| `query_loc` | Localization key values across languages |
| `query_resource` | Resource file existence and references |
| `query_examples` | Vanilla-first script examples by object type; `type:term` also searches object bodies |
| `query_rules` | Schema fields learned from local `.info` files |
| `query_patterns` | Empirical field shapes learned from active indexed scripts |

### Validation Tools
| Tool | Purpose |
|---|---|
| `validate_project` | Chat-fast cached parser/index/compiler diagnostic summary; does not rescan |
| `explain_diagnostic` | Diagnostics filtered by code |

### Local Scope & Shape Tools (verify correctness before generating)
| Tool | Purpose |
|---|---|
| `lookup_scope` | What scope type does this trigger/effect probably require? |
| `lookup_shape` | What value shape does this trigger/effect expect? (boolean, compare, scope, item, block, etc.) |
| `lookup_define` | Does this @define name exist in the game's define table? |
| `lookup_on_action` | Is this a known vanilla on_action name? |
| `lookup_iterator` | What scope does this iterator consume and produce? |
| `lookup_example` | What description or syntax example is known for this trigger/effect? |
| `lookup_modifier` | Is this static modifier key known, and what scope areas can use it? |

## Workflow

1. **Query before editing**:
   - `inspect_object` for one-id context
   - `prepare_edit` before generating or changing script
   - `preflight_code` before trusting generated code or after refreshing the index
   - `diagnose_key` when unsure what an id is
   - `lookup_scope` to verify trigger/effect scope requirements
   - `lookup_shape` to know how to write a trigger/effect value
   - `lookup_iterator`, `lookup_example`, and `lookup_modifier` for iterator, syntax, and modifier uncertainty
   - `query_examples` before writing new script; use `type:term` for nested syntax such as `event:add_character_modifier`
   - `query_patterns` to inspect field shapes commonly used by the same object type
   - Read `guidance` in MCP responses first; it is written for low-cost models and explains how to use the evidence

2. **Respect source priority**:
   - Current submod first: `我的工程/22/` (rank=1)
   - Godherja upstream: `mod本体/godherja-beta/` (rank=2, read-only)
   - CK3 game files: `游戏本体/game/` (rank=3, read-only)
   - Godherja CN translation: `mod翻译/Godherja CN/` (rank=4, read-only)

3. **File override semantics**: CK3 loads files by rel_path; same-path files from higher-priority sources replace lower ones entirely. ck3-index automatically detects overridden files and excludes them from queries.

4. **Never use localization text alone as proof of mechanics**. Confirm in scripts, history, GUI, or indexed definitions.

5. **Before finalizing changes**, run `ck3-index scan` then `ck3-index diag_stats` to verify no new diagnostics were introduced. MCP `validate_project` is cached and fast; do not treat it as fresh after file edits unless the index was just refreshed.

6. **Keep generated code conservative**:
   - Match nearby file style
   - Prefer existing scripted triggers/effects/values
   - Add new localization keys with clear prefixes
   - Avoid touching upstream files

## Diagnostics Reference

| Code | Severity | Meaning |
|---|---|---|
| `parse_error` | error | CK3 script syntax error |
| `effect_in_trigger` | error | Effect used inside a trigger block |
| `scope_mismatch` | warning | Trigger/effect used on wrong scope (iterator-aware) |
| `trigger_in_effect` | warning | Trigger used inside an effect block |
| `missing_localization` | warning | Loc key referenced but not defined |
| `missing_object_reference` | warning | Object (trait/title/faith/...) referenced but not indexed |
| `missing_resource` | warning | Gfx/resource path referenced but file not found |
| `missing_sound` | warning | `event:/...` sound event referenced but not known from game/Tiger logs |
| `duplicate_object` | warning | Same-source duplicate object definition |
| `scope_never_saved` | warning | `scope:name` block opener without `save_scope_as` |
| `on_action_direct_override` | warning | Direct effect/trigger block in a vanilla on_action |
| `missing_trigger_else` | warning | `trigger_if` chain without `trigger_else` terminator |
| `event_no_option` | warning | Event definition without an option block |
| `nested_iterator` | warning | Iterator inside another iterator (potential performance issue) |
| `gui_layout_misuse` | warning | `parentanchor` inside `hbox`/`vbox` (use `expand={}` instead) |
| `gui_crash_risk` | error | Known crash pattern in GUI (percent-size in hbox/vbox, etc.) |
| `missing_event_loc` | warning | Event/decision without localization references |
| `variable_never_set` | warning | Variable referenced but never `set_variable` in active files |
| `lios_partial_override` | warning | File overrides upstream but defines fewer objects |

## Generation Rules

When generating events, decisions, traits, modifiers, or history entries:

1. First call `prepare_edit` on the object type to see vanilla-first examples and schema fields
2. Call `query_patterns` on the object type to see empirical field shapes from active indexed scripts
3. Call `lookup_scope` on any unfamiliar trigger/effect keys to verify scope requirements
4. Call `lookup_shape` to confirm the correct value format for each trigger/effect
5. Call `query_examples type:term` for concrete nested syntax before inventing blocks
6. Call `preflight_code` on the target id as a cheap accuracy gate
7. Generate a patch-sized change
8. Validate through `ck3-index scan` then `ck3-index diag_stats`, then call `preflight_code` again if an LLM needs a compact pass/fail summary

## CLI Commands

```
ck3-index scan [--clean]    # Incremental index (--clean for full rebuild)
ck3-index diag_stats        # Diagnostic code counts
ck3-index patterns <type>   # Empirical field shapes from indexed scripts
ck3-index preflight <id>    # LLM-ready generation/edit blockers and warnings
ck3-index validate          # Full validation + compiler checks
ck3-index mcp               # Start MCP server over stdio
```

## Data Sources

- **Compiled local rule seeds**: trigger/effect scopes, iterators, scope transitions, defines, on_actions, examples, modifiers, and sound events. Accessible via `lookup_scope`, `lookup_shape`, `lookup_define`, `lookup_on_action`, `lookup_iterator`, `lookup_example`, `lookup_modifier`, and `preflight_code`/diagnostics for `event:/...` sounds. Do not treat these as Tiger authority; confirm with local CK3 `.info` files, game logs, and vanilla examples when editing.
- **Local wiki notes**: `docs/CK3_EXPERIENCE_NOTES.md` summarizes workflow hints from the local CK3 modding wiki. Treat them as generation guidance, not engine authority.
- **Regenerate**: `python tools/extract_all_scopes.py` (scope+iterator data), `python tools/extract_shapes.py` (shape data), `python tools/extract_defines.py`, `python tools/extract_on_actions.py`, `python tools/extract_targets.py`.
