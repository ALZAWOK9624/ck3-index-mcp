package indexer

import (
	"context"
	"database/sql"
	"image"
)

// mapProvinceMeta is the subset of map_provinces the renderer reads per
// province. A full atlas touches every column below for every province in the
// viewport, so the renderer loads the table once instead of issuing one
// single-row query per province per layer.
type mapProvinceMeta struct {
	CenterX     float64
	CenterY     float64
	MinX        int
	MinY        int
	MaxX        int
	MaxY        int
	Area        int
	Blocked     int
	BlockKind   string
	WaterKind   string
	Terrain     string
	Barony      string
	County      string
	Duchy       string
	Kingdom     string
	Empire      string
	CountyCapit bool
}

// levelID returns the de jure title at the requested level, matching the
// column names accepted by mapLevelColumn.
func (m mapProvinceMeta) levelID(level string) string {
	switch level {
	case "barony":
		return m.Barony
	case "county":
		return m.County
	case "duchy":
		return m.Duchy
	case "kingdom":
		return m.Kingdom
	case "empire":
		return m.Empire
	}
	return ""
}

// mapRenderScratch is per-render state shared by every layer pass. It exists
// because a single political atlas draws nine border layers plus fill, physical
// and relief passes over the same provinces: without it each pass re-queried
// and re-decoded identical province geometry, and each border layer allocated a
// fresh full-canvas mask.
type mapRenderScratch struct {
	fillRuns     map[int][]MapRun
	boundaryRuns map[int][]MapRun
	meta         map[int]mapProvinceMeta
	borderMask   []int32
	boolMasks    [2][]bool
	columnSource []float64
	rowSource    []float64
	// occupied is shared by the symbol and label passes. Labels used to avoid
	// only other labels, so place names were routinely overprinted by the very
	// settlement glyphs they were naming.
	occupied []image.Rectangle
}

// claim reserves a rectangle if it is still free, reporting whether it was
// taken. Symbols claim first; labels then have to find clear paper.
func (s *mapRenderScratch) claim(r image.Rectangle) bool {
	for _, taken := range s.occupied {
		if r.Overlaps(taken) {
			return false
		}
	}
	s.occupied = append(s.occupied, r)
	return true
}

// occupiedBy reports whether anything already sits in r, without claiming it.
func (s *mapRenderScratch) occupiedBy(r image.Rectangle) bool {
	for _, taken := range s.occupied {
		if r.Overlaps(taken) {
			return true
		}
	}
	return false
}

func newMapRenderScratch() *mapRenderScratch {
	return &mapRenderScratch{
		fillRuns:     map[int][]MapRun{},
		boundaryRuns: map[int][]MapRun{},
	}
}

// provinceMeta loads map_provinces once on first use. The renderer always ends
// up reading most of the table, so a single scan beats thousands of point
// lookups even for a narrow target.
func (s *mapRenderScratch) provinceMeta(ctx context.Context, db *DB) (map[int]mapProvinceMeta, error) {
	if s.meta != nil {
		return s.meta, nil
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id,COALESCE(center_x,0),COALESCE(center_y,0),
		COALESCE(min_x,0),COALESCE(min_y,0),COALESCE(max_x,0),COALESCE(max_y,0),COALESCE(area,0),
		blocked,COALESCE(block_kind,''),COALESCE(water_kind,''),COALESCE(terrain,''),
		COALESCE(barony,''),COALESCE(county,''),COALESCE(duchy,''),COALESCE(kingdom,''),COALESCE(empire,''),
		is_county_capital FROM map_provinces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	meta := make(map[int]mapProvinceMeta, 12288)
	for rows.Next() {
		var pid, capital int
		var m mapProvinceMeta
		if err := rows.Scan(&pid, &m.CenterX, &m.CenterY, &m.MinX, &m.MinY, &m.MaxX, &m.MaxY, &m.Area,
			&m.Blocked, &m.BlockKind, &m.WaterKind, &m.Terrain,
			&m.Barony, &m.County, &m.Duchy, &m.Kingdom, &m.Empire, &capital); err != nil {
			return nil, err
		}
		m.CountyCapit = capital != 0
		meta[pid] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.meta = meta
	return s.meta, nil
}

func (s *mapRenderScratch) provinceMetaFor(ctx context.Context, db *DB, pid int) (mapProvinceMeta, bool, error) {
	meta, err := s.provinceMeta(ctx, db)
	if err != nil {
		return mapProvinceMeta{}, false, err
	}
	value, ok := meta[pid]
	return value, ok, nil
}

// runs returns the cached RLE spans for one province, loading them on first
// use. Decoding is not repeated: every layer pass shares the same slice, which
// callers only read.
func (s *mapRenderScratch) runs(ctx context.Context, db *DB, pid int, boundary bool) ([]MapRun, error) {
	cache := s.fillRuns
	if boundary {
		cache = s.boundaryRuns
	}
	if runs, ok := cache[pid]; ok {
		return runs, nil
	}
	column := "fill_rle"
	if boundary {
		column = "boundary_rle"
	}
	var data []byte
	err := db.sql.QueryRowContext(ctx, `SELECT `+column+` FROM map_province_geometry WHERE province_id=?`, pid).Scan(&data)
	if err == sql.ErrNoRows {
		cache[pid] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	runs, err := DecodeMapRuns(data)
	if err != nil {
		return nil, err
	}
	cache[pid] = runs
	return runs, nil
}

// primeRuns bulk-loads geometry when the render is about to walk a large share
// of the map. One table scan replaces thousands of point lookups; for narrow
// targets the lazy path in runs stays cheaper.
func (s *mapRenderScratch) primeRuns(ctx context.Context, db *DB, wanted int, boundary bool) error {
	if wanted < 512 {
		return nil
	}
	cache := s.fillRuns
	if boundary {
		cache = s.boundaryRuns
	}
	if len(cache) > 0 {
		return nil
	}
	column := "fill_rle"
	if boundary {
		column = "boundary_rle"
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id,`+column+` FROM map_province_geometry`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var data []byte
		if err := rows.Scan(&pid, &data); err != nil {
			return err
		}
		runs, err := DecodeMapRuns(data)
		if err != nil {
			return err
		}
		cache[pid] = runs
	}
	return rows.Err()
}

// mask hands out the single reusable border mask, cleared for the caller. Each
// border layer needs a width*height int32 buffer, and a political atlas draws
// nine of them; allocating per layer churned over a gigabyte per render.
func (s *mapRenderScratch) mask(size int) []int32 {
	if cap(s.borderMask) < size {
		s.borderMask = make([]int32, size)
		return s.borderMask
	}
	s.borderMask = s.borderMask[:size]
	clear(s.borderMask)
	return s.borderMask
}

// boolMask hands out one of the reusable land/water coverage masks. Each is a
// full canvas of bytes, so they are kept for the life of the render.
func (s *mapRenderScratch) boolMask(index, size int) []bool {
	if s.boolMasks[index] == nil || cap(s.boolMasks[index]) < size {
		s.boolMasks[index] = make([]bool, size)
		return s.boolMasks[index]
	}
	s.boolMasks[index] = s.boolMasks[index][:size]
	clear(s.boolMasks[index])
	return s.boolMasks[index]
}

// sourceColumns and sourceRows precompute the render-pixel to source-pixel
// projection once per render. The raster passes previously recomputed the same
// float division for every pixel of every pass.
func (s *mapRenderScratch) sourceColumns(v renderViewport) []float64 {
	if len(s.columnSource) == v.Width {
		return s.columnSource
	}
	out := make([]float64, v.Width)
	for x := 0; x < v.Width; x++ {
		out[x] = float64(v.MinX) + float64(x-v.OffsetX)/v.Scale
	}
	s.columnSource = out
	return out
}

func (s *mapRenderScratch) sourceRows(v renderViewport) []float64 {
	if len(s.rowSource) == v.Height {
		return s.rowSource
	}
	out := make([]float64, v.Height)
	for y := 0; y < v.Height; y++ {
		out[y] = float64(v.MinY) + float64(y-v.OffsetY)/v.Scale
	}
	s.rowSource = out
	return out
}
