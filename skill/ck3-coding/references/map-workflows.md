# Map workflows

Read only for geographic analysis, map visualization, terrain/province editing, or upstream migration. Use the advertised schemas for exact arguments and limits. Expensive map operations are sequential.

## Geography and routes

Resolve exact province/title/region identifiers first. `geographical_region:<id>` is an indexed object: use inspect/dependencies for definitions and consumers, map tools for physical membership and aggregation.

Use `map_province_info` for one province and `map_title_context` for title coverage/history. `map_physical_context` supplies terrain, surface, hydrology, oceanography, and barriers. Keep major river channels separate from sea depth. Do not turn normalized elevation, relative depth, pixel catchment, or width proxies into real-world units.

For surface questions, `operation=surface` reports observed material blends. Scripted `common/province_terrain` and painted terrain are different evidence. Placement/weight rasters support the blend; mask/DDS filenames alone do not prove climate, ecology, or gameplay terrain.

For a region's coast/shelf/depth, use one bounded physical-context call with the resolved region, `operation=oceanography`, and adjacent water when relevant. Use `map_build_metric` for an explicit metric or thematic render, not just to answer a simple context question. Consult `map_recipe_catalog` when a recipe is needed.

For routes, call `map_route` with exact endpoints and `year`. Pass its complete route to `map_render` with `auto_context=true`; use returned `route_points_output` and transforms for overlays. Do not fabricate a path from straight-line distance, loop over neighbors to reconstruct routing, or guess image crop transforms.

Map-cache tools require private visibility because their cache lacks per-source provenance. Follow the actual tool schema; do not retry private-only tools with public. Public `map_asset_audit` or `map_province_mapping` can provide their own redacted evidence, not an equivalent public route/render capability.

## Province splits and terrain artifacts

Use `map_split_province` for a plan and inspect its boundaries, validation, and preview before applying. `map_apply_split` requires the returned plan id/hash and explicit confirmation and creates an artifact. Artifact creation and writing a resulting raster into the project are separate actions within the user's authorized scope.

`map_terrain_edit` previews with `confirm=false` and publishes an immutable artifact with `confirm=true`. Use supported ordered layers or river operations and a verified parent artifact when continuing a chain. Keep the artifact's identifiers, hash, manifest, warnings, and synchronization obligations together. A preview does not itself update the active game files.

If a publication response is lost, query `map_artifact` for status/inspection before retrying a write. Reuse the declared request identity when appropriate; do not blindly create duplicate artifacts.

## Upstream map migration

Capture `map_migration_snapshot` while the old upstream and project baseline are still available. After the update, call `map_province_migration` with that snapshot and the configured new source. Inspect renumber, split, merge, complex, and unmapped cases; supply explicit conflict resolutions.

Use `map_province_mapping` for read-only version comparison. If geometry moved, use appropriate geographic control points. Bare province numbers are not stable identities; global numeric replacement is not a migration algorithm.

A blocked migration contains review material, not a usable Mod. A ready migration is a local test fork and still needs validation before packaging. Preserve source originals and establish provenance for the resulting assets; do not reconstruct a missing old baseline by guessing.
