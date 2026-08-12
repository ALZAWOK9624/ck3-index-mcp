package indexer

import (
	"image"
	"math"
)

// Relief is the most CPU-bound step the scanner has. Each output pixel reads
// seventeen height samples, and at 8192x4096 that is 570 million samples for
// one heightmap. Everything here exists to make each of those samples an array
// index instead of an interface call, and to stop recomputing values that were
// never going to change.

// reliefLight is a normalized light direction. The two the shader uses are
// fixed by the recipe -- azimuth 315 and 45 at altitude 45 -- so their vectors
// are constants. They used to be derived inside the per-pixel loop, which cost
// four transcendental calls per direction per pixel: 268 million sin/cos calls
// over a full-size map, all with the same two arguments.
type reliefLight struct{ X, Y, Z float64 }

func newReliefLight(azimuthDegrees, altitudeDegrees float64) reliefLight {
	altitude := altitudeDegrees * math.Pi / 180
	azimuth := azimuthDegrees * math.Pi / 180
	return reliefLight{
		X: math.Cos(altitude) * math.Sin(azimuth),
		Y: -math.Cos(altitude) * math.Cos(azimuth),
		Z: math.Sin(altitude),
	}
}

var (
	reliefKeyLight  = newReliefLight(315, 45)
	reliefFillLight = newReliefLight(45, 45)
)

// heightField is the heightmap flattened once into a row-major grid of 16-bit
// luma. Storing luma rather than float64 keeps a full-size map at 67 MiB
// instead of 268 MiB, and the conversion on read is a multiply.
type heightField struct {
	width, height int
	values        []uint16
}

const heightFieldScale = 1.0 / 65535.0

func newHeightField(img image.Image) *heightField {
	bounds := img.Bounds()
	field := &heightField{
		width:  bounds.Dx(),
		height: bounds.Dy(),
		values: make([]uint16, bounds.Dx()*bounds.Dy()),
	}
	if gray, ok := img.(*image.Gray16); ok {
		// Gray16.Pix is big-endian pairs; reading them directly avoids a
		// Gray16At call and its bounds check for every pixel of the map.
		for y := 0; y < field.height; y++ {
			offset := gray.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := field.values[y*field.width : (y+1)*field.width]
			for x := range row {
				i := offset + x*2
				row[x] = uint16(gray.Pix[i])<<8 | uint16(gray.Pix[i+1])
			}
		}
		return field
	}
	for y := 0; y < field.height; y++ {
		row := field.values[y*field.width : (y+1)*field.width]
		for x := range row {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			row[x] = uint16(math.Round(0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)))
		}
	}
	return field
}

// at clamps to the edge, matching the original heightSample. The clamp is what
// makes the wide ±5 taps safe at the border.
func (f *heightField) at(x, y int) float64 {
	if x < 0 {
		x = 0
	} else if x >= f.width {
		x = f.width - 1
	}
	if y < 0 {
		y = 0
	} else if y >= f.height {
		y = f.height - 1
	}
	return float64(f.values[y*f.width+x]) * heightFieldScale
}

func buildMultiDirectionalHillshade(heightmap image.Image) *image.Gray {
	hillshade, _, _ := buildMultiScaleRelief(heightmap)
	return hillshade
}

// buildMultiScaleReliefGo is the pure-Go implementation. buildMultiScaleRelief
// dispatches to this one or to the native implementation in
// map_relief_dispatch_native.go when built with -tags ck3_native.
func buildMultiScaleReliefGo(heightmap image.Image) (*image.Gray, *image.Gray, *image.Gray) {
	bounds := heightmap.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	field := newHeightField(heightmap)
	hillshade := image.NewGray(image.Rect(0, 0, width, height))
	detail := image.NewGray(image.Rect(0, 0, width, height))
	elevation := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		rowOffset := y * width
		for x := 0; x < width; x++ {
			h0 := field.at(x, y)
			dxFine := (field.at(x+1, y) - field.at(x-1, y)) * 9.0
			dyFine := (field.at(x, y+1) - field.at(x, y-1)) * 9.0
			dxBroad := (field.at(x+4, y) - field.at(x-4, y)) * 2.25
			dyBroad := (field.at(x, y+4) - field.at(x, y-4)) * 2.25
			dx := 0.62*dxFine + 0.38*dxBroad
			dy := 0.62*dyFine + 0.38*dyBroad
			nx, ny, nz := -dx, -dy, 1.0
			length := math.Sqrt(nx*nx + ny*ny + nz*nz)
			nx, ny, nz = nx/length, ny/length, nz/length
			key := math.Max(0, nx*reliefKeyLight.X+ny*reliefKeyLight.Y+nz*reliefKeyLight.Z)
			fill := math.Max(0, nx*reliefFillLight.X+ny*reliefFillLight.Y+nz*reliefFillLight.Z)
			shade := 0.72*key + 0.28*fill
			broadMean := (field.at(x-5, y) + field.at(x+5, y) + field.at(x, y-5) + field.at(x, y+5)) / 4
			fineMean := (field.at(x-2, y) + field.at(x+2, y) + field.at(x, y-2) + field.at(x, y+2)) / 4
			curvature := (h0-fineMean)*42 + (h0-broadMean)*18
			shade = math.Max(0, math.Min(1, shade+math.Max(-0.10, math.Min(0.10, curvature*0.12))))
			hillshade.Pix[rowOffset+x] = uint8(math.Round(math.Max(0, math.Min(1, 0.12+shade*0.88)) * 255))
			detail.Pix[rowOffset+x] = uint8(math.Round(math.Max(0, math.Min(1, 0.5+curvature)) * 255))
			elevation.Pix[rowOffset+x] = uint8(math.Round(math.Max(0, math.Min(1, h0)) * 255))
		}
	}
	return hillshade, detail, elevation
}
