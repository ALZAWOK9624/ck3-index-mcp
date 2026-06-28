# ck3-index

Read-only CK3/Godherja mod indexer, dependency query tool, validator, and MCP wrapper. Built-in local scope/shape rules help LLM-assisted code generation without requiring Tiger at runtime.

## Quick Start

```powershell
# Full rebuild (first time or after source changes)
ck3-index scan --clean

# Incremental update (after editing mod files) — ~12s
ck3-index scan

# Diagnostic summary by code
ck3-index diag_stats

# Full validation + compiler checks
ck3-index validate

# Start MCP server (for LLM integration)
ck3-index mcp
```

## MCP Tools (21)

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
| `inspect_object` | Aggregate: definitions, refs, loc, diagnostics |
| `prepare_edit` | Aggregate: examples, schema, edit context |
| `preflight_code` | Aggregate: generation/edit blockers, unresolved refs, loc candidates |
| `diagnose_key` | Aggregate: classify any id |
| `validate_project` | Chat-fast cached parser/index/compiler diagnostic summary |
| `explain_diagnostic` | Diagnostics filtered by code |

### Local Scope & Shape Rules (LLM code generation helpers)
| Tool | Description |
|---|---|
| `lookup_scope` | Scope type required by a trigger/effect key |
| `lookup_shape` | Value shape expected by a trigger/effect (boolean, compare, scope, item, block) |
| `lookup_define` | Check if @define name exists in game defines |
| `lookup_on_action` | Check if on_action name is known |
| `lookup_iterator` | Check iterator/scope input and output hints |
| `lookup_example` | Trigger/effect description and syntax example |
| `lookup_modifier` | Static modifier validity and use areas |

## Architecture

```
ck3-index scan --clean
  │
  ├── Walk sources (project > godherja > game > translation)
  ├── File override detection (rel_path grouping)
  ├── Concurrent worker pool (parse + hash + lint)
  ├── Serial DB insert (prepared statements, WAL mode)
  ├── Index build + validator pass + ref resolution
  └── SQLite DB: objects, refs, loc, resources, diagnostics

ck3-index mcp
  │
  ├── JSON-RPC over stdio with Content-Length framing
  └── Read-only queries + local rule lookups
```

## LLM Fast Path

- Use `prepare_edit <type>` before generation; it returns examples, schema fields, and explicit `guidance`.
- Use `query_patterns <type>` to see how active indexed scripts actually structure object fields; this is empirical evidence, not official schema.
- Use `preflight_code <id>` before and after generation to cheaply catch definition conflicts, unresolved object/resource/sound refs, missing localization candidates, and related diagnostics.
- Use `query_examples type:term` for nested syntax such as `event:add_character_modifier` or `decision:trigger_event`; snippets are centered on the matched term when possible.
- Use `lookup_scope`, `lookup_shape`, `lookup_iterator`, `lookup_example`, and `lookup_modifier` for unfamiliar keys. Lookup tools return short guidance strings for low-cost models.
- MCP `validate_project` is intentionally cached and read-only so chat does not block on full validation. Run `ck3-index scan` or CLI `ck3-index validate` after edits to refresh diagnostics.

## Data Sources

Some rule seeds were derived from ck3-tiger source and CK3 engine dumps, then compiled directly into the binary. Treat them as local rule hints, not as Tiger authority:

| Source | Entries | Go file |
|---|---|---|
| `triggers.rs` | 1,315 triggers | `scope_data.gen.go` + `shape_data.gen.go` |
| `effects.rs` | 848 effects | same |
| `iterators.rs` | 1,324 iterators | `scope_data.gen.go` |
| `targets.rs` | 222 scope transitions | `scope_transitions.gen.go` |
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
