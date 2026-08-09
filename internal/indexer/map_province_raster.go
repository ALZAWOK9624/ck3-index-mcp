package indexer

import (
	"image"
)

// provinces.png is the largest single input the scanner decodes: a full-size
// CK3 map is 8192x4096, so every per-pixel decision is taken 33.5 million
// times and every byte of per-pixel state costs 32 MiB.
//
// Province ids are bounded by the definition table -- CK3 addresses them as a
// 24-bit colour key and real projects hold thousands, not billions -- so the
// label matrix is int32 rather than int. That alone halves it from 268 MiB to
// 134 MiB at 8K, which is the difference between a comfortable scan and one
// that pushes a small VPS into swap.
type provinceLabel = int32

// Keep the parser's public ceiling mechanically tied to the raster storage
// type. Raising MaxProvinceID without widening provinceLabel must not compile.
const _ provinceLabel = MaxProvinceID

// packedRowReader fills one row with 24-bit RGB keys. Reading through
// image.Image.At allocates a color.Color and returns four 16-bit components
// per pixel, all to recover three bytes that are already contiguous in memory.
// The concrete decodings provinces.png actually produces are handled directly.
type packedRowReader func(y int, out []uint32)

// newPackedRowReader is exact, not approximate: each fast path reproduces what
// img.At(x, y).RGBA() would have returned for that pixel, including the
// premultiplication image.NRGBA applies on the way out. Anything it does not
// recognise falls back to the interface it is replacing.
func newPackedRowReader(img image.Image) packedRowReader {
	bounds := img.Bounds()
	minX, minY, width := bounds.Min.X, bounds.Min.Y, bounds.Dx()
	switch source := img.(type) {
	case *image.RGBA:
		// RGBA.Pix is already premultiplied, and At().RGBA() expands each byte
		// to v*0x101 before the caller shifts it back down by 8. The raw byte
		// is therefore the same value, with no reconstruction needed.
		return func(y int, out []uint32) {
			offset := source.PixOffset(minX, minY+y)
			pix := source.Pix
			for x := 0; x < width; x++ {
				i := offset + x*4
				out[x] = uint32(pix[i])<<16 | uint32(pix[i+1])<<8 | uint32(pix[i+2])
			}
		}
	case *image.NRGBA:
		// NRGBA.Pix is straight alpha, so a translucent pixel has to be
		// premultiplied to match At().RGBA(). That premultiplication happens in
		// 16-bit space -- v |= v<<8, then *A, then /0xff -- and doing the same
		// arithmetic in 8 bits rounds differently, which the exactness test
		// catches. provinces.png is opaque in practice, which is why the common
		// branch skips all of it.
		premultiply := func(v, a uint8) uint32 {
			wide := uint32(v)
			wide |= wide << 8
			wide *= uint32(a)
			wide /= 0xff
			return wide >> 8
		}
		return func(y int, out []uint32) {
			offset := source.PixOffset(minX, minY+y)
			pix := source.Pix
			for x := 0; x < width; x++ {
				i := offset + x*4
				a := pix[i+3]
				if a == 0xff {
					out[x] = uint32(pix[i])<<16 | uint32(pix[i+1])<<8 | uint32(pix[i+2])
					continue
				}
				out[x] = premultiply(pix[i], a)<<16 | premultiply(pix[i+1], a)<<8 | premultiply(pix[i+2], a)
			}
		}
	case *image.Paletted:
		// One conversion per palette entry instead of one per pixel, using the
		// same RGBA()>>8 arithmetic the general path would have used.
		lookup := make([]uint32, len(source.Palette))
		for i, entry := range source.Palette {
			r16, g16, b16, _ := entry.RGBA()
			lookup[i] = uint32(uint8(r16>>8))<<16 | uint32(uint8(g16>>8))<<8 | uint32(uint8(b16>>8))
		}
		return func(y int, out []uint32) {
			offset := source.PixOffset(minX, minY+y)
			pix := source.Pix
			for x := 0; x < width; x++ {
				index := int(pix[offset+x])
				if index < len(lookup) {
					out[x] = lookup[index]
					continue
				}
				out[x] = 0
			}
		}
	default:
		return func(y int, out []uint32) {
			for x := 0; x < width; x++ {
				r16, g16, b16, _ := img.At(minX+x, minY+y).RGBA()
				out[x] = uint32(uint8(r16>>8))<<16 | uint32(uint8(g16>>8))<<8 | uint32(uint8(b16>>8))
			}
		}
	}
}
