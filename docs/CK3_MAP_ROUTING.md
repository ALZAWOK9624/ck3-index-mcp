# CK3 map routing and route-render contract

## Purpose

The route workflow is intentionally split between geography and presentation:

```text
exact place resolution
  -> legal province-graph route
  -> bounded county/duchy corridor
  -> PNG basemap + exact output coordinates
  -> HTML/SVG presentation
```

`map_route` owns topology and route legality. `map_render` owns the actual crop, padding, letterbox, output size and coordinate transform. The browser layer only draws on `route_points_output`; it must not recalculate the transform.

## Authoritative database

Every CLI map command and every MCP map tool opens the database selected by the same parsed `ck3-index.toml` configuration. A relative `database` value is anchored to the configuration file directory, not the current working directory.

There is no implicit `cache/ck3_index.sqlite`, similarly named fallback, or create-on-query database. Missing configuration, a missing database file, or an incomplete map cache fails immediately. The minimum map cache is:

- `map_provinces`
- `map_adjacencies`
- `map_province_geometry`
- `map_titles`

`ck3_health` reports the schema version, stable database fingerprint, authority decision, core counts and geometry availability without serializing the database or WAL absolute paths. An incomplete cache reports `MAP_DATABASE_INCOMPLETE`; route code never converts that condition to `MAP_ROUTE_NO_PATH`.

The health command is read-only. A large active WAL is reported as a signal, but is not automatically removed or checkpointed.

## `map_route`

Canonical MCP request:

```json
{
  "from": "俄赖斯波尔",
  "to": "Asiupoli",
  "year": 6254,
  "mode": "sea",
  "objective": "shortest",
  "waypoints": [],
  "corridor_radius_pixels": 120,
  "context_level": "duchy",
  "label_language": "bilingual",
  "max_nodes": 5000,
  "visibility": "private"
}
```

`from`, `to`, and every waypoint accept a numeric province ID, `b_`/`c_`/`d_`/`k_`/`e_` title ID, or an exact unique English or Chinese localized name. Ambiguous names return stable candidates and are never guessed.

The equivalent CLI command is:

```powershell
ck3-index --config ck3-index.toml map route testdata/map-route-oraispol-asiupoli.json
```

The current v0.3.0 acceptance database resolves the city provinces to `1911` and `1302`, then returns this 19-province sea body:

```text
8142 -> 8293 -> 8139 -> 8137 -> 8136 -> 8135 -> 8127
     -> 8134 -> 8189 -> 8192 -> 8363 -> 8226 -> 8208
     -> 8209 -> 8210 -> 8211 -> 8212 -> 8217 -> 8218
```

Compact response shape:

```json
{
  "status": "ready",
  "intent": "map_route",
  "resolved_from": {
    "input": "俄赖斯波尔",
    "province_id": 1911,
    "barony": "b_oraispol",
    "county": "c_oraispol",
    "name_en": "Oraispol",
    "name_zh": "俄赖斯波尔"
  },
  "resolved_to": {
    "input": "Asiupoli",
    "province_id": 1302,
    "barony": "b_asiupoli",
    "county": "c_asiupoli",
    "name_en": "Asiupoli",
    "name_zh": "阿西乌波利"
  },
  "mode": "sea",
  "objective": "shortest",
  "path": [
    {
      "province_id": 8142,
      "center_x": 2407.7,
      "center_y": 2310.2,
      "water_kind": "sea"
    },
    {
      "province_id": 8293,
      "center_x": 2320.727050516024,
      "center_y": 2355.8294405214556,
      "water_kind": "sea",
      "adjacency_from_previous": "water_boundary",
      "distance_from_previous_pixels": 98.19
    }
  ],
  "legs": [
    { "kind": "embark", "start_index": 0, "end_index": 0 },
    { "kind": "sea", "start_index": 0, "end_index": 18 },
    { "kind": "disembark", "start_index": 18, "end_index": 18 }
  ],
  "distance_pixels": 1850.43,
  "corridor_targets": {
    "province_ids": [],
    "county_ids": [],
    "duchy_ids": []
  },
  "warnings": [
    "Pixel distance is a source-map centroid-path measurement, not in-game travel time or real-world distance."
  ],
  "timings_ms": {
    "resolve_ms": 170,
    "graph_load_ms": 52,
    "route_ms": 1,
    "corridor_ms": 3
  }
}
```

The response above abbreviates `path` and corridor arrays for documentation. Runtime output contains the complete bounded path and corridor.

### Routing rules

- Adjacency rows are loaded as an undirected graph even when a cache stores one row per unordered pair.
- Sea routes use navigable `sea`/`coastal_sea` provinces and legal water boundaries. They exclude lakes, river provinces and impassable seas, and add embark/disembark semantics around land endpoints.
- Land routes use traversable land boundaries plus allowed strategic passages. They reject water and impassable mountain boundaries.
- Mixed routes permit land/sea transitions with a penalty to discourage repeated embark/disembark zigzags.
- Strategic adjacency kinds remain explicit. Off-map and underground gateways are not silently treated as ordinary geography.
- `shortest` minimizes centroid-path pixel cost. `scenic` applies bounded coastal/island preferences and is accepted only within 25% of the shortest route.
- `distance_pixels` is not CK3 travel time, real distance or marching duration.

No-route results include resolved endpoints, mode, endpoint component sizes, rejected boundary classes and a stable error code. Node-budget failures use `MAP_ROUTE_NODE_LIMIT`; disconnected legal graphs use `MAP_ROUTE_NO_PATH`.

## Route-aware `map_render`

Pass the complete successful `map_route` object as `route`. This preserves endpoint names and route legs in the render response:

```json
{
  "year": 6254,
  "route": { "status": "ready", "resolved_from": {}, "resolved_to": {}, "path": [] },
  "auto_context": true,
  "corridor_radius_pixels": 120,
  "context_level": "duchy",
  "label_language": "bilingual",
  "width": 2200,
  "height": 1300,
  "layers": [
    { "type": "fill", "level": "duchy", "palette": "political_muted" },
    { "type": "borders", "level": "county" },
    { "type": "borders", "level": "duchy" },
    { "type": "labels", "level": "duchy", "limit": 20 }
  ]
}
```

`route_province_ids` is a compact alternative for clients that only need coordinates. It does not carry resolved endpoint names or legs.

CLI rendering and sidecar output:

```powershell
ck3-index --config ck3-index.toml map render route-render.json --out route.png --meta route-render.json
```

The response contains the actual renderer transform:

```json
{
  "source_map": { "width": 8192, "height": 4096 },
  "source_viewport": { "min_x": 2032, "min_y": 2174, "max_x": 3826, "max_y": 3163 },
  "output": { "width": 2200, "height": 1300 },
  "transform": {
    "scale_x": 1.1721448468,
    "scale_y": 1.1721448468,
    "offset_x": -2333.7983,
    "offset_y": -2479.2429
  },
  "route_points_output": [
    { "province_id": 1911, "x": 501.70, "y": 251.05, "role": "origin" },
    { "province_id": 8142, "x": 488.36, "y": 228.67, "path_index": 0 }
  ]
}
```

`route_points_output` is computed before supersampling is applied to the internal working canvas, so it always targets final output pixels. The renderer validates that returned route points are inside the output canvas. Clients must not infer padding, letterboxing or source viewport.

When bilingual labels were requested but no usable CJK font or localization produced labels, the result contains a warning instead of claiming complete success.

## HTML/SVG presentation

The data-driven example is in [`examples/map-route-overlay`](../examples/map-route-overlay/README.md). It uses a fixed `2200×1300` image, draws the polyline, arrows and endpoints from `route_points_output`, and derives titles and journey stages from the JSON response. No route name is hard-coded.

The example includes a headless Chromium screenshot command, but ck3-index does not install, launch or manage a browser.

## Compatibility

- New requests use `year` everywhere.
- `map_render.history_year` remains a deprecated alias for old clients. If both fields are supplied with different values, the request is rejected.
- Existing numeric `map_province_info`, `map_neighbors` and `map_spatial_relation` calls remain valid; they additionally accept the unified subject forms.
- Canonical schemas expose all array and limit maxima before execution. `limit` is consistently `1..20`, default `8`.
- New documentation and generated `next_queries` use canonical tool names and `year` only.

## Acceptance performance

Measured on the configured v0.3.0 database with 9,886 provinces, 56,928 adjacency rows and 9,886 cached geometries:

| Stage | Observed |
|---|---:|
| Exact localized endpoint resolution | 192 ms |
| Route graph load | 67 ms |
| 19-water-province shortest path | 1 ms |
| Corridor selection | 3 ms |
| 2200×1300 bilingual render | 2,585 ms |
| PNG encode | 371 ms |

The warm route total is about 263 ms. The CJK-font route render, including corridor selection and PNG encoding, completed in about 3.03 seconds with 90 label items; both remain inside the requested 1-second and 6-second targets. Results are workload-specific; timings are returned per call so deployments can detect regressions.

The default real-route JSON response was 17,001 bytes, below the 32 KiB acceptance ceiling. `verbose=true` is the explicit opt-in for additional bounded evidence.

## Known limits

- Pixel distance is a map-space proxy only.
- Route legality describes the indexed physical/strategic province graph, not every CK3 AI movement modifier or travel-time rule.
- Localized name resolution is exact rather than fuzzy by design; ambiguous names require an explicit candidate.
- The route renderer selects bounded context and labels, but complex editorial layout remains an HTML/SVG responsibility.
- A font must be configured server-side for Chinese map labels. MCP never accepts a client filesystem font path.
