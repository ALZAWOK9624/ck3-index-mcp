# Narrative state and recurring hooks

Read for event chains, persistent systems, or on_action changes. These are design and performance considerations, not requirements to replace a user-requested linear story.

## State and lifecycle

Decide what persists independently of an event window: variables, flags, relationships, modifiers, or a native story system. Use an event for a meaningful scene, choice, or notification, and keep necessary transitions reachable if another event becomes ineligible.

Place state on the entity whose lifetime it belongs to. Realm state may belong on a title; personal state may belong on a character. Define what succession, conquest, death, character switching, cancellation, and mid-save upgrades do. Title ownership changes do not automatically supply the desired transfer semantics.

Choose timers, thresholds, and player actions according to the intended pacing. Delays are valid when elapsed time matters. Branch on visible state when useful, but explicit choice history is appropriate when remembering that choice is part of the story. Decide how an interrupted or completed arc leaves the world; do not impose a fixed number of events or outcomes.

Inspect a chain with `ck3_dependencies operation=event_chain`, an exact id, and the needed `direction`. Check unresolved calls, cycles, roots, and leaves. A cycle may be intentional; graph shape alone does not prove a bug.

## on_action performance

Inspect the current hook through `ck3_script_reference kind=on_action` and indexed vanilla examples. Confirm its scope, population, and frequency instead of inferring them from its name.

- Choose the narrowest hook that covers the intended recipients.
- Put cheap, semantically valid gates before expensive traversal where evaluation permits short-circuiting. Do not reorder expressions that depend on prior scope or state.
- Avoid repeating a population scan for each member of that same population. Estimate work as hook frequency × eligible recipients × traversal size; nested iterators are an advisory cost signal, not illegal syntax or automatically quadratic.
- Use a bounded or random selection only when it preserves the requested behavior. Keep shared work in a scripted effect when that improves reuse.
- Resolve repeated scope traversal once when the saved target remains valid throughout the effect. Verify what mutations invalidate it.
- Use an appropriate initialization hook for new games and provide a separate migration path when existing saves must gain the feature.

Append a named project on_action through a supported list container in a separate project file where possible. The list containers merge, while direct `trigger`/`effect` and other singleton fields can overwrite another contribution. A same-path file override can discard the lower file before container merging occurs. Inspect provenance and `on_action_direct_override` findings; do not claim every additional declaration replaces every list.

After changes, validate the chain and test its interruption paths. Measure performance before calling an optimization faster.
