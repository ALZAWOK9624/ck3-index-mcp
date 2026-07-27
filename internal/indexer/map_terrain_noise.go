package indexer

import (
	"math"
	"math/bits"
)

// Terrain noise is built from three ideas that each fix a specific way
// generated landscapes look wrong.
//
// Gradient noise, not value noise: value noise puts its extrema on the lattice
// points, so folding it into ridges traces the lattice edges and the result is
// a rectangular grid of creases.
//
// Domain warping: sampling the noise at coordinates that have themselves been
// displaced by noise. Unwarped fractal noise is statistically uniform in every
// direction, which reads as static; warping bends its features into the
// flowing, braided shapes that erosion and folding actually produce.
//
// Multifractal rather than plain fBm: in fBm every octave contributes equally
// everywhere, so peaks and plains end up equally rough. In a multifractal each
// octave is scaled by what the previous octaves already built, so detail
// accumulates on high ground and low ground stays smooth -- which is what real
// terrain does, because valleys fill with sediment while ridges keep fracturing.

// TerrainNoise describes one fractal field.
type TerrainNoise struct {
	// Frequency is the base spatial frequency in cycles per pixel.
	Frequency float64
	Octaves   int
	// Lacunarity is the frequency multiplier between octaves; Gain is the
	// amplitude multiplier. 2.0 and 0.5 keep the spectrum flat.
	Lacunarity float64
	Gain       float64
	// Warp displaces sampling coordinates by this many pixels of noise before
	// evaluating. Zero disables warping.
	Warp float64
	// Ridged folds each octave into creases. Multifractal makes each octave's
	// weight depend on the previous octaves.
	Ridged       bool
	Multifractal bool
	Seed         int64
}

// DefaultMountainNoise is tuned to read as a fold-mountain range at the pixel
// scale of a CK3 heightmap.
func DefaultMountainNoise(seed int64, frequency float64) TerrainNoise {
	return TerrainNoise{
		Frequency: frequency, Octaves: 5, Lacunarity: 2.03, Gain: 0.5,
		Warp: 0.55 / frequency, Ridged: true, Multifractal: true, Seed: seed,
	}
}

// Sample evaluates the field at a pixel, returning 0..1.
func (n TerrainNoise) Sample(x, y float64) float64 {
	octaves := n.Octaves
	if octaves < 1 {
		octaves = 1
	}
	lacunarity := n.Lacunarity
	if lacunarity <= 0 {
		lacunarity = 2
	}
	gain := n.Gain
	if gain <= 0 {
		gain = 0.5
	}
	sx, sy := x, y
	if n.Warp > 0 {
		// Warping with an independent field at the same base frequency bends
		// features without changing their scale. Offsetting the seeds keeps the
		// two displacement axes from being identical, which would only shift
		// everything diagonally.
		wx := gradientNoise(x*n.Frequency, y*n.Frequency, n.Seed+1013)
		wy := gradientNoise(x*n.Frequency, y*n.Frequency, n.Seed+7717)
		sx += (wx*2 - 1) * n.Warp
		sy += (wy*2 - 1) * n.Warp
	}

	total, amplitude, frequency, normal := 0.0, 1.0, n.Frequency, 0.0
	// weight carries the previous octaves' result forward for the multifractal.
	weight := 1.0
	for octave := 0; octave < octaves; octave++ {
		// Rotating each octave stops crests at different scales from all lining
		// up with the pixel axes.
		angle := octaveRotation * float64(octave)
		sin, cos := math.Sincos(angle)
		rx := (sx*cos - sy*sin) * frequency
		ry := (sx*sin + sy*cos) * frequency

		sample := gradientNoise(rx, ry, n.Seed+int64(octave)*7919)
		if n.Ridged {
			sample = 1 - math.Abs(sample*2-1)
			sample *= sample
		}
		contribution := sample * amplitude
		if n.Multifractal {
			contribution *= weight
			// The weight must never reach zero. Multiplying the running weight
			// by the sample lets it collapse wherever an octave happens to be
			// dark, which switches off every finer octave at that point and
			// punches a smooth pit into otherwise rough ground -- on a ridge
			// those pits read as gouges rather than terrain. Deriving the
			// weight from the current sample alone, over a floor, keeps detail
			// concentrated on high ground without ever extinguishing it.
			weight = multifractalFloor + (1-multifractalFloor)*sample
		}
		total += contribution
		normal += amplitude
		amplitude *= gain
		frequency *= lacunarity
	}
	if normal <= 0 {
		return 0
	}
	value := total / normal
	if n.Multifractal {
		// Multifractal weighting suppresses the mean, so rescale to keep the
		// field using its full range rather than hugging zero.
		value = clamp01(value * 1.7)
	}
	return clamp01(value)
}

// octaveRotation is deliberately not a multiple of pi/2, so octaves cannot all
// align with the pixel axes.
const octaveRotation = 0.7

// multifractalFloor is the smallest share of an octave that survives on low
// ground. It trades some of the peak-versus-plain contrast for the guarantee
// that no point loses its fine detail entirely.
const multifractalFloor = 0.35

// gradientNoise interpolates dot products against pseudo-random lattice
// gradients rather than interpolating lattice values directly. It is zero at
// every lattice point and takes its extrema between them, so ridges wander
// instead of tracing the grid.
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
	return clamp01(value*0.707 + 0.5)
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
