package indexer

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
)

// Province splitting divides one province's pixels among caller-chosen seeds.
//
// The boundaries have to look like they were drawn by hand, which rules out
// the obvious approaches: a straight bisection or a plain Voronoi diagram
// produces geometric edges that cut across ridges and valleys, and every
// player recognises them as artificial. Instead each seed grows outward by
// accumulated traversal cost, and cost rises with terrain resistance. Growth
// therefore stalls against steep ground and races along flat ground, so the
// frontier where two seeds meet settles into the ridge line between them —
// which is where a real border would sit.
//
// Growing from seeds also guarantees the property CK3 requires and a
// geometric partition does not: every resulting part is connected.

// MapSplitInputError marks a request the caller can correct: too few seeds, a
// seed outside the province, a duplicate. Keeping it distinct from internal
// failures lets the tool layer answer "your request is wrong" instead of "the
// server broke", which are different problems with different fixes.
type MapSplitInputError struct{ message string }

func (e *MapSplitInputError) Error() string { return e.message }

func splitInputErrorf(format string, args ...any) error {
	return &MapSplitInputError{message: fmt.Sprintf(format, args...)}
}

// MapSplitSeed is one growth origin inside the province being split.
type MapSplitSeed struct {
	X    int    `json:"x"`
	Y    int    `json:"y"`
	Name string `json:"name,omitempty"`
}

// MapSplitRequest describes one province division.
type MapSplitRequest struct {
	ProvinceID int            `json:"province_id"`
	Seeds      []MapSplitSeed `json:"seeds"`
	// TerrainWeight scales how strongly terrain deflects the boundary. 0 grows
	// purely by distance, which yields Voronoi-like edges that ignore the
	// landscape; higher values bend boundaries toward ridges. Values above
	// about 8 make growth so terrain-bound that parts can become slivers.
	TerrainWeight float64 `json:"terrain_weight"`
}

// MapSplitPart is one resulting region.
type MapSplitPart struct {
	Index      int          `json:"index"`
	Seed       MapSplitSeed `json:"seed"`
	PixelCount int          `json:"pixel_count"`
	MinX       int          `json:"min_x"`
	MinY       int          `json:"min_y"`
	MaxX       int          `json:"max_x"`
	MaxY       int          `json:"max_y"`
	Runs       []MapRun     `json:"-"`
}

// MapSplitResult reports the division and anything the caller must resolve
// before it can be turned into a patch.
type MapSplitResult struct {
	ProvinceID  int            `json:"province_id"`
	SourcePixel int            `json:"source_pixel_count"`
	Parts       []MapSplitPart `json:"parts"`
	Unreachable int            `json:"unreachable_pixel_count"`
	// OrphanPieces counts disconnected pieces of the source province that no
	// seed could grow into and were attached to their nearest seed instead.
	// Non-zero is normal for islands and exclaves; it is reported so the caller
	// can place a seed inside a piece when the automatic choice is wrong.
	OrphanPieces int      `json:"orphan_piece_count"`
	OrphanPixels int      `json:"orphan_pixel_count"`
	Warnings     []string `json:"warnings,omitempty"`
}

// MapSplitTerrain supplies per-pixel traversal resistance in [0,1], where 1 is
// the most costly ground to cross. It is an interface so the caller can back it
// with a slope raster, a ruggedness raster, or nothing at all.
type MapSplitTerrain interface {
	Resistance(x, y int) float64
}

// uniformTerrain makes every pixel equally cheap, reducing the growth to a
// connected Voronoi partition. It is the honest fallback when no raster is
// indexed: boundaries are then geometric, and the caller is told so.
type uniformTerrain struct{}

func (uniformTerrain) Resistance(int, int) float64 { return 0 }

type splitCell struct {
	index int
	cost  float64
	owner int
}

type splitQueue []splitCell

func (q splitQueue) Len() int           { return len(q) }
func (q splitQueue) Less(i, j int) bool { return q[i].cost < q[j].cost }
func (q splitQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *splitQueue) Push(v any)        { *q = append(*q, v.(splitCell)) }
func (q *splitQueue) Pop() any          { old := *q; n := len(old); v := old[n-1]; *q = old[:n-1]; return v }

// SplitProvinceGeometry divides the pixels described by runs among the request's
// seeds. It does not touch any file: the result is geometry the caller can turn
// into a reviewable patch.
func SplitProvinceGeometry(runs []MapRun, request MapSplitRequest, terrain MapSplitTerrain) (MapSplitResult, error) {
	if len(request.Seeds) < 2 {
		return MapSplitResult{}, splitInputErrorf("splitting a province needs at least 2 seeds, got %d", len(request.Seeds))
	}
	if len(runs) == 0 {
		return MapSplitResult{}, splitInputErrorf("province %d has no indexed geometry to split", request.ProvinceID)
	}
	if terrain == nil {
		terrain = uniformTerrain{}
	}
	weight := request.TerrainWeight
	if weight < 0 || math.IsNaN(weight) {
		weight = 0
	}

	minX, minY, maxX, maxY := runBounds(runs)
	width, height := maxX-minX+1, maxY-minY+1
	// Work in a bounding-box-local grid: a province is a small fraction of an
	// 8192x4096 map, so a full-map array would waste three orders of magnitude
	// more memory than the region being split.
	owner := make([]int32, width*height)
	best := make([]float64, width*height)
	inside := make([]bool, width*height)
	for i := range owner {
		owner[i] = -1
		best[i] = math.Inf(1)
	}
	total := 0
	for _, run := range runs {
		y := int(run.Y)
		for x := int(run.X0); x <= int(run.X1); x++ {
			inside[(y-minY)*width+(x-minX)] = true
			total++
		}
	}

	queue := &splitQueue{}
	heap.Init(queue)
	result := MapSplitResult{ProvinceID: request.ProvinceID, SourcePixel: total}
	for seedIndex, seed := range request.Seeds {
		if seed.X < minX || seed.X > maxX || seed.Y < minY || seed.Y > maxY {
			return MapSplitResult{}, splitInputErrorf("seed %d (%d,%d) lies outside province %d", seedIndex, seed.X, seed.Y, request.ProvinceID)
		}
		cell := (seed.Y-minY)*width + (seed.X - minX)
		if !inside[cell] {
			return MapSplitResult{}, splitInputErrorf("seed %d (%d,%d) is not a pixel of province %d", seedIndex, seed.X, seed.Y, request.ProvinceID)
		}
		if owner[cell] >= 0 {
			return MapSplitResult{}, splitInputErrorf("seeds %d and %d are the same pixel (%d,%d)", owner[cell], seedIndex, seed.X, seed.Y)
		}
		owner[cell] = int32(seedIndex)
		best[cell] = 0
		heap.Push(queue, splitCell{index: cell, cost: 0, owner: seedIndex})
	}

	// Multi-source Dijkstra. Each pixel is claimed by whichever seed reaches it
	// most cheaply, so the meeting frontier follows the terrain rather than the
	// midpoint between seeds.
	for queue.Len() > 0 {
		cell := heap.Pop(queue).(splitCell)
		if cell.cost > best[cell.index] {
			continue
		}
		cx := cell.index%width + minX
		cy := cell.index/width + minY
		for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := cx+step[0], cy+step[1]
			if nx < minX || nx > maxX || ny < minY || ny > maxY {
				continue
			}
			next := (ny-minY)*width + (nx - minX)
			if !inside[next] {
				continue
			}
			resistance := terrain.Resistance(nx, ny)
			if resistance < 0 || math.IsNaN(resistance) {
				resistance = 0
			} else if resistance > 1 {
				resistance = 1
			}
			// The base step of 1 keeps growth advancing even on maximally
			// resistant ground, so a ridge deflects a boundary without being
			// able to strand pixels behind it.
			cost := cell.cost + 1 + weight*resistance
			if cost < best[next] {
				best[next] = cost
				owner[next] = int32(cell.owner)
				heap.Push(queue, splitCell{index: next, cost: cost, owner: cell.owner})
			}
		}
	}

	// Pixels no seed could reach sit in a disconnected piece of the province.
	// That is ordinary, not broken: islands, exclaves and decorative terrain are
	// routinely painted as one province in several pieces, and in this workspace
	// 987 playable provinces are built that way. Such a piece is still part of
	// the province and has to end up somewhere once the province is divided, so
	// each piece is attached whole to its nearest seed.
	//
	// Whole pieces rather than individual pixels: splitting one island between
	// two provinces is a worse answer than either province owning all of it.
	orphanPieces, orphanPixels := assignOrphanPieces(inside, owner, width, minX, minY, request.Seeds)

	parts := make([]MapSplitPart, len(request.Seeds))
	for i, seed := range request.Seeds {
		parts[i] = MapSplitPart{Index: i, Seed: seed, MinX: math.MaxInt, MinY: math.MaxInt, MaxX: -1, MaxY: -1}
	}
	unreachable := 0
	// Rebuild runs per part in raster order so the output is deterministic and
	// directly comparable to the geometry the index already stores.
	for y := minY; y <= maxY; y++ {
		runStart, runOwner := -1, int32(-1)
		flush := func(endX int) {
			if runStart < 0 || runOwner < 0 {
				return
			}
			part := &parts[runOwner]
			part.Runs = append(part.Runs, MapRun{Y: int32(y), X0: int32(runStart), X1: int32(endX)})
			part.PixelCount += endX - runStart + 1
			if runStart < part.MinX {
				part.MinX = runStart
			}
			if endX > part.MaxX {
				part.MaxX = endX
			}
			if y < part.MinY {
				part.MinY = y
			}
			if y > part.MaxY {
				part.MaxY = y
			}
		}
		for x := minX; x <= maxX; x++ {
			cell := (y-minY)*width + (x - minX)
			var current int32 = -1
			if inside[cell] {
				current = owner[cell]
				if current < 0 {
					unreachable++
				}
			}
			if current != runOwner {
				flush(x - 1)
				runStart, runOwner = x, current
			}
		}
		flush(maxX)
	}

	for i := range parts {
		if parts[i].PixelCount == 0 {
			parts[i].MinX, parts[i].MinY, parts[i].MaxX, parts[i].MaxY = 0, 0, 0, 0
			result.Warnings = append(result.Warnings, fmt.Sprintf("seed %d claimed no pixels; it was likely enclosed by a cheaper neighbour", i))
		}
	}
	if orphanPieces > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"province %d is painted in %d disconnected piece(s) totalling %d pixel(s); each was attached whole to its nearest seed. Place a seed inside a piece to control which part keeps it",
			request.ProvinceID, orphanPieces, orphanPixels))
	}
	if unreachable > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d pixel(s) could not be attached to any seed", unreachable))
	}
	if _, uniform := terrain.(uniformTerrain); uniform {
		result.Warnings = append(result.Warnings,
			"no terrain raster was supplied, so boundaries follow distance alone and will look geometric")
	}
	result.Parts = parts
	result.Unreachable = unreachable
	result.OrphanPieces = orphanPieces
	result.OrphanPixels = orphanPixels
	return result, nil
}

// assignOrphanPieces attaches every pixel the growth could not reach to the
// seed nearest that piece, and reports how many pieces and pixels were moved.
func assignOrphanPieces(inside []bool, owner []int32, width, minX, minY int, seeds []MapSplitSeed) (int, int) {
	visited := make([]bool, len(inside))
	pieces, moved := 0, 0
	for start := range inside {
		if !inside[start] || owner[start] >= 0 || visited[start] {
			continue
		}
		visited[start] = true
		queue := []int{start}
		component := []int{start}
		sumX, sumY := 0, 0
		for len(queue) > 0 {
			cell := queue[0]
			queue = queue[1:]
			cx, cy := cell%width, cell/width
			sumX += cx
			sumY += cy
			for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := cx+step[0], cy+step[1]
				if nx < 0 || nx >= width || ny < 0 || ny >= len(inside)/width {
					continue
				}
				next := ny*width + nx
				if !inside[next] || owner[next] >= 0 || visited[next] {
					continue
				}
				visited[next] = true
				queue = append(queue, next)
				component = append(component, next)
			}
		}
		// Compare against the piece's centroid rather than any single pixel of
		// it, so a long island is judged by where it sits as a whole.
		centroidX := float64(sumX)/float64(len(component)) + float64(minX)
		centroidY := float64(sumY)/float64(len(component)) + float64(minY)
		nearest, nearestDistance := -1, math.Inf(1)
		for seedIndex, seed := range seeds {
			distance := math.Hypot(float64(seed.X)-centroidX, float64(seed.Y)-centroidY)
			if distance < nearestDistance {
				nearest, nearestDistance = seedIndex, distance
			}
		}
		if nearest < 0 {
			continue
		}
		for _, cell := range component {
			owner[cell] = int32(nearest)
		}
		pieces++
		moved += len(component)
	}
	return pieces, moved
}

func runBounds(runs []MapRun) (minX, minY, maxX, maxY int) {
	minX, minY, maxX, maxY = math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, run := range runs {
		if int(run.X0) < minX {
			minX = int(run.X0)
		}
		if int(run.X1) > maxX {
			maxX = int(run.X1)
		}
		if int(run.Y) < minY {
			minY = int(run.Y)
		}
		if int(run.Y) > maxY {
			maxY = int(run.Y)
		}
	}
	return minX, minY, maxX, maxY
}

// SortMapRuns orders runs in raster order, which keeps encoded geometry stable
// across runs regardless of how it was produced.
func SortMapRuns(runs []MapRun) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].Y != runs[j].Y {
			return runs[i].Y < runs[j].Y
		}
		return runs[i].X0 < runs[j].X0
	})
}
