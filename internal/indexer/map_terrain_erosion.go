package indexer

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// Hydraulic erosion is what makes separately placed landforms belong to the
// same world.
//
// Noise alone produces relief that is locally plausible and globally
// incoherent: two ranges built beside each other have no reason to share a
// drainage, so nothing about the ground between them says they are neighbours.
// Erosion supplies that reason. Water released on the new terrain runs downhill
// across whatever it meets, cutting valleys that continue from one feature onto
// the next and depositing the load where the slope eases. The valleys are not
// decoration -- they are the record of a process that crossed both features,
// which is exactly the connection that stamping shapes cannot fake.
//
// It also produces the drainage texture no amount of fractal noise gives you:
// dense branching gullies on steep flanks, smooth aprons at the foot.

// MapErosionSettings controls the droplet simulation.
type MapErosionSettings struct {
	// Droplets is how many water particles are released. Cost is linear;
	// coverage is what matters, so scale it with the eroded area.
	Droplets int `json:"droplets"`
	// MaxSteps bounds how far one droplet travels before it is abandoned.
	MaxSteps int `json:"max_steps"`
	// Inertia blends the previous direction with the downhill gradient. 0
	// follows the steepest slope exactly and carves unnaturally straight
	// channels; near 1 the droplet ignores terrain. Around 0.05 meanders.
	Inertia float64 `json:"inertia"`
	// Capacity scales how much sediment moving water can hold.
	Capacity float64 `json:"capacity"`
	// Erode and Deposit are the fractions of the gap to capacity resolved per
	// step. Keeping both well below 1 spreads the effect over the path instead
	// of gouging a pit at the first steep pixel.
	Erode   float64 `json:"erode"`
	Deposit float64 `json:"deposit"`
	// Evaporate is the fraction of water lost per step, which ends a droplet's
	// run and forces it to drop its load.
	Evaporate float64 `json:"evaporate"`
	// Radius spreads each erosion event over neighbouring pixels. Without it
	// droplets cut single-pixel scratches.
	Radius int   `json:"radius"`
	Seed   int64 `json:"seed"`
}

// DefaultErosion returns settings tuned for CK3-scale heightmaps, where one
// pixel is a substantial distance and channels should stay legible.
func DefaultErosion(seed int64, droplets int) MapErosionSettings {
	return MapErosionSettings{
		Droplets: droplets, MaxSteps: 64, Inertia: 0.05, Capacity: 4,
		Erode: 0.3, Deposit: 0.3, Evaporate: 0.02, Radius: 2, Seed: seed,
	}
}

// ErodeHeightmapRegion runs droplets over one region of the heightmap in place
// and returns how many were simulated.
func ErodeHeightmapRegion(img *image.Gray, region image.Rectangle, settings MapErosionSettings) (int, error) {
	if img == nil {
		return 0, fmt.Errorf("no heightmap supplied")
	}
	region = region.Intersect(img.Bounds())
	if region.Dx() < 4 || region.Dy() < 4 {
		return 0, nil
	}
	if settings.Droplets < 0 {
		return 0, fmt.Errorf("droplets must not be negative")
	}
	for name, value := range map[string]float64{
		"inertia": settings.Inertia, "capacity": settings.Capacity,
		"erode": settings.Erode, "deposit": settings.Deposit, "evaporate": settings.Evaporate,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("erosion %s is not finite", name)
		}
	}
	maxSteps := settings.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 64
	}
	radius := settings.Radius
	if radius < 1 {
		radius = 1
	}

	// Work on a float field so repeated small changes accumulate instead of
	// being lost to rounding back into bytes on every step.
	w, h := region.Dx(), region.Dy()
	height := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			height[y*w+x] = float64(img.GrayAt(region.Min.X+x, region.Min.Y+y).Y)
		}
	}

	random := splitMix64(uint64(settings.Seed) + 0x9E3779B97F4A7C15)
	simulated := 0
	for drop := 0; drop < settings.Droplets; drop++ {
		px := float64(random()%uint64(w-2)) + 1
		py := float64(random()%uint64(h-2)) + 1
		var dirX, dirY, water, sediment, speed float64
		water, speed = 1, 1

		for step := 0; step < maxSteps; step++ {
			cellX, cellY := int(px), int(py)
			if cellX < 1 || cellY < 1 || cellX >= w-1 || cellY >= h-1 {
				break
			}
			fx, fy := px-float64(cellX), py-float64(cellY)
			heightHere, gradX, gradY := bilinearHeightGradient(height, w, cellX, cellY, fx, fy)

			// Blend the previous direction with the downhill gradient so the
			// droplet has momentum; pure gradient descent carves straight
			// channels that look drawn rather than eroded.
			dirX = dirX*settings.Inertia - gradX*(1-settings.Inertia)
			dirY = dirY*settings.Inertia - gradY*(1-settings.Inertia)
			length := math.Hypot(dirX, dirY)
			if length < 1e-9 {
				break
			}
			dirX, dirY = dirX/length, dirY/length
			px, py = px+dirX, py+dirY
			if px < 1 || py < 1 || px >= float64(w-1) || py >= float64(h-1) {
				break
			}

			newHeight, _, _ := bilinearHeightGradient(height, w, int(px), int(py), px-float64(int(px)), py-float64(int(py)))
			deltaHeight := newHeight - heightHere

			// Capacity depends on how fast the water is moving and how steeply
			// it is falling: fast water on a steep slope carries the most, and
			// water running uphill carries almost nothing.
			capacity := math.Max(-deltaHeight, 0.01) * speed * water * settings.Capacity

			if sediment > capacity || deltaHeight > 0 {
				// Uphill means the droplet hit a dip: fill it rather than
				// climbing, which is what turns hollows into flats.
				amount := settings.Deposit * (sediment - capacity)
				if deltaHeight > 0 {
					amount = math.Min(deltaHeight, sediment)
				}
				sediment -= amount
				depositAt(height, w, h, cellX, cellY, fx, fy, amount)
			} else {
				amount := math.Min(settings.Erode*(capacity-sediment), -deltaHeight)
				sediment += amount
				erodeAround(height, w, h, cellX, cellY, radius, amount)
			}

			speed = math.Sqrt(math.Max(speed*speed-deltaHeight, 0.0001))
			water *= 1 - settings.Evaporate
			if water < 0.01 {
				break
			}
		}
		simulated++
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			value := height[y*w+x]
			if value < 0 {
				value = 0
			} else if value > 255 {
				value = 255
			}
			img.SetGray(region.Min.X+x, region.Min.Y+y, color.Gray{Y: uint8(math.Round(value))})
		}
	}
	return simulated, nil
}

// bilinearHeightGradient returns the interpolated height and its gradient,
// which together decide where a droplet goes and how fast.
func bilinearHeightGradient(height []float64, w int, cellX, cellY int, fx, fy float64) (float64, float64, float64) {
	i := cellY*w + cellX
	h00, h10 := height[i], height[i+1]
	h01, h11 := height[i+w], height[i+w+1]
	gradX := (h10-h00)*(1-fy) + (h11-h01)*fy
	gradY := (h01-h00)*(1-fx) + (h11-h10)*fx
	value := h00*(1-fx)*(1-fy) + h10*fx*(1-fy) + h01*(1-fx)*fy + h11*fx*fy
	return value, gradX, gradY
}

// depositAt spreads sediment over the four cells the droplet straddles, so
// deposition follows the sub-pixel path instead of snapping to a grid cell.
func depositAt(height []float64, w, h, cellX, cellY int, fx, fy, amount float64) {
	if amount == 0 {
		return
	}
	i := cellY*w + cellX
	height[i] += amount * (1 - fx) * (1 - fy)
	height[i+1] += amount * fx * (1 - fy)
	height[i+w] += amount * (1 - fx) * fy
	height[i+w+1] += amount * fx * fy
}

// erodeAround removes material over a disc, weighted toward the centre. A
// single-cell edit would cut a one-pixel scratch rather than a valley.
func erodeAround(height []float64, w, h, cellX, cellY, radius int, amount float64) {
	if amount == 0 {
		return
	}
	total := 0.0
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			x, y := cellX+dx, cellY+dy
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			weight := float64(radius) - math.Hypot(float64(dx), float64(dy))
			if weight > 0 {
				total += weight
			}
		}
	}
	if total <= 0 {
		return
	}
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			x, y := cellX+dx, cellY+dy
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			weight := float64(radius) - math.Hypot(float64(dx), float64(dy))
			if weight <= 0 {
				continue
			}
			height[y*w+x] -= amount * weight / total
		}
	}
}

// splitMix64 gives a deterministic stream, so an eroded terrain can be
// reproduced from its seed for review.
func splitMix64(state uint64) func() uint64 {
	return func() uint64 {
		state += 0x9E3779B97F4A7C15
		z := state
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		return z ^ (z >> 31)
	}
}
