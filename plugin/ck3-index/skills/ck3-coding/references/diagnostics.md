# Diagnostic interpretation

Read for debugging or regression tracking. `ck3_diagnostics` reads existing indexed findings; it does not parse unsaved text or refresh changed files.

QQ may explain permitted public findings or supplied error text. Private-only file validation and the baseline/refresh operations below belong to authorized local work; a QQ debugging question does not activate them.

## Use the right validation scope

- Proposed complete files: `ck3_review files=[...]`; use `ck3_preflight operation=patch` for acceptance.
- On-disk project changes not yet indexed: review without `files` or use `ck3_preflight operation=dirty`.
- Published index: `ck3_diagnostics operation=summary`, then `operation=explain code=...` with relevant filters.
- Exact indexed subject: `ck3_inspect operation=diagnose` or `ck3_preflight operation=subject`.
- Proposed semantic deletion/rename: `ck3_impact files=[...]`.

These indexed proposal-validation operations use the current database for context. For generated text alone, `ck3_check` is the separate database-free checker; see [generated-code checking](generated-code-check.md). Its partial static coverage does not replace indexed reference resolution or game execution.

## Triage

Use each result's severity, confidence, scope trace, provenance, and guidance. Examples of useful distinctions:

| Finding | Interpretation |
|---|---|
| `parse_error`, localization entry/macro syntax | Inspect the complete file and the indicated source span |
| `missing_object_reference`, `missing_localization`, `missing_resource` | Confirm active source, exact key/path, and resolution context |
| `resource_resolution_uncertain`, `scope_uncertain` | Missing static evidence; not established engine illegality |
| `scope_mismatch`, `effect_in_trigger`, `trigger_in_effect` | Read the scope/context trace and the current engine reference |
| `illegal_field_context`, modifier/runtime contract codes | Verify the owning container and current rule provenance |
| `nested_iterator` | Review population and frequency; not a syntax error |
| `on_action_direct_override`, `lios_partial_override` | Inspect lower-layer definitions and merge policy |
| Variable or unused-definition findings | Check dynamic callers and localization runtime reads before deleting |

For other codes, request the tool's explanation rather than loading a static catalog of every diagnostic. Do not suppress or weaken a rule solely to restore an old count. A clean static result is limited by source coverage and supported checks.

## Baselines and refresh

For an authorized regression-tracking task, establish a current index before saving a baseline. `ck3_diagnostic_baseline operation=save baseline=<name>` records the findings then present, including an empty set. Pass the name to diagnostics `summary` or `explain`. `operation=list` lists names; `operation=clear` removes one. These controls are separate from read-only diagnostics.

Saving the same name replaces its snapshot. Baselines survive `ck3_refresh operation=full`; re-record only when deliberately accepting a new baseline. A filtered report answers what appeared since that snapshot, not whether the whole project is clean. Do not compare hardcoded totals from another database, generation, or source configuration.

`ck3_refresh operation=files` refreshes named project-relative paths, at most 64 per call. For a scan conflict or finalizing generation, check `operation=status` before retrying. If the service reports a full scan is required, address the reason and use `operation=full` within the authorized maintenance scope. Never use a second CLI writer to bypass the service's refresh guard.
