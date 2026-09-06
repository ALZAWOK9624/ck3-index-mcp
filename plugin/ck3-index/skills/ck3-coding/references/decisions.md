# Decisions

Read for decision implementation or review. Use current indexed game/project examples and `ck3_script_reference` for the applicable field contracts. Historical field counts are not a specification.

## Availability and feedback

Distinguish whether a decision should be shown, whether it can currently be taken, and which failure reasons the player should see. Inspect a comparable decision's `is_shown`, `is_valid`, and `is_valid_showing_failures_only` behavior rather than substituting one for another by frequency.

Keep expected, actionable requirements visible. Use a localized `custom_tooltip` when a raw variable or compound condition would be unintelligible. A `selection_tooltip` should explain the action; its text must agree with the actual cost, effect, chance, and cooldown. Alerts should reflect the intended urgency instead of appearing for every available repeatable action.

Inspect `picture`, explicit or implicit localization keys, and any referenced resources. Add prose only within the user's authorized localization scope.

## Scheduling and interruption

Choose `ai_potential`, `ai_will_do`, and supported AI intervals for the actual eligible population. Tier-specific intervals are useful when populations or evaluation costs differ by tier; a flat interval may fit a single-holder decision. A player-only gate can reduce downstream AI work, but do not claim zero engine cost without measurements.

For a multi-step chain, distinguish an in-progress guard from its cooldown. Decide explicitly whether cooldown starts at entry, completion, or failure. Check cancellation, death, succession, and timeout cleanup. A timed guard may expire while the chain is still running; a permanent guard without cleanup may lock it forever. Test both re-entry and interrupted completion.

## Implementation and review

Share a scripted effect between entry points that intentionally perform the same action. Check that each caller supplies the same required scopes and authorization conditions. Keep project prefixes and adjacent file style.

Use `ck3_inspect operation=references` for callers of a known scripted GUI, effect, or decision. Use `ck3_impact` with complete proposed `files` for deletion or rename consequences; it has no `operation=references` or `id` argument.

Use `ck3_prepare_edit operation=patterns` to inspect observed fields without direct SQL. Review complete proposed files, preflight the final patch, then refresh changed paths and inspect new diagnostics. An unused-looking definition may have a dynamic caller that static references cannot resolve.
