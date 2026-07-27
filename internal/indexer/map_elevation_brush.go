package indexer

import (
	"fmt"
	"image"
	"math"
	"math/bits"
)

// Raising terrain is an additive edit to heightmap.png, never a replacement of
// the region. Painting an absolute shape would flatten whatever was already
// there -- coastlines, river valleys, the slope a province was drawn around --
// and the result reads as a cut-out pasted onto the map. Adding a decaying
// mass instead keeps the existing landscape visible through the new one.
//
// CK3 derives land and water from provinces.png and default.map, not from
// elevation, so this changes how terrain looks and how steep it is, and cannot
// turn sea into land by itself.
//
// Measured against this workspace's heightmap, the bands are: water median 11,
// playable land median 37 (p90 88), impassable mountain median 58 (p90 116),
// highest 212 of a possible 255. An amplitude near 60 therefore turns ordinary
// land into convincing mountains, and anything past ~150 exceeds every peak the
// map already has.
type MapElevationBrush struct {
	CenterX int `json:"center_x"`
	CenterY int `json:"center_y"`
	// Radius is the distance at which the brush has faded to nothing.
	Radius int `json:"radius"`
	// Amplitude is the peak change in grey levels at the centre. Negative
	// values carve basins.
	Amplitude float64 `json:"amplitude"`
	// Roughness mixes between a smooth dome (0) and fully ridged relief (1).
	// Real ranges are ridged; a smooth dome looks like a burial mound.
	Roughness float64 `json:"roughness"`
	// Seed selects which ridge pattern is generated. The same seed always
	// produces the same terrain, so a result can be reproduced or reviewed.
	Seed int64 `json:"seed"`
}

// MapElevationChange reports what a brush pass did, in terms a caller can
// sanity-check without opening the image.
type MapElevationChange struct {
	Width         int      `json:"width"`
	Height        int      `json:"height"`
	ChangedPixels int      `json:"changed_pixels"`
	RaisedPixels  int      `json:"raised_pixels"`
	LoweredPixels int      `json:"lowered_pixels"`
	PeakBefore    int      `json:"peak_before"`
	PeakAfter     int      `json:"peak_after"`
	MeanDelta     float64  `json:"mean_delta"`
	ClampedPixels int      `json:"clamped_pixels"`
	Warnings      []string `json:"warnings,omitempty"`
}

// ApplyElevationBrushes returns a new heightmap with every brush applied in
// order. The source image is not modified.
func ApplyElevationBrushes(source *image.Gray, brushes []MapElevationBrush) (*image.Gray, MapElevationChange, error) {
	if source == nil {
		return nil, MapElevationChange{}, fmt.Errorf("no source heightmap supplied")
	}
	if len(brushes) == 0 {
		return nil, MapElevationChange{}, fmt.Errorf("no brushes supplied")
	}
	bounds := source.Bounds()
	change := MapElevationChange{Width: bounds.Dx(), Height: bounds.Dy()}

	out := image.NewGray(bounds)
	copy(out.Pix, source.Pix)

	deltaSum, deltaCount := 0.0, 0
	for brushIndex, brush := range brushes {
		if brush.Radius <= 0 {
			return nil, change, fmt.Errorf("brush %d has radius %d; a brush needs a positive radius", brushIndex, brush.Radius)
		}
		if math.IsNaN(brush.Amplitude) || math.IsInf(brush.Amplitude, 0) {
			return nil, change, fmt.Errorf("brush %d has a non-finite amplitude", brushIndex)
		}
		// clamp01 here is math.Max/math.Min based, which propagates NaN rather
		// than bounding it, so a non-finite roughness has to be refused before
		// it reaches the arithmetic and silently poisons the output pixels.
		if math.IsNaN(brush.Roughness) || math.IsInf(brush.Roughness, 0) {
			return nil, change, fmt.Errorf("brush %d has a non-finite roughness", brushIndex)
		}
		roughness := clamp01(brush.Roughness)
		radius := float64(brush.Radius)
		// Crest spacing scales with the brush rather than being fixed in pixels.
		// At a fixed frequency a small hill gets a fraction of one crest and
		// reads as a smooth dome, while a large range gets so few that it reads
		// as a single swelling; tying it to the radius gives both a comparable
		// number of ridges and keeps them recognisable as a range.
		crestFrequency := ridgeCrestsPerRadius / radius
		minX := maxInt(bounds.Min.X, brush.CenterX-brush.Radius)
		maxX := minInt(bounds.Max.X-1, brush.CenterX+brush.Radius)
		minY := maxInt(bounds.Min.Y, brush.CenterY-brush.Radius)
		maxY := minInt(bounds.Max.Y-1, brush.CenterY+brush.Radius)
		if minX > maxX || minY > maxY {
			change.Warnings = append(change.Warnings,
				fmt.Sprintf("brush %d at (%d,%d) lies entirely outside the heightmap and changed nothing", brushIndex, brush.CenterX, brush.CenterY))
			continue
		}
		for y := minY; y <= maxY; y++ {
			for x := minX; x <= maxX; x++ {
				distance := math.Hypot(float64(x-brush.CenterX), float64(y-brush.CenterY))
				if distance > radius {
					continue
				}
				// smoothstep reaches zero slope at both ends, so the new mass
				// meets the old ground without the visible crease a linear or
				// cosine falloff leaves behind.
				falloff := smoothstep(1 - distance/radius)
				relief := 1.0
				if roughness > 0 {
					relief = (1-roughness)*1.0 + roughness*ridgedNoise(x, y, brush.Seed, crestFrequency)
				}
				delta := brush.Amplitude * falloff * relief
				if delta == 0 {
					continue
				}
				offset := out.PixOffset(x, y)
				before := float64(out.Pix[offset])
				after := before + delta
				if after < 0 {
					after = 0
					change.ClampedPixels++
				} else if after > 255 {
					after = 255
					change.ClampedPixels++
				}
				value := uint8(math.Round(after))
				if value == out.Pix[offset] {
					continue
				}
				if value > out.Pix[offset] {
					change.RaisedPixels++
				} else {
					change.LoweredPixels++
				}
				deltaSum += float64(value) - before
				deltaCount++
				out.Pix[offset] = value
				change.ChangedPixels++
			}
		}
	}

	for i := range source.Pix {
		if int(source.Pix[i]) > change.PeakBefore {
			change.PeakBefore = int(source.Pix[i])
		}
		if int(out.Pix[i]) > change.PeakAfter {
			change.PeakAfter = int(out.Pix[i])
		}
	}
	if deltaCount > 0 {
		change.MeanDelta = deltaSum / float64(deltaCount)
	}
	if change.ClampedPixels > 0 {
		change.Warnings = append(change.Warnings, fmt.Sprintf(
			"%d pixel(s) hit the 0-255 limit and were flattened there; lower the amplitude to keep the relief intact", change.ClampedPixels))
	}
	if change.ChangedPixels == 0 {
		change.Warnings = append(change.Warnings, "no pixel changed; check the brush centre, radius, and amplitude")
	}
	return out, change, nil
}

// smoothstep eases 0..1 with zero derivative at both ends.
func smoothstep(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}

// ridgeCrestsPerRadius sets how many major crests span the brush radius. Around
// five reads as a range with distinct spurs; far fewer looks like one swelling,
// far more like gravel.
const ridgeCrestsPerRadius = 5.0

// ridgedNoise returns 0..1 relief whose maxima form continuous crests rather
// than isolated bumps, which is what distinguishes a mountain range from a
// field of hills. Octaves are summed at halving amplitude so broad ridges carry
// finer detail.
// octaveRotation is deliberately not a multiple of pi/2. Each octave is sampled
// on a rotated grid so crests from different scales cannot all line up with the
// pixel axes; without it the ridges of every octave reinforce the same
// horizontal and vertical directions.
const octaveRotation = 0.7

func ridgedNoise(x, y int, seed int64, baseFrequency float64) float64 {
	total, amplitude, frequency, normal := 0.0, 1.0, baseFrequency, 0.0
	fx, fy := float64(x), float64(y)
	for octave := 0; octave < 4; octave++ {
		angle := octaveRotation * float64(octave)
		sin, cos := math.Sincos(angle)
		rx := (fx*cos - fy*sin) * frequency
		ry := (fx*sin + fy*cos) * frequency
		sample := gradientNoise(rx, ry, seed+int64(octave)*7919)
		// Folding the noise around its midpoint turns smooth hills into creases.
		ridge := 1 - math.Abs(sample*2-1)
		total += ridge * ridge * amplitude
		normal += amplitude
		amplitude *= 0.5
		frequency *= 2
	}
	if normal == 0 {
		return 0
	}
	return clamp01(total / normal)
}

// gradientNoise interpolates dot products against pseudo-random lattice
// gradients rather than interpolating lattice *values* directly.
//
// The distinction decides whether the result looks like terrain. Value noise
// puts its extrema on the lattice points themselves, so after the ridge fold
// the crests trace the lattice edges and the range comes out as a rectangular
// grid of creases -- unmistakably artificial. Gradient noise is zero at every
// lattice point and takes its extrema between them, in directions set by the
// gradients, so crests wander.
func gradientNoise(x, y float64, seed int64) float64 {
	xi, yi := math.Floor(x), math.Floor(y)
	xf, yf := x-xi, y-yi
	x0, y0 := int(xi), int(yi)
	u, v := smoothstep(xf), smoothstep(yf)
	dot := func(cx, cy int, dx, dy float64) float64 {
		sin, cos := math.Sincos(latticeValue(cx, cy, seed) * 2 * math.Pi)
		return cos*dx + sin*dy
	}
	n00 := dot(x0, y0, xf, yf)
	n10 := dot(x0+1, y0, xf-1, yf)
	n01 := dot(x0, y0+1, xf, yf-1)
	n11 := dot(x0+1, y0+1, xf-1, yf-1)
	value := (n00*(1-u)+n10*u)*(1-v) + (n01*(1-u)+n11*u)*v
	// The interpolated dot products span roughly -0.7..0.7; rescale to 0..1.
	return clamp01(value*0.707 + 0.5)
}

// latticeValue hashes a lattice point to 0..1. It must depend on the seed and
// both coordinates in a way that avoids axis-aligned repetition, or ridges line
// up with the pixel grid and read as artificial.
func latticeValue(x, y int, seed int64) float64 {
	h := uint64(seed) * 0x9E3779B97F4A7C15
	h ^= uint64(int64(x)) * 0xBF58476D1CE4E5B9
	h = bits.RotateLeft64(h, 29)
	h ^= uint64(int64(y)) * 0x94D049BB133111EB
	h ^= h >> 31
	h *= 0x2545F4914F6CDD1D
	h ^= h >> 29
	return float64(h>>11) / float64(uint64(1)<<53)
}
