---
name: ck3-coding
description: CK3 mod coding workflow. Use when editing, reviewing, generating, or validating Crusader Kings III mod scripts, localization, resources, events, traits, decisions, history, GUI, or related ck3-index/MCP diagnostics in a configured workspace.
---

# CK3 Coding

Use this skill for CK3 mod scripting work. Treat `ck3-index` as the first source of truth for definitions, references, localization, resources, and diagnostics. Treat compiled scope/shape rules as hints that must be checked against indexed examples before risky edits.

For CK3 semantic questions, do not begin with `rg`. Call `ck3_search`, `ck3_inspect`, `ck3_prepare_edit`, or the relevant map tool first; use `rg` only to inspect the exact text behind indexed evidence. If the MCP tools are not attached to the current session, use the equivalent `ck3-index` CLI command instead of silently falling back to broad text search.

<!-- BEGIN GENERATED MCP TOOLS -->
## MCP Tools (standard: 29; expert: 57)

The standard profile advertises the canonical tools below. The expert profile also advertises deprecated compatibility names; all legacy names remain callable during the compatibility window.

### Core Tools

| Tool | Purpose |
|---|---|
| `ck3_search` | Search when the exact CK3 id is unknown. Returns ranked object, localization, resource, reference, diagnostic, datatype, and script-key evidence. |
| `ck3_inspect` | Inspect one exact CK3 id, key, or resource path after discovery. Definition views include resolution status, override provenance, event fields, and character static/history profiles; reference views include relation, phase, confidence, and unresolved reasons. compare performs a bounded read-only source-versus-upstream object comparison for an exact typed id. |
| `ck3_review` | Review complete proposed CK3 files, or current dirty project files when none are supplied. Performs read-only parser, scope, reference, localization, and resource checks. |
| `ck3_workspace` | Inspect indexed workspace structure before choosing a specific object. The overview includes object/ref hotspots, override causes, event relations, dynamic refs, and true unresolved refs. on_action_evidence is a bounded read-only reconciliation of live engine, static Tiger, and adjacent vanilla-comment root contracts. |
| `ck3_dependencies` | Trace semantic dependencies around one CK3 id. Use neighborhood for the bounded general graph or event_chain for caller/callee topology with roots, leaves, cycles, shortest paths, and unresolved calls. event_chain can additionally return a self-contained CSP-contained interactive HTML inspector. |
| `ck3_prepare_edit` | Load edit evidence before generating CK3 script. Defaults to combined context; operation can request examples, schema rules, or empirical patterns only. |
| `ck3_preflight` | Run a read-only gate for an indexed subject, proposed complete files, or current dirty files. Select subject, patch, or dirty with operation. |
| `ck3_impact` | Analyze proposed upsert, delete, and rename operations before editing. Returns read-only dependency and unresolved-reference risks. |
| `ck3_diagnostics` | Inspect cached project diagnostics without rescanning. Defaults to summary; explain filters one diagnostic code and optional provenance fields. |
| `ck3_script_reference` | Look up one local engine or script-rule fact. Select scope, datatype, shape, define, on_action, iterator, example, or modifier with kind; on_action responses keep live engine rules authoritative while adding bounded review-only vanilla-comment and structured static-Tiger evidence when available. |
| `ck3_health` | Check whether the database, schema, indexes, and MCP registration are trustworthy. Returns a short path-redacted health report. |
| `ck3_package` | Validate proposed CK3 text and binary files, generate canonical descriptors, and create a portable manual-install ZIP in the configured temporary artifact root. Does not install or modify a live mod directory. |
| `ck3_gui` | Inspect active CK3 GUI files through the existing index. Summarize the workspace, parse one file, resolve cross-file custom type/template dependencies, or render a bounded diagnostic PNG and/or self-contained HTML preview. HTML supports tree browsing, clipped scrollbox and grid navigation, indexed dynamic-texture samples, and controlled visual behavior simulation. model_samples can instantiate bounded caller-provided datamodel rows from one exact item template; runtime_facts and action_effects never execute arbitrary Jomini code. |

### Map Tools

| Tool | Purpose |
|---|---|
| `map_migration_snapshot` | Persist a content-addressed old-upstream/current-project baseline from configured private sources before an upstream map update. Does not accept client paths or modify either source. |
| `map_province_migration` | Build a complete local test fork from a new configured upstream, replay project changes by conservative three-way merge, rewrite proven province references, and publish only after strict validation. Writes only the configured artifact area. |
| `map_asset_audit` | Audit active CK3 map rasters for province-definition coverage, PNG encoding, river palette-index semantics, and orthogonal river topology. This absorbs AzgaarToCK3's distinctive validators without duplicating ck3-index parsing or geometry. |
| `map_province_mapping` | Compare two configured CK3 province rasters through a Delaunay piecewise-affine warp. Returns auditable overlap shares and one-to-one, renumber, split, merge, complex, and unmapped groups without writing converter files. |
| `map_province_info` | Inspect one province's exact geometry, titles, scripted terrain, observed surface-material blend, texture resources, and direct boundaries. Returns read-only precision context and classified neighbors. |
| `map_physical_context` | Inspect normalized elevation, terrain, observed gfx/map/terrain surface-material blends and texture resources, composite rivers, water bodies, relative bathymetry, and physical barriers without modifying map assets. Region coast queries can include adjacent water in one bounded cached-database call. Observed, derived, and inferred facts remain explicitly separated. |
| `map_neighbors` | Inspect the bounded graph neighborhood around a province or landed title. Returns radius groups, direction, distance, and boundary classifications. |
| `map_spatial_relation` | Compare the exact spatial relation between two provinces. Returns centroid delta, bearing, distance, direct border, and nearby barriers. |
| `map_strategic_passages` | Inspect explicit adjacencies separately from pixel-border neighbors. Returns straits, crossings, underground links, and off-map gateways. |
| `map_title_context` | Inspect province coverage, holder, culture, faith, and neighboring titles for one landed title. Returns read-only historical and visual context. |
| `map_assignment_plan` | Generate review-only religion or placeholder-character assignment recommendations. Returns patch previews privately and removes them when visibility=public. |
| `map_building_candidates` | Rank auditable special-building candidates for a province or landed title. Returns terrain, holding, water, culture, and border evidence without writing files. |
| `map_recipe_catalog` | List supported map recipes, levels, transforms, layers, palettes, and guidance. Use this before building a custom metric or render specification. |
| `map_build_metric` | Build an auditable indexed or source-noted map metric before rendering. Returns values, quantiles, outliers, provenance, and warnings. |
| `map_route` | Resolve CK3 places and calculate a deterministic legal land, sea, or mixed route over indexed province topology. Returns compact route points, legs, corridor context, diagnostics, and pixel-distance caveats. |
| `map_render` | Render a read-only adaptive CK3 atlas with automatic resolution when dimensions are omitted. Returns structured metadata and an in-memory PNG without accepting client file paths. |

### Compatibility

Set `CK3_INDEX_MCP_PROFILE=expert` only when an existing client still discovers legacy specialist names. New prompts and `next_queries` use canonical names.
<!-- END GENERATED MCP TOOLS -->

## Workflow

1. Query before editing:
	- Start bot sessions with `ck3_health` if index trust is uncertain.
	- Use `ck3_search` for broad discovery and `ck3_inspect` for one-id investigation before raw text search.
	- Use `ck3_workspace` for broad repository orientation and `ck3_dependencies` for bounded semantic impact tracing.
	- When debugging an event or on_action chain, call `ck3_dependencies` with `operation=event_chain`; select `callers`, `callees`, or `both` with `direction`, and inspect its roots, leaves, cycles, shortest paths, and unresolved calls before editing.
	- When editing `history/characters`, call `ck3_inspect` on `character:<id>` and use its `character_field` and dated `character_history` evidence; confirm father, mother, spouse, employer, dynasty, trait, culture, faith, and death-reason refs before writing.
	- Before an upstream map update, call `map_migration_snapshot` with the configured project and old-base source. After the update, call `map_province_migration` with that `snapshot_id` and the configured new target. Review stable conflicts and resubmit explicit resolutions; never globally replace bare province numbers or treat a blocked report as a usable Mod.
	- Use `map_province_mapping` separately when only read-only mapping evidence is needed. Review renumber, split, merge, complex, and unmapped groups; add geographic control points when the raster geometry moved, and never infer a one-to-one migration from ids alone.
	- Before reasoning about mountains, valleys, drainage, major rivers, coastlines, ocean basins, or seabed depth, call `map_physical_context`. Treat `river_provinces` as major river channels, keep them out of bathymetry, and never convert normalized elevation, relative depth, pixel catchment, or width proxies into real-world units.
	- For painted ground-cover or map-surface questions, call `map_physical_context` with `operation=surface`, or use `map_province_info` for one province. Keep scripted `common/province_terrain` separate from the observed `gfx/map/terrain` blend: `detail_index.tga` and `detail_intensity.tga` are the placement-and-weight evidence, while mask and DDS paths are resource evidence. Do not infer climate, ecology, or gameplay terrain from texture filenames alone.
	- For a region-level coast, shelf, or shallow-versus-deep question, call `map_physical_context` once with `target_type=region`, an exact `region:<id>`, `operation=oceanography`, and `include_adjacent_water=true`. Do not build a full metric or loop over `map_neighbors`; reserve `map_build_metric` for thematic rendering and explicit value tables.
	- For route-map work, call `map_route` once with exact origin/destination subjects and `year`. Pass its complete route object to `map_render` with `auto_context=true`, then use `route_points_output` for HTML/SVG overlays. Never loop over `map_neighbors`, use endpoint straight-line distance as a route, guess crop/padding transforms, or emit new requests with deprecated `history_year`.
	- Geographical-region definitions are first-class `geographical_region:<id>` objects. Use `ck3_search` to resolve an exact region id, then `ck3_inspect` or `ck3_dependencies` for its definition, parent/child regions, and script consumers. Use map tools only for province membership and physical aggregation; do not raw-search the whole workspace for region dependencies.
	- Use `ck3_review` as the default code-review gate; canonical specialist tools remain available for precise follow-up.
   - Use `ck3_prepare_edit` before generating or changing script. Select `examples`, `rules`, or `patterns` with its `operation` field instead of discovering legacy specialist names.
   - Use `ck3_script_reference` with the appropriate `kind` for unfamiliar scopes, datatypes, shapes, defines, on_actions, iterators, examples, or modifiers.
   - Use `ck3_preflight` with `operation=patch` on proposed complete file contents before writing them.
   - Use `ck3_impact` before risky delete/rename changes.
   - Use `ck3_preflight` with `operation=dirty` for quick local checks of changed project files.
   - Use `scan --files <relpath...>` after writing a small number of current-project files.
   - Use `accuracy` after changing extraction, references, resources, localization, scope rules, or diagnostics.
   - Read `guidance` first; it is written for low-cost models.

2. Respect configured source priority:
   - Treat the configured rank as the effective load order; lower rank wins same-path overrides.
   - Treat the active Mod source as writable only when the task authorizes an edit.
   - Treat game installations, upstream Mods, translations, and other reference sources as read-only unless explicitly placed in scope for editing.

3. File override semantics: CK3 loads files by `rel_path`; same-path files from higher-priority sources replace lower ones entirely. `ck3-index` detects overridden files and excludes them from active queries.

4. Source boundary semantics: only source-root-relative CK3 load roots (`common`, `events`, `history`, `gui`, `localization`, `gfx`, `map_data`, and `sound`) are indexed. Root-level backups, tools, docs, caches, and temporary folders are intentionally ignored even when they contain nested CK3-looking paths.

5. Never use localization text alone as proof of mechanics. Confirm in scripts, history, GUI, or indexed definitions.

6. During the edit loop, prefer canonical `ck3_preflight` operations and `scan --files` over repeated full `scan`; follow every scan with `diag_stats`. Before final release or a large handoff, run `ck3-index scan`, `ck3-index validate`, and `ck3-index diag_stats`.

7. Keep generated code conservative:
   - Match nearby file style.
   - Prefer existing scripted triggers/effects/values.
   - Add new localization keys with clear prefixes.
   - Avoid touching upstream files.

8. For GUI work, inspect before imitating:
   - Call `ck3_gui` with `operation=summary`, then narrow with `file`, `type`, or `template` instead of treating raw GUI text as a flat format.
	- After changing a named widget or custom type, call `ck3_gui` with `operation=preview`, `format=both`, `html_mode=inspector`, and the appropriate `language` (`raw`, `english`, `simp_chinese`, or `bilingual`). Use the PNG for immediate visual review and the self-contained inspector for tree browsing, zoom, search, localization switching, property inspection, and controlled visual-state simulation. Use `html_mode=static` only when a script-free artifact is required.
	- Read `preview.nodes`, `semantics`, `textures`, `approximate`, and `warnings` before claiming fidelity. Runtime `visible`, `enabled`, numeric `value`, `down`, `selected`, `datacontext`, repeated `onclick`, localization, effects, and dynamic textures are preserved as expressions. The bounded preview evaluator may compose `And`, `Or`, `Not`, and typed comparisons from explicit `runtime_facts`; direct numeric facts or literals may drive bounded progress values. It never executes arbitrary Jomini code or invents missing facts.
	- Treat `texture_ref.embedded=true` as evidence that a bounded indexed PNG or supported DDS top mip was decoded into the HTML. Literal `modify_texture` nodes without a size fill their parent; allowlisted blend modes use fixed CSS mappings and the nearest textured ancestor's alpha mask. Unknown blends, missing parents, unsupported, oversized, and dynamic textures remain explicit approximations; never infer their appearance.
	- When `texture_ref.resized=true`, compare `source_width/source_height` with `width/height`: ck3-index deterministically downsamples to the largest rendered use of that indexed asset and never upscales it. This is display evidence at the requested HTML resolution, not proof of the source texture's full-resolution appearance.
	- Treat node `mirror` as indexed visual evidence. The HTML replays horizontal, vertical, and combined mirroring on the texture layer only; it does not flip control coordinates or text. Other image transforms and shader effects still require in-game validation.
	- Treat `texture_frames` and `texture_slice` as indexed visual evidence. Literal `framesize + frame` and button state frames are replayed in HTML; `Corneredstretched`/`Corneredtiled` borders use bounded nine-slice rendering, including per-frame crops when the same control combines a sprite sheet with `spriteborder`.
	- Treat `state_definition` as a behavior-only preview fact. The inspector may replay explicit `alpha` and `duration` for standard hover/leave states, but animation templates, scripted actions, and engine state machines remain unevaluated.
	- Treat `text_localization` and `tooltip_localization` as indexed display evidence. Static nested localization keys and macros such as `[aspect_blood]`, `[concept|E]`, and `$blood_name$` may be expanded from the same active language index with a bounded four-level/256-key closure; inspect `value` versus `resolved_value` when provenance matters. The preview may compile remaining simple `[scope.path]`, `$MACRO$`, numeric formatting, and bounded lazy `SelectLocalization` / `Select_CString` / `AddTextIf` / `AddLocalizationIf` branches into deduplicated runtime text plans driven only by explicit `runtime_facts`. Static branch keys use the active English/Chinese localization closure; dynamic branches remain explicit string facts. A remaining `<unknown>`, `<runtime>`, `partial=true`, unresolved marker, or unsupported plan means the selected localization still depends on missing or complex game context; never replace it with an invented character, title, value, or condition.
	- Use bounded `sample_values` only when a reproducible review scenario is useful. Require exact expression/key matches, check every `matched_nodes` count and `unused=0`, and describe `scenario.source=provided` values as examples rather than observed game state. A `texture` sample must map the exact dynamic texture expression to an already indexed source-root-relative `gfx/` PNG/DDS/TGA path; never pass a URL or client filesystem path.
	- For a `fixedgridbox` or `dynamicgridbox` datamodel list, use bounded `model_samples` instead of inventing virtualized rows. Select exactly one grid by literal `target` and/or `datamodel`, provide stable unique row ids plus exact row-local `text|texture|visible|enabled` samples, and require `model_samples.unused_samples=0`. Treat every cloned row as caller-provided review data, not a scanned save-game fact.
	- Treat GUI `path_prefix` as symbol-selection scope, not a dependency boundary. Compare `files` with `resolution_files`; a narrow prefix may legitimately select one definition while inheritance, templates, and `blockoverride` resolve across every active GUI file.
	- Prefer `runtime_facts` for shared atomic state such as `IsPauseMenuShown`, `GetPlayer.IsValid`, or a numeric scope path. Direct numeric facts can drive `min`, `max`, and `value` bindings while preserving the original expressions and un-clamped results; visual progress is normalized over the control's range and only defaults to `0..1` when no range is declared. Inspect `runtime.stats`, `missing_facts`, and `unsupported`; blank facts intentionally remain unknown. Use `sample_values` only for an exact per-expression/text override, which takes precedence for that property.
	- For a complex real `onclick` such as `GetScriptedGui(...).Execute(...)`, use `action_effects` only when the review scenario has explicit, independently known postconditions. Match the normalized complete action, provide at most eight typed `set`/`toggle` fact updates, require `unused_action_effects=0`, and describe them as caller-provided consequences rather than inferred engine behavior. Builtin action semantics cannot be overridden.
	- Inspector controls replay visual consequences for `visible`, `enabled`, numeric `min` / `max` / `value`, `down`, `selected`, dynamic text, state, and click actions. `progresspie` uses a deterministic conic mask; `progressbar` normalizes over its declared range, clips the real progress texture, retains the no-progress texture underneath, and keeps ordinary overlays above it. Repeated `onclick` properties execute in source order where each individual effect is supported. `OpenGameView`, `CloseGameView`, `ToggleGameView`, and `ToggleGameViewData` may update their matching `IsGameViewOpen(...)` fact. Static `SetMapMode` selects its matching `IsMapMode(...)` fact and clears other known map-mode facts. Static `GetVariableSystem.Toggle` updates existence, literal-only `Set` updates both `GetVariableSystem.Exists(...)` and typed `GetVariableSystem.Get(...)`, and `Clear` removes both existence and value. `GetVariableSystem.HasValue(name, literal)` is lowered to existence plus a typed equality check; dynamic keys or values remain unsupported. These changes trigger bounded recomputation, and Visual mode with `Replay clicks` enabled lets canvas clicks invoke the same bounded plan while selection-only inspection remains available. Data arguments for unrelated actions are preserved as metadata but are not evaluated, and every other click remains log-only. Generated RPN/action plans are data interpreted by one fixed CSP-hashed script; no expression string is evaluated as JavaScript and no game effect runs. A clean inspector session proves deterministic translation and controlled simulation, not pixel-perfect engine behavior. Validate the final GUI in CK3 before release.
	- Treat a `flowcontainer` without an explicit direction as horizontal. Treat a resolved `scrollbox` as a clipped vertical viewport even when its primitive kind is `scrollarea`: use the preserved `type_chain`; structural `block`/`blockoverride` wrappers are transparent to its content flow, scroll chrome stays outside that flow, `allow_outside=yes` descendants do not inflate its extent, wheel and range controls move only flow content, and nested viewports intersect their clips. Pair a missing `widgetanchor` with the declared `parentanchor`, while preserving an explicit widget anchor. Preserve literal zero dimensions for ordinary widgets; text, autoresize, and expanding axes may treat zero as an auto-measure request. `autoresize=yes` multiline text is remeasured after language or runtime-text changes within explicit width/height limits. Resolved grids preserve wrap/row/column steps; provided model rows enter deterministic cells, and row-local manual changes stay isolated by row id. When a resolved flow or grid has `ignoreinvisible=yes`, verify that known-hidden direct children leave the inspector layout. Margins, spacing, expanding policies, `flipdirection`, unprovided virtualized rows, external engine templates, and compound anchors remain approximate and still require in-game validation.
	- Treat `tooltipwidget` descendants as hover-only overlay evidence, not permanent parent content. The PNG omits them from ordinary layout, while the inspector retains them in the tree and opens the resolved overlay next to its owner on hover. When no overlay exists, resolved tooltip text and bounded tooltip plans use a fixed text-only hover panel; `textContent` keeps runtime values inert. Exact engine tooltip templates, timing, pointer shapes, animation, and multi-monitor placement remain in-game validation items.
	- Review the inspector in its default `Visual` mode first: embedded textures and resolved text are shown without diagnostic container chrome, a known-hidden parent suppresses its whole flattened preview subtree, allowlisted `modify_texture` blends are alpha-masked to the parent icon, and `Replay clicks` makes supported buttons react directly on the canvas. Disable `Replay clicks` when selecting nodes without changing state. Turn `Visual` off to inspect colored kind boxes, approximate geometry, missing-texture placeholders, and hidden nodes. Visual mode improves artifact fidelity but does not manufacture unresolved engine templates or assets.

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
| `duplicate_title_id` | warning | Same-source active landed-title id is defined more than once; all locations are reported |
| `duplicate_barony_province` | warning | One active province is assigned to multiple baronies |
| `invalid_title_hierarchy` | warning | Landed-title parent rank or barony province assignment is invalid |
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

1. Call `ck3_prepare_edit` on the object type.
2. Call `ck3_prepare_edit` with `operation=patterns` on the object type.
3. Call `ck3_prepare_edit` with `operation=examples` and a `type:term` id for concrete nested syntax before inventing blocks.
4. Call `ck3_script_reference` with `kind=scope` or `kind=shape` on unfamiliar trigger/effect keys.
5. Generate a patch-sized change.
6. Call `ck3_preflight` with `operation=patch` on the proposed complete file contents.
7. Write only after patch preflight has no blockers.
8. For risky delete/rename work, call `ck3_impact` first.
9. After writing, call `ck3_preflight` with `operation=dirty` for a cheap local gate.
10. For small current-project edits, run `ck3-index scan --files <relpath...>`, `ck3-index diag_stats`, then `ck3-index preflight <id>`.
11. For final verification, run `ck3-index scan`, `ck3-index validate`, then `ck3-index diag_stats`.
12. When the user needs a distributable Mod, call `ck3_package` with the final metadata and complete file list after preflight succeeds. Treat `status=blocked` as a failed release gate and fix the reported diagnostics before retrying.
13. Never handcraft the ZIP, launcher descriptor, internal descriptor, install instructions, or artifact manifest. The packager owns their canonical structure, portable `path="mod/<slug>"`, deterministic ordering, and temporary artifact lifecycle.
14. Before changing a configured upstream map source, create `map_migration_snapshot`; without the saved old baseline, do not attempt a three-way province migration.
15. After the upstream update, use `map_province_migration`. A ready result is only a complete local test Fork, not a release artifact; a blocked result contains review files only and must not be passed to `ck3_package`.

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
ck3-index package <spec.json>           # Validate model files and build a portable deterministic ZIP
ck3-index package-dir <dir> --meta <metadata.json> # Validate and package an existing Mod directory
ck3-index map audit [operation]         # Audit active province and river raster integrity
ck3-index map province-mapping <spec.json> # Compare two configured province-map versions without writing files
ck3-index map physical-context <spec.json> # Query bounded terrain, surface, hydrology, and bathymetry facts
ck3-index map migration-snapshot <spec.json> # Persist old upstream, project hashes, text baselines, and effective old map
ck3-index map migrate <spec.json> [--out <new-mod-dir>] # Build a validated local Fork; output path must not exist
ck3-index map recipes                   # List thematic map recipes and constrained layer capabilities
ck3-index map metric <spec.json>        # Compute an auditable map metric without rendering
ck3-index map route <spec.json>         # Resolve places and calculate a legal land, sea, or mixed route
ck3-index map render <spec.json> --out <png> [--meta <json>] # Reproduce a map and optional transform sidecar
ck3-index mcp                           # Start MCP server over stdio
```

## Data Sources

- Compiled local rule seeds: trigger/effect scopes, iterators, scope transitions, defines, on_actions, examples, modifiers, and sound events. Use `ck3_script_reference` with the matching `kind`, plus `ck3_diagnostics` for `event:/...` sound findings.
- Do not treat compiled rule seeds as engine authority. Confirm risky edits with local CK3 `.info` files, vanilla examples, active-workspace examples, and indexed project evidence.
- Local wiki notes: `docs/CK3_EXPERIENCE_NOTES.md` summarizes workflow hints from the local CK3 modding wiki. Treat them as generation guidance, not engine authority.
- Regenerate rule data with `python tools/extract_all_scopes.py`, `python tools/extract_shapes.py`, `python tools/extract_defines.py`, `python tools/extract_on_actions.py`, and `python tools/extract_targets.py`.
