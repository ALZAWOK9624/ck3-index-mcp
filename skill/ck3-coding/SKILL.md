---
name: ck3-coding
description: CK3 mod coding workflow. Use when editing, reviewing, generating, or validating Crusader Kings III mod scripts, localization, resources, events, traits, decisions, history, GUI, or related ck3-index/MCP diagnostics in a configured workspace.
---

# CK3 Coding

Use this skill for CK3 mod scripting work. Treat `ck3-index` as the first source of truth for definitions, references, localization, resources, and diagnostics. Treat compiled scope/shape rules as hints that must be checked against indexed examples before risky edits.

For CK3 semantic questions, do not begin with `rg`. Call `ck3_search`, `ck3_inspect`, `ck3_prepare_edit`, or the relevant map tool first; use `rg` only to inspect the exact text behind indexed evidence.

## The MCP tools, not the CLI

Anything an MCP tool covers goes through the MCP tool. Do not decide from the outside that the tools are unavailable: call `ck3_health` and let it answer. Only a failure of that call itself licenses the `ck3-index` CLI, and an answer built on CLI output has to say so.

The two are not equivalent, so a CLI result is never evidence about the service. The CLI runs without the launcher that exports the GIS sidecar path and hash, and without visibility redaction. Reading "GIS unavailable" or a missing database from a shell invocation describes that invocation's environment, not what the server does for a caller.

A handful of operations genuinely have no MCP tool -- `bench`, `accuracy`, `scan`, `validate`, `diag_stats`, `package-dir`. Use the CLI for exactly those, and return to the tools for everything else in the same task. Needing the shell for one step is not a reason to run the rest of the task there.

Do not launch expensive MCP calls in parallel. Await each workspace-wide query, review, preflight, impact analysis, refresh, package, GUI analysis, map analysis, map authoring, or raster operation before starting another expensive call. Cheap exact searches and inspections may use ordinary concurrency, but the server queue is an overload boundary rather than a batching API.

## Two modes

The same tools serve two jobs with different limits. Decide which one you are in before the first call.

**Answering a question.** Read-only. Evidence must be citable: every claim traces to an id, path, and line the tools returned. Prefer `visibility=public` so the answer contains nothing from a private source. Never propose a file write, never call `ck3_refresh`, and say "not indexed" rather than filling a gap from memory.

**Editing the workspace.** Requires `visibility=private` and an authorized session. Follow the edit loop in Generation Rules: prepare, generate, preflight the proposed contents, write, refresh, gate again. Only the configured project source is writable.

When a question turns into an edit, restate the mode change before the first write; do not drift from one into the other.

<!-- BEGIN GENERATED MCP TOOLS -->
## MCP Tools (37 canonical tools)

ck3-index exposes one canonical MCP tool surface. Each tool uses bounded operations rather than legacy specialist aliases.

### Core Tools

| Tool | Reach for it when |
|---|---|
| `ck3_search` | Use when the exact CK3 identifier, key, path, or object type is unknown. |
| `ck3_inspect` | Use when an exact existing identifier, key, or indexed resource is known. |
| `ck3_review` | Use when diagnosing complete proposed CK3 files or the current project files. |
| `ck3_workspace` | Use when the question concerns workspace-wide structure, indexed object types, supported capabilities, or on_action evidence. |
| `ck3_dependencies` | Use when the semantic neighborhood or event topology around one known id is needed. |
| `ck3_prepare_edit` | Use when creating or modifying CK3 content and relevant examples, rules, or conventions are needed first. |
| `ck3_preflight` | Use when a final pass/fail gate is needed before accepting, applying, packaging, or publishing a change. |
| `ck3_impact` | Use when deleting, renaming, replacing, or substantially changing an existing object. |
| `ck3_diagnostics` | Use when reading diagnostics already produced by the current index generation. |
| `ck3_save` | Use when a CK3 save file must be identified, its declared content listed, its ids checked against the Mod, or one character profiled. |
| `ck3_refresh` | Use when configured project source files changed and the index must reflect them. |
| `ck3_script_reference` | Use when a local CK3 engine or script-rule fact needs authoritative indexed evidence. |
| `ck3_health` | Use when checking whether the MCP registration and index database are trustworthy, or confirming which configuration and source trees are live. |
| `ck3_database` | Use when the running MCP service has multiple configured SQLite indexes and the task needs a different evidence base. |
| `ck3_package` | Use when a validated set of Mod files must be packaged into a portable installation artifact. |
| `ck3_gui` | Use when inspecting indexed GUI structure, dependencies, or a bounded static preview. |
| `ck3_coat_of_arms` | Use when a coat of arms must be read as resolved colours and textures, or seen rather than described. |

### Map Tools

| Tool | Reach for it when |
|---|---|
| `map_migration_snapshot` | Use when an upstream map update needs a durable old-upstream/project migration baseline. |
| `map_province_migration` | Use when a previously captured map migration snapshot must be replayed against a configured new upstream. |
| `map_asset_audit` | Use when checking active map raster, province-definition, or river-topology integrity. |
| `map_province_mapping` | Use when two configured province-map versions must be compared for migration evidence. |
| `map_split_province` | Use when one indexed province must be divided into several, with boundaries that follow terrain. |
| `map_apply_split` | Use when a split already reviewed through map_split_province must be turned into an actual recoloured provinces.png. |
| `map_terrain_edit` | Use when ordered physical-landform layers or synchronized large/small river edits must be generated from the active map or a verified parent artifact. |
| `map_artifact` | Use when a map publication response was lost, or committed terrain, split-plan, and split-result artifacts need listing or integrity inspection. |
| `map_province_info` | Use when one province or title's exact map, title, terrain, and neighbor context is needed. |
| `map_physical_context` | Use when terrain, elevation, hydrology, oceanography, materials, or physical barriers must be analyzed. |
| `map_neighbors` | Use when a bounded geographic neighborhood around a province or title is needed. |
| `map_spatial_relation` | Use when the exact spatial relation between two selected map subjects is needed. |
| `map_strategic_passages` | Use when explicit straits, crossings, gateways, or other special passages are relevant. |
| `map_title_context` | Use when one landed title's province coverage, holder, culture, faith, and neighbors are needed. |
| `map_assignment_plan` | Use when review-only religion or placeholder-character assignment recommendations are needed. |
| `map_building_candidates` | Use when ranking special-building candidates for one province or landed title. |
| `map_recipe_catalog` | Use when choosing supported map metric or render recipes before constructing a request. |
| `map_build_metric` | Use when an auditable indexed or source-noted map metric must be calculated before rendering. |
| `map_route` | Use when a deterministic legal land, sea, or mixed route between two map subjects is needed. |
| `map_render` | Use when a read-only CK3 map visualization is needed from indexed map data. |
<!-- END GENERATED MCP TOOLS -->

## Using the tools well

The tool descriptions above say which tool to reach for. This section says what to do when the call does not go the way you expected — which is where sessions actually lose their time.

### Arguments the server already handles

Do not spend a turn correcting these. The server does it and tells you.

- `limit` is capped at 20 and `max_response_bytes` has a 16384 floor. Out-of-range values are clamped to the bound and reported in `argument_notices`; the result is real, not an error. Never reissue a call that differs only in these fields.
- `province_id` and `subject` are read as `id`, and `history_year` as `year`. The response says so in `argument_notices`. Prefer the documented name next time; do not retry.
- Map-cache tools declare `visibility` as `private` only, because the map cache records no per-source provenance. Read the schema rather than discovering it from a rejection. For public map evidence use `map_asset_audit` or `map_province_mapping`.
- An unknown-field error lists every field that tool accepts and names the closest match. Correct from that list; do not guess a second spelling.

### When a call does not return what you wanted

Three outcomes, three different next moves. Do not treat them the same.

**Empty evidence.** First ask whether the term could be indexed at all. The index contains the configured CK3 source roots and nothing else -- `ck3_health` lists them. A name that only ever appears in worldbuilding prose, a wiki, a design document or a chat log is not in there, and no spelling of it will be: 333 of the audited calls were chains averaging zero hits, hunting lore names through an index that could not hold them. Search those documents with `rg` instead, and say plainly that the index does not cover them.

Otherwise the response already names the spellings that were tried on your behalf and any `kind`, `source`, or `path_prefix` that narrowed the search. Rephrasing the same concept again is the single most wasteful thing you can do here, and it is the most common. Instead:

1. Drop the filter the guidance names, and retry once without it.
2. If the term is a display name rather than an identifier, search the localization value with `kind=localization`.
3. If it is a concept rather than a name, ask `ck3_workspace` with `operation=object_types` whether that object type exists at all.
4. If those fail, the term is not indexed. Say so. Do not invent an id.

**Truncated or `pagination.has_more`.** Judge the results you already have. If they are relevant and you need more of the same, request the next `page`. If they are not relevant, paging returns more of the same irrelevance — change the query instead.

**Low-confidence search suggestions.** `suggestions` are candidates only, never evidence. Check `recovered_query` and `recovery_confidence`, and page them with `suggestion_pagination.next_page` rather than `pagination`; inspect a candidate only after its identity is plausible for the task.

**`RESPONSE_TOO_LARGE`.** Lower `limit` first, then narrow with `kind` or `path_prefix`. Raising `max_response_bytes` only moves the ceiling and usually returns more than the answer needs.

### When to stop

- Two searches for the same concept have returned nothing: stop searching and switch tool or report the absence. Do not try a third spelling, a translation, or a synonym.
- A tool has failed twice for the same reason: the argument is not the problem. Re-read its schema or choose a different tool.
- The question is answered: stop. Additional confirming queries do not raise confidence in an indexed fact.

### Getting the most out of one call

- Read `guidance` before the evidence. It is written for exactly this decision and is present on both visibilities.
- `next_actions`, when present, is a validated call you can issue as-is.
- Ask `ck3_inspect` for one known id rather than searching for it; search is for when the id is unknown.
- `ck3_search` without `kind` covers objects, references, localization, resources, diagnostics, script keys, datatypes, and full script text in one call. Add `kind` to narrow a known-noisy term, not by default.
- Walking a family of ids -- every innovation, every game concept, every doctrine of a faith -- goes in one call through `queries`, up to eight terms. Each term is reported separately in `batch`, including the ones that matched nothing, so the absent members are as visible as the present ones. Asking for them one per call is the same evidence at eight times the round trips.
- Do not re-summarize a batch by searching its terms again individually. The per-term rows are the summary; a term that needs more depth is the only reason to ask for it alone.

## Reading an idiom, not just finding one

CK3 script is a restricted declarative language: no functions that return, no
real loops, no data structures beyond flags, variables and lists. Every
non-trivial mechanic is therefore built out of conventions, and a large mod is
mostly a record of which convention its authors settled on. That makes an
indexed mod excellent evidence of *what people write* and no evidence at all of
*what is correct*.

Frequency is not authority. One indexed mod holds 11291 `add_character_flag`
against 360 `set_variable`; another holds 2453 `set_variable` against 207
flags. Neither ratio is a recommendation -- they are two schools, and the
flag-heavy one is usually a codebase old enough to predate typed variables
still running unrewritten. An example chosen by how often it occurs will pick
whichever era the mod is stuck in.

So when a question is "how do I implement X", find the pattern in the mod and
then check it against the game:

1. Locate a working instance (`ck3_search kind=script_text`, or
   `ck3_prepare_edit operation=patterns` for the empirical field shapes).
2. Ask the same question of vanilla with `source=game`. Vanilla is the
   reference implementation: it shows the usage the engine was built around.
3. If the mod and vanilla reach the same result by different routes, say so and
   name the trade -- a flag is cheap and boolean-only and has to be cleaned up
   by hand; a variable carries a value and a scope and costs more state.
4. If only the mod does it, treat it as a workaround rather than an idiom, and
   say which limitation it is working around.

A pattern presented without that check is a claim that "this is how it is
done", supported only by having seen it somewhere.

## Reference indexes versus the project

`ck3_database` may expose indexes that exist only as reference material -- a
third-party total conversion carrying no relation to the configured project.
Their evidence is authoritative about themselves and about nothing else.

- Script from a reference index proves how *that* mod implements something. It
  never proves how the configured project works, and the two must not be mixed
  in one answer without naming which index each claim came from.
- A reference index built from mod files alone holds no setting or lore
  authority. Localization strings there are UI flavour written for that mod's
  own continuity; they are not the source work it adapts, and adaptations
  compress, reorder and invent. Answer setting questions from such an index
  only with an explicit note that the source is the mod's own text.
- `ck3_database operation=switch` changes what every later call sees. Say which
  index an answer came from, and switch back when the task returns to the
  project.

## Workflow

1. Query before editing:
	- If the MCP server may expose multiple indexes, call `ck3_database` with `operation=list`, select only one exact configured name whose description matches the task, and await `operation=switch` before dependent evidence calls. Never invent or submit a SQLite path. Check each result's `database.name` and `database.epoch` before combining evidence from separate calls.
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

4. Know what an override costs before writing one. Replacement is by file, but what that destroys depends on the folder:

   | Folder | Policy | Replacing a file at the same path |
   |---|---|---|
   | most of `common/`, `events/` | `override` | The replaced file is not read at all; every definition it held is gone unless you declare it again. |
   | `common/on_action/` | `container_merge` | List containers (`events`, `on_actions`, `random_events`, `first_valid`, ...) accumulate across files, but `trigger`, `effect`, `weight_multiplier` and `fallback` take the last writer. Append behaviour by adding a list entry pointing at your own on_action; never edit a vanilla on_action's `effect` in place. |
   | `common/defines/`, `history/`, `localization/` | `per_key_override` | Files combine key by key, so a key you omit falls back to another loaded file or the engine default. |
   | `gui/` | `first_in_wins` | The first definition read is kept. An override has to load *earlier*: lead with `00_`, not the trailing `zzz_` that works in override folders. |

   Filename prefixes carry load-order intent: a numeric prefix loads first, a `z`/`zz`/`zzz` prefix loads last. The override drift audit reports `merge_policy`, `policy_consequence` and `load_order_note` on every finding, so read a `base_only_definition` against the policy rather than assuming it is harmless.

5. Source boundary semantics: only source-root-relative CK3 load roots (`common`, `events`, `history`, `gui`, `localization`, `gfx`, `map_data`, and `sound`) are indexed. Root-level backups, tools, docs, caches, and temporary folders are intentionally ignored even when they contain nested CK3-looking paths.

6. Never use localization text alone as proof of mechanics. Confirm in scripts, history, GUI, or indexed definitions.

7. During the edit loop, prefer canonical `ck3_preflight` operations and `scan --files` over repeated full `scan`; follow every scan with `diag_stats`. Before final release or a large handoff, run `ck3-index scan`, `ck3-index validate`, and `ck3-index diag_stats`.

8. Keep generated code conservative:
   - Match nearby file style.
   - Prefer existing scripted triggers/effects/values.
   - Add new localization keys with clear prefixes.
   - Avoid touching upstream files.

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

## GUI Previews

Inspect before imitating.

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

### Coat of arms

`ck3_coat_of_arms` reads and draws heraldry. `inspect` resolves a definition's pattern, its three colours and every emblem against the active `named_colors` and the indexed textures, and reports which references no source supplies; `render` returns the field as a PNG; `assets` lists the pattern and emblem names a definition may refer to.

- Only `common/coat_of_arms/coat_of_arms/` holds heraldry. Its sibling folders (`options/`, `template_lists/`, `dynamic_definitions/`) index under the same object type but describe other things, and the tool deliberately does not serve them.
- An emblem's own `color1`/`color2`/`color3` override the definition's for that emblem; a slot it omits falls back. A colour name no active `named_colors` file defines renders as **black in game** and is reported as a warning rather than substituted.
- `render` is the field CK3 composites *before* the frame, material and dirt overlays, so the shield outline is absent by design.
- Texture names carry no directory: CK3 supplies it. `assets` lists the winning source per name, so a mod emblem shadowing a vanilla one shows as the mod's.
- `visibility=public` withholds both the definition and the render for a coat of arms defined in a private source, and says so via `redacted`. A render is the content made legible, so it does not bypass the evidence boundary.

## Diagnostics Reference

This project sits on top of an upstream mod, so a bare `ck3_diagnostics` summary reports the upstream's findings alongside yours. Record what is already there once with `operation=baseline_save`, then pass the same `baseline` name to `summary` and `explain` to see only what appeared since. The baseline survives `ck3_refresh operation=full`; re-record it after deliberately accepting new upstream findings, and use `baseline_list` / `baseline_clear` to manage them.

| Code | Severity | Meaning |
|---|---|---|
| `parse_error` | error | CK3 script syntax error |
| `unread_script_folder` | error | Project content sits in a folder CK3 does not dispatch, next to a near-identical folder it does; the message names the folder the game layer actually reads |
| `trigger_always_false` | error | An explicit AND block requires a condition and its own negation, so nothing satisfies it |
| `trigger_always_true` | error | An explicit OR block accepts a condition and its own negation, so it filters nothing |
| `trigger_duplicate_condition` | warning | The same condition is checked twice in one AND/OR/NOT block |
| `trigger_double_negation` | warning | NOT wraps a NOT/NOR/NAND, negating twice |
| `hidden_scope_dependency` | warning | A scripted effect or trigger uses `prev` before entering any scope of its own, so it reads a scope opened by the caller |
| `trigger_nested_same_operator` | info | AND nested directly in AND, or OR in OR; the members can move up |
| `trigger_common_condition` | info | Every branch of an OR checks the same condition; lift it out |
| `trigger_absorbed_branch` | info | A branch repeats a condition the enclosing block already decides |
| `effect_in_trigger` | error | Effect used inside a trigger block |
| `scope_mismatch` | warning | Proven trigger/effect scope conflict with a root/current scope trace |
| `trigger_in_effect` | warning | Trigger used inside an effect block |
| `missing_localization` | warning | Loc key referenced but not defined |
| `localization_invalid_character` | error | Localization line contains a replacement character or illegal control byte |
| `localization_entry_syntax` | error | Localization value has an unterminated quoted entry |
| `localization_macro_syntax` | error | Localization square brackets or known function-style macro are unbalanced |
| `missing_object_reference` | warning | Object reference not indexed |
| `missing_resource` | warning | Gfx/resource path referenced but file not found |
| `resource_resolution_uncertain` | info | Bare/context-relative resource needs owning-context resolution before it can be called missing |
| `scope_uncertain` | info | A static scope hint is not fully confirmed by current engine logs and a concrete trace |
| `missing_sound` | warning | `event:/...` sound event referenced but not known from local rule seeds |
| `duplicate_title_id` | warning | Same-source active landed-title id is defined more than once; all locations are reported |
| `duplicate_barony_province` | warning | One active province is assigned to multiple baronies |
| `invalid_title_hierarchy` | warning | Landed-title parent rank or barony province assignment is invalid |
| `map_definition_non_contiguous_ids` | error | Positive province ids in definition.csv contain gaps |
| `map_definition_out_of_order` | error | Positive province ids in definition.csv are not in ascending row order |
| `map_definition_duplicate_id` | error | A positive province id occurs on more than one definition.csv row |
| `province_reference_missing_definition` | error | A typed map consumer, including non-generated locators, references a province absent from definition.csv |
| `duplicate_default_map_field` | error | A singleton default.map province list is defined more than once |
| `conflicting_province_terrain_assignment` | error | One province receives conflicting scripted-terrain assignments |
| `duplicate_province_terrain_assignment` | warning | One province repeats the same scripted-terrain assignment |
| `duplicate_province_history_block` | error | One province has multiple active history blocks |
| `conflicting_province_history_field` | error | One province/date/history field has conflicting values |
| `invalid_title_capital_reference` | error | A county capital is not its direct barony, or a higher-rank capital is not an in-tree county |
| `county_history_anchor_mismatch` | warning | Declared county capital differs from the first direct barony used for province-history coloring |
| `county_history_anchor_missing` | error | A county's effective history anchor lacks culture, religion, or a non-none holding at a bookmark date |
| `invalid_holding_province` | error | Water, river, or impassable province history assigns a non-none holding |
| `on_action_direct_override` | warning | Project/dependency directly overrides a known vanilla on_action effect/trigger block; this is an overwrite-risk review, not an illegal-field claim |
| `unsupported_event_field` | error | CK3 1.19 event field such as direct `is_triggered_only` that the event loader does not accept |
| `event_option_selection_conflict` | warning | Event option declares both `ai_chance` and `ai_will_select`, which are competing AI-selection grammars |
| `invalid_script_value_field` | error | Script-value block, such as CB `ai_score`, using scripted-modifier syntax like `base` |
| `unknown_modifier_field` | error | Modifier container contains a tag absent from the current CK3 1.19 modifier contract |
| `invalid_modifier_context` | error | Known modifier tag is used on an incompatible receiver, such as scheme-only data in `character_modifier` |
| `illegal_modifier_container` | error | Obsolete or unsupported modifier container, such as building `country_modifier` |
| `unknown_modifier_definition` | error | `common/modifier_definition_formats` attempts to create a modifier type not defined by the engine |
| `duplicate_on_action_field` | error | Named on_action declares `trigger` or `effect` more than once |
| `illegal_field_context` | error | A covered runtime container contains a direct field outside its CK3 contract |
| `invalid_government_rule_context` | error | Government field is placed inside the `government_rules` enum container instead of the government object |
| `unregistered_government_type` | error | Project government ID is missing from `NGovernment.GOVERNMENT_TYPES` |
| `opinion_modifier_time_conflict` | error | Opinion modifier combines `monthly_change` with fixed `days`/`months`/`years` duration |
| `opinion_modifier_mode_conflict` | error | Opinion modifier enables both `decaying` and `growing` |
| `opinion_modifier_invalid_delay` | error | Opinion modifier uses a delay field without `decaying = yes` |
| `opinion_modifier_missing_duration` | error | Opinion modifier enables decaying/growing without `monthly_change` or a fixed duration |
| `opinion_modifier_invalid_value` | error | Opinion modifier uses a negative literal `monthly_change` |
| `unknown_scripted_relation_modifier` | error | Scripted relation uses a missing or generated modifier that is not valid in the relation modifier block |
| `scripted_relation_flag_limit` | error | Scripted relation declares more than CK3's 32 supported flags |
| `religion_doctrine_order` | error | Religion-level doctrine appears after the `faiths` block |
| `name_list_probability_sum` | error | Culture name-list ancestry probabilities for one sex exceed 100 |
| `activity_duplicate_category` | error | Activity option category name is repeated within one activity type |
| `activity_duplicate_option` | error | Activity option name is repeated within one category |
| `activity_duplicate_phase` | error | Activity phase name is repeated within one activity type |
| `activity_missing_phase` | error | Activity type has no active phase |
| `situation_missing_phase` | error | Situation has no active phase under `phases` |
| `situation_takeover_conflict` | error | Situation future phase combines mutually exclusive `takeover_points` and `takeover_duration` |
| `law_succession_field_context` | error | Succession field is incompatible with `order_of_succession` or another succession field |
| `government_missing_fallback` | error | No active government has a positive fallback priority |
| `government_missing_mechanic_default` | error | Mechanic-type government family has no default government |
| `government_duplicate_mechanic_default` | error | Mechanic-type government family has more than one default government |
| `council_task_clone_context` | error | Council task clone redefines fields other than position or omits position |
| `council_task_field_context` | error | Council task field conflicts with its task type or progress mode |
| `house_relation_missing_level` | error | House relation has no relationship level |
| `flavorization_missing_domicile_type` | error | Domicile flavourization lacks its domicile database key |
| `trait_genetic_inheritance_conflict` | error | Trait combines `genetic = yes` with manual inheritance chances |
| `lease_contract_value_range` | error | Lease share or UI maximum is outside 0..100 |
| `lease_contract_hierarchy_context` | error | Lease uses `lease_liege` without a hierarchy |
| `lease_contract_enum` | error | Lease enum value is outside the documented choices |
| `subject_contract_contribution_range` | error | Subject-contract literal contribution is outside 0..1 |
| `subject_contract_enum` | error | Subject-contract display mode is invalid |
| `accolade_name_option_count` | error | Accolade name `num_options` does not match its option blocks |
| `culture_era_year` | error | Culture era year is missing or negative |
| `ai_war_stance_side` | error | War stance side is not attacker or defender |
| `ai_war_stance_behaviour_attribute` | error | War stance has no enabled stronger/weaker/desperate behaviour attribute |
| `ai_war_stance_behaviour_field` | error | War stance behaviour container has an illegal field |
| `ai_war_stance_objective_field` | error | War stance objective name or enemy-unit objective field is invalid |
| `ai_war_stance_objective_context` | error | Object-style war objective is used outside enemy_unit_province |
| `ai_war_stance_objective_priority` | error | War stance priority is missing or outside integer range 0..1000 |
| `ai_war_stance_area_enum` | error | War stance enemy-unit area is outside the documented enum |
| `ai_war_stance_area_overlap` | error | War stance enemy-unit areas overlap |
| `house_unity_stage_points` | error | House-unity stage points are missing, nonpositive, or noninteger |
| `story_cycle_duration_missing` | error | Story-cycle effect group has no duration unit |
| `story_cycle_duration_conflict` | error | Story-cycle effect group mixes duration units |
| `story_cycle_chance_range` | error | Story-cycle literal chance is outside 0..100 |
| `story_cycle_triggered_effect_shape` | error | Story-cycle triggered effect has no effect block |
| `activity_ai_tier_missing` | error | Activity AI tier interval block is incomplete |
| `activity_intent_default_invalid` | error | Activity default/player intent is not listed in intents |
| `decision_ai_interval_missing` | error | Decision has no AI interval and is not an AI goal |
| `decision_picture_missing` | error | Decision has no direct picture block containing a resource reference |
| `decision_ai_tier_missing` | error | Decision AI tier interval block is incomplete |
| `interaction_ai_tier_missing` | error | Character interaction AI frequency tier block is incomplete |
| `great_project_ai_tier_missing` | error | Great-project AI tier interval block is incomplete |
| `struggle_missing_future_phase` | error | Non-ending struggle phase has no future phase |
| `struggle_invalid_duration` | error | Struggle phase duration is nonpositive or invalid |
| `struggle_phase_reference` | error | Struggle start/future phase references an undeclared phase |
| `struggle_ending_phase_fields` | error | Ending struggle phase contains fields the engine ignores there |
| `court_type_duplicate_default` | error | More than one active court type is marked default |
| `trait_opinion_gender_conflict` | error | Triggered trait opinion enables both `male_only` and `female_only` |
| `trait_track_duplicate_name` | error | Trait track name is repeated within one trait |
| `trait_track_xp_range` | error | Literal trait track XP threshold is outside 0..100 |
| `trait_track_xp_order` | error | Literal trait track XP thresholds are not ascending |
| `innovation_asset_display_missing` | error | Innovation asset has neither `name` nor `icon` |
| `event_transition_invalid_duration` | error | Event transition duration is zero or negative |
| `event_2d_invalid_duration` | error | Event 2D effect duration is negative |
| `event_theme_missing_required_field` | error | Event theme lacks direct `background`, `icon`, or `sound` |
| `house_aspiration_missing_level` | error | House aspiration has no level block |
| `dynasty_perk_trait_chance` | error | Dynasty perk traits block has no nonzero literal AI chance |
| `struggle_missing_phase_list` | error | Struggle has no phase list |
| `struggle_missing_start_phase` | error | Struggle has no initial `start_phase` |
| `struggle_missing_ending_decision` | error | No struggle phase defines an ending decision |
| `missing_trigger_else` | warning | `trigger_if` / `trigger_else_if` chain without a terminal `trigger_else` |
| `event_no_option` | warning | Visible numeric event definition without an option block |
| `nested_iterator` | warning | Project code nests iterators; advisory performance review only, not CK3 illegality |
| `gui_crash_risk` | error | Known crash pattern in GUI |
| `missing_event_loc` | warning | Visible event lacks usable title/description/option localization, or a decision lacks explicit/implicit localization; hidden events and `<id>`/`<id>_desc` decision keys are understood |
| `variable_never_set` | warning | Variable referenced but never `set_variable` in active files |
| `lios_partial_override` | warning | File overrides upstream but defines fewer objects |
| `history_character_name_localization_missing` | warning | Direct unquoted character-history name has no active localization value |
| `variable_write_only` | warning | Project variable is set but has no indexed read across active source layers or literal localization runtime expressions |

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
ck3-index map terrain-edit <spec.json> [--preview-out <png>] [--confirm] # Preview or publish a verified raw physical-terrain artifact
ck3-index map render <spec.json> --out <png> [--meta <json>] # Reproduce a map and optional transform sidecar
ck3-index mcp                           # Start MCP server over stdio
```

## Data Sources

- Compiled local rule seeds: trigger/effect scopes, iterators, scope transitions, defines, on_actions, examples, modifiers, and sound events. Use `ck3_script_reference` with the matching `kind`, plus `ck3_diagnostics` for `event:/...` sound findings.
- Do not treat compiled rule seeds as engine authority. Confirm risky edits with local CK3 `.info` files, vanilla examples, active-workspace examples, and indexed project evidence.
- Local wiki notes: `docs/CK3_EXPERIENCE_NOTES.md` summarizes workflow hints from the local CK3 modding wiki. Treat them as generation guidance, not engine authority.
- Regenerate rule data only from a matching current CK3 log bundle and game tree. `event_scopes.log`, `event_targets.log`, `triggers.log`, `effects.log`, `on_actions.log`, and `modifiers.log` supply engine-log evidence; `common/defines/`, `common/on_action/`, `common/modifier_definition_formats/`, and `sound/GUIDs.txt` supply the matching vanilla-source evidence.
- Runtime field contracts are documented in `docs/CK3_RUNTIME_FIELD_CONTRACTS.md`. Treat `.info` files as non-exhaustive hints, combine them with vanilla instances and engine logs, and keep cross-file checks such as government registration in the full-scan path. The modifier receiver area is the object that receives the modifier; selector metadata such as `name`, `parameter`, `terrain`, `object`, `target`, and `holding` is not itself a numeric modifier tag.

  ```text
  python tools/extract_engine_scopes.py --logs <logs> --scope-output internal/indexer/scope_data.gen.go --targets-output internal/indexer/scope_transitions.gen.go
  python tools/extract_engine_on_actions.py --logs <logs> --game <game> --output internal/indexer/on_action_data.gen.go
  python tools/extract_engine_defines.py --game <game> --output internal/indexer/engine_defines.gen.go
  python tools/extract_engine_sounds.py --game <game> --output internal/indexer/engine_sounds.gen.go
  python tools/extract_engine_shapes.py --logs <logs> --output internal/indexer/engine_shapes.gen.go
  python tools/extract_modifiers.py --logs <logs> --game <game> --output internal/indexer/modifiers_data.gen.go
  python tools/extract_effect_examples.py --logs <logs> --output internal/indexer/effect_examples.gen.go
  python tools/extract_trigger_examples.py --logs <logs> --output internal/indexer/trigger_examples.gen.go
  ```

  `engine_shapes.gen.go` is documentation only: it must not infer an exhaustive CK3 value grammar. Run `gofmt` on every generated Go file, then perform a full scan and compare `diag_stats`; do not restore an unproven compatibility rule merely to suppress a diagnostic.
