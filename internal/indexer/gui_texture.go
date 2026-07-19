package indexer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/bits"
	"os"
	"strings"

	xdraw "golang.org/x/image/draw"
)

const (
	guiTextureMaxSourceBytes = 16 << 20
	guiTextureMaxPixels      = 4 << 20
	guiTextureMaxDimension   = 4096
	guiTextureMaxDataURI     = 384 << 10
	guiTextureTotalDataURI   = 640 << 10
)

type guiEmbeddedTexture struct {
	dataURI       string
	width         int
	height        int
	sourceW       int
	sourceH       int
	resized       bool
	frameW        int
	frameH        int
	columns       int
	rows          int
	frameDataURIs []string
	format        string
	err           error
}

type guiTextureEmbedKey struct {
	filePath string
	frameW   int
	frameH   int
	slice    bool
}

type guiTextureEmbedTarget struct {
	width  int
	height int
}

// embedGUIPreviewTextures converts bounded, already-indexed literal resources
// into deterministic data: PNGs. It never accepts a client path and keeps the
// indexed filesystem path in unexported fields only.
func embedGUIPreviewTextures(ctx context.Context, preview *GUIPreviewResult) error {
	if preview == nil {
		return nil
	}
	targets := guiTextureEmbedTargets(preview.Nodes)
	cache := map[guiTextureEmbedKey]guiEmbeddedTexture{}
	remaining := guiTextureTotalDataURI
	unsupported := false
	for index := range preview.Nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, ref := range guiNodeTextureRefs(&preview.Nodes[index]) {
			if ref == nil || !ref.Resolved || ref.Dynamic || strings.TrimSpace(ref.filePath) == "" {
				continue
			}
			key := guiTextureKeyForRef(preview.Nodes[index], ref)
			result, ok := cache[key]
			if !ok {
				target := targets[key]
				result = loadGUIEmbeddedTexture(ref.filePath, ref.Kind, target.width, target.height, key.frameW, key.frameH, key.slice)
				embeddedBytes := guiEmbeddedTextureBytes(result)
				if result.err == nil && (embeddedBytes > guiTextureMaxDataURI || embeddedBytes > remaining) {
					result = guiEmbeddedTexture{err: errors.New("decoded texture exceeds HTML embedding budget")}
				}
				if result.err == nil {
					remaining -= embeddedBytes
				}
				cache[key] = result
			}
			if result.err != nil {
				preview.Textures.Unsupported++
				unsupported = true
				continue
			}
			ref.Embedded = true
			ref.Width = result.width
			ref.Height = result.height
			ref.SourceW = result.sourceW
			ref.SourceH = result.sourceH
			ref.Resized = result.resized
			ref.FrameW = result.frameW
			ref.FrameH = result.frameH
			ref.FrameCols = result.columns
			ref.FrameRows = result.rows
			ref.FrameImages = len(result.frameDataURIs)
			ref.Format = result.format
			ref.dataURI = result.dataURI
			ref.frameDataURIs = append([]string(nil), result.frameDataURIs...)
			preview.Textures.Embedded++
		}
	}
	if unsupported {
		preview.Warnings = append(preview.Warnings, "Some resolved GUI textures use unsupported encodings or exceed the bounded HTML embedding budget")
	}
	return nil
}

func guiTextureEmbedTargets(nodes []GUIPreviewNode) map[guiTextureEmbedKey]guiTextureEmbedTarget {
	targets := make(map[guiTextureEmbedKey]guiTextureEmbedTarget)
	for index := range nodes {
		for _, ref := range guiNodeTextureRefs(&nodes[index]) {
			if ref == nil || !ref.Resolved || ref.Dynamic || strings.TrimSpace(ref.filePath) == "" {
				continue
			}
			key := guiTextureKeyForRef(nodes[index], ref)
			target := targets[key]
			target.width = maxInt(target.width, nodes[index].Bounds.Width)
			target.height = maxInt(target.height, nodes[index].Bounds.Height)
			targets[key] = target
		}
	}
	return targets
}

func guiTextureKey(node GUIPreviewNode) guiTextureEmbedKey {
	return guiTextureKeyForRef(node, node.TextureRef)
}

func guiTextureKeyForRef(node GUIPreviewNode, ref *GUITextureRef) guiTextureEmbedKey {
	key := guiTextureEmbedKey{}
	if ref != nil {
		key.filePath = ref.filePath
	}
	if node.TextureFrames != nil {
		key.frameW = node.TextureFrames.Width
		key.frameH = node.TextureFrames.Height
	}
	key.slice = node.TextureSlice != nil
	return key
}

func guiNodeTextureRefs(node *GUIPreviewNode) []*GUITextureRef {
	if node == nil {
		return nil
	}
	return []*GUITextureRef{node.TextureRef, node.NoProgressTextureRef}
}

func guiEmbeddedTextureBytes(texture guiEmbeddedTexture) int {
	if len(texture.frameDataURIs) == 0 {
		return len(texture.dataURI)
	}
	total := 0
	for _, dataURI := range texture.frameDataURIs {
		total += len(dataURI)
	}
	return total
}

func loadGUIEmbeddedTexture(filePath, kind string, targetW, targetH, requestedFrameW, requestedFrameH int, sliceFrames ...bool) guiEmbeddedTexture {
	info, err := os.Stat(filePath)
	if err != nil {
		return guiEmbeddedTexture{err: err}
	}
	if info.Size() <= 0 || info.Size() > guiTextureMaxSourceBytes {
		return guiEmbeddedTexture{err: fmt.Errorf("texture source size %d is outside the bounded range", info.Size())}
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return guiEmbeddedTexture{err: err}
	}
	decoded, format, err := decodeGUITexture(data, kind)
	if err != nil {
		return guiEmbeddedTexture{err: err}
	}
	bounds := decoded.Bounds()
	sourceW, sourceH := bounds.Dx(), bounds.Dy()
	if sourceW <= 0 || sourceH <= 0 || sourceW > guiTextureMaxDimension || sourceH > guiTextureMaxDimension || sourceW*sourceH > guiTextureMaxPixels {
		return guiEmbeddedTexture{err: fmt.Errorf("decoded texture dimensions %dx%d exceed the bounded range", sourceW, sourceH)}
	}
	frameW, frameH := sourceW, sourceH
	columns, rows := 1, 1
	if requestedFrameW > 0 && requestedFrameH > 0 && sourceW%requestedFrameW == 0 && sourceH%requestedFrameH == 0 {
		frameW, frameH = requestedFrameW, requestedFrameH
		columns, rows = sourceW/frameW, sourceH/frameH
	}
	frameSlice := len(sliceFrames) > 0 && sliceFrames[0] && columns*rows > 1
	embedded := decoded
	embeddedFrameW, embeddedFrameH := guiTextureEmbeddedDimensions(frameW, frameH, targetW, targetH)
	targetSourceW := maxInt(1, embeddedFrameW*columns)
	targetSourceH := maxInt(1, embeddedFrameH*rows)
	resized := targetSourceW != sourceW || targetSourceH != sourceH
	if resized {
		scaled := image.NewNRGBA(image.Rect(0, 0, targetSourceW, targetSourceH))
		xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), decoded, decoded.Bounds(), xdraw.Over, nil)
		embedded = scaled
	}
	width, height := embedded.Bounds().Dx(), embedded.Bounds().Dy()
	embeddedFrameW = maxInt(1, width/columns)
	embeddedFrameH = maxInt(1, height/rows)
	result := guiEmbeddedTexture{
		width:   width,
		height:  height,
		sourceW: sourceW,
		sourceH: sourceH,
		resized: resized,
		frameW:  embeddedFrameW,
		frameH:  embeddedFrameH,
		columns: columns,
		rows:    rows,
		format:  format,
	}
	if frameSlice {
		result.frameDataURIs = make([]string, 0, columns*rows)
		for row := 0; row < rows; row++ {
			for column := 0; column < columns; column++ {
				bounds := image.Rect(
					column*embeddedFrameW,
					row*embeddedFrameH,
					(column+1)*embeddedFrameW,
					(row+1)*embeddedFrameH,
				)
				dataURI, err := encodeGUITextureDataURI(embedded.SubImage(bounds))
				if err != nil {
					return guiEmbeddedTexture{err: err}
				}
				result.frameDataURIs = append(result.frameDataURIs, dataURI)
			}
		}
		result.dataURI = result.frameDataURIs[0]
		result.width = embeddedFrameW
		result.height = embeddedFrameH
		return result
	}
	dataURI, err := encodeGUITextureDataURI(embedded)
	if err != nil {
		return guiEmbeddedTexture{err: err}
	}
	result.dataURI = dataURI
	return result
}

func encodeGUITextureDataURI(source image.Image) (string, error) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()), nil
}

func guiTextureEmbeddedDimensions(sourceW, sourceH, targetW, targetH int) (int, int) {
	if sourceW <= 0 || sourceH <= 0 || targetW <= 0 || targetH <= 0 {
		return sourceW, sourceH
	}
	scale := math.Min(float64(targetW)/float64(sourceW), float64(targetH)/float64(sourceH))
	if scale >= 1 {
		return sourceW, sourceH
	}
	return maxInt(1, int(math.Round(float64(sourceW)*scale))), maxInt(1, int(math.Round(float64(sourceH)*scale)))
}

func decodeGUITexture(data []byte, kind string) (*image.NRGBA, string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "dds":
		return decodeGUIDDS(data)
	case "png":
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", err
		}
		return toNRGBA(decoded), "png", nil
	default:
		return nil, "", fmt.Errorf("unsupported GUI texture kind %q", kind)
	}
}

func toNRGBA(source image.Image) *image.NRGBA {
	bounds := source.Bounds()
	result := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			result.Set(x, y, source.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return result
}

func decodeGUIDDS(data []byte) (*image.NRGBA, string, error) {
	if len(data) < 128 || string(data[:4]) != "DDS " || binary.LittleEndian.Uint32(data[4:8]) != 124 {
		return nil, "", errors.New("invalid DDS header")
	}
	width := int(binary.LittleEndian.Uint32(data[16:20]))
	height := int(binary.LittleEndian.Uint32(data[12:16]))
	if width <= 0 || height <= 0 || width > guiTextureMaxDimension || height > guiTextureMaxDimension || width*height > guiTextureMaxPixels {
		return nil, "", fmt.Errorf("DDS dimensions %dx%d exceed the bounded range", width, height)
	}
	fourCC := string(data[84:88])
	dataOffset := 128
	format := ""
	if fourCC == "\x00\x00\x00\x00" {
		return decodeGUIUncompressedDDS(data, width, height, dataOffset)
	}
	switch fourCC {
	case "DXT1":
		format = "bc1"
	case "DXT3":
		format = "bc2"
	case "DXT5":
		format = "bc3"
	case "DX10":
		if len(data) < 148 {
			return nil, "", errors.New("truncated DDS DX10 header")
		}
		dataOffset = 148
		switch binary.LittleEndian.Uint32(data[128:132]) {
		case 71, 72:
			format = "bc1"
		case 74, 75:
			format = "bc2"
		case 77, 78:
			format = "bc3"
		default:
			return nil, "", fmt.Errorf("unsupported DDS DXGI format %d", binary.LittleEndian.Uint32(data[128:132]))
		}
	default:
		return nil, "", fmt.Errorf("unsupported DDS fourCC %q", fourCC)
	}
	blockBytes := 16
	if format == "bc1" {
		blockBytes = 8
	}
	blocksWide := (width + 3) / 4
	blocksHigh := (height + 3) / 4
	required := dataOffset + blocksWide*blocksHigh*blockBytes
	if required > len(data) {
		return nil, "", fmt.Errorf("truncated DDS top mip: need %d bytes, have %d", required, len(data))
	}
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	offset := dataOffset
	for blockY := 0; blockY < blocksHigh; blockY++ {
		for blockX := 0; blockX < blocksWide; blockX++ {
			decodeGUIBCBlock(result, blockX, blockY, data[offset:offset+blockBytes], format)
			offset += blockBytes
		}
	}
	return result, format, nil
}

func decodeGUIUncompressedDDS(data []byte, width, height, dataOffset int) (*image.NRGBA, string, error) {
	const (
		ddsHeaderPitch      = uint32(0x00000008)
		ddsHeaderLinearSize = uint32(0x00080000)
	)
	headerFlags := binary.LittleEndian.Uint32(data[8:12])
	pixelFlags := binary.LittleEndian.Uint32(data[80:84])
	bitCount := binary.LittleEndian.Uint32(data[88:92])
	if pixelFlags&0x40 == 0 || bitCount != 32 {
		return nil, "", fmt.Errorf("unsupported uncompressed DDS pixel format flags=0x%x bits=%d", pixelFlags, bitCount)
	}
	redMask := binary.LittleEndian.Uint32(data[92:96])
	greenMask := binary.LittleEndian.Uint32(data[96:100])
	blueMask := binary.LittleEndian.Uint32(data[100:104])
	alphaMask := binary.LittleEndian.Uint32(data[104:108])
	if redMask == 0 || greenMask == 0 || blueMask == 0 {
		return nil, "", errors.New("uncompressed DDS is missing RGB channel masks")
	}
	pitchOrLinearSize := int(binary.LittleEndian.Uint32(data[20:24]))
	pitch := width * 4
	switch {
	case headerFlags&ddsHeaderPitch != 0 && pitchOrLinearSize > 0:
		pitch = pitchOrLinearSize
	case headerFlags&ddsHeaderLinearSize != 0 && pitchOrLinearSize > 0 && height > 0 && pitchOrLinearSize%height == 0:
		candidate := pitchOrLinearSize / height
		if candidate >= width*4 {
			pitch = candidate
		}
	}
	if pitch < width*4 || dataOffset+pitch*height > len(data) {
		return nil, "", errors.New("truncated uncompressed DDS top mip")
	}
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		row := data[dataOffset+y*pitch : dataOffset+(y+1)*pitch]
		for x := 0; x < width; x++ {
			value := binary.LittleEndian.Uint32(row[x*4 : x*4+4])
			alpha := uint8(255)
			if alphaMask != 0 {
				alpha = decodeGUIMaskedChannel(value, alphaMask)
			}
			result.SetNRGBA(x, y, color.NRGBA{
				R: decodeGUIMaskedChannel(value, redMask),
				G: decodeGUIMaskedChannel(value, greenMask),
				B: decodeGUIMaskedChannel(value, blueMask),
				A: alpha,
			})
		}
	}
	return result, "rgba32", nil
}

func decodeGUIMaskedChannel(value, mask uint32) uint8 {
	shift := bits.TrailingZeros32(mask)
	maximum := mask >> uint(shift)
	if maximum == 0 {
		return 0
	}
	channel := (value & mask) >> uint(shift)
	return uint8((uint64(channel)*255 + uint64(maximum)/2) / uint64(maximum))
}

func decodeGUIBCBlock(destination *image.NRGBA, blockX, blockY int, block []byte, format string) {
	alpha := [16]uint8{}
	for index := range alpha {
		alpha[index] = 255
	}
	colorOffset := 0
	forceFourColor := false
	switch format {
	case "bc2":
		forceFourColor = true
		bits := binary.LittleEndian.Uint64(block[:8])
		for index := range alpha {
			alpha[index] = uint8(((bits >> (4 * index)) & 0x0f) * 17)
		}
		colorOffset = 8
	case "bc3":
		forceFourColor = true
		alpha = decodeGUIBC3Alpha(block[:8])
		colorOffset = 8
	}
	colorBlock := block[colorOffset : colorOffset+8]
	color0 := decodeGUIRGB565(binary.LittleEndian.Uint16(colorBlock[0:2]))
	color1 := decodeGUIRGB565(binary.LittleEndian.Uint16(colorBlock[2:4]))
	colors := [4]color.NRGBA{color0, color1}
	c0 := binary.LittleEndian.Uint16(colorBlock[0:2])
	c1 := binary.LittleEndian.Uint16(colorBlock[2:4])
	if forceFourColor || c0 > c1 {
		colors[2] = interpolateGUIColor(color0, color1, 2, 1, 3)
		colors[3] = interpolateGUIColor(color0, color1, 1, 2, 3)
	} else {
		colors[2] = interpolateGUIColor(color0, color1, 1, 1, 2)
		colors[3] = color.NRGBA{}
	}
	indices := binary.LittleEndian.Uint32(colorBlock[4:8])
	bounds := destination.Bounds()
	for pixel := 0; pixel < 16; pixel++ {
		x := blockX*4 + pixel%4
		y := blockY*4 + pixel/4
		if x >= bounds.Dx() || y >= bounds.Dy() {
			continue
		}
		value := colors[(indices>>uint(2*pixel))&3]
		if format != "bc1" || value.A != 0 {
			value.A = alpha[pixel]
		}
		destination.SetNRGBA(x, y, value)
	}
}

func decodeGUIBC3Alpha(block []byte) [16]uint8 {
	values := [8]uint8{block[0], block[1]}
	if values[0] > values[1] {
		for index := 1; index <= 6; index++ {
			values[index+1] = uint8(((7-index)*int(values[0]) + index*int(values[1])) / 7)
		}
	} else {
		for index := 1; index <= 4; index++ {
			values[index+1] = uint8(((5-index)*int(values[0]) + index*int(values[1])) / 5)
		}
		values[6] = 0
		values[7] = 255
	}
	var indices uint64
	for index := 0; index < 6; index++ {
		indices |= uint64(block[2+index]) << uint(8*index)
	}
	var result [16]uint8
	for index := range result {
		result[index] = values[(indices>>uint(3*index))&7]
	}
	return result
}

func decodeGUIRGB565(value uint16) color.NRGBA {
	red := uint8((value >> 11) & 0x1f)
	green := uint8((value >> 5) & 0x3f)
	blue := uint8(value & 0x1f)
	return color.NRGBA{R: red<<3 | red>>2, G: green<<2 | green>>4, B: blue<<3 | blue>>2, A: 255}
}

func interpolateGUIColor(first, second color.NRGBA, firstWeight, secondWeight, divisor int) color.NRGBA {
	return color.NRGBA{
		R: uint8((firstWeight*int(first.R) + secondWeight*int(second.R)) / divisor),
		G: uint8((firstWeight*int(first.G) + secondWeight*int(second.G)) / divisor),
		B: uint8((firstWeight*int(first.B) + secondWeight*int(second.B)) / divisor),
		A: 255,
	}
}
