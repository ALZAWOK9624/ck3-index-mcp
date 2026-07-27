package indexer

import (
	"image"
	"image/color"
	"testing"
)

// bowlTerrain builds a basin: high at the rim, low in the middle, with one gap
// in the rim so water has an outlet. It exercises both sink filling and routing.
func bowlTerrain(size int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, size, size))
	centre := float64(size) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-centre, float64(y)-centre
			d := (dx*dx + dy*dy) / (centre * centre)
			if d > 1 {
				d = 1
			}
			img.SetGray(x, y, color.Gray{Y: uint8(30 + 120*d)})
		}
	}
	// Cut a notch through the rim so the basin drains instead of being endorheic.
	for y := 0; y < size; y++ {
		if y > size/2-4 && y < size/2+4 {
			for x := size - 12; x < size; x++ {
				img.SetGray(x, y, color.Gray{Y: 25})
			}
		}
	}
	return img
}

// TestFillDepressionsGivesEveryCellAnOutlet is the precondition for routing:
// after filling, no interior cell may be a strict local minimum, or drainage
// accumulation stops there and the network fragments.
func TestFillDepressionsGivesEveryCellAnOutlet(t *testing.T) {
	const size = 80
	img := bowlTerrain(size)
	height := make([]float64, size*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			height[y*size+x] = float64(img.GrayAt(x, y).Y)
		}
	}
	filled, sinks := fillDepressions(height, size, size, 20)
	if sinks == 0 {
		t.Fatal("a bowl should have required filling")
	}
	stuck := 0
	for y := 1; y < size-1; y++ {
		for x := 1; x < size-1; x++ {
			i := y*size + x
			if filled[i] <= 20 {
				continue // sea drains by definition
			}
			lowest := filled[i]
			for _, step := range [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
				if n := filled[(y+step[1])*size+x+step[0]]; n < lowest {
					lowest = n
				}
			}
			if lowest >= filled[i] {
				stuck++
			}
		}
	}
	if stuck > 0 {
		t.Fatalf("%d interior cells have no downhill neighbour after filling", stuck)
	}
}

// TestRiversRunDownhill is the property that makes a derived network credible:
// following a channel must never climb.
func TestRiversRunDownhill(t *testing.T) {
	const size = 120
	img := bowlTerrain(size)
	before := image.NewGray(img.Bounds())
	copy(before.Pix, img.Pix)

	settings := MapRiverSettings{MinDrainage: 30, Depth: 14, ValleyWidth: 1, SeaLevel: 20}
	stats, err := CarveRiverNetwork(img, img.Bounds(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelCells == 0 {
		t.Fatalf("no channel was cut: %+v", stats)
	}
	// Every carved cell must be lower than it was, and no carving may go below
	// sea level.
	lowered := 0
	for i := range img.Pix {
		if img.Pix[i] < before.Pix[i] {
			lowered++
		}
		if img.Pix[i] > before.Pix[i] {
			t.Fatalf("river carving raised pixel %d from %d to %d", i, before.Pix[i], img.Pix[i])
		}
		if img.Pix[i] < uint8(settings.SeaLevel) && before.Pix[i] >= uint8(settings.SeaLevel) {
			t.Fatalf("river cut land below sea level at pixel %d", i)
		}
	}
	if lowered == 0 {
		t.Fatal("nothing was carved")
	}
}

// TestRiversAreDeterministic keeps a generated map reproducible from its inputs.
func TestRiversAreDeterministic(t *testing.T) {
	settings := MapRiverSettings{MinDrainage: 40, Depth: 12, ValleyWidth: 2, SeaLevel: 20}
	first := bowlTerrain(90)
	if _, err := CarveRiverNetwork(first, first.Bounds(), settings); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		again := bowlTerrain(90)
		if _, err := CarveRiverNetwork(again, again.Bounds(), settings); err != nil {
			t.Fatal(err)
		}
		for i := range first.Pix {
			if first.Pix[i] != again.Pix[i] {
				t.Fatalf("attempt %d: pixel %d differs (%d vs %d)", attempt, i, first.Pix[i], again.Pix[i])
			}
		}
	}
}

// TestRiversSkipPondedGround covers the artefact that made the first output
// obviously synthetic: a filled basin is a lake, and laying an artificial slope
// across it points every cell the same way, so D8 routing cut a rectangular
// grid of trenches through what should be still water.
func TestRiversSkipPondedGround(t *testing.T) {
	const size = 100
	// A flat pan enclosed by a rim: everything inside gets filled, so nothing
	// inside should be carved.
	img := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			value := uint8(40)
			if x < 6 || y < 6 || x >= size-6 || y >= size-6 {
				value = 200
			}
			img.SetGray(x, y, color.Gray{Y: value})
		}
	}
	before := image.NewGray(img.Bounds())
	copy(before.Pix, img.Pix)

	if _, err := CarveRiverNetwork(img, img.Bounds(), MapRiverSettings{
		MinDrainage: 20, Depth: 20, ValleyWidth: 1, SeaLevel: 10,
	}); err != nil {
		t.Fatal(err)
	}
	for y := 10; y < size-10; y++ {
		for x := 10; x < size-10; x++ {
			i := y*size + x
			if img.Pix[i] != before.Pix[i] {
				t.Fatalf("standing water at (%d,%d) was carved: %d -> %d", x, y, before.Pix[i], img.Pix[i])
			}
		}
	}
}

func TestRiversRejectBadSettings(t *testing.T) {
	img := bowlTerrain(40)
	if _, err := CarveRiverNetwork(img, img.Bounds(), MapRiverSettings{MinDrainage: 0, Depth: 5}); err == nil {
		t.Fatal("expected min_drainage below 1 to be refused")
	}
	if _, err := CarveRiverNetwork(nil, img.Bounds(), DefaultRivers()); err == nil {
		t.Fatal("expected a nil heightmap to be refused")
	}
	// A region with no catchment large enough must say so rather than silently
	// doing nothing.
	stats, err := CarveRiverNetwork(bowlTerrain(40), image.Rect(0, 0, 40, 40), MapRiverSettings{
		MinDrainage: 1 << 20, Depth: 5, SeaLevel: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelCells != 0 || len(stats.Warnings) == 0 {
		t.Fatalf("an unreachable threshold should warn and carve nothing: %+v", stats)
	}
}
