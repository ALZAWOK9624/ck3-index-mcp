package indexer

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
)

// Rivers are derived from the heightmap rather than drawn onto it.
//
// A drawn river is a line someone chose; a derived one is where water actually
// collects given the ground that exists, which is why it looks right and why it
// ties the map together. Every channel is the outlet of a catchment, so the
// network branches the way real drainage branches, joins where slopes converge,
// and cannot run uphill or ignore a range standing in its way. Nothing else
// produces that from separately authored landforms.
//
// The upstream half of this map shows the same structure: fine channels reach
// from every massif down into the lowlands, and it is those channels -- not the
// peaks -- that make the terrain read as one continuous world.
//
// The pipeline is the standard hydrology one: fill sinks so water has somewhere
// to go, route each cell to its steepest downhill neighbour, accumulate how much
// land drains through each cell, then cut channels where that accumulation
// exceeds a threshold.

// fillGradient is the slope added across filled basins so water keeps moving
// over them. It must be small enough to vanish when the field is written back
// to 8-bit pixels, and large enough to survive float comparison.
const fillGradient = 1e-5

// pondedDepth is how much a cell may have been raised by sink filling and still
// count as real ground. Anything raised more than this is standing water.
const pondedDepth = 0.5

// MapRiverSettings controls channel derivation.
type MapRiverSettings struct {
	// MinDrainage is how many upstream cells must feed a point before it counts
	// as a channel. Lower values produce denser, finer networks.
	MinDrainage int `json:"min_drainage"`
	// Depth is how deep the largest channels cut, in grey levels. Smaller
	// tributaries cut proportionally less.
	Depth float64 `json:"depth"`
	// ValleyWidth is how far the cut spreads either side of the channel, giving
	// the river a valley instead of a slot.
	ValleyWidth int `json:"valley_width"`
	// SeaLevel marks water; cells at or below it are outlets and are never cut.
	SeaLevel int `json:"sea_level"`
}

// MapRiverStats reports what was carved.
type MapRiverStats struct {
	ChannelCells int      `json:"channel_cells"`
	SinksFilled  int      `json:"sinks_filled"`
	MaxDrainage  int      `json:"max_drainage"`
	MeanDepth    float64  `json:"mean_depth"`
	Warnings     []string `json:"warnings,omitempty"`
}

// DefaultRivers is tuned for the elevation bands measured on this workspace's
// heightmap, where sea sits near 20 and playable land near 37.
func DefaultRivers() MapRiverSettings {
	return MapRiverSettings{MinDrainage: 220, Depth: 9, ValleyWidth: 3, SeaLevel: 20}
}

// CarveRiverNetwork routes drainage over a region and cuts the resulting
// channels into the heightmap in place.
func CarveRiverNetwork(img *image.Gray, region image.Rectangle, settings MapRiverSettings) (MapRiverStats, error) {
	stats := MapRiverStats{}
	if img == nil {
		return stats, fmt.Errorf("no heightmap supplied")
	}
	region = region.Intersect(img.Bounds())
	if region.Dx() < 8 || region.Dy() < 8 {
		return stats, nil
	}
	if settings.MinDrainage < 1 {
		return stats, fmt.Errorf("min_drainage must be at least 1")
	}
	if math.IsNaN(settings.Depth) || math.IsInf(settings.Depth, 0) {
		return stats, fmt.Errorf("river depth is not finite")
	}
	valleyWidth := settings.ValleyWidth
	if valleyWidth < 0 {
		valleyWidth = 0
	}

	w, h := region.Dx(), region.Dy()
	height := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			height[y*w+x] = float64(img.GrayAt(region.Min.X+x, region.Min.Y+y).Y)
		}
	}

	filled, sinks := fillDepressions(height, w, h, float64(settings.SeaLevel))
	stats.SinksFilled = sinks
	drainage := accumulateDrainage(filled, w, h)

	// Cut depth grows with the square root of drainage rather than linearly: a
	// river with a hundred times the catchment is deeper, but not a hundred
	// times deeper, and a linear law makes trunk streams into canyons while
	// leaving tributaries invisible.
	maxDrainage := 0
	for _, d := range drainage {
		if d > maxDrainage {
			maxDrainage = d
		}
	}
	stats.MaxDrainage = maxDrainage
	if maxDrainage < settings.MinDrainage {
		stats.Warnings = append(stats.Warnings, fmt.Sprintf(
			"the largest catchment collects %d cells, below the %d threshold, so no channel was cut; lower min_drainage or enlarge the region",
			maxDrainage, settings.MinDrainage))
		return stats, nil
	}
	scale := math.Sqrt(float64(maxDrainage))

	cut := make([]float64, w*h)
	depthSum := 0.0
	for i, d := range drainage {
		if d < settings.MinDrainage {
			continue
		}
		if height[i] <= float64(settings.SeaLevel) {
			continue
		}
		// Skip ground that only drains because it was filled. A filled basin is
		// a lake, and a lake bed has no incised channel; worse, the artificial
		// slope laid across it points every cell the same way, so D8 routing
		// locks onto the eight compass directions and cuts a rectangular grid of
		// trenches that is unmistakably synthetic. Channels belong on slopes
		// that were already there.
		if filled[i]-height[i] > pondedDepth {
			continue
		}
		depth := settings.Depth * math.Sqrt(float64(d)) / scale
		cut[i] = depth
		depthSum += depth
		stats.ChannelCells++
	}
	if stats.ChannelCells == 0 {
		return stats, nil
	}
	stats.MeanDepth = depthSum / float64(stats.ChannelCells)

	// Spread each cut sideways so the channel sits in a valley. Without this a
	// river is a one-pixel slot that reads as a scratch rather than a landform.
	if valleyWidth > 0 {
		cut = spreadValley(cut, w, h, valleyWidth)
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if cut[i] <= 0 {
				continue
			}
			value := height[i] - cut[i]
			// Never cut below the sea: a river mouth meets the water, it does
			// not tunnel under it.
			if value < float64(settings.SeaLevel) {
				value = float64(settings.SeaLevel)
			}
			if value < 0 {
				value = 0
			}
			img.SetGray(region.Min.X+x, region.Min.Y+y, grayLevel(value))
		}
	}
	return stats, nil
}

// fillDepressions raises closed basins to the level of their lowest outlet so
// every cell has a downhill path to the edge or the sea. Without it drainage
// accumulation stops dead in every pit the noise created, and the network comes
// out as disconnected fragments instead of a tree.
//
// This is the priority-flood method: grow outward from the boundary, always
// from the lowest cell reached so far, and raise anything lower to that level.
func fillDepressions(height []float64, w, h int, seaLevel float64) ([]float64, int) {
	filled, sinks, _ := fillDepressionsContext(context.Background(), height, w, h, seaLevel)
	return filled, sinks
}

func fillDepressionsContext(ctx context.Context, height []float64, w, h int, seaLevel float64) ([]float64, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	filled := make([]float64, len(height))
	copy(filled, height)
	visited := make([]bool, len(height))
	queue := &floodQueue{}

	push := func(i int) {
		if visited[i] {
			return
		}
		visited[i] = true
		queue.push(floodCell{index: i, level: filled[i]})
	}
	for x := 0; x < w; x++ {
		push(x)
		push((h-1)*w + x)
	}
	for y := 0; y < h; y++ {
		push(y * w)
		push(y*w + w - 1)
	}
	// Sea cells are outlets too, otherwise inland water bodies would be filled
	// to the brim as though they were pits.
	for i := range filled {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		if filled[i] <= seaLevel {
			push(i)
		}
	}

	sinks := 0
	processed := 0
	for queue.len() > 0 {
		if processed&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		processed++
		cell := queue.pop()
		cx, cy := cell.index%w, cell.index/w
		for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := cx+step[0], cy+step[1]
			if nx < 0 || ny < 0 || nx >= w || ny >= h {
				continue
			}
			next := ny*w + nx
			if visited[next] {
				continue
			}
			visited[next] = true
			// The comparison must include equality. A cell exactly level with
			// the one it was reached from has no strictly lower neighbour, so
			// flow routing stops dead on every flat -- and filled basins are
			// flat by construction, which is precisely where the water needs to
			// keep moving.
			if filled[next] <= cell.level {
				// Raise to just *above* the outlet, not level with it. Filling a
				// basin exactly flat leaves a plateau where no cell has a lower
				// neighbour, so flow routing dies there and every catchment
				// upstream is cut off from the sea -- the drainage comes out as
				// a scatter of stubs instead of a network. The epsilon tilts the
				// filled surface toward its outlet by an amount far below one
				// grey level, so it steers water without being visible.
				filled[next] = cell.level + fillGradient
				sinks++
			}
			queue.push(floodCell{index: next, level: filled[next]})
		}
	}
	return filled, sinks, ctx.Err()
}

// accumulateDrainage counts how many cells drain through each cell by walking
// the grid from high to low, which needs no explicit flow graph: by the time a
// cell is processed every cell above it already has been.
func accumulateDrainage(height []float64, w, h int) []int {
	drainage, _ := accumulateDrainageContext(context.Background(), height, w, h)
	return drainage
}

func accumulateDrainageContext(ctx context.Context, height []float64, w, h int) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	order := make([]int, len(height))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return height[order[a]] > height[order[b]] })
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	drainage := make([]int, len(height))
	for i := range drainage {
		drainage[i] = 1
	}
	for orderIndex, i := range order {
		if orderIndex&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		cx, cy := i%w, i/w
		lowest, lowestHeight := -1, height[i]
		// Eight directions, because four-way routing forces every channel onto
		// the axes and produces staircase rivers.
		for _, step := range [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
			nx, ny := cx+step[0], cy+step[1]
			if nx < 0 || ny < 0 || nx >= w || ny >= h {
				continue
			}
			next := ny*w + nx
			if height[next] < lowestHeight {
				lowest, lowestHeight = next, height[next]
			}
		}
		if lowest >= 0 {
			drainage[lowest] += drainage[i]
		}
	}
	return drainage, ctx.Err()
}

// spreadValley widens each cut with a falling profile, turning a channel into a
// valley whose sides slope toward the water.
func spreadValley(cut []float64, w, h, width int) []float64 {
	out, _ := spreadValleyContext(context.Background(), cut, w, h, width)
	return out
}

func spreadValleyContext(ctx context.Context, cut []float64, w, h, width int) ([]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]float64, len(cut))
	copy(out, cut)
	for y := 0; y < h; y++ {
		if y&15 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for x := 0; x < w; x++ {
			depth := cut[y*w+x]
			if depth <= 0 {
				continue
			}
			for dy := -width; dy <= width; dy++ {
				for dx := -width; dx <= width; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= w || ny >= h {
						continue
					}
					distance := math.Hypot(float64(dx), float64(dy))
					if distance > float64(width) {
						continue
					}
					side := depth * smoothstep(1-distance/float64(width))
					if side > out[ny*w+nx] {
						out[ny*w+nx] = side
					}
				}
			}
		}
	}
	return out, ctx.Err()
}

type floodCell struct {
	index int
	level float64
}

// floodQueue is a binary heap ordered by level, so the flood always expands
// from the lowest frontier cell.
type floodQueue struct{ cells []floodCell }

func (q *floodQueue) len() int { return len(q.cells) }

func (q *floodQueue) push(cell floodCell) {
	q.cells = append(q.cells, cell)
	i := len(q.cells) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if q.cells[parent].level <= q.cells[i].level {
			break
		}
		q.cells[parent], q.cells[i] = q.cells[i], q.cells[parent]
		i = parent
	}
}

func (q *floodQueue) pop() floodCell {
	top := q.cells[0]
	last := len(q.cells) - 1
	q.cells[0] = q.cells[last]
	q.cells = q.cells[:last]
	i := 0
	for {
		left, right, smallest := 2*i+1, 2*i+2, i
		if left < len(q.cells) && q.cells[left].level < q.cells[smallest].level {
			smallest = left
		}
		if right < len(q.cells) && q.cells[right].level < q.cells[smallest].level {
			smallest = right
		}
		if smallest == i {
			break
		}
		q.cells[i], q.cells[smallest] = q.cells[smallest], q.cells[i]
		i = smallest
	}
	return top
}

// grayLevel rounds a working height back into a pixel value.
func grayLevel(value float64) color.Gray {
	if value < 0 {
		value = 0
	} else if value > 255 {
		value = 255
	}
	return color.Gray{Y: uint8(math.Round(value))}
}
