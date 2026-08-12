//go:build ck3_native && cgo && amd64

package indexer

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// The raster contract is that a plate renders identically whichever backend
// produced it, so the C relief has to agree with the Go relief byte for byte,
// not merely closely. Sizes are deliberately not multiples of the worker
// count: the native path splits the image into row bands, and an off-by-one
// band boundary would only show up on a height that does not divide evenly.
func TestNativeReliefMatchesGoByteForByte(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{1, 1},
		{3, 2},
		{64, 5},
		{137, 89},
		{200, 151},
	} {
		heightmap := syntheticHeightmap(size.width, size.height)
		wantHill, wantDetail, wantElevation := buildMultiScaleReliefGo(heightmap)
		gotHill, gotDetail, gotElevation := buildMultiScaleRelief(heightmap)
		for _, plane := range []struct {
			name string
			got  *image.Gray
			want *image.Gray
		}{
			{"hillshade", gotHill, wantHill},
			{"detail", gotDetail, wantDetail},
			{"elevation", gotElevation, wantElevation},
		} {
			if len(plane.got.Pix) != len(plane.want.Pix) {
				t.Fatalf("%dx%d %s: length %d, want %d", size.width, size.height, plane.name, len(plane.got.Pix), len(plane.want.Pix))
			}
			for i := range plane.want.Pix {
				if plane.got.Pix[i] != plane.want.Pix[i] {
					x, y := i%plane.want.Stride, i/plane.want.Stride
					t.Fatalf("%dx%d %s: pixel (%d,%d) native %d, Go %d",
						size.width, size.height, plane.name, x, y, plane.got.Pix[i], plane.want.Pix[i])
				}
			}
		}
	}
}

// syntheticHeightmap builds terrain with slopes, ridges and flats so the
// hillshade, curvature and clamping branches all get exercised.
func syntheticHeightmap(width, height int) *image.Gray16 {
	img := image.NewGray16(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			fx := float64(x) / float64(width+1)
			fy := float64(y) / float64(height+1)
			ridge := math.Abs(math.Sin(fx*7)) * math.Cos(fy*5)
			slope := fx*0.4 + fy*0.3
			value := (ridge*0.5 + slope) * 40000
			if value < 0 {
				value = 0
			}
			if value > 65535 {
				value = 65535
			}
			img.SetGray16(x, y, color.Gray16{Y: uint16(value)})
		}
	}
	return img
}
