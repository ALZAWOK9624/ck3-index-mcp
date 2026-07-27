# CK3 migration version gate and coordinate map delta

Rebase profile schema 2 separates two migration modes:

- `same_game_version`: Base and Target must share the same CK3 `major.minor`
  family. The current semantic bundle supports CK3 `1.19`.
- `cross_game_version`: Base and Target use different CK3 `major.minor`
  families. Hash classification and exact raster evidence are still produced,
  but Jomini semantic replay is blocked until an explicit compatibility
  adapter for that version pair is registered.

Every profile must declare the mode and both versions:

```toml
schema_version = 2
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.0.6"
```

`plan` resolves this gate before loading semantic indexes. When a source has a
`descriptor.mod` (or a root launcher `.mod` file), its `supported_version` is
checked against the declared Base or Target family. A mismatch, malformed
descriptor, unsupported Target family, or unregistered cross-version adapter
is a transaction conflict rather than an implicit best guess.

With `map_authority = "project"`, PNG and TGA files under `map_data/` or
`gfx/map/` use a coordinate-delta strategy:

1. Decode Base and Ours into a common top-left RGBA canvas.
2. Make a transparent PNG whose non-transparent pixels are exactly the
   coordinates where Ours differs from Base.
3. Check each changed coordinate against Theirs. If Theirs is unchanged from
   Base, or already equals Ours, apply the Ours pixel. If both sides changed
   the coordinate differently, block and generate the existing magenta
   collision mask.
4. Encode the merged CK3-facing candidate in the Target file type. The
   transparent patch remains transaction evidence under `map-deltas/`.

The strategy never scales, warps, or guesses geometry. Base, Ours, and Theirs
must have identical dimensions. Changed project pixels must be opaque because
the patch alpha channel is the coordinate-presence mask; non-opaque changes
are blocked instead of being represented lossily.
