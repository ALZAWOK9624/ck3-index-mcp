---
name: ck3-coding
description: CK3 and Godherja mod coding workflow. Use when editing, reviewing, generating, or validating Crusader Kings III mod scripts, localization, resources, events, traits, decisions, history, GUI, or related ck3-index/MCP diagnostics in this workspace.
---

# CK3 Coding

Use this skill for CK3 mod scripting work. Treat `ck3-index` as the first source of truth for definitions, references, localization, resources, and diagnostics. Treat compiled scope/shape rules as hints that must be checked against indexed examples before risky edits.

For CK3 semantic questions, do not begin with `rg`. Call `inspect_object`, `diagnose_key`, `prepare_edit`, or the relevant map/query tool first; use `rg` only to inspect the exact text behind indexed evidence. If the MCP tools are not attached to the current session, use the equivalent `ck3-index` CLI command instead of silently falling back to broad text search.

## MCP Tools (36 tools)

### High-Level Entry Points

| Tool | Purpose |
|---|---|
| `ck3_search` | Exact/prefix/FTS discovery across ids, paths, resources, fields, datatypes, and English/Chinese localization values; use before `rg` |
| `ck3_inspect` | Aggregate one-id classification across objects, refs, loc, resources, sounds, and diagnostics |
| `ck3_review` | Review proposed complete files or current dirty project files, including scope traces and reference/resource checks |

### Core Index Queries

| Tool | Purpose |
|---|---|
| `inspect_object` | One-stop context: definitions, override chain, refs, localization, diagnostics |
| `prepare_edit` | Examples, schema fields, patterns, and edit context before generating code |
| `preflight_code` | Cheap cached object gate: blockers, unresolved refs, localization candidates, diagnostics |
| `preflight_patch` | Temporary patch gate for proposed complete file contents; does not scan or write SQLite |
| `impact_patch` | Temporary impact/risk summary for proposed `upsert`, `delete`, or `rename` patch ops |
| `preflight_dirty` | Temporary validation for changed current-project files on disk; does not scan or write SQLite |
| `diagnose_key` | Classify an id as object, loc key, resource, reference, or diagnostic clue |
| `health_check` | Short DB/schema/index/MCP trust signal for low-cost models |
| `query_object` | Active definitions |
| `query_object_types` | All indexed object types and counts |
| `find_refs` | Incoming/outgoing references with resolved/unresolved flags |
| `query_loc` | Localization key values across languages |
| `query_resource` | Resource file existence and references |
| `query_examples` | Vanilla-first script examples by object type; `type:term` also searches object bodies |
| `query_rules` | Schema fields learned from local `.info` files |
| `query_patterns` | Empirical field shapes learned from active indexed scripts |
| `architecture_overview` | Cached codebase-memory style map of indexed sources, object types, reference kinds, and diagnostic hotspots |
| `dependency_graph` | Codebase-memory style graph around an object or referenced id; supports `depth` 1-2 and CK3 semantic edges |

### Validation Tools

| Tool | Purpose |
|---|---|
| `validate_project` | Chat-fast cached parser/index/compiler diagnostic summary; does not rescan |
| `explain_diagnostic` | Diagnostics filtered by code |

### Local Scope & Shape Tools

| Tool | Purpose |
|---|---|
| `lookup_scope` | What scope type a trigger/effect probably requires |
| `lookup_datatype` | Engine data_types signatures, descriptions, definition types, return types, and provenance |
| `lookup_shape` | What value shape a trigger/effect expects: boolean, compare, scope, item, block, etc. |
| `lookup_define` | Whether an @define name exists in the game's define table |
| `lookup_on_action` | Whether a vanilla on_action name is known |
| `lookup_iterator` | What scope an iterator consumes and produces |
| `lookup_example` | Known trigger/effect description or syntax example |
| `lookup_modifier` | Static modifier key validity and likely use areas |

## Workflow

1. Query before editing:
	- Start bot sessions with `health_check` if index trust is uncertain.
	- Use `ck3_search` for broad discovery and `ck3_inspect` for one-id investigation before raw text search.
	- Use `ck3_review` as the default code-review gate; specialized tools remain available for precise follow-up.
   - Use `inspect_object` for one-id context.
   - Use `prepare_edit` before generating or changing script.
   - Use `query_examples`, `query_patterns`, and `query_rules` before inventing structure.
   - Use `lookup_scope`, `lookup_shape`, `lookup_iterator`, `lookup_example`, and `lookup_modifier` for unfamiliar keys.
   - Use `preflight_patch` on proposed complete file contents before writing them.
   - Use `impact_patch` before risky delete/rename changes.
   - Use `preflight_dirty` for quick local checks of changed project files.
   - Use `scan --files <relpath...>` after writing a small number of current-project files.
   - Use `accuracy` after changing extraction, references, resources, localization, scope rules, or diagnostics.
   - Read `guidance` first; it is written for low-cost models.

2. Respect source priority:
   - Current submod, rank 1: project/22, writable.
   - Godherja upstream, rank 2: read-only.
   - CK3 game files, rank 3: read-only.
   - Godherja CN translation, rank 4: read-only unless the task is explicitly translation work.

3. File override semantics: CK3 loads files by `rel_path`; same-path files from higher-priority sources replace lower ones entirely. `ck3-index` detects overridden files and excludes them from active queries.

4. Source boundary semantics: only source-root-relative CK3 load roots (`common`, `events`, `history`, `gui`, `localization`, `gfx`, `map_data`, and `sound`) are indexed. Root-level backups, tools, docs, caches, and temporary folders are intentionally ignored even when they contain nested CK3-looking paths.

5. Never use localization text alone as proof of mechanics. Confirm in scripts, history, GUI, or indexed definitions.

6. During the edit loop, prefer `preflight_patch`, `preflight_dirty`, and `scan --files` over repeated full `scan`. Before final release or a large handoff, run `ck3-index scan` and `ck3-index diag_stats`.

7. Keep generated code conservative:
   - Match nearby file style.
   - Prefer existing scripted triggers/effects/values.
   - Add new localization keys with clear prefixes.
   - Avoid touching upstream files.

## Diagnostics Reference

| Code | Severity | Meaning |
|---|---|---|
| `parse_error` | error | CK3 script syntax error |
| `effect_in_trigger` | error | Effect used inside a trigger block |
| `scope_mismatch` | warning | Proven trigger/effect scope conflict with a root/current scope trace |
| `trigger_in_effect` | warning | Trigger used inside an effect block |
| `missing_localization` | warning | Loc key referenced but not defined |
| `missing_object_reference` | warning | Object reference not indexed |
| `missing_resource` | warning | Gfx/resource path referenced but file not found |
| `resource_resolution_uncertain` | info | Bare/context-relative resource needs owning-context resolution before it can be called missing |
| `scope_uncertain` | info | Tiger suspicion not fully confirmed by current engine logs and a concrete trace |
| `missing_sound` | warning | `event:/...` sound event referenced but not known from local rule seeds |
| `duplicate_object` | warning | Same-source duplicate object definition |
| `scope_never_saved` | warning | `scope:name` block opener without `save_scope_as` |
| `on_action_direct_override` | warning | Direct effect/trigger block in a vanilla on_action |
| `missing_trigger_else` | warning | `trigger_if` chain without `trigger_else` terminator |
| `event_no_option` | warning | Event definition without an option block |
| `nested_iterator` | warning | Iterator inside another iterator, likely expensive or suspicious |
| `gui_layout_misuse` | warning | `parentanchor` inside `hbox`/`vbox`; usually use `expand={}` instead |
| `gui_crash_risk` | error | Known crash pattern in GUI |
| `missing_event_loc` | warning | Event/decision without localization references |
| `variable_never_set` | warning | Variable referenced but never `set_variable` in active files |
| `lios_partial_override` | warning | File overrides upstream but defines fewer objects |

## Generation Rules

When generating events, decisions, traits, modifiers, men-at-arms, traditions, or history entries:

1. Call `prepare_edit` on the object type.
2. Call `query_patterns` on the object type.
3. Call `query_examples type:term` for concrete nested syntax before inventing blocks.
4. Call `lookup_scope` and `lookup_shape` on unfamiliar trigger/effect keys.
5. Generate a patch-sized change.
6. Call `preflight_patch` on the proposed complete file contents.
7. Write only after patch preflight has no blockers.
8. For risky delete/rename work, call `impact_patch` first.
9. After writing, call `preflight_dirty` for a cheap local gate.
10. For small current-project edits, run `ck3-index scan --files <relpath...>` then `ck3-index preflight <id>`.
11. For final verification, run `ck3-index scan` then `ck3-index diag_stats`.

## CLI Commands

```text
ck3-index scan [--clean]                # Incremental index; --clean for full rebuild
ck3-index scan --files <relpath...>     # Refresh current-project files and affected refs
ck3-index diag_stats                    # Diagnostic code counts
ck3-index accuracy [dir]                # Golden accuracy regression fixtures
ck3-index patterns <type>               # Empirical field shapes from indexed scripts
ck3-index preflight <id>                # LLM-ready generation/edit blockers and warnings
ck3-index preflight-patch <json-file>   # Temporary patch validation; no DB writes
ck3-index impact-patch <json-file>      # Temporary patch impact summary; no DB writes
ck3-index preflight-dirty               # Temporary dirty-file validation; no DB writes
ck3-index search <query>                 # Exact/prefix/FTS semantic discovery
ck3-index lookup-datatype <name>         # Engine data_types lookup
ck3-index bench                         # Hot query benchmark and query-plan risk check
ck3-index health                        # DB/schema/index/MCP health report
ck3-index validate                      # Full validation + compiler checks
ck3-index mcp                           # Start MCP server over stdio
```

## Data Sources

- Compiled local rule seeds: trigger/effect scopes, iterators, scope transitions, defines, on_actions, examples, modifiers, and sound events. Use `lookup_scope`, `lookup_shape`, `lookup_define`, `lookup_on_action`, `lookup_iterator`, `lookup_example`, `lookup_modifier`, and diagnostics for `event:/...` sounds.
- Do not treat the compiled seeds as Tiger authority. Confirm risky edits with local CK3 `.info` files, vanilla examples, Godherja examples, and indexed project evidence.
- Local wiki notes: `docs/CK3_EXPERIENCE_NOTES.md` summarizes workflow hints from the local CK3 modding wiki. Treat them as generation guidance, not engine authority.
- Regenerate rule data with `python tools/extract_all_scopes.py`, `python tools/extract_shapes.py`, `python tools/extract_defines.py`, `python tools/extract_on_actions.py`, and `python tools/extract_targets.py`.
