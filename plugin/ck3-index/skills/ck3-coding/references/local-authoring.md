# Local Mod authoring and validation

Read before authorized local file creation, editing, validation, or packaging. This workflow is not activated by a QQ code question. Preserve the user's requested scope; a small fix does not require redesigning the system.

## Prepare and diagnose

Confirm the configured writable project and active source priority. Inspect a known definition before changing it. Use `ck3_prepare_edit` for unfamiliar or substantial work, choosing `patterns`, `examples`, or `rules` only for missing detail. Compare matching current-game examples and relevant engine references. Field frequency is usage evidence, not mandatory grammar.

Prepare source-root-relative `files` with complete proposed contents, not unified diffs or isolated replacement lines. Use the declared `op`/`from`/`to` shape for deletion or semantic rename. Inspect callers, then use `ck3_impact files=[...]` when deletion, renaming, or replacement can break consumers.

Use `ck3_review files=[...]` for exploratory diagnosis. Omitting `files` reviews current dirty project files, so do that only when those files are in the task's scope. Review/preflight/impact require private visibility and use the active index; “read-only” does not mean database-free.

Use `ck3_preflight operation=patch` for the final gate on complete proposed files. Fix blockers or establish a false-positive rule with concrete source evidence. Do not suppress a finding merely to pass. Additional preparation/review calls are unnecessary when the required evidence and final gate already suffice.

## Apply and refresh

Write the authorized files. For small project changes, call `ck3_refresh operation=files` with changed `paths`, including removed/renamed paths as needed, and await completion. Inspect relevant diagnostics from the new generation. Use `ck3_preflight operation=dirty` for unrefreshed on-disk changes, or `operation=subject` for an indexed subject needing a final gate.

Use `ck3_refresh operation=full` for first indexing, changes the service says require it, or release-scale validation that warrants a complete scan. It stages and publishes a generation. Do not rebuild after every small edit. On a scan conflict, inspect `operation=status` and wait; do not start a competing CLI writer.

A same-path source override can remove the lower layer's whole file. Object/container merging is a separate folder-specific behavior. Inspect override provenance and `merge_policy`/`policy_consequence` before deciding what must be preserved. Numeric or z-prefixed filenames alone do not prove the intended override.

## Regression and release

`ck3_diagnostics` reads existing findings for its generation. Start with `operation=summary`, then `operation=explain` for relevant codes. For regression tracking, save a named baseline before changes on a current index using `ck3_diagnostic_baseline`; reuse its name afterward. Saving the same name replaces it, and full refresh preserves it. Filtered silence is not zero total findings. See [diagnostics](diagnostics.md).

When a distributable is requested, use `ck3_package` with final metadata and complete files. A blocked package is not a release. Validate gameplay, GUI interaction, dynamic scopes, and other unsupported static behavior in CK3 as appropriate. Report which checks ran and what remains unverified.

Choose only the domain references relevant to the change: [decisions](decisions.md), [narrative/on_actions](narrative-design.md), [writing/localization](writing-and-localization.md), [GUI](gui-preview.md), or [maps](map-workflows.md). Design references support requested design work; they do not authorize expanding an existing repair.
