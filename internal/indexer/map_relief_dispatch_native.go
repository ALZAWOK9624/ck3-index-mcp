//go:build ck3_native && cgo && amd64

package indexer

/*
// Keep the scalar fallback on baseline x86-64. SIMD is enabled only on the
// specialized functions and dispatched after checking CPU and OS support.
#cgo CFLAGS: -O3

#include <stdint.h>
#include <math.h>

// no-math-errno lets GCC inline sqrt as SQRTSD. The remaining library calls
// are replaced below: fmax/fmin are hand-inlined (their library versions are
// per-pixel function calls on the baseline x86-64 target, and the ternary
// versions are byte-identical to Go's math.Max/math.Min for the finite
// inputs this kernel produces), and round is replaced by the half-away
// truncation identity that Go's math.Round uses for non-negative values.
#pragma GCC optimize ("no-math-errno", "fp-contract=off")

#include "map_relief_simd.h"

// Mirrors buildMultiScaleReliefGo in IEEE-754 operation order. The pragma
// above disables contraction explicitly; splitting C statements alone is not
// sufficient to stop a compiler from fusing multiply-add operations.

// Height samples are scaled by 1/65535, exactly like heightField.at in the
// Go implementation.
static const double relief_height_scale = 1.0 / 65535.0;

// Byte-identical to Go's math.Max/math.Min for the finite inputs this kernel
// produces. Sign-of-zero flips only occur where the downstream arithmetic
// collapses them before the byte is written.
static inline double gh_fmax(double a, double b)
{
	return a > b ? a : b;
}

static inline double gh_fmin(double a, double b)
{
	return a < b ? a : b;
}

// Byte-identical to uint8(math.Round(v)) for finite 0 <= v <= 255.
static inline uint8_t gh_round_u8(double v)
{
	return (uint8_t)(v + 0.5);
}

// memcpy keeps odd byte strides and unaligned Gray16 subimages defined in C.
static inline double gh_relief_sample(const uint8_t *p, int big_endian)
{
    uint16_t value;
    memcpy(&value, p, sizeof(value));
    if (big_endian) value = __builtin_bswap16(value);
    return (double)value * relief_height_scale;
}

static void gh_relief_band_impl(const uint8_t *field, int width, int height, int stride, int big_endian, int y0, int y1,
                                uint8_t *hill, uint8_t *detail, uint8_t *elev,
                                double keyX, double keyY, double keyZ,
                                double fillX, double fillY, double fillZ, int kernel)
{
	for (int y = y0; y < y1; ++y) {
		// Row pointers clamped once per row; row 5 is the current row.
		const uint8_t *rp[11];
		for (int k = 0; k < 11; ++k) {
			int ry = y + k - 5;
			if (ry < 0) {
				ry = 0;
			} else if (ry >= height) {
				ry = height - 1;
			}
			rp[k] = field + (size_t)ry * stride;
		}
		uint8_t *hillRow = hill + (size_t)y * width;
		uint8_t *detailRow = detail + (size_t)y * width;
		uint8_t *elevRow = elev + (size_t)y * width;
		int vectorEnd = 5;
		if (kernel == 2 && width >= 18) {
			vectorEnd = gh_relief_row_avx512(rp, width, hillRow, detailRow, elevRow,
				keyX, keyY, keyZ, fillX, fillY, fillZ, big_endian);
		} else if (kernel && width >= 14) {
			vectorEnd = gh_relief_row_avx2(rp, width, hillRow, detailRow, elevRow,
				keyX, keyY, keyZ, fillX, fillY, fillZ, big_endian);
		}
		for (int x = 0; x < width; ++x) {
			if (x == 5) x = vectorEnd;
			if (x >= width) break;
			const int xm5 = x > 4 ? x - 5 : 0;
			const int xm4 = x > 3 ? x - 4 : 0;
			const int xm2 = x > 1 ? x - 2 : 0;
			const int xm1 = x > 0 ? x - 1 : 0;
			const int xp1 = x < width - 1 ? x + 1 : width - 1;
			const int xp2 = x < width - 2 ? x + 2 : width - 1;
			const int xp4 = x < width - 4 ? x + 4 : width - 1;
			const int xp5 = x < width - 5 ? x + 5 : width - 1;

			const double h0 = gh_relief_sample(rp[5] + 2*(x), big_endian);

			const double sx1 = gh_relief_sample(rp[5] + 2*(xp1), big_endian);
			const double sxm1 = gh_relief_sample(rp[5] + 2*(xm1), big_endian);
			const double dxf = sx1 - sxm1;
			const double dxFine = dxf * 9.0;
			const double sy1 = gh_relief_sample(rp[6] + 2*(x), big_endian);
			const double sym1 = gh_relief_sample(rp[4] + 2*(x), big_endian);
			const double dyf = sy1 - sym1;
			const double dyFine = dyf * 9.0;
			const double sx4 = gh_relief_sample(rp[5] + 2*(xp4), big_endian);
			const double sxm4 = gh_relief_sample(rp[5] + 2*(xm4), big_endian);
			const double dxb = sx4 - sxm4;
			const double dxBroad = dxb * 2.25;
			const double sy4 = gh_relief_sample(rp[9] + 2*(x), big_endian);
			const double sym4 = gh_relief_sample(rp[1] + 2*(x), big_endian);
			const double dyb = sy4 - sym4;
			const double dyBroad = dyb * 2.25;

			const double dxm1 = 0.62 * dxFine;
			const double dxm2 = 0.38 * dxBroad;
			const double dx = dxm1 + dxm2;
			const double dym1 = 0.62 * dyFine;
			const double dym2 = 0.38 * dyBroad;
			const double dy = dym1 + dym2;

			double nx = -dx;
			double ny = -dy;
			double nz = 1.0;
			const double l1 = nx * nx;
			const double l2 = ny * ny;
			const double l3 = nz * nz;
			const double l4 = l1 + l2;
			const double l5 = l4 + l3;
			const double length = sqrt(l5);
			nx = nx / length;
			ny = ny / length;
			nz = nz / length;

			const double k1 = nx * keyX;
			const double k2 = ny * keyY;
			const double k3 = nz * keyZ;
			const double k4 = k1 + k2;
			const double k5 = k4 + k3;
			const double key = gh_fmax(0.0, k5);
			const double f1 = nx * fillX;
			const double f2 = ny * fillY;
			const double f3 = nz * fillZ;
			const double f4 = f1 + f2;
			const double f5 = f4 + f3;
			const double fill = gh_fmax(0.0, f5);

			const double sh1 = 0.72 * key;
			const double sh2 = 0.28 * fill;
			const double shadeBase = sh1 + sh2;

			const double bm1 = gh_relief_sample(rp[5] + 2*(xm5), big_endian);
			const double bm2 = gh_relief_sample(rp[5] + 2*(xp5), big_endian);
			const double bm3 = gh_relief_sample(rp[0] + 2*(x), big_endian);
			const double bm4 = gh_relief_sample(rp[10] + 2*(x), big_endian);
			// Left-to-right, exactly as Go evaluates a+b+c+d. Summing the
			// two axes separately and adding the pairs is a different
			// IEEE-754 rounding and drifts one ULP, which survives the x42
			// and x18 curvature weights and flips whole bytes in the detail
			// plane.
			const double bm5 = bm1 + bm2;
			const double bm6 = bm5 + bm3;
			const double bm7 = bm6 + bm4;
			const double broadMean = bm7 / 4.0;
			const double fm1 = gh_relief_sample(rp[5] + 2*(xm2), big_endian);
			const double fm2 = gh_relief_sample(rp[5] + 2*(xp2), big_endian);
			const double fm3 = gh_relief_sample(rp[3] + 2*(x), big_endian);
			const double fm4 = gh_relief_sample(rp[7] + 2*(x), big_endian);
			const double fm5 = fm1 + fm2;
			const double fm6 = fm5 + fm3;
			const double fm7 = fm6 + fm4;
			const double fineMean = fm7 / 4.0;

			const double cu1 = h0 - fineMean;
			const double cu2 = cu1 * 42.0;
			const double cu3 = h0 - broadMean;
			const double cu4 = cu3 * 18.0;
			const double curvature = cu2 + cu4;

			const double cl1 = curvature * 0.12;
			const double cl2 = gh_fmin(0.10, cl1);
			const double cl3 = gh_fmax(-0.10, cl2);
			const double shadePre = shadeBase + cl3;
			const double shadeMin = gh_fmin(1.0, shadePre);
			const double shade = gh_fmax(0.0, shadeMin);

			const double hi1 = shade * 0.88;
			const double hi2 = 0.12 + hi1;
			const double hi3 = gh_fmin(1.0, hi2);
			const double hi4 = gh_fmax(0.0, hi3);
			const double hi5 = hi4 * 255.0;
			hillRow[x] = gh_round_u8(hi5);

			const double de1 = 0.5 + curvature;
			const double de2 = gh_fmin(1.0, de1);
			const double de3 = gh_fmax(0.0, de2);
			const double de4 = de3 * 255.0;
			detailRow[x] = gh_round_u8(de4);

			const double el1 = gh_fmin(1.0, h0);
			const double el2 = gh_fmax(0.0, el1);
			const double el3 = el2 * 255.0;
			elevRow[x] = gh_round_u8(el3);
		}
	}
}
*/
import "C"

import (
	"image"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/cpu"
)

// buildMultiScaleRelief dispatches to the C implementation above when built
// with -tags ck3_native and cgo enabled. The light vectors are computed once
// in Go and passed in, so the only C arithmetic is IEEE-754
// add/mul/div/sqrt/min/max/round, which keeps the output byte-for-byte
// identical to the pure-Go implementation.
func buildMultiScaleRelief(heightmap image.Image) (*image.Gray, *image.Gray, *image.Gray) {
	kernel := 0
	if cpu.X86.HasAVX2 {
		kernel = 1
		if cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512DQ {
			kernel = 2
		}
	}
	return buildMultiScaleReliefNative(heightmap, kernel)
}

// kernel 0 is scalar, 1 is AVX2, and 2 is AVX-512. The caller must check
// CPU/OS support before selecting a nonzero mode.
func buildMultiScaleReliefNative(heightmap image.Image, kernel int) (*image.Gray, *image.Gray, *image.Gray) {
	bounds := heightmap.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	hillshade := image.NewGray(image.Rect(0, 0, width, height))
	detail := image.NewGray(image.Rect(0, 0, width, height))
	elevation := image.NewGray(image.Rect(0, 0, width, height))
	if width == 0 || height == 0 {
		return hillshade, detail, elevation
	}

	var fieldData unsafe.Pointer
	stride, bigEndian := width*2, 0
	if gray, ok := heightmap.(*image.Gray16); ok {
		// PNG heightmaps already contain every sample we need. Load and
		// byte-swap them in SIMD registers instead of copying a full raster.
		fieldData = unsafe.Pointer(&gray.Pix[0])
		stride, bigEndian = gray.Stride, 1
	} else {
		field := newHeightField(heightmap)
		fieldData = unsafe.Pointer(&field.values[0])
	}

	workers := runtime.GOMAXPROCS(0)
	// A cgo call and goroutine per row cost more than the shader on thumbnails.
	// Give each worker at least 64K pixels before adding another worker.
	if useful := width * height / (64 << 10); workers > useful {
		workers = useful
	}
	if workers > height {
		workers = height
	}
	if workers < 1 {
		workers = 1
	}
	bandSize := (height + workers - 1) / workers
	runBand := func(y0, y1 int) {
		C.gh_relief_band_impl(
			(*C.uint8_t)(fieldData),
			C.int(width), C.int(height), C.int(stride), C.int(bigEndian), C.int(y0), C.int(y1),
			(*C.uint8_t)(unsafe.Pointer(&hillshade.Pix[0])),
			(*C.uint8_t)(unsafe.Pointer(&detail.Pix[0])),
			(*C.uint8_t)(unsafe.Pointer(&elevation.Pix[0])),
			C.double(reliefKeyLight.X), C.double(reliefKeyLight.Y), C.double(reliefKeyLight.Z),
			C.double(reliefFillLight.X), C.double(reliefFillLight.Y), C.double(reliefFillLight.Z),
			C.int(kernel),
		)
	}
	if workers == 1 {
		runBand(0, height)
		return hillshade, detail, elevation
	}
	var wg sync.WaitGroup
	for start := 0; start < height; start += bandSize {
		end := start + bandSize
		if end > height {
			end = height
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			runBand(y0, y1)
		}(start, end)
	}
	wg.Wait()
	return hillshade, detail, elevation
}
