package indexer

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
)

// CK3 carries two river systems and they are not interchangeable.
//
// Large rivers live in the heightmap: they are valleys the land is cut into,
// they block movement, and armies need crossings. Small rivers live in
// rivers.png as a painted overlay -- decorative water that the engine draws but
// the terrain does not contain. A map with only the first has no streams; a map
// with only the second has water running across ground that never dips for it.
//
// So this generates the overlay and, as the same operation, cuts a shallow bed
// for every stream it paints. A stream drawn on ground that does not fall
// toward it reads as a line painted on a hillside, which is exactly what it is.
//
// The palette is fixed by the engine: 0 is land, 1 marks a source, 2 marks a
// confluence, 3..11 are the river body from narrowest to widest, 255 is sea.
// Topology is checked on orthogonal neighbours only, which is the constraint
// that shapes the generator: flow routing works on eight directions, so every
// diagonal step has to be broken into two axis steps or the channel is
// diagonally disconnected as far as CK3 is concerned.

// MapSmallRiverSettings controls overlay generation.
type MapSmallRiverSettings struct {
	// MinDrainage is the catchment at which a stream appears.
	MinDrainage int `json:"min_drainage"`
	// MaxDrainage is where a stream becomes a large river. Anything above this
	// belongs in the heightmap instead, so the two systems do not overlap.
	MaxDrainage int `json:"max_drainage"`
	// BedDepth is how deep the painted streams are cut into the heightmap.
	// Shallow by design: a stream is not a gorge.
	BedDepth float64 `json:"bed_depth"`
	SeaLevel int     `json:"sea_level"`
	// MaxWidthIndex caps the painted width. Streams should stay narrow; the
	// widest indices belong to rivers that are in the heightmap anyway.
	MaxWidthIndex int `json:"max_width_index"`
}

// MapSmallRiverStats reports what was painted and cut.
type MapSmallRiverStats struct {
	StreamPixels int      `json:"stream_pixels"`
	Sources      int      `json:"sources"`
	Confluences  int      `json:"confluences"`
	BedCells     int      `json:"bed_cells"`
	Warnings     []string `json:"warnings,omitempty"`
}

// DefaultSmallRivers is scaled for the drainage densities this workspace's
// heightmap produces.
func DefaultSmallRivers() MapSmallRiverSettings {
	return MapSmallRiverSettings{
		MinDrainage: 60, MaxDrainage: 4000, BedDepth: 2.5, SeaLevel: 20, MaxWidthIndex: 6,
	}
}

// GenerateSmallRivers paints a CK3 rivers.png overlay for one region and cuts a
// shallow bed for each stream into the heightmap, which is modified in place.
func GenerateSmallRivers(height *image.Gray, region image.Rectangle, settings MapSmallRiverSettings) (*image.Paletted, MapSmallRiverStats, error) {
	stats := MapSmallRiverStats{}
	if height == nil {
		return nil, stats, fmt.Errorf("no heightmap supplied")
	}
	if settings.MinDrainage < 1 {
		return nil, stats, fmt.Errorf("min_drainage must be at least 1")
	}
	if settings.MaxDrainage > 0 && settings.MaxDrainage <= settings.MinDrainage {
		return nil, stats, fmt.Errorf("max_drainage must exceed min_drainage")
	}
	if math.IsNaN(settings.BedDepth) || math.IsInf(settings.BedDepth, 0) {
		return nil, stats, fmt.Errorf("bed_depth is not finite")
	}
	maxWidth := settings.MaxWidthIndex
	if maxWidth < riverBodyMin {
		maxWidth = riverBodyMin
	}
	if maxWidth > riverBodyMax {
		maxWidth = riverBodyMax
	}

	bounds := height.Bounds()
	region = region.Intersect(bounds)
	if region.Dx() < 8 || region.Dy() < 8 {
		return nil, stats, fmt.Errorf("region is too small to route drainage")
	}

	overlay := image.NewPaletted(bounds, ck3RiverPalette())
	// Everything starts as land or sea; streams are painted over it.
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			index := uint8(riverLand)
			if int(height.GrayAt(x, y).Y) <= settings.SeaLevel {
				index = riverSea
			}
			overlay.SetColorIndex(x, y, index)
		}
	}

	w, h := region.Dx(), region.Dy()
	field := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			field[y*w+x] = float64(height.GrayAt(region.Min.X+x, region.Min.Y+y).Y)
		}
	}
	filled, _ := fillDepressions(field, w, h, float64(settings.SeaLevel))
	drainage := accumulateDrainage(filled, w, h)

	// Walk each stream from its head downstream, converting the eight-direction
	// flow path into orthogonal steps as it goes.
	inStream := make([]bool, w*h)
	arrivals := make([]int, w*h)
	var painted []int

	heads := make([]int, 0, 64)
	for i, d := range drainage {
		if d < settings.MinDrainage {
			continue
		}
		if settings.MaxDrainage > 0 && d > settings.MaxDrainage {
			continue
		}
		if field[i] <= float64(settings.SeaLevel) || filled[i]-field[i] > pondedDepth {
			continue
		}
		// A head is a stream cell no upstream stream cell drains into, which is
		// where the engine expects the source marker.
		if !hasUpstreamStream(filled, drainage, w, h, i, settings) {
			heads = append(heads, i)
		}
	}
	sort.Ints(heads)

	for _, head := range heads {
		cell := head
		for step := 0; step < w*h; step++ {
			if !inStream[cell] {
				inStream[cell] = true
				painted = append(painted, cell)
			}
			next, ok := steepestDownhill(filled, w, h, cell)
			if !ok {
				break
			}
			if field[next] <= float64(settings.SeaLevel) {
				break
			}
			if settings.MaxDrainage > 0 && drainage[next] > settings.MaxDrainage {
				// The stream has grown into a large river, which the heightmap
				// carries; stop before the two systems overlap.
				break
			}
			// Break a diagonal into two orthogonal steps so the painted channel
			// is connected under CK3's four-neighbour topology rules.
			cx, cy := cell%w, cell/w
			nx, ny := next%w, next/w
			if cx != nx && cy != ny {
				// Either corner joins the two cells. Prefer the one carrying
				// less water: the bridge is an artefact of the routing grid, not
				// a real channel, so it must not drag the stream over the
				// large-river ceiling and paint water the heightmap already owns.
				bridge := cy*w + nx
				if other := ny*w + cx; drainage[other] < drainage[bridge] {
					bridge = other
				}
				if settings.MaxDrainage > 0 && drainage[bridge] > settings.MaxDrainage {
					break
				}
				if !inStream[bridge] {
					inStream[bridge] = true
					painted = append(painted, bridge)
				}
				arrivals[bridge]++
			}
			arrivals[next]++
			cell = next
		}
	}

	// A head whose very first step leaves the region, meets the sea, or crosses
	// into large-river territory paints exactly one cell. CK3 checks topology on
	// orthogonal neighbours, so a lone cell is a channel connected to nothing --
	// it renders as a stray dot of water sitting on dry land.
	painted = dropIsolatedStreamCells(painted, inStream, w, h)

	if len(painted) == 0 {
		stats.Warnings = append(stats.Warnings,
			"no stream met the drainage window; lower min_drainage or widen the region")
		return overlay, stats, nil
	}

	// Width follows catchment, so a stream thickens as tributaries join.
	maxStreamDrainage := 1
	for _, i := range painted {
		if drainage[i] > maxStreamDrainage {
			maxStreamDrainage = drainage[i]
		}
	}
	for _, i := range painted {
		x, y := region.Min.X+i%w, region.Min.Y+i/w
		share := math.Sqrt(float64(drainage[i])) / math.Sqrt(float64(maxStreamDrainage))
		index := riverBodyMin + int(math.Round(share*float64(maxWidth-riverBodyMin)))
		if index < riverBodyMin {
			index = riverBodyMin
		}
		if index > maxWidth {
			index = maxWidth
		}
		overlay.SetColorIndex(x, y, uint8(index))
		stats.StreamPixels++

		// Cut the bed. Depth follows the same catchment share so a headwater
		// trickle does not trench as deeply as the stream it feeds.
		if settings.BedDepth > 0 {
			value := float64(height.GrayAt(x, y).Y) - settings.BedDepth*(0.4+0.6*share)
			if value < float64(settings.SeaLevel) {
				value = float64(settings.SeaLevel)
			}
			height.SetGray(x, y, grayLevel(value))
			stats.BedCells++
		}
	}

	// Markers go on last: a cell nothing flows into is a source, and a cell two
	// or more streams flow into is a confluence.
	for _, i := range painted {
		x, y := region.Min.X+i%w, region.Min.Y+i/w
		switch {
		case arrivals[i] == 0:
			overlay.SetColorIndex(x, y, riverSource)
			stats.Sources++
		case arrivals[i] > 1:
			overlay.SetColorIndex(x, y, riverConfluence)
			stats.Confluences++
		}
	}
	return overlay, stats, nil
}

const (
	// Land is 255 and water is 254, and neither is 0.
	//
	// An earlier reading of this got the two backwards by matching whole-image
	// index ratios against an assumed land/sea split. Ratios cannot settle it;
	// provinces.png and default.map can, because they define the domains. Cross
	// tabulating the upstream map's rivers.png against them is unambiguous:
	// 99.2% of ocean-province pixels and 98.1% of lake-province pixels carry
	// index 254, while 80.2% of land-province pixels carry 255. Swapping them
	// paints every channel onto water and the whole sea onto land.
	riverLand       = 255
	riverSource     = 1
	riverConfluence = 2
	riverBodyMin    = 3
	riverBodyMax    = 11
	riverSea        = 254
)

// hasUpstreamStream reports whether any neighbour that flows into this cell is
// itself already a stream, which decides where a source marker belongs.
func hasUpstreamStream(filled []float64, drainage []int, w, h, index int, settings MapSmallRiverSettings) bool {
	cx, cy := index%w, index/w
	for _, step := range [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		nx, ny := cx+step[0], cy+step[1]
		if nx < 0 || ny < 0 || nx >= w || ny >= h {
			continue
		}
		neighbour := ny*w + nx
		if drainage[neighbour] < settings.MinDrainage {
			continue
		}
		if settings.MaxDrainage > 0 && drainage[neighbour] > settings.MaxDrainage {
			continue
		}
		if down, ok := steepestDownhill(filled, w, h, neighbour); ok && down == index {
			return true
		}
	}
	return false
}

// steepestDownhill returns the neighbour a cell drains into.
func steepestDownhill(filled []float64, w, h, index int) (int, bool) {
	cx, cy := index%w, index/w
	best, bestHeight := -1, filled[index]
	for _, step := range [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		nx, ny := cx+step[0], cy+step[1]
		if nx < 0 || ny < 0 || nx >= w || ny >= h {
			continue
		}
		neighbour := ny*w + nx
		if filled[neighbour] < bestHeight {
			best, bestHeight = neighbour, filled[neighbour]
		}
	}
	return best, best >= 0
}

// ck3RiverPalette returns the exact palette CK3 requires. The engine reads
// rivers.png by palette index, so a visually identical image with a different
// palette order is silently wrong.
func ck3RiverPalette() color.Palette {
	canonical := canonicalCK3RiverPalette()
	palette := make(color.Palette, len(canonical))
	for i, rgb := range canonical {
		palette[i] = color.RGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 255}
	}
	return palette
}

// dropIsolatedStreamCells removes painted cells that ended up with no
// orthogonal stream neighbour, and clears them from the membership set so the
// marker pass does not treat them as sources.
func dropIsolatedStreamCells(painted []int, inStream []bool, w, h int) []int {
	kept := painted[:0]
	for _, i := range painted {
		x, y := i%w, i/w
		connected := false
		for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := x+step[0], y+step[1]
			if nx < 0 || ny < 0 || nx >= w || ny >= h {
				continue
			}
			if inStream[ny*w+nx] {
				connected = true
				break
			}
		}
		if connected {
			kept = append(kept, i)
		} else {
			inStream[i] = false
		}
	}
	return kept
}
