package indexer

import (
	"fmt"
	"image"
	"math"
)

// A landform is a profile swept along a path, not a stamp placed at a point.
//
// Looking at how the upstream map was drawn makes the reason concrete: its
// ranges run as bands along winding lines with branches, because that is what
// orogeny produces -- mountains rise along the seam where plates meet, and the
// seam is a curve. Stamping circles gives a field of disconnected lumps that
// reads as decoration however good the noise inside each lump is.
//
// Each kind below is a different cross-section swept along that curve, which is
// what makes a volcano and a plateau feel like different geology rather than
// the same blob at different sizes.

const (
	// TerrainRange is a fold mountain belt: highest along the spine, ridged.
	TerrainRange = "range"
	// TerrainHills is subdued, rounded relief with no dominant spine.
	TerrainHills = "hills"
	// TerrainPlateau is a raised flat top with steep sides.
	TerrainPlateau = "plateau"
	// TerrainVolcano is a cone with a depressed crater at its summit.
	TerrainVolcano = "volcano"
	// TerrainCanyon cuts downward with steep walls and a flat floor.
	TerrainCanyon = "canyon"
	// TerrainShelf slopes gently in one direction; used for coastal shelves and
	// sea beds, where a uniform dome would look like a submerged hill.
	TerrainShelf = "shelf"
)

// MapTerrainPoint is a pixel coordinate on the heightmap.
type MapTerrainPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// MapTerrainFeature is one landform to synthesise.
type MapTerrainFeature struct {
	Kind string `json:"kind"`
	// Path is the feature's centre line. One point makes it radial; several
	// sweep the profile along the polyline, which is how ranges are built.
	Path []MapTerrainPoint `json:"path"`
	// Width is the half-width of influence in pixels, measured from the path.
	Width int `json:"width"`
	// Amplitude is the peak height change in grey levels. Negative carves.
	Amplitude float64 `json:"amplitude"`
	// Roughness mixes between the bare profile and full fractal relief.
	Roughness float64 `json:"roughness"`
	// Detail scales the noise frequency; higher packs more crests into the
	// same width.
	Detail float64 `json:"detail"`
	Seed   int64   `json:"seed"`
}

// MapTerrainChange reports a synthesis pass.
type MapTerrainChange struct {
	Width         int            `json:"width"`
	Height        int            `json:"height"`
	ChangedPixels int            `json:"changed_pixels"`
	RaisedPixels  int            `json:"raised_pixels"`
	LoweredPixels int            `json:"lowered_pixels"`
	PeakBefore    int            `json:"peak_before"`
	PeakAfter     int            `json:"peak_after"`
	MeanDelta     float64        `json:"mean_delta"`
	ClampedPixels int            `json:"clamped_pixels"`
	ErodedDrops   int            `json:"eroded_droplets,omitempty"`
	Rivers        *MapRiverStats `json:"rivers,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}

// SynthesizeTerrain applies every feature to a copy of the heightmap, then runs
// the optional erosion pass. Erosion is what ties separately placed features
// into one landscape: water routed downhill crosses whatever it meets, so
// valleys carved on one feature continue onto its neighbour.
func SynthesizeTerrain(source *image.Gray, features []MapTerrainFeature, erosion *MapErosionSettings, rivers *MapRiverSettings) (*image.Gray, MapTerrainChange, error) {
	if source == nil {
		return nil, MapTerrainChange{}, fmt.Errorf("no source heightmap supplied")
	}
	if len(features) == 0 {
		return nil, MapTerrainChange{}, fmt.Errorf("no terrain features supplied")
	}
	bounds := source.Bounds()
	change := MapTerrainChange{Width: bounds.Dx(), Height: bounds.Dy()}

	out := image.NewGray(bounds)
	copy(out.Pix, source.Pix)

	for index, feature := range features {
		if err := validateTerrainFeature(index, feature); err != nil {
			return nil, change, err
		}
		if err := applyTerrainFeature(out, feature, &change); err != nil {
			return nil, change, err
		}
	}

	region := featureRegion(bounds, features)
	if erosion != nil && erosion.Droplets > 0 {
		drops, err := ErodeHeightmapRegion(out, region, *erosion)
		if err != nil {
			return nil, change, err
		}
		change.ErodedDrops = drops
	}
	// Rivers come last so they route over the finished relief, including
	// whatever erosion just reshaped. Deriving them earlier would send water
	// down slopes that no longer exist.
	if rivers != nil {
		stats, err := CarveRiverNetwork(out, region, *rivers)
		if err != nil {
			return nil, change, err
		}
		change.Rivers = &stats
		change.Warnings = append(change.Warnings, stats.Warnings...)
	}

	deltaSum, deltaCount := 0.0, 0
	for i := range source.Pix {
		before, after := int(source.Pix[i]), int(out.Pix[i])
		if int(source.Pix[i]) > change.PeakBefore {
			change.PeakBefore = int(source.Pix[i])
		}
		if after > change.PeakAfter {
			change.PeakAfter = after
		}
		if before != after {
			deltaSum += float64(after - before)
			deltaCount++
		}
	}
	change.ChangedPixels = deltaCount
	if deltaCount > 0 {
		change.MeanDelta = deltaSum / float64(deltaCount)
	}
	if change.ClampedPixels > 0 {
		change.Warnings = append(change.Warnings, fmt.Sprintf(
			"%d pixel(s) hit the 0-255 limit and were flattened there; lower the amplitude to keep the relief intact", change.ClampedPixels))
	}
	if change.ChangedPixels == 0 {
		change.Warnings = append(change.Warnings, "no pixel changed; check the feature paths, widths, and amplitudes")
	}
	return out, change, nil
}

func validateTerrainFeature(index int, feature MapTerrainFeature) error {
	if len(feature.Path) == 0 {
		return fmt.Errorf("feature %d has an empty path", index)
	}
	if feature.Width <= 0 {
		return fmt.Errorf("feature %d has width %d; a feature needs a positive width", index, feature.Width)
	}
	if math.IsNaN(feature.Amplitude) || math.IsInf(feature.Amplitude, 0) {
		return fmt.Errorf("feature %d has a non-finite amplitude", index)
	}
	if math.IsNaN(feature.Roughness) || math.IsInf(feature.Roughness, 0) {
		return fmt.Errorf("feature %d has a non-finite roughness", index)
	}
	if math.IsNaN(feature.Detail) || math.IsInf(feature.Detail, 0) {
		return fmt.Errorf("feature %d has a non-finite detail", index)
	}
	switch feature.Kind {
	case TerrainRange, TerrainHills, TerrainPlateau, TerrainVolcano, TerrainCanyon, TerrainShelf, "":
		return nil
	default:
		return fmt.Errorf("feature %d has unknown kind %q", index, feature.Kind)
	}
}

func applyTerrainFeature(out *image.Gray, feature MapTerrainFeature, change *MapTerrainChange) error {
	bounds := out.Bounds()
	width := float64(feature.Width)
	detail := feature.Detail
	if detail <= 0 {
		detail = 1
	}
	// Crest spacing scales with the feature so a small hill and a long range
	// both read at their own size instead of sharing a fixed pixel frequency.
	frequency := detail * ridgeCrestsPerWidth / width
	noise := DefaultMountainNoise(feature.Seed, frequency)
	if feature.Kind == TerrainHills || feature.Kind == TerrainShelf {
		// Rounded landforms should not be ridged; folding would give them
		// crests they do not have.
		noise.Ridged = false
		noise.Multifractal = false
	}
	roughness := clamp01(feature.Roughness)

	minX, minY, maxX, maxY := pathBounds(feature.Path, feature.Width)
	minX = maxInt(bounds.Min.X, minX)
	minY = maxInt(bounds.Min.Y, minY)
	maxX = minInt(bounds.Max.X-1, maxX)
	maxY = minInt(bounds.Max.Y-1, maxY)
	if minX > maxX || minY > maxY {
		change.Warnings = append(change.Warnings,
			fmt.Sprintf("a %s feature lies entirely outside the heightmap and changed nothing", displayKind(feature.Kind)))
		return nil
	}

	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			distance := distanceToPath(float64(x), float64(y), feature.Path)
			if distance > width {
				continue
			}
			t := distance / width
			profile := terrainProfile(feature.Kind, t)
			if profile == 0 {
				continue
			}
			relief := 1.0
			if roughness > 0 {
				relief = (1 - roughness) + roughness*noise.Sample(float64(x), float64(y))
			}
			delta := feature.Amplitude * profile * relief
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
			out.Pix[offset] = value
		}
	}
	return nil
}

// ridgeCrestsPerWidth sets how many major crests span a feature's half-width.
const ridgeCrestsPerWidth = 4.0

// terrainProfile returns the cross-section weight at normalised distance t
// (0 on the path, 1 at the edge of influence). The shape of this curve is what
// distinguishes the landforms from each other.
func terrainProfile(kind string, t float64) float64 {
	t = clamp01(t)
	switch kind {
	case TerrainPlateau:
		// Flat top out to 60% of the width, then a steep drop: a mesa, not a
		// dome. smoothstep on the rim keeps it from being a cylinder.
		if t < 0.6 {
			return 1
		}
		return smoothstep(1 - (t-0.6)/0.4)
	case TerrainVolcano:
		// A cone with a crater: the summit dips, the rim is the maximum, and
		// the flanks fall away. Without the dip it is just a steep hill.
		const rim = 0.28
		if t < rim {
			return 0.45 + 0.55*smoothstep(t/rim)
		}
		return smoothstep(1 - (t-rim)/(1-rim))
	case TerrainCanyon:
		// Flat floor with steep walls; used with a negative amplitude.
		if t < 0.45 {
			return 1
		}
		return smoothstep(1 - (t-0.45)/0.55)
	case TerrainShelf:
		// Linear falloff, so a sea bed slopes evenly away from the coast
		// instead of bulging in the middle.
		return 1 - t
	case TerrainHills:
		// Wide and gentle: a low power keeps the shoulders broad.
		return math.Pow(smoothstep(1-t), 0.7)
	default: // TerrainRange
		// Concentrated along the spine so the belt reads as a ridge line with
		// falling flanks rather than a plateau.
		return math.Pow(smoothstep(1-t), 1.6)
	}
}

func displayKind(kind string) string {
	if kind == "" {
		return TerrainRange
	}
	return kind
}

// distanceToPath returns the distance from a pixel to the nearest point on the
// polyline, which is what turns a point profile into a swept band.
func distanceToPath(x, y float64, path []MapTerrainPoint) float64 {
	if len(path) == 1 {
		return math.Hypot(x-float64(path[0].X), y-float64(path[0].Y))
	}
	best := math.Inf(1)
	for i := 0; i+1 < len(path); i++ {
		d := distanceToSegment(x, y,
			float64(path[i].X), float64(path[i].Y),
			float64(path[i+1].X), float64(path[i+1].Y))
		if d < best {
			best = d
		}
	}
	return best
}

func distanceToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	lengthSquared := dx*dx + dy*dy
	if lengthSquared == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / lengthSquared
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func pathBounds(path []MapTerrainPoint, pad int) (int, int, int, int) {
	minX, minY := math.MaxInt, math.MaxInt
	maxX, maxY := math.MinInt, math.MinInt
	for _, point := range path {
		minX = minInt(minX, point.X)
		minY = minInt(minY, point.Y)
		maxX = maxInt(maxX, point.X)
		maxY = maxInt(maxY, point.Y)
	}
	return minX - pad, minY - pad, maxX + pad, maxY + pad
}

// featureRegion is the area erosion should cover: the features plus a margin,
// so water leaving a new mountain keeps cutting into the ground beyond it
// instead of stopping at an invisible boundary.
func featureRegion(bounds image.Rectangle, features []MapTerrainFeature) image.Rectangle {
	minX, minY := math.MaxInt, math.MaxInt
	maxX, maxY := math.MinInt, math.MinInt
	for _, feature := range features {
		fx0, fy0, fx1, fy1 := pathBounds(feature.Path, feature.Width*2)
		minX, minY = minInt(minX, fx0), minInt(minY, fy0)
		maxX, maxY = maxInt(maxX, fx1), maxInt(maxY, fy1)
	}
	region := image.Rect(minX, minY, maxX+1, maxY+1)
	return region.Intersect(bounds)
}
