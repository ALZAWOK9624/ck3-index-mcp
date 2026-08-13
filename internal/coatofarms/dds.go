// Package coatofarms reads CK3 coat-of-arms definitions and rasterizes them,
// so a heraldic design can be checked without launching the game.
//
// CK3 renders a coat of arms on the GPU from a pattern texture and a stack of
// emblem textures, all of them block-compressed DDS. Nothing in the Go image
// libraries reads that format, and the compositing is not alpha blending: the
// texture channels are masks that select between the three colours a definition
// names. Both are implemented here from the shader behaviour CK3 ships.
package coatofarms

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

const (
	ddsMagic       = 0x20534444 // "DDS "
	ddsHeaderBytes = 128        // 4 magic + 124 header
	fourCCDXT1     = 0x31545844
	fourCCDXT3     = 0x33545844
	fourCCDXT5     = 0x35545844

	// Pixel-format flags. A DDS carries either a fourCC naming a compression
	// scheme or channel bit masks describing uncompressed pixels; 36 of CK3's
	// 1629 coat-of-arms textures take the second route, so a decoder that only
	// reads fourCC drops them.
	ddpfAlphaPixels = 0x1
	ddpfFourCC      = 0x4
	ddpfRGB         = 0x40
)

// DecodeDDS reads the top mip level of a block-compressed DDS image. CK3 ships
// patterns as DXT1 and emblems as DXT5; DXT3 is accepted because the format
// allows it and the alpha path is a strict simplification of DXT5's.
func DecodeDDS(data []byte) (*image.NRGBA, error) {
	if len(data) < ddsHeaderBytes {
		return nil, fmt.Errorf("dds: file is %d bytes, shorter than the %d-byte header", len(data), ddsHeaderBytes)
	}
	if binary.LittleEndian.Uint32(data[0:4]) != ddsMagic {
		return nil, fmt.Errorf("dds: missing DDS magic")
	}
	height := int(binary.LittleEndian.Uint32(data[12:16]))
	width := int(binary.LittleEndian.Uint32(data[16:20]))
	fourCC := binary.LittleEndian.Uint32(data[84:88])
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("dds: header declares a %dx%d image", width, height)
	}
	// A coat of arms texture is 512x512 at most; anything far larger is either
	// a corrupt header or a file this code has no business decoding into RAM.
	if width > 8192 || height > 8192 {
		return nil, fmt.Errorf("dds: %dx%d is larger than this decoder accepts", width, height)
	}

	pixelFlags := binary.LittleEndian.Uint32(data[80:84])
	if pixelFlags&ddpfFourCC == 0 {
		if pixelFlags&ddpfRGB == 0 {
			return nil, fmt.Errorf("dds: pixel format flags %#x declare neither a fourCC nor RGB channel masks", pixelFlags)
		}
		return decodeUncompressed(data, width, height)
	}

	var blockBytes int
	switch fourCC {
	case fourCCDXT1:
		blockBytes = 8
	case fourCCDXT3, fourCCDXT5:
		blockBytes = 16
	default:
		return nil, fmt.Errorf("dds: unsupported compression %q; only DXT1, DXT3 and DXT5 are implemented",
			string(data[84:88]))
	}

	blocksWide := (width + 3) / 4
	blocksHigh := (height + 3) / 4
	need := blocksWide * blocksHigh * blockBytes
	if len(data) < ddsHeaderBytes+need {
		return nil, fmt.Errorf("dds: %dx%d needs %d bytes of pixel data, file holds %d",
			width, height, need, len(data)-ddsHeaderBytes)
	}

	out := image.NewNRGBA(image.Rect(0, 0, width, height))
	pixels := data[ddsHeaderBytes:]
	var texels [16]color.NRGBA
	for blockY := 0; blockY < blocksHigh; blockY++ {
		for blockX := 0; blockX < blocksWide; blockX++ {
			block := pixels[(blockY*blocksWide+blockX)*blockBytes:][:blockBytes]
			switch fourCC {
			case fourCCDXT1:
				decodeBC1Block(block, &texels, true)
			case fourCCDXT3:
				decodeBC1Block(block[8:], &texels, false)
				decodeBC2Alpha(block[:8], &texels)
			case fourCCDXT5:
				decodeBC1Block(block[8:], &texels, false)
				decodeBC3Alpha(block[:8], &texels)
			}
			for row := 0; row < 4; row++ {
				y := blockY*4 + row
				if y >= height {
					break
				}
				for col := 0; col < 4; col++ {
					x := blockX*4 + col
					if x >= width {
						break
					}
					out.SetNRGBA(x, y, texels[row*4+col])
				}
			}
		}
	}
	return out, nil
}

// decodeUncompressed reads pixels described by channel bit masks rather than a
// compression scheme. The masks are honoured instead of assuming a byte order,
// because CK3 writes these as BGRA while the same header shape is equally legal
// for RGBA, and a fixed order silently swaps red and blue on half the files.
func decodeUncompressed(data []byte, width, height int) (*image.NRGBA, error) {
	bitCount := int(binary.LittleEndian.Uint32(data[88:92]))
	redMask := binary.LittleEndian.Uint32(data[92:96])
	greenMask := binary.LittleEndian.Uint32(data[96:100])
	blueMask := binary.LittleEndian.Uint32(data[100:104])
	alphaMask := binary.LittleEndian.Uint32(data[104:108])
	if bitCount != 32 && bitCount != 24 {
		return nil, fmt.Errorf("dds: uncompressed %d-bit pixels are not implemented", bitCount)
	}
	bytesPerPixel := bitCount / 8
	need := width * height * bytesPerPixel
	if len(data) < ddsHeaderBytes+need {
		return nil, fmt.Errorf("dds: uncompressed %dx%d needs %d bytes, file holds %d",
			width, height, need, len(data)-ddsHeaderBytes)
	}
	pixels := data[ddsHeaderBytes:]
	out := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			offset := (y*width + x) * bytesPerPixel
			var raw uint32
			if bytesPerPixel == 4 {
				raw = binary.LittleEndian.Uint32(pixels[offset : offset+4])
			} else {
				raw = uint32(pixels[offset]) | uint32(pixels[offset+1])<<8 | uint32(pixels[offset+2])<<16
			}
			pixel := color.NRGBA{
				R: extractChannel(raw, redMask),
				G: extractChannel(raw, greenMask),
				B: extractChannel(raw, blueMask),
				A: 255,
			}
			if alphaMask != 0 {
				pixel.A = extractChannel(raw, alphaMask)
			}
			out.SetNRGBA(x, y, pixel)
		}
	}
	return out, nil
}

// extractChannel pulls one masked field out and rescales it to a full byte.
func extractChannel(raw, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := 0
	for mask&(1<<uint(shift)) == 0 {
		shift++
	}
	value := (raw & mask) >> uint(shift)
	width := mask >> uint(shift)
	if width == 0 {
		return 0
	}
	return uint8(value * 255 / width)
}

// decodeBC1Block expands the 8-byte colour part of a BC1/BC2/BC3 block. In BC1
// the ordering of the two endpoints selects a one-bit alpha mode; inside BC2
// and BC3 the alpha comes from its own block and the four-colour ramp is always
// used, so punchThrough is false there.
func decodeBC1Block(block []byte, out *[16]color.NRGBA, punchThrough bool) {
	color0 := binary.LittleEndian.Uint16(block[0:2])
	color1 := binary.LittleEndian.Uint16(block[2:4])
	indices := binary.LittleEndian.Uint32(block[4:8])

	var palette [4]color.NRGBA
	palette[0] = rgb565(color0)
	palette[1] = rgb565(color1)
	if !punchThrough || color0 > color1 {
		palette[2] = lerpNRGBA(palette[0], palette[1], 1, 3)
		palette[3] = lerpNRGBA(palette[0], palette[1], 2, 3)
	} else {
		palette[2] = lerpNRGBA(palette[0], palette[1], 1, 2)
		palette[3] = color.NRGBA{}
	}
	for texel := 0; texel < 16; texel++ {
		out[texel] = palette[(indices>>(2*uint(texel)))&0x3]
	}
}

// decodeBC2Alpha expands DXT3's four-bit-per-texel explicit alpha.
func decodeBC2Alpha(block []byte, out *[16]color.NRGBA) {
	bits := binary.LittleEndian.Uint64(block[0:8])
	for texel := 0; texel < 16; texel++ {
		nibble := uint8((bits >> (4 * uint(texel))) & 0xf)
		out[texel].A = nibble * 17 // 0..15 spread over 0..255
	}
}

// decodeBC3Alpha expands DXT5's interpolated alpha ramp.
func decodeBC3Alpha(block []byte, out *[16]color.NRGBA) {
	alpha0 := block[0]
	alpha1 := block[1]
	var ramp [8]uint8
	ramp[0] = alpha0
	ramp[1] = alpha1
	if alpha0 > alpha1 {
		for i := 1; i <= 6; i++ {
			ramp[i+1] = uint8((int(alpha0)*(7-i) + int(alpha1)*i) / 7)
		}
	} else {
		for i := 1; i <= 4; i++ {
			ramp[i+1] = uint8((int(alpha0)*(5-i) + int(alpha1)*i) / 5)
		}
		ramp[6] = 0
		ramp[7] = 255
	}
	// The 16 three-bit indices occupy the six bytes after the two endpoints.
	indices := uint64(block[2]) | uint64(block[3])<<8 | uint64(block[4])<<16 |
		uint64(block[5])<<24 | uint64(block[6])<<32 | uint64(block[7])<<40
	for texel := 0; texel < 16; texel++ {
		out[texel].A = ramp[(indices>>(3*uint(texel)))&0x7]
	}
}

func rgb565(value uint16) color.NRGBA {
	r := uint32(value>>11) & 0x1f
	g := uint32(value>>5) & 0x3f
	b := uint32(value) & 0x1f
	// Replicate the high bits into the low ones so full-scale input maps to 255.
	return color.NRGBA{
		R: uint8(r<<3 | r>>2),
		G: uint8(g<<2 | g>>4),
		B: uint8(b<<3 | b>>2),
		A: 255,
	}
}

func lerpNRGBA(from, to color.NRGBA, numerator, denominator int) color.NRGBA {
	mix := func(a, b uint8) uint8 {
		return uint8((int(a)*(denominator-numerator) + int(b)*numerator) / denominator)
	}
	return color.NRGBA{R: mix(from.R, to.R), G: mix(from.G, to.G), B: mix(from.B, to.B), A: 255}
}
