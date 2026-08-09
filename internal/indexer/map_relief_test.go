package indexer

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// legacyMultiScaleRelief is the pre-optimization implementation, kept as the
// oracle. The optimization is only worth having if the plate is unchanged.
func legacyMultiScaleRelief(heightmap image.Image) (*image.Gray, *image.Gray, *image.Gray) {
	b := heightmap.Bounds()
	hillshade := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	detail := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	elevation := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			h0 := heightSample(heightmap, x, y)
			dxFine := (heightSample(heightmap, x+1, y) - heightSample(heightmap, x-1, y)) * 9.0
			dyFine := (heightSample(heightmap, x, y+1) - heightSample(heightmap, x, y-1)) * 9.0
			dxBroad := (heightSample(heightmap, x+4, y) - heightSample(heightmap, x-4, y)) * 2.25
			dyBroad := (heightSample(heightmap, x, y+4) - heightSample(heightmap, x, y-4)) * 2.25
			dx := 0.62*dxFine + 0.38*dxBroad
			dy := 0.62*dyFine + 0.38*dyBroad
			nx, ny, nz := -dx, -dy, 1.0
			length := math.Sqrt(nx*nx + ny*ny + nz*nz)
			nx, ny, nz = nx/length, ny/length, nz/length
			light := func(azimuth float64) float64 {
				altitude := 45 * math.Pi / 180
				azimuth *= math.Pi / 180
				lx := math.Cos(altitude) * math.Sin(azimuth)
				ly := -math.Cos(altitude) * math.Cos(azimuth)
				lz := math.Sin(altitude)
				return math.Max(0, nx*lx+ny*ly+nz*lz)
			}
			shade := 0.72*light(315) + 0.28*light(45)
			broadMean := (heightSample(heightmap, x-5, y) + heightSample(heightmap, x+5, y) + heightSample(heightmap, x, y-5) + heightSample(heightmap, x, y+5)) / 4
			fineMean := (heightSample(heightmap, x-2, y) + heightSample(heightmap, x+2, y) + heightSample(heightmap, x, y-2) + heightSample(heightmap, x, y+2)) / 4
			curvature := (h0-fineMean)*42 + (h0-broadMean)*18
			shade = math.Max(0, math.Min(1, shade+math.Max(-0.10, math.Min(0.10, curvature*0.12))))
			hillshade.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: uint8(math.Round(math.Max(0, math.Min(1, 0.12+shade*0.88)) * 255))})
			detail.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: uint8(math.Round(math.Max(0, math.Min(1, 0.5+curvature)) * 255))})
			elevation.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: uint8(math.Round(math.Max(0, math.Min(1, h0)) * 255))})
		}
	}
	return hillshade, detail, elevation
}

func syntheticGray16Heightmap(width, height int) *image.Gray16 {
	img := image.NewGray16(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Ridges plus a step, so gradients, curvature and the edge clamp
			// all get exercised rather than a smooth ramp that hides errors.
			v := 20000 +
				9000*math.Sin(float64(x)*0.35) +
				7000*math.Cos(float64(y)*0.27) +
				4000*math.Sin(float64(x+y)*0.11)
			if x > width*2/3 {
				v += 12000
			}
			img.SetGray16(x, y, color.Gray16{Y: uint16(math.Max(0, math.Min(65535, v)))})
		}
	}
	return img
}

// heightmap.png is 16-bit grayscale, so this is the production path and it has
// to be byte-for-byte identical: the relief plate is reviewed visually and a
// drifting one is very hard to attribute later.
func TestMultiScaleReliefMatchesTheOriginalOnGray16(t *testing.T) {
	img := syntheticGray16Heightmap(37, 29)
	gotHill, gotDetail, gotElevation := buildMultiScaleRelief(img)
	wantHill, wantDetail, wantElevation := legacyMultiScaleRelief(img)
	for label, pair := range map[string][2]*image.Gray{
		"hillshade": {gotHill, wantHill},
		"detail":    {gotDetail, wantDetail},
		"elevation": {gotElevation, wantElevation},
	} {
		got, want := pair[0], pair[1]
		if len(got.Pix) != len(want.Pix) {
			t.Fatalf("%s: %d pixels, want %d", label, len(got.Pix), len(want.Pix))
		}
		for i := range want.Pix {
			if got.Pix[i] != want.Pix[i] {
				t.Fatalf("%s: pixel %d = %d, want %d", label, i, got.Pix[i], want.Pix[i])
			}
		}
	}
}

// An RGB heightmap goes through a luma conversion that the flattened field
// stores as an integer, so a sub-level rounding difference is possible. It
// must stay sub-level: anything larger would be a visible change.
func TestMultiScaleReliefStaysWithinOneLevelOnRGB(t *testing.T) {
	const width, height = 31, 23
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(40 + 70*math.Sin(float64(x)*0.4)),
				G: uint8(120 + 60*math.Cos(float64(y)*0.3)),
				B: uint8(90 + 50*math.Sin(float64(x+y)*0.2)),
				A: 255,
			})
		}
	}
	gotHill, _, gotElevation := buildMultiScaleRelief(img)
	wantHill, _, wantElevation := legacyMultiScaleRelief(img)
	for label, pair := range map[string][2]*image.Gray{
		"hillshade": {gotHill, wantHill},
		"elevation": {gotElevation, wantElevation},
	} {
		got, want := pair[0], pair[1]
		for i := range want.Pix {
			delta := int(got.Pix[i]) - int(want.Pix[i])
			if delta < -1 || delta > 1 {
				t.Fatalf("%s: pixel %d = %d, want %d (delta %d)", label, i, got.Pix[i], want.Pix[i], delta)
			}
		}
	}
}

// The two light directions are fixed by the recipe. Deriving them per pixel
// cost four transcendental calls each; the hoisted vectors must be the same
// numbers the loop used to compute.
func TestReliefLightVectorsMatchTheInlineDerivation(t *testing.T) {
	for _, testCase := range []struct {
		azimuth float64
		want    reliefLight
	}{
		{315, reliefKeyLight},
		{45, reliefFillLight},
	} {
		altitude := 45 * math.Pi / 180
		azimuth := testCase.azimuth * math.Pi / 180
		inline := reliefLight{
			X: math.Cos(altitude) * math.Sin(azimuth),
			Y: -math.Cos(altitude) * math.Cos(azimuth),
			Z: math.Sin(altitude),
		}
		if inline != testCase.want {
			t.Errorf("azimuth %.0f: hoisted %v, inline %v", testCase.azimuth, testCase.want, inline)
		}
	}
}

func BenchmarkMultiScaleRelief512(b *testing.B) {
	img := syntheticGray16Heightmap(512, 512)
	b.ReportAllocs()
	for b.Loop() {
		buildMultiScaleRelief(img)
	}
}

func BenchmarkMultiScaleReliefLegacy512(b *testing.B) {
	img := syntheticGray16Heightmap(512, 512)
	b.ReportAllocs()
	for b.Loop() {
		legacyMultiScaleRelief(img)
	}
}
