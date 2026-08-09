package indexer

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// referencePackedRow is the expression the fast readers replace. Any divergence
// between them relabels pixels, which silently changes province geometry,
// adjacency and area rather than failing.
func referencePackedRow(img image.Image, y int, out []uint32) {
	bounds := img.Bounds()
	for x := 0; x < bounds.Dx(); x++ {
		r16, g16, b16, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
		out[x] = uint32(uint8(r16>>8))<<16 | uint32(uint8(g16>>8))<<8 | uint32(uint8(b16>>8))
	}
}

type opaqueGenericImage struct{ *image.RGBA }

func (opaqueGenericImage) isNotAConcreteFastPath() {}

func TestPackedRowReaderMatchesTheInterfaceItReplaces(t *testing.T) {
	const width, height = 7, 5
	fill := func(set func(x, y int, c color.Color)) {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				set(x, y, color.NRGBA{
					R: uint8(x*31 + y*7),
					G: uint8(x*13 + y*57),
					B: uint8(x*97 + y*3),
					A: uint8(255 - (x+y)%4*60),
				})
			}
		}
	}

	rgba := image.NewRGBA(image.Rect(0, 0, width, height))
	fill(func(x, y int, c color.Color) { rgba.Set(x, y, c) })

	nrgba := image.NewNRGBA(image.Rect(0, 0, width, height))
	fill(func(x, y int, c color.Color) { nrgba.Set(x, y, c) })

	opaqueNRGBA := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			opaqueNRGBA.Set(x, y, color.NRGBA{R: uint8(x * 37), G: uint8(y * 41), B: uint8(x*11 + y*3), A: 255})
		}
	}

	palette := make(color.Palette, 0, 16)
	for i := 0; i < 16; i++ {
		palette = append(palette, color.RGBA{R: uint8(i * 17), G: uint8(255 - i*15), B: uint8(i * 3), A: 255})
	}
	paletted := image.NewPaletted(image.Rect(0, 0, width, height), palette)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			paletted.SetColorIndex(x, y, uint8((x+y)%len(palette)))
		}
	}

	// A non-zero origin is what a cropped sub-image looks like; the row reader
	// has to honour Bounds().Min rather than assume the pixel buffer starts at
	// the top-left of the image it was cut from.
	offsetSource := image.NewRGBA(image.Rect(0, 0, width+4, height+4))
	fill(func(x, y int, c color.Color) { offsetSource.Set(x+3, y+2, c) })
	offset := offsetSource.SubImage(image.Rect(3, 2, 3+width, 2+height))

	for name, img := range map[string]image.Image{
		"RGBA":              rgba,
		"NRGBA translucent": nrgba,
		"NRGBA opaque":      opaqueNRGBA,
		"Paletted":          paletted,
		"RGBA sub-image":    offset,
		"generic fallback":  opaqueGenericImage{rgba},
	} {
		t.Run(name, func(t *testing.T) {
			read := newPackedRowReader(img)
			got := make([]uint32, img.Bounds().Dx())
			want := make([]uint32, img.Bounds().Dx())
			for y := 0; y < img.Bounds().Dy(); y++ {
				read(y, got)
				referencePackedRow(img, y, want)
				for x := range want {
					if got[x] != want[x] {
						t.Fatalf("row %d pixel %d: fast reader gave %06x, At().RGBA() gives %06x", y, x, got[x], want[x])
					}
				}
			}
		})
	}
}

// The merged walk has to produce exactly what two separate walks produced, or
// every stored province outline changes.
func TestMergedRunEncodingMatchesSeparateFillAndBoundaryWalks(t *testing.T) {
	const width, height = 9, 6
	labels := make([]provinceLabel, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			switch {
			case x < 3:
				labels[y*width+x] = 1
			case x < 6 && y > 1:
				labels[y*width+x] = 2
			case x == 7 && y == 3:
				labels[y*width+x] = 0 // a hole, so boundaries are not just the frame
			default:
				labels[y*width+x] = 3
			}
		}
	}
	fills, boundaries := encodeProvinceRuns(labels, width, height)
	wantFills := legacyEncodeProvinceRuns(labels, width, height, false)
	wantBoundaries := legacyEncodeProvinceRuns(labels, width, height, true)

	compare := func(label string, got, want map[int][]byte) {
		if len(got) != len(want) {
			t.Fatalf("%s: %d provinces, want %d", label, len(got), len(want))
		}
		for id, expected := range want {
			actual, present := got[id]
			if !present {
				t.Fatalf("%s: province %d missing", label, id)
			}
			if string(actual) != string(expected) {
				t.Fatalf("%s: province %d runs differ", label, id)
			}
		}
	}
	compare("fills", fills, wantFills)
	compare("boundaries", boundaries, wantBoundaries)
}

// legacyEncodeProvinceRuns is the pre-merge implementation, kept as the oracle
// for the merged one.
func legacyEncodeProvinceRuns(labels []provinceLabel, width, height int, boundaryOnly bool) map[int][]byte {
	buffers := map[int]*bytes.Buffer{}
	isBoundary := func(x, y int, id provinceLabel) bool {
		if x == 0 || y == 0 || x+1 == width || y+1 == height {
			return true
		}
		return labels[y*width+x-1] != id || labels[y*width+x+1] != id ||
			labels[(y-1)*width+x] != id || labels[(y+1)*width+x] != id
	}
	for y := 0; y < height; y++ {
		runID, runStart := 0, -1
		flush := func(x int) {
			if runID > 0 {
				appendMapRun(buffers, runID, y, runStart, x-1)
			}
			runID, runStart = 0, -1
		}
		for x := 0; x < width; x++ {
			label := labels[y*width+x]
			id := int(label)
			include := id > 0 && (!boundaryOnly || isBoundary(x, y, label))
			if !include {
				flush(x)
				continue
			}
			if runID != id {
				flush(x)
				runID, runStart = id, x
			}
		}
		flush(width)
	}
	out := map[int][]byte{}
	for id, buffer := range buffers {
		out[id] = buffer.Bytes()
	}
	return out
}
