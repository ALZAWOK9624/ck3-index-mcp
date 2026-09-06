# Index and rule maintenance

Read for authorized local index maintenance, service troubleshooting, release validation, or ck3-index development. Ordinary Mod editing uses MCP review/preflight and `ck3_refresh operation=files`. This is not a fallback path for a QQ answer that lacks public evidence or a public validator.

## Service context

Call the running service's health/status when diagnosing it. An independently launched CLI has separate configuration, GIS environment, and visibility handling. It cannot prove whether the registered service has a database or sidecar problem.

If MCP transport itself is unavailable and CLI work is needed, confirm the intended configuration and authorization, state the fallback, and avoid a concurrent writer. A validation error from a healthy tool is not a transport failure.

The default refresh operation is read-only `status`. `files` updates project-relative paths; `full` hashes all indexed input content and reuses a healthy unchanged generation; changes use a staged generation and atomic publication. `no_op` means unchanged semantics. Use `committed` to distinguish read-only reuse from a durable metadata-only refresh; `no_op=true` with `committed=false` keeps the generation unchanged and is a successful result. Prefer these when working through the service. CLI `scan --clean` is an explicit rebuild choice, not a routine response to any stale result.

## CLI-only and offline work

Use these for their actual maintenance purpose. Consult the current CLI help for flags.

```text
ck3-index bench                         # Query benchmark and query-plan checks
ck3-index accuracy [dir]                # Golden extraction/diagnostic accuracy fixtures
ck3-index validate                      # Full validation/compiler pass
ck3-index diag_stats                    # Offline diagnostic counts
ck3-index package-dir <dir> --meta <metadata.json> # Package an existing directory
ck3-index scan [--clean]                # Offline index scan or explicit rebuild
ck3-index scan --files <relpath...>      # Offline project-file update
```

Do not run scan beside an active service refresh. For release checks, verify the relevant index generation, full validation, diagnostics, and package gate. A small documentation edit does not require rebuilding the game index.

The following canonical map CLI commands are for deliberate offline reproduction or service-unavailable troubleshooting. They do not replace functioning MCP tools in ordinary map tasks:

```text
ck3-index map audit [operation]
ck3-index map province-mapping <spec.json>
ck3-index map physical-context <spec.json>
ck3-index map migration-snapshot <spec.json>
ck3-index map migrate <spec.json> [--out <new-mod-dir>]
ck3-index map recipes
ck3-index map metric <spec.json>
ck3-index map route <spec.json>
ck3-index map terrain-edit <spec.json> [--preview-out <png>] [--confirm]
ck3-index map render <spec.json> --out <png> [--meta <json>]
```

Migration output must be a new directory. Terrain confirmation publishes an artifact. Artifact-producing commands remain writes even when they preserve the original game files.

## Rule-data changes in a source checkout

Use a matching game tree and engine log bundle. Scope/target/trigger/effect logs establish engine evidence; current vanilla instances and `.info` files provide usage/context. Empirical shapes and field frequency are not exhaustive grammar.

Repository-only extraction scripts live under `tools/`: `extract_engine_scopes.py`, `extract_engine_on_actions.py`, `extract_engine_defines.py`, `extract_engine_sounds.py`, `extract_engine_shapes.py`, `extract_modifiers.py`, `extract_effect_examples.py`, and `extract_trigger_examples.py`. Inspect their current help and output targets before regeneration. These scripts and repository docs are not assumed to exist inside an installed skill.

After rule/extraction changes, format generated Go, run relevant accuracy/tests, and compare diagnostics on a fresh index. Preserve the distinction between a modifier's receiver and selector metadata such as `name`, `parameter`, `terrain`, or `target`. Cross-file contracts need indexed validation; a parser-only check cannot prove registration or reference resolution.

Canonical skill resources live in `skill/ck3-coding/`. Run `go run ./cmd/mcp-docgen` to generate the tool catalog and synchronize the complete skill bundle into the plugin, and `go run ./cmd/mcp-docgen -check` to detect drift.
