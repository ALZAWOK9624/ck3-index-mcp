# CWTools absorption: delta-only matrix

The goal is to absorb useful CWTools ideas into `ck3-index`, not to recreate a
second CWTools runtime. Every candidate is classified as:

- **Reuse**: `ck3-index` already owns the capability; do not reimplement it.
- **Delta**: CWTools exposes a real CK3 workflow gap; integrate it into the
  existing parser, SQLite file catalog, queries, diagnostics, or MCP surface.
- **Reject**: stale, game-specific, duplicated, or unsafe for this project.

## Evidence boundary

- Reviewed `Aa728848/cwtools-vscode` at
  `8372ec0c7136657de20f676c2f5128693246a134` (2026-07-14).
- Reviewed `cwtools/cwtools-ck3-config` at
  `27db56f995af6b73baebae43e04d044f1a0a0bbe` (2023-04-16).
- The 98-file CK3 CWT inventory is research evidence only. It is not bundled,
  loaded at runtime, or treated as current engine authority.
- Current engine logs, active game files, active Godherja files, project
  overrides, and indexed examples outrank the old CWT configuration.

## Capability decisions

| CWTools idea | Existing ck3-index evidence | Decision | Remaining work |
|---|---|---|---|
| Loss-aware PDXScript AST and source ranges | `internal/script/parser.go`; nodes table and patch preflight | **Reuse** | Keep one parser; add dialect behavior only where fixtures prove a gap |
| Workspace discovery and incremental indexing | `scan.go`, `scan_files.go`, files table, override/replace-path handling | **Reuse** | None from CWTools |
| Definition/type discovery | objects/object_defs, `.info` schema extraction, active examples | **Reuse** | Improve existing extractors from current evidence, never import 2023 paths as truth |
| Nested database object types | Generic directory singularization treated law groups as laws and their nested laws as fields | **Delta implemented** | Current `_laws.info` now drives separate `law_group`/`law` objects, clean patterns, default-law refs, and membership refs |
| Doctrine groups and doctrine references | Doctrine files were treated as religions; nested doctrines and `has/add/remove_doctrine` references were absent | **Delta implemented** | Current `_doctrines.info` now drives `doctrine_group`/`doctrine` objects and typed membership/script references; parameter keys remain fields, not invented database objects |
| Game-rule settings | Only top-level rules were indexed even though `has_game_rule` targets nested settings | **Delta implemented** | Current `_game_rules.info` now drives `game_rule`/`game_rule_setting` objects, default-setting refs, membership refs, and typed `has_game_rule` refs |
| Court-amenity categories and levels | Only four top-level categories were indexed; twenty selectable levels and amenity target/type references were absent | **Delta implemented** | Current `_court_amenities.info` and active scripts now drive category/level objects, defaults, membership, and contextual `amenity_level`/`set_amenity_level`/`add_amenity_level` refs |
| Canonical CK3 type names | Mechanical singularization emitted `focuse` and `deathreason`, breaking schema/pattern lookup and typed references | **Delta implemented** | Current `_focuses.info`, death-reason rules, engine shapes, and active scripts now use canonical `focus`/`death_reason` objects with focus, lifestyle, and death-reason refs |
| Religion subtypes | Religion families and fervor modifiers were flattened into `religion`, and family membership/doctrine links were absent | **Delta implemented** | Current `_religion_families.info` and `_fervor_modifiers.info` now drive canonical object types; religion-family and hostility-doctrine refs are typed |
| Lifestyle/perk references | Lifestyle and perk objects already existed, but current `has/add/remove_perk`, `has_lifestyle`, and `refund_perks` calls had no typed edges | **Delta implemented** | Added only the five engine-log-proven keys and empirical patterns; no separate skill-tree UI or duplicate query layer |
| Achievement groups | The special current root file `common/achievement_groups.txt` was indexed as object type `achievement_groups.txt`, with repeated objects named `group` | **Delta implemented** | Groups now use their `name` field as `achievement_group` identity and `order` entries resolve to existing `achievement` objects; the old CWT config did not cover this current format |
| Scheme sub-databases and references | Four current `.info` schemas were collapsed into one polluted `scheme` type, while engine-log-proven scheme and agent references were untyped | **Delta implemented** | Split the exact current paths into `scheme_type`, `scheme_agent_type`, `scheme_pulse_action`, and `scheme_countermeasure`; added only proven type/agent/pulse references, with no scheme UI |
| Current nested `.info` databases | Mechanical first-directory classification mixed 36 current sub-databases into 13 parent types, contaminating rules and examples (activities, artifacts, bookmarks, court positions, diarchies, domiciles, legends, raids, situations, struggles, subject contracts, tax slots, and travel) | **Delta implemented** | Exact current load paths now retain independent object namespaces while primary legacy names stay stable; all 5,216 objects were repartitioned without changing object count or diagnostics, and each rules query now reads only its own `.info` schema |
| Current sub-database relationships | After separating types, current scripts still exposed untyped membership edges between activities, artifact types, bookmarks, subject contracts, tax slots, situations, and legend seeds | **Delta implemented** | Added only field/list shapes demonstrated by current `.info`, engine logs, and active files; `artifacts/slots` is now a distinct `artifact_slot` database and `artifact_type` follows the current engine term |
| References, definition lookup, dependency graph | refs table, `query.go`, `dependency_graph`, patch impact | **Reuse** | Add new ref kinds only when a concrete CK3 syntax is currently missed |
| Definition stacks and active-candidate status | Definitions were returned in rank order, but rank order was presented as if it always selected one active definition | **Delta implemented** | Exact inspect now reports `unique`, `source_priority`, `ambiguous`, or `merged`; each candidate carries active/shadowed/merged status and a full source range. No parallel definition-stack table was added |
| File override provenance and override modes | Same-relative-path and descriptor `replace_path` behavior already existed, but the losing file had no stored explanation | **Delta implemented** | The existing files table now records cause, winning source/rank, and exact replace rule. Current `_on_actions.info` proves `on_action` declarations merge; stale generic CWT override modes were not imported |
| Structured unresolved reasons | refs only exposed a boolean, making runtime scopes look like missing definitions | **Delta implemented** | Existing refs now distinguish resolved, dynamic, and truly unresolved states with reasons such as `runtime_scope`, `missing_definition`, `missing_resource`, and `unknown_engine_symbol` |
| Event/on_action graph | Event calls embedded in `events`, `random_events`, `first_valid`, delayed `trigger_event`, on-action lists, and fallback were absent from the ordinary dependency graph | **Delta implemented** | Current `_on_actions.info` forms emit typed edges with relation, nearest phase, and exact confidence; `ck3_dependencies operation=event_chain` now turns the same refs into callers/callees, roots, leaves, cycles, and shortest paths. Its self-contained HTML inspector filters visible nodes, edges, and fit bounds from one result, distinguishes unresolved/dynamic/bounded continuation stubs, and rejects oversize input before JSON construction. The meta CSP pins the fixed script and forbids external resource/network loads; embedding policy remains the host's responsibility rather than a claimed `frame-ancestors` control. Event fields still reuse `object_fields`; no event-only database was added |
| Event runtime logic facts | Variables, character flags, and ordinary object refs inside event phases were searchable only as raw script or partial prefixes | **Delta implemented narrowly** | Event/on_action variable and character-flag mutations/reads are runtime-symbol edges; known object refs such as innovations carry their script relation and phase. This is graph evidence, not global-variable existence validation |
| Localisation/resource discovery | localization/resources tables and preflight diagnostics | **Reuse** | GUI preview now binds literal textures to this table and reports dynamic/missing refs; no second sprite index |
| Trigger/effect scopes and shapes | engine logs, generated scope/shape data, empirical examples | **Reuse** | CWT may be used manually as a drift clue, never as a competing validator |
| Completion/hover/definition/reference data | search, inspect, prepare-edit, rules, patterns, examples, MCP | **Reuse** | An editor adapter is a separate product decision, not CWTools absorption work |
| Rename safety | dependency graph plus `impact_patch` rename analysis | **Reuse** | Do not build an automatic writer until typed-reference coverage proves safe |
| Diagnostics and code explanations | validator, scope tracker, diagnostics table, `ck3_review` | **Reuse** | Only add mechanically proven GUI diagnostics |
| CWT type/cardinality/required/alias engine | Duplicates schema/pattern/scope systems and is based on stale CK3 rules | **Reject** | Independent `internal/cwt` runtime and CLI were removed |
| Jomini `@[ ... ]` arithmetic-expression token | The generic lexer split expressions containing whitespace, creating truncated values and false define refs | **Delta implemented** | One lexer now preserves nested/multiline expressions; field shapes label them as expressions instead of define refs |
| Scripted `@name = value` variables | CWTools could list values, while `ck3-index` only emitted untyped-looking define refs and could not locate the active declaration | **Delta implemented** | Scripted variables are ordinary `scripted_variable` objects with values and typed refs; engine `@NAMESPACE\|KEY` defines remain separate, GUI text tags such as `@warning_icon!` are excluded, and no new query tool or parallel table was added |
| GUI/Jomini parsing dialect | Generic parser previously missed `types`, `type`, templates and `blockoverride` | **Delta implemented** | CK3 fixtures now also distinguish layout children from visual vector properties such as `spriteborder`, `framesize`, `color`, and min/max size |
| GUI templates, custom types, inheritance and slots | No equivalent existed before this work | **Delta implemented** | Active GUI proved that engine `icon` primitives expose an opaque slot surface; unknown overrides are retained without hard-coding slot names |
| GUI query through active override state | Existing file index had no resolved GUI view | **Delta implemented** | `QueryGUI` and canonical `ck3_gui` reuse the files table; no parallel GUI DB |
| GUI preview renderer | Resolved renderer-neutral model now feeds a bounded CK3/Jomini layout scene and PNG | **Delta implemented** | Existing preview handles custom types, templates, and ordinary named GUI elements; it preserves native bounds, fits the PNG view, marks inferred sizes, and does not copy the Stellaris Webview/editor |
| GUI source write-back | Patch preflight exists, but no span edit engine | **Delta deferred** | Only implement after round-trip golden fixtures and reparse validation |
| Token-preserving formatter | No demonstrated CK3 project need from this review | **Reject for now** | Reconsider only with concrete formatting failures |
| Stellaris/entity/particle/tech-tree previews | Not a current CK3/Godherja workflow gap | **Reject** | None |
| Embedded AI/orchestration layer | Duplicates Codex and the MCP workflow | **Reject** | Keep ck3-index deterministic |

## CWTools MCP tool-by-tool audit

This table closes the read-only MCP surface one tool at a time. "Reuse" means
the capability is already reached through an existing `ck3-index` surface; it
does not justify another alias or handler.

| CWTools tool | ck3-index disposition | Decision |
|---|---|---|
| `query_types` | `ck3_search`, `ck3_inspect`, and `ck3_workspace operation=object_types` | **Reuse** |
| `query_rules` | `ck3_script_reference` plus `ck3_prepare_edit operation=rules` | **Reuse** |
| `query_cwt_schema` | Current `.info` rules, empirical patterns, and active examples through `ck3_prepare_edit` | **Reuse; reject stale CWT runtime** |
| `search_rule_capabilities` | Search indexed script keys, then verify with scope, shape, datatype, and example lookups | **Reuse** |
| `explain_scope` | `ck3_script_reference kind=scope` | **Reuse** |
| `parse_pdx_fragment` | In-memory `ck3_review` / `ck3_preflight operation=patch` parser gate | **Reuse; no fragment-only alias** |
| `query_scope` | Scope traces produced by review/preflight diagnostics | **Reuse; arbitrary cursor LSP is an editor-adapter concern** |
| `get_diagnostics` | `ck3_diagnostics` and `ck3_review` | **Reuse** |
| `analyze_diagnostic_error` | `ck3_diagnostics operation=explain` with provenance and confidence | **Reuse** |
| `query_project_profile` | `ck3_workspace operation=overview` | **Reuse** |
| `query_project_knowledge` | SQLite object/ref/field catalog, workspace overview, inspect, and dependencies | **Reuse; no second knowledge DB** |
| `query_workspace_index` | Existing persistent SQLite catalog and incremental scan | **Reuse** |
| `explore_pdx_project` | `ck3_dependencies` plus `ck3_workspace` | **Reuse** |
| `query_localisation_index` | `ck3_search` and `ck3_inspect operation=localization` | **Reuse** |
| `get_pdx_block` | Exact object/example snippets; `ck3_gui operation=file/type/template` for GUI structure | **Reuse** |
| `get_completion_at` | `ck3_prepare_edit` supplies bounded rules, patterns, examples, and related refs | **Reuse; cursor completion belongs in an editor adapter** |
| `document_symbols` | Indexed object definitions and bounded GUI file trees | **Reuse for agent workflows; no LSP alias** |
| `workspace_symbols` | `ck3_search` with source/path filters | **Reuse** |
| `query_definition` | `ck3_inspect operation=definition` after semantic search | **Reuse** |
| `query_definition_by_name` | `ck3_inspect operation=definition` | **Reuse** |
| `query_references` | `ck3_inspect operation=references` and `ck3_dependencies` | **Reuse** |
| `query_scripted_effects` | `scripted_effect` objects through search/workspace/inspect | **Reuse** |
| `query_scripted_triggers` | `scripted_trigger` objects through search/workspace/inspect | **Reuse** |
| `query_enums` | Current engine datatypes, `.info` fields, and indexed active examples | **Reuse; old CWT enum lists are not authority** |
| `query_static_modifiers` | `ck3_script_reference kind=modifier` plus modifier objects | **Reuse** |
| `query_variables` | `scripted_variable` objects, values, refs, search, and inspect | **Delta integrated into existing graph** |
| `get_entity_info` | `ck3_inspect`, `ck3_dependencies`, and bounded GUI models | **Reuse** |

The reviewed MCP registry exposes no write tools. `ck3-index` keeps the same
read-only boundary: patch and impact tools analyze complete proposed contents
in memory but do not edit workspace files.

## Project-knowledge database field audit

The newer CWTools project-knowledge database was compared at field level, not
only by tool name. The useful differences were folded into existing tables and
queries:

| CWTools table/field family | ck3-index disposition |
|---|---|
| `definitions`, `definition_subtypes`, `definition_stacks`, `stack_candidates` | Keep independent CK3 namespaces as object types; expose query-time candidate mode/status instead of duplicating objects into another stack schema |
| `override_modes`, `override_mode_info` | Store only override behavior proven by current load rules: same logical path, descriptor `replace_path`, and merging on-actions |
| `references_graph` relation/domain/confidence fields | Existing refs now carry `relation`, `phase`, `confidence`, and structured resolution reasons; dependency queries reuse them |
| `unresolved` kind/message/resolution | Folded into refs as dynamic versus genuinely unresolved evidence so AI guidance does not recommend fixing runtime scopes |
| `event_nodes`, `event_edges`, `event_logic` | Object ranges and direct event fields remain in objects/object_fields; event calls, on-action calls, variables, flags, and phase-aware object refs remain in the ordinary dependency graph |
| `archetypes` | Existing vanilla-first examples and empirical patterns already provide current CK3 exemplars; a second archetype table would duplicate them |
| `domains` | Existing object type, source, logical path, rank, and architecture hotspots already provide this partitioning; no extra domain registry is justified |

## VS Code feature audit

The repository also advertises IDE and Stellaris features outside its MCP
surface. They were reviewed separately so they do not silently become
`ck3-index` requirements.

| VS Code feature | Decision for ck3-index | Reason |
|---|---|---|
| Concurrent LSP reads and lazy line-offset cache | **Reject as an index feature** | SQLite indexes and bounded read-only handlers already own server concurrency; cursor latency belongs to an editor adapter |
| Hover, CodeLens, semantic tokens, folding, and cursor completion | **Reject as core; reuse evidence providers** | Search, inspect, rules, patterns, examples, and source ranges provide the data; presentation belongs to an IDE client |
| `@[...]` macro handling | **Delta implemented** | The shared parser now preserves nested and multiline CK3 expressions without a second evaluator |
| Stellaris `value:xxx\|` evaluation | **Reject** | No active CK3/Godherja/project example was found; do not import Stellaris syntax as CK3 authority |
| Incremental scripted type refresh | **Reuse** | `scan --files` refreshes selected project files and affected refs without a full rebuild |
| GUI canvas and layer tree | **Delta implemented narrowly** | CK3 GUI gets a renderer-neutral resolved tree, bounded layout preview, texture provenance, and named-element preview through the existing index |
| GUI drag/resize source write-back | **Deferred** | Requires token-preserving round-trip edits and stale-span protection; current patch tools remain analysis-only |
| Stellaris 9-slice/frame animation renderer | **Reject for CK3** | The advertised implementation targets Stellaris sprite contracts; CK3 preview reports literal/dynamic textures without claiming runtime fidelity |
| 3D solar-system editor | **Reject** | Stellaris-only data model |
| Technology/event Cytoscape graph | **Reuse graph idea; delta delivered** | `ck3_dependencies operation=event_chain format=html` now delivers a self-contained interactive HTML inspector over the bounded typed graph. Host embedding, IDE integration, and any richer client presentation remain client concerns |
| Entity, mesh, skeletal animation, and particle editors | **Reject** | Not a demonstrated CK3/Godherja scripting gap |
| Embedded multi-agent coprocessor and memory compression | **Reject** | Duplicates the host agent/orchestration layer |
| Workspace localization watcher | **Reuse** | Localization is already part of incremental source scanning, override state, search, and diagnostics |
| Vanilla compare and bottom-up block merge | **Reuse analysis; defer write-back** | Active override state, dependency impact, and patch preflight cover safe analysis; automatic source mutation remains outside the read-only core |
| IDE bridge, global vanilla cache, and upgrade-stable proxy | **Reject as deployment coupling** | `ck3-index` owns one workspace SQLite cache with explicit rule version, source priority, health, and MCP process boundaries |

## Integration gates

1. A feature must fill a demonstrated gap in the current `ck3-index` behavior.
2. It must reuse the existing parser, file override state, SQLite catalog, and
   MCP privacy model whenever those layers already solve the problem.
3. Old CWT data cannot emit production diagnostics by itself.
4. GUI queries accept only indexed, source-root-relative `gui/` paths; public
   mode excludes rank-1 project files.
5. Resolver cycles and expansion depth are bounded before previewing.
6. Full-directory tests and real Mod measurements are required in addition to
   small fixtures.
7. Preview output must distinguish native layout evidence from view-only scale
   and must label inferred dimensions as approximate.
