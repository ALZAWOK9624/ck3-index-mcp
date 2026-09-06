# Choosing a CK3 system or interface

Read when selecting an implementation surface. Check available object types and capabilities in the active version; expansions and total conversions can change their contracts.

| Intended interaction | Candidates to inspect |
|---|---|
| Immediate action by a ruler | Decision |
| One character acts on another | Character interaction; scheme for an extended process |
| Guests gather with location and phases | Activity, intent, travel |
| Region-wide conflict evolving through phases | Struggle or another supported regional system |
| Reputation or a deed spreading over time | Legend |
| Durable personal identity or progression | Trait, lifestyle, modifier |
| Institutional rules and obligations | Government, law, subject contract, court position |
| Practices or beliefs shared by peoples | Culture, faith, doctrine |
| Objects or a physical seat with progression | Artifact, domicile |

These are candidates, not automatic equivalences. Use `ck3_workspace operation=object_types` and `ck3_prepare_edit` to verify exact indexed type names, then inspect matching examples and required registrations.

## Events

Choose supported event types for their actual context. Character, letter, court, and activity events have different presentation and scope requirements. In particular, an activity event needs its activity context; window appearance alone does not supply it.

Use hidden events for machinery when no visible scene is intended. Use a supported message/toast mechanism when a notification is enough. Verify themes and their icon, background, and sound resources through indexed definitions. Historical event counts do not mandate a distribution or restrict how frequently a project may use a window.

## Existing or custom GUI

An existing system may provide useful scheduling, AI, and interface behavior, but it also brings constraints. Compare those constraints with the user's desired behavior before adopting it.

A tooltip or existing panel may suffice for a small addition. A custom window can be justified by new controls, comparisons, or presentation needs. Do not require replacing a requested GUI simply because a native system is adjacent. For new GUI work, inspect the existing templates and use [GUI preview](gui-preview.md).
