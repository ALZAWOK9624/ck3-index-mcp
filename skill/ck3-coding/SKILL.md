---
name: ck3-coding
description: Author, review, and validate CK3 Mod scripts, localization, resources, history, GUI, and maps with ck3-index MCP evidence. Use for CK3 content work and index diagnostics, not generic server implementation.
---

# CK3 Coding

Use ck3-index first for configured CK3 definitions, references, localization, resources, and diagnostics. Use targeted file reads or `rg` to inspect the source locations it returns. Static findings describe the indexed sources and supported checks; they are not proof that CK3 will execute a feature correctly.

## Select the evidence context

- Use the tools actually advertised by the running server. Check `ck3_health mode=quick` when configuration, source coverage, or index readiness is uncertain; a healthy session does not need another health call before each search.
- If multiple databases may apply, use `ck3_database operation=list`, select an exact configured name, and await `operation=switch` before dependent calls. Never supply a guessed SQLite path. Keep `database.name`, `database.epoch`, and index generation consistent when combining results.
- Use `visibility=public` for public answers. Authorized project investigation and editing may use private evidence where supported. Private visibility is not permission to disclose or modify a source; follow the session's source boundaries. Do not bypass redaction through shell access.
- Ordinary questions are read-only. Refresh after authorized source changes or an explicit index maintenance request, not merely to answer a question. Only files within the user's authorized edit scope are writable.
- Reference databases establish what their own Mod implements. Name the source when comparing Mods; localization proves displayed text, not mechanics or external setting canon.
- Prefer MCP for operations it supports. A shell process may lack the service's configuration, visibility handling, or GIS launcher environment. Its failure does not establish a service failure. Use CLI-only maintenance deliberately; see [maintenance](references/maintenance.md).

## Choose the smallest useful call

| Need | Tool |
|---|---|
| Find an unknown id, key, display name, path, or script occurrence | `ck3_search` |
| Read a known id, its callers, localization, resource, or source comparison | `ck3_inspect`; select the relevant `operation` |
| Learn an object type's authoring conventions | `ck3_prepare_edit` |
| Check a scope, datatype, engine key, or rule example | `ck3_script_reference` |
| Diagnose complete proposed files or changed project files | `ck3_review` |
| Gate a proposed patch, changed files, or known subject | `ck3_preflight` |
| Assess proposed deletion, rename, or replacement consequences | `ck3_impact` with `files` |
| Explore event topology or a semantic neighborhood | `ck3_dependencies` |
| Read existing findings or update the index after edits | `ck3_diagnostics` / `ck3_refresh` |

Use `ck3_workspace operation=object_types` for supported indexed types and `operation=capabilities` for domain support. The [generated tool catalog](references/tool-catalog.md) covers specialized save, package, GUI, heraldry, and map tools; read it only when routing is unclear. Tool input schemas supply exact arguments.

## Search and response handling

`ck3_search` has one result contract: read `structuredContent`; `content` is empty. `evidence`, and any returned `suggestions` or `batch`, are tables with `columns` and `rows`. Match each row cell to its column name. Null means an absent field; paths are literal strings. Zero and single-hit results use the same table shape. There is no `format` argument. Do not expand the whole table into another payload just to read it.

- Start with the default bounded limit. Search without `kind` when discovery should span domains; use `kind`, `source`, and `path_prefix` when they express real scope. Use `kind=localization` for display text and `kind=script_text` for script occurrences.
- Batch related discovery terms in `queries` (up to eight). Read per-term outcomes from `batch`; do not repeat each term individually unless it needs deeper evidence.
- Read `guidance`, `argument_notices`, confidence, and pagination with the evidence. A clamped argument and a valid result do not require a retry. Correct unknown fields from the schema/error's accepted fields.
- Suggestions are possible identities, not confirmed evidence. Use `suggestion_pagination` for them and inspect a plausible candidate before relying on it.
- Empty evidence means no match within that query's indexed coverage and filters. Check the named filters or use a localization search when justified. After repeated empty calls without a new lead, inspect coverage or consult the appropriate documents; do not cycle through synonyms or claim the thing cannot exist.
- Page only when the returned results are relevant and more are needed. On truncation or `RESPONSE_TOO_LARGE`, narrow the request or reduce `limit` before increasing its byte budget.
- `next_actions` are suggestions to evaluate against the task, visibility, and current database. They do not authorize writes or require execution.
- Await expensive workspace scans, reviews, preflights, impact analysis, refreshes, packaging, GUI/map analysis, and raster work sequentially. Batch search terms instead of flooding the queue.

## Authoring and validation

1. Confirm the intended source and existing definition. Use `ck3_prepare_edit` for unfamiliar or substantial work; request `patterns`, `examples`, or `rules` only for details still missing. Compare relevant current-game examples using the configured game source. Frequency describes usage, not required grammar; absence from vanilla alone does not prove invalidity.
2. Prepare the change at file scope. `files` entries use source-root-relative paths and complete proposed contents, not unified diffs or isolated replacement lines. Use the declared delete/rename shape when applicable. For destructive semantic changes, inspect callers and assess the proposed `files` with `ck3_impact`.
3. Diagnose with `ck3_review` when findings need exploration. Use `ck3_preflight operation=patch` for a final gate on complete proposed files. Resolve blockers, or establish that a rule is wrong using source evidence; do not hide a finding to get a passing result. These calls are read-only but use the active index: do not describe them as database-free syntax checking.
4. Apply the authorized files. For a small project edit, call `ck3_refresh operation=files` with the changed `paths`, including removed/renamed paths as needed. Await completion, then inspect diagnostics in the new generation. Use `ck3_preflight operation=dirty` for unrefreshed on-disk changes, or `operation=subject` for an indexed subject that needs a final gate.
5. Use `ck3_refresh operation=full` when a first index is needed, source/configuration changes or the service require a full scan, or release scope warrants it. It stages and publishes a generation; do not perform a full rebuild after every small edit. On a scan conflict, inspect `operation=status` and wait; do not start another writer.
6. When a distributable is requested, use `ck3_package` with final metadata and complete files. A blocked package is not a release. Validate runtime behavior in CK3 for features that static analysis cannot prove.

Same-path source overrides can remove the lower layer's entire file. Separately, object/container merge behavior depends on the folder. Read override provenance and `merge_policy`/`policy_consequence` before deciding which definitions must be preserved; a filename prefix alone does not prove the intended result.

Diagnostics describe their generation. Use `ck3_diagnostics operation=summary`, then `operation=explain` for relevant codes. To track new findings, save a named baseline with `ck3_diagnostic_baseline` before the change on a current index and reuse that name. Baselines survive full refresh; saving the same name replaces it. Do not accept new problems by silently replacing a baseline, and do not treat baseline-filtered silence as zero total findings. See [diagnostics](references/diagnostics.md) for triage and coverage limits.

## Read only the relevant reference

- [Decisions](references/decisions.md): availability, failure text, AI scheduling, cooldowns, and caller checks.
- [Narrative and on_actions](references/narrative-design.md): persistent state, interruption recovery, and recurring-hook costs.
- [Writing and localization](references/writing-and-localization.md): text scope, terminology, formatting, and truthful tooltips.
- [Systems and surfaces](references/systems-and-surfaces.md): choosing a native mechanism or event window.
- [Mechanic design](references/design-methodology.md): requested design or balance review; not a mandatory redesign during a bug fix.
- [GUI preview](references/gui-preview.md): renderer evidence, controlled runtime samples, and heraldry.
- [GUI visual design](references/gui-visual-design.md) and [composition](references/gui-composition.md): layout or appearance work.
- [Map workflows](references/map-workflows.md): physical evidence, routes, province edits, terrain artifacts, and migration.
- [Maintenance](references/maintenance.md): CLI-only index work and rule-data regeneration.

Report what changed, which validation ran, and remaining runtime uncertainty. Cite relevant ids and source locations; do not dump the tool payload or equate a static pass with engine correctness.
