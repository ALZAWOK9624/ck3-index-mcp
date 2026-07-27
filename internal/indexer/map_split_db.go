package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// Everything a split needs is already indexed: the province outline, every id
// and colour in use, the parent's culture and county, and the elevation raster.
// Gathering it here keeps the planner a pure function while making the tool
// usable from a single province id.

// ProvinceSplitInputs is the indexed state one split draws on.
type ProvinceSplitInputs struct {
	Geometry []MapRun
	Context  MapSplitContext
	Terrain  MapSplitTerrain
	// TerrainSource names what backs Terrain so the caller can report whether
	// boundaries actually followed the landscape or fell back to distance.
	TerrainSource string
}

// elevationTerrain derives traversal resistance from the local elevation
// gradient rather than from elevation itself. Height alone would push
// boundaries to whatever is lowest; what makes a border look natural is that
// it runs along the steep ground separating two basins, and steepness is the
// gradient.
type elevationTerrain struct {
	raster *mapPhysicalRaster
	scaleX float64
	scaleY float64
}

// elevationGradientFullScale is the per-pixel grey-level difference treated as
// maximum resistance. CK3 heightmaps span 0..255 over the whole altitude
// range, so a step of this size between neighbouring pixels is already a cliff;
// anything steeper does not need to be distinguished further.
const elevationGradientFullScale = 24.0

func (t elevationTerrain) Resistance(x, y int) float64 {
	if t.raster == nil || t.raster.Image == nil {
		return 0
	}
	rx := int(float64(x) * t.scaleX)
	ry := int(float64(y) * t.scaleY)
	bounds := t.raster.Image.Bounds()
	if rx <= bounds.Min.X || ry <= bounds.Min.Y || rx >= bounds.Max.X-1 || ry >= bounds.Max.Y-1 {
		return 0
	}
	at := func(px, py int) float64 { return float64(t.raster.Image.GrayAt(px, py).Y) }
	dx := at(rx+1, ry) - at(rx-1, ry)
	dy := at(rx, ry+1) - at(rx, ry-1)
	gradient := math.Hypot(dx, dy) / 2
	resistance := gradient / elevationGradientFullScale
	if resistance > 1 {
		return 1
	}
	return resistance
}

// LoadProvinceSplitInputs collects the geometry, uniqueness sets, inherited
// attributes and terrain for one province. A missing elevation raster is not an
// error: the split degrades to distance-based growth and says so.
func (db *DB) LoadProvinceSplitInputs(ctx context.Context, provinceID int) (ProvinceSplitInputs, error) {
	inputs := ProvinceSplitInputs{TerrainSource: "none"}

	var fill []byte
	err := db.sql.QueryRowContext(ctx, `SELECT fill_rle FROM map_province_geometry WHERE province_id=?`, provinceID).Scan(&fill)
	if err == sql.ErrNoRows {
		return inputs, fmt.Errorf("province %d has no indexed geometry; run a full scan against a workspace whose provinces.png contains it", provinceID)
	}
	if err != nil {
		return inputs, err
	}
	runs, err := DecodeMapRuns(fill)
	if err != nil {
		return inputs, fmt.Errorf("province %d geometry: %w", provinceID, err)
	}
	inputs.Geometry = runs

	usedIDs := map[int]bool{}
	usedColors := map[uint32]bool{}
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id,COALESCE(color_rgb,-1) FROM map_provinces`)
	if err != nil {
		return inputs, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, color int64
		if err := rows.Scan(&id, &color); err != nil {
			return inputs, err
		}
		usedIDs[int(id)] = true
		if color >= 0 {
			usedColors[uint32(color)&0xFFFFFF] = true
		}
	}
	if err := rows.Err(); err != nil {
		return inputs, err
	}
	inputs.Context.UsedProvinceIDs = usedIDs
	inputs.Context.UsedColors = usedColors

	var county, barony sql.NullString
	var sourceColor sql.NullInt64
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COALESCE(county,''),COALESCE(barony,''),color_rgb FROM map_provinces WHERE province_id=?`, provinceID,
	).Scan(&county, &barony, &sourceColor); err != nil && err != sql.ErrNoRows {
		return inputs, err
	}
	inputs.Context.SourceCounty = county.String
	if sourceColor.Valid && sourceColor.Int64 >= 0 {
		inputs.Context.SourceColor = uint32(sourceColor.Int64) & 0xFFFFFF
	}
	// The barony name is the closest thing the index holds to the province's
	// own label, and it is what a modder recognises; definition.csv names are
	// not indexed separately.
	if barony.String != "" {
		inputs.Context.SourceName = barony.String
	} else {
		inputs.Context.SourceName = fmt.Sprintf("province_%d", provinceID)
	}

	// History is keyed by date, so the earliest entry per field is the province's
	// starting state. Later dated changes belong to the parent's timeline and
	// should not be baked into a new province's initial definition.
	historyRows, err := db.sql.QueryContext(ctx,
		`SELECT field,value FROM map_province_history WHERE province_id=? ORDER BY date_key ASC`, provinceID)
	if err != nil {
		return inputs, err
	}
	defer historyRows.Close()
	for historyRows.Next() {
		var field, value string
		if err := historyRows.Scan(&field, &value); err != nil {
			return inputs, err
		}
		switch field {
		case "culture":
			if inputs.Context.SourceCulture == "" {
				inputs.Context.SourceCulture = value
			}
		case "religion", "faith":
			if inputs.Context.SourceReligion == "" {
				inputs.Context.SourceReligion = value
			}
		case "holding":
			if inputs.Context.SourceHolding == "" {
				inputs.Context.SourceHolding = value
			}
		}
	}
	if err := historyRows.Err(); err != nil {
		return inputs, err
	}

	terrain, source, err := db.loadSplitTerrain(ctx)
	if err != nil {
		return inputs, err
	}
	inputs.Terrain = terrain
	inputs.TerrainSource = source
	return inputs, nil
}

func (db *DB) loadSplitTerrain(ctx context.Context) (MapSplitTerrain, string, error) {
	raster, err := db.loadMapPhysicalRaster(ctx, "elevation")
	if err != nil {
		return nil, "none", err
	}
	if raster == nil || raster.Image == nil {
		return nil, "none", nil
	}
	mapWidth, mapHeight := db.mapPixelSize(ctx)
	bounds := raster.Image.Bounds()
	scaleX, scaleY := 1.0, 1.0
	// The heightmap is routinely a different resolution from provinces.png, so
	// province coordinates have to be rescaled before sampling it.
	if mapWidth > 0 && bounds.Dx() > 0 {
		scaleX = float64(bounds.Dx()) / float64(mapWidth)
	}
	if mapHeight > 0 && bounds.Dy() > 0 {
		scaleY = float64(bounds.Dy()) / float64(mapHeight)
	}
	return elevationTerrain{raster: raster, scaleX: scaleX, scaleY: scaleY}, "elevation", nil
}

func (db *DB) mapPixelSize(ctx context.Context) (int, int) {
	width, height := 0, 0
	var raw string
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_width'`).Scan(&raw); err == nil {
		fmt.Sscanf(raw, "%d", &width)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_height'`).Scan(&raw); err == nil {
		fmt.Sscanf(raw, "%d", &height)
	}
	return width, height
}

// SplitProvince runs the whole flow for one province id: gather indexed state,
// divide the pixels, then plan the downstream files.
func (db *DB) SplitProvince(ctx context.Context, request MapSplitRequest, emit MapSplitEmit) (MapSplitResult, MapSplitPlan, error) {
	inputs, err := db.LoadProvinceSplitInputs(ctx, request.ProvinceID)
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, err
	}
	result, err := SplitProvinceGeometry(inputs.Geometry, request, inputs.Terrain)
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, err
	}
	if inputs.TerrainSource == "elevation" && request.TerrainWeight <= 0 {
		result.Warnings = append(result.Warnings,
			"an elevation raster is indexed but terrain_weight is 0, so boundaries ignored it; raise terrain_weight to follow the landscape")
	}
	plan, err := PlanProvinceSplit(result, inputs.Context, emit)
	if err != nil {
		return result, MapSplitPlan{}, err
	}
	return result, plan, nil
}
