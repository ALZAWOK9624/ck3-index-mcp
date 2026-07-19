# CK3 GIS physical-context analysis

`map_physical_context` is a read-only map-analysis interface. It never writes CK3 map assets and never converts normalized height values into metres.

## Evidence layers

- Observed: `heightmap.png`, `rivers.png`, `default.map`, `provinces.png`, `definition.csv`, `adjacencies.csv`, and the active `gfx/map/terrain` material configuration and blend rasters.
- Derived: province zonal elevation statistics, normalized per-pixel slope and ruggedness, connected ocean/lake bodies, relative bathymetry, river-pixel counts, and verified WhiteboxTools D8 catchment pixels.
- Inferred: ridge/valley scores, shelf/trench scores, major-river direction, mouths, coastal drop-off, and strait sill depth.

`river_order` is deliberately reported as a province-scale 1-5 proxy derived from verified D8 catchment percentiles and only attached to provinces containing an ordinary river pixel or a `river_provinces` channel. It is not presented as surveyed discharge or an exact Strahler order.

`default.map` water classes remain distinct. `river_provinces` are major river channels, not ocean floor. `impassable_seas` are observed gameplay barriers, not geological conclusions. Lakes use a local boundary reference; connected sea zones use an ocean reference.

## Observed map-surface materials

The surface-material layer reads the active `materials.settings` together with `detail_index.tga` and `detail_intensity.tga`. The index and intensity channels are the authority for where a material is painted and how strongly it contributes. The material definition links that observed blend to its diffuse, normal, properties, and mask resources, including PNG masks and DDS textures.

Province aggregates are sampled at a deterministic four-pixel stride and report blend-weight shares, retained weight, sample count, and effective material count. A 40% material weight means 40% of the retained sampled blend weight; it is not necessarily 40% of the province's area. These facts are also distinct from scripted `common/province_terrain`, which affects gameplay.

Use `operation=surface` to query only this cached observed layer. It works without WhiteboxTools and returns bounded relative resource paths rather than raster or DDS bodies. Texture filenames and masks are evidence about the configured renderer material, not sufficient proof of climate, ecology, soil, or gameplay terrain.

## WhiteboxTools boundary

Release bundles pin WhiteboxTools Open Core v2.4.0 by archive and executable SHA-256 using `third_party/whitebox-tools-v2.4.0.json`. The sidecar is invoked directly without a shell, accepts only the compiled allowlist, writes only under `gis_cache_root`, has bounded output, and is killed as a process tree on timeout.

If the sidecar is missing, disabled, the wrong version, or has the wrong hash, CK3-native facts and built-in relative raster aggregates remain queryable. Catchment pixels, flow percentile, and other sidecar-only facts remain absent and the result is `degraded`.

## Query contract

```json
{
  "target_type": "province",
  "target": "123",
  "operation": "oceanography"
}
```

Operations are `summary`, `terrain`, `surface`, `hydrology`, `oceanography`, and `barriers`. `targets` accepts at most 16 province or landed-title targets. `limit` accepts 1-20 result items and defaults to 16 in the CLI. `all` returns a bounded global aggregate rather than dumping every province.

Exact geographical regions are first-class targets. For a coast question, request the cached adjacent-water aggregate directly:

```json
{
  "target_type": "region",
  "target": "region:GH_geographic_shattered_coast",
  "operation": "oceanography",
  "include_adjacent_water": true,
  "limit": 6
}
```

This performs one set-based database query path from expanded region members through pixel-border adjacency. It does not start WhiteboxTools, decode a raster, call `map_neighbors`, or build a full thematic metric. The original `aggregate` still describes the selected region; `adjacent_water` describes unique water provinces touching selected land. If a region contains no land, selected water is analyzed instead and `selection_mode` records that fallback.

Ocean depth classes use the current map's sample-weighted P33 and P67 relative-depth thresholds. A coast is `predominantly_shallow` or `predominantly_deep` at a 60% share; otherwise it is `mixed`. Coverage below 80% returns `insufficient_data`. Lakes and `river_provinces` are always reported separately and cannot affect this verdict. `impassable_sea` remains part of physical ocean depth while retaining its separate gameplay-barrier count.

The corresponding CLI is:

```text
ck3-index map physical-context <spec.json>
```
