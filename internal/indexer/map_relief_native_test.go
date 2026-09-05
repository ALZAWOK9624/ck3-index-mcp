//go:build ck3_native && cgo && amd64

package indexer

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand"
	"runtime"
	"testing"

	"golang.org/x/sys/cpu"
)

func TestNativeReliefKernels(t *testing.T) {
	kernels := []int{0}
	if cpu.X86.HasAVX2 {
		kernels = append(kernels, 1)
		if cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512DQ {
			kernels = append(kernels, 2)
		}
	}
	check := func(t *testing.T, img image.Image) {
		t.Helper()
		wh, wd, we := buildMultiScaleReliefGo(img)
		for _, kernel := range kernels {
			gh, gd, ge := buildMultiScaleReliefNative(img, kernel)
			if gh.Rect != wh.Rect || !bytes.Equal(gh.Pix, wh.Pix) || !bytes.Equal(gd.Pix, wd.Pix) || !bytes.Equal(ge.Pix, we.Pix) {
				t.Fatalf("kernel %d differs from Go on %v", kernel, img.Bounds())
			}
		}
	}
	// Every SIMD remainder and clamped border, including empty dimensions.
	for w := 0; w <= 33; w++ {
		for h := 0; h <= 15; h++ {
			check(t, syntheticHeightmap(w, h))
		}
	}
	rng := rand.New(rand.NewSource(20260905))
	img := image.NewGray16(image.Rect(-7, 11, 519, 534))
	rng.Read(img.Pix)
	// This subimage has a nonzero origin, padding between rows, unaligned
	// samples and enough pixels to split into non-divisible worker bands.
	sub := img.SubImage(image.Rect(-4, 14, 509, 529))
	for _, workers := range []int{1, 3, 8} {
		t.Run(fmt.Sprintf("random/workers=%d", workers), func(t *testing.T) {
			old := runtime.GOMAXPROCS(workers)
			defer runtime.GOMAXPROCS(old)
			check(t, sub)
		})
	}
	// Exercise every 16-bit elevation, flat terrain, sharp steps, and the
	// floating-point half-byte thresholds in curvature quantization.
	for pattern := 0; pattern < 4; pattern++ {
		img := image.NewGray16(image.Rect(0, 0, 256, 256))
		for i := 0; i < 65536; i++ {
			v := uint16(i)
			if pattern == 1 {
				v = 65535
			}
			if pattern == 2 {
				v = uint16(i%2) * 65535
			}
			if pattern == 3 {
				v = 32760 + uint16(rng.Intn(16))
			}
			img.Pix[2*i], img.Pix[2*i+1] = byte(v>>8), byte(v)
		}
		check(t, img)
	}
	odd := &image.Gray16{Pix: make([]byte, 79*39+1)[1:], Stride: 79, Rect: image.Rect(3, 4, 42, 43)}
	rng.Read(odd.Pix)
	check(t, odd)
	gray := image.NewGray(image.Rect(3, 5, 131, 106))
	rng.Read(gray.Pix)
	check(t, gray)
}

func BenchmarkNativeReliefKernels(b *testing.B) {
	for _, size := range []int{512, 2048} {
		img := syntheticHeightmap(size, size)
		for _, kernel := range []int{0, 1, 2} {
			b.Run(fmt.Sprintf("%d/kernel%d", size, kernel), func(b *testing.B) {
				if kernel != 0 && !cpu.X86.HasAVX2 {
					b.Skip("CPU/OS has no AVX2")
				}
				if kernel == 2 && !(cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512DQ) {
					b.Skip("CPU/OS has no AVX-512")
				}
				b.ReportAllocs()
				b.SetBytes(int64(size * size * 2))
				for i := 0; i < b.N; i++ {
					buildMultiScaleReliefNative(img, kernel)
				}
			})
		}
	}
}

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
