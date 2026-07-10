# ck3-index

Read-only CK3/Godherja mod indexer, dependency query tool, validator, and MCP wrapper. Built-in local scope/shape rules help LLM-assisted code generation without requiring Tiger at runtime.

## Quick Start

```powershell
# Full rebuild (first time or after source changes)
ck3-index scan --clean

# Incremental update after editing mod files
ck3-index scan

# Refresh one or a few current-project files without a full scan
ck3-index scan --files common/decisions/example.txt localization/english/example_l_english.yml

# Diagnostic summary by code
ck3-index diag_stats

# Golden accuracy regression cases
ck3-index accuracy

# Hot-path health and benchmark checks
ck3-index health
ck3-index bench

# Full validation + compiler checks
ck3-index validate

# Start MCP server for LLM integration
ck3-index mcp
```

## MCP Tools (36)

### High-Level Entry Points

| Tool | Description |
|---|---|
| `ck3_search` | Exact ID/prefix first, then FTS5 over objects, paths, resources, fields, datatypes, and English/Chinese localization values |
| `ck3_inspect` | One-id aggregate classification before choosing a specialized query |
| `ck3_review` | Full proposed-file or dirty-file review with parser, scope, refs, localization, and resource checks |

### Index Queries

| Tool | Description |
|---|---|
| `query_object` | Active object definitions |
| `query_object_types` | Indexed object types and counts |
| `find_refs` | Incoming/outgoing references with resolve status |
| `query_loc` | Localization key values across languages |
| `query_resource` | Resource file existence and references |
| `query_examples` | Vanilla-first script examples by object type; `type:term` also searches object bodies |
| `query_rules` | Schema fields from local `.info` files |
| `query_patterns` | Empirical field shapes learned from active indexed scripts |
| `architecture_overview` | Cached codebase-memory style workspace map with grouped source/object/ref/diagnostic hotspots |
| `dependency_graph` | Codebase-memory style graph around an object or referenced id; supports `depth` 1-2 and CK3 semantic edges |
| `inspect_object` | Aggregate: definitions, refs, loc, diagnostics |
| `prepare_edit` | Aggregate: examples, schema, edit context |
| `preflight_code` | Aggregate: generation/edit blockers, unresolved refs, loc candidates |
| `preflight_patch` | Temporary in-memory patch validation without scan or SQLite writes; supports `upsert`, `delete`, and `rename` patch ops |
| `impact_patch` | Temporary in-memory impact summary for proposed upsert/delete/rename operations |
| `preflight_dirty` | Temporary in-memory validation of changed current-project files against the SQLite cache |
| `diagnose_key` | Aggregate: classify any id |
| `validate_project` | Chat-fast cached parser/index/compiler summary limited to the current project source plus global diagnostics |
| `health_check` | Compact DB/schema/index/MCP health signal for low-cost models |
| `explain_diagnostic` | Aggregated diagnostics filtered by code, source, path prefix, and confidence |

### Local Scope & Shape Rules

| Tool | Description |
|---|---|
| `lookup_scope` | Engine-log scope rule and confidence, with Tiger fallback |
| `lookup_datatype` | Engine `data_types` signatures, descriptions, definition types, return types, and source |
| `lookup_shape` | Value shape expected by a trigger/effect (boolean, compare, scope, item, block) |
| `lookup_define` | Check if @define name exists in game defines |
| `lookup_on_action` | Check if on_action name is known |
| `lookup_iterator` | Check iterator/scope input and output hints |
| `lookup_example` | Trigger/effect description and syntax example |
| `lookup_modifier` | Static modifier validity and use areas |

## Architecture

```text
ck3-index scan --clean
  - Walk sources (project > godherja > game > translation)
  - File override detection by rel_path
  - Concurrent worker pool for parse, hash, and lint
  - Serial DB insert with prepared statements and WAL mode
  - Index build, validator pass, and reference resolution
  - SQLite DB: objects, refs, localization, resources, diagnostics, engine data, coarse map context, and FTS5

ck3-index mcp
  - JSON-RPC over stdio with Content-Length framing
  - Read-only SQLite connection; schema/index maintenance belongs to scan/health paths
  - Read-only queries, local rule lookups, patch overlay checks, compact health checks
  - Codebase-memory style architecture overview cache and dependency graph tools
```

Current implementation still keeps the core code under `internal/indexer` as a compatibility facade. New performance work is being carved out around query/index health, patch overlays, and hot-path LLM result shaping before a deeper package split.

## LLM Fast Path

- Start broad discovery with `ck3_search`, investigate one id with `ck3_inspect`, and review code with `ck3_review`; all specialized tools remain available.
- For CK3 objects, references, localization, resources, diagnostics, and map context, use ck3-index before raw text search. Use `rg` only to inspect the exact files returned as evidence.
- Use `health_check` first when a bot session starts or when results look stale; it gives a short trust signal without dumping private paths.
- Use `prepare_edit <type>` before generation; it returns examples, schema fields, and explicit `guidance`.
- Use `query_patterns <type>` to see how active indexed scripts actually structure object fields; this is empirical evidence, not official schema.
- Use `architecture_overview` at the start of broad investigations; it summarizes indexed sources, object-type distribution, reference-kind pressure, and diagnostic hotspots without dumping raw files. Scan writes a cached summary, and uncached DBs fall back to live aggregation.
- Use `dependency_graph <id>` when rename/delete/edit impact matters; it gives incoming/outgoing edges plus CK3 semantic edges such as tradition parameters unlocking men-at-arms. Set `depth` to `2` only for deliberate neighbor expansion.
- Use `preflight_code <id>` before and after generation to cheaply catch definition conflicts, unresolved object/resource/sound refs, missing localization candidates, and related diagnostics.
- Use `preflight_patch` while drafting edits: send complete proposed file contents and it checks parser/compiler diagnostics plus object/localization/resource refs with an in-memory overlay. It sets `needs_scan=true` because SQLite is not refreshed. Patch ops can be `upsert`, `delete`, or `rename`; delete/rename responses include impact evidence.
- Use `impact_patch` when you only need the risk surface for a proposed delete/rename/upsert and do not need full validation evidence.
- Use `preflight_dirty` for a quick local gate over files changed on disk in the current project; it still does not write SQLite.
- Use `scan --files <relpath...>` after writing a small number of current-project files. It refreshes only those files and affected refs/validator diagnostics; use full `scan` for source config changes, deletes, or override-chain uncertainty.
- Use `query_examples type:term` for nested syntax such as `event:add_character_modifier` or `decision:trigger_event`; snippets are centered on the matched term when possible.
- Use `lookup_scope`, `lookup_datatype`, `lookup_shape`, `lookup_iterator`, `lookup_example`, and `lookup_modifier` for unfamiliar keys. Live engine logs take precedence over compiled Tiger hints.
- Map tools intentionally expose topology and coarse geography only: graph radius, eight directions, coast/inland, island tendency, terrain mix, and relative map region. They are not precision geometry tools.
- MCP `validate_project` is intentionally cached, read-only, and project-scoped so upstream/reference diagnostics do not drown out the editable submod. Run `ck3-index scan` or CLI `ck3-index validate` after edits to refresh diagnostics; use `ck3_search` with a `source` filter when auditing upstream layers.
- Use `ck3-index bench` after performance-sensitive changes. Hot query plans should not report `SCAN refs` or `SCAN objects` risks.
- Use `ck3-index accuracy` after changing extraction, refs, resources, localization, scope rules, or diagnostics. It runs fixture-based golden cases for known false positives and false negatives.
- Scans are restricted to CK3 load roots relative to each configured source. Root-level backups, tools, docs, caches, and temporary trees are pruned even if they contain nested `common/`, `events/`, or `history/` directories.
- Culture definitions now expose typed dependencies for pillars, traditions, name lists, and parent cultures; culture traditions, pillars, innovations, and name lists have distinct object types.

## Data Sources

Some rule seeds were derived from ck3-tiger source and CK3 engine dumps, then compiled directly into the binary. Treat them as local rule hints, not as Tiger authority:

| Source | Entries | Go file |
|---|---|---|
| `triggers.rs` | 1,315 triggers | `scope_data.gen.go` + `shape_data.gen.go` |
| `effects.rs` | 848 effects | same |
| `iterators.rs` | 1,324 iterators | `scope_data.gen.go` |
| `targets.rs` | 278 scope transitions and typed scope prefixes | `scope_transitions.gen.go` |
| `defines.rs` | 1,903 defines | `defines_data.gen.go` |
| `on_action.rs` | 200 on_actions | `on_action_data.gen.go` |
| CK3/Tiger sound dump | event:/ sound events | `tiger_extra.gen.go` |

Regenerate after rule seed updates:

```powershell
python tools/extract_all_scopes.py   > internal/indexer/scope_data.gen.go
python tools/extract_shapes.py       > internal/indexer/shape_data.gen.go
python tools/extract_defines.py      > internal/indexer/defines_data.gen.go
python tools/extract_on_actions.py   > internal/indexer/on_action_data.gen.go
python tools/extract_targets.py      > internal/indexer/scope_transitions.gen.go
```

Local wiki-derived workflow notes are summarized in `docs/CK3_EXPERIENCE_NOTES.md`. Treat them as generation guidance, not engine authority.
