package indexer

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeGUIDDSBC1AndBC3(t *testing.T) {
	bc1Block := make([]byte, 8)
	binary.LittleEndian.PutUint16(bc1Block[0:2], 0xf800)
	binary.LittleEndian.PutUint16(bc1Block[2:4], 0x07e0)
	bc1, format, err := decodeGUIDDS(makeGUIDDSTestData("DXT1", bc1Block, 4, 4))
	if err != nil {
		t.Fatal(err)
	}
	if format != "bc1" || bc1.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("unexpected BC1 top-left pixel: format=%s pixel=%+v", format, bc1.NRGBAAt(0, 0))
	}

	bc3Block := make([]byte, 16)
	bc3Block[0] = 128
	bc3Block[1] = 0
	binary.LittleEndian.PutUint16(bc3Block[8:10], 0xffff)
	binary.LittleEndian.PutUint16(bc3Block[10:12], 0)
	bc3, format, err := decodeGUIDDS(makeGUIDDSTestData("DXT5", bc3Block, 4, 4))
	if err != nil {
		t.Fatal(err)
	}
	if format != "bc3" || bc3.NRGBAAt(2, 2) != (color.NRGBA{R: 255, G: 255, B: 255, A: 128}) {
		t.Fatalf("unexpected BC3 pixel: format=%s pixel=%+v", format, bc3.NRGBAAt(2, 2))
	}
}

func TestDecodeGUIDDSUncompressedBGRA(t *testing.T) {
	data := makeGUIDDSTestData("\x00\x00\x00\x00", []byte{0x11, 0x22, 0x33, 0x44}, 1, 1)
	binary.LittleEndian.PutUint32(data[8:12], 0x0000100f)
	binary.LittleEndian.PutUint32(data[20:24], 4)
	binary.LittleEndian.PutUint32(data[80:84], 0x41)
	binary.LittleEndian.PutUint32(data[88:92], 32)
	binary.LittleEndian.PutUint32(data[92:96], 0x00ff0000)
	binary.LittleEndian.PutUint32(data[96:100], 0x0000ff00)
	binary.LittleEndian.PutUint32(data[100:104], 0x000000ff)
	binary.LittleEndian.PutUint32(data[104:108], 0xff000000)
	decoded, format, err := decodeGUIDDS(data)
	if err != nil {
		t.Fatal(err)
	}
	if format != "rgba32" || decoded.NRGBAAt(0, 0) != (color.NRGBA{R: 0x33, G: 0x22, B: 0x11, A: 0x44}) {
		t.Fatalf("unexpected uncompressed DDS pixel: format=%s pixel=%+v", format, decoded.NRGBAAt(0, 0))
	}

	linearSizeData := makeGUIDDSTestData("\x00\x00\x00\x00", []byte{
		0x11, 0x22, 0x33, 0xff, 0x44, 0x55, 0x66, 0xff,
		0x77, 0x88, 0x99, 0xff, 0xaa, 0xbb, 0xcc, 0xff,
	}, 2, 2)
	binary.LittleEndian.PutUint32(linearSizeData[20:24], 16)
	binary.LittleEndian.PutUint32(linearSizeData[80:84], 0x41)
	binary.LittleEndian.PutUint32(linearSizeData[88:92], 32)
	binary.LittleEndian.PutUint32(linearSizeData[92:96], 0x00ff0000)
	binary.LittleEndian.PutUint32(linearSizeData[96:100], 0x0000ff00)
	binary.LittleEndian.PutUint32(linearSizeData[100:104], 0x000000ff)
	binary.LittleEndian.PutUint32(linearSizeData[104:108], 0xff000000)
	linearDecoded, _, err := decodeGUIDDS(linearSizeData)
	if err != nil {
		t.Fatalf("linear-size uncompressed DDS: %v", err)
	}
	if linearDecoded.NRGBAAt(1, 1) != (color.NRGBA{R: 0xcc, G: 0xbb, B: 0xaa, A: 0xff}) {
		t.Fatalf("unexpected linear-size DDS pixel: %+v", linearDecoded.NRGBAAt(1, 1))
	}
}

func TestEmbedGUIPreviewTexturesProducesBoundedDataPNG(t *testing.T) {
	block := make([]byte, 16)
	block[0] = 255
	binary.LittleEndian.PutUint16(block[8:10], 0x001f)
	binary.LittleEndian.PutUint16(block[10:12], 0)
	filePath := filepath.Join(t.TempDir(), "icon.dds")
	if err := os.WriteFile(filePath, makeGUIDDSTestData("DXT5", block, 4, 4), 0600); err != nil {
		t.Fatal(err)
	}
	preview := GUIPreviewResult{
		Symbol: "texture", SymbolKind: "element", Width: 320, Height: 180,
		Nodes: []GUIPreviewNode{{Index: 0, Parent: -1, Kind: "icon", Bounds: GUIPreviewRect{Width: 64, Height: 64}, TextureRef: &GUITextureRef{
			Path: "gfx/interface/icon.dds", Resolved: true, Kind: "dds", filePath: filePath,
		}}},
		Textures: GUIPreviewTextures{Total: 1, Resolved: 1},
	}
	if err := embedGUIPreviewTextures(context.Background(), &preview); err != nil {
		t.Fatal(err)
	}
	ref := preview.Nodes[0].TextureRef
	if preview.Textures.Embedded != 1 || !ref.Embedded || ref.Width != 4 || ref.Height != 4 || ref.SourceW != 4 || ref.SourceH != 4 || ref.Resized || ref.Format != "bc3" || !strings.HasPrefix(ref.dataURI, "data:image/png;base64,") {
		t.Fatalf("texture was not embedded correctly: stats=%+v ref=%+v", preview.Textures, ref)
	}
	duplicate := *ref
	preview.Nodes = append(preview.Nodes, GUIPreviewNode{
		Index: 1, Parent: -1, Kind: "icon", Mirror: "horizontal", Bounds: GUIPreviewRect{X: 72, Width: 64, Height: 64}, TextureRef: &duplicate,
	})
	sliced := *ref
	preview.Nodes = append(preview.Nodes, GUIPreviewNode{
		Index: 2, Parent: -1, Kind: "icon", Bounds: GUIPreviewRect{X: 144, Width: 96, Height: 64},
		TextureSlice: &GUITextureSlice{SpriteType: "corneredtiled", BorderX: 1, BorderY: 1, TextureDensity: 1},
		TextureRef:   &sliced,
	})
	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(htmlPreview.Document, `:root{--ck3-texture-image-a0:url("data:image/png;base64,`) || !strings.Contains(htmlPreview.Document, `.ck3-texture-a0{--ck3-texture-image:var(--ck3-texture-image-a0);`) || !strings.Contains(htmlPreview.Document, `class="ck3-texture ck3-texture-a0"`) || !strings.Contains(htmlPreview.Document, `class="ck3-texture ck3-texture-a0 ck3-mirror-horizontal"`) || !strings.Contains(htmlPreview.Document, `data-ck3-mirror="horizontal"`) || !strings.Contains(htmlPreview.Document, `data-ck3-texture-size="4x4"`) || !strings.Contains(htmlPreview.Document, `ck3-has-texture`) {
		t.Fatal("embedded texture is missing from inspector HTML")
	}
	for _, expected := range []string{
		`class="ck3-texture ck3-texture-a0 ck3-nine-slice ck3-nine-slice-tiled"`,
		`data-ck3-sprite-border="1x1"`,
		`data-ck3-sprite-type="corneredtiled"`,
		`--ck3-source-slice-x:1.000`,
		`--ck3-slice-y:1.000px`,
	} {
		if !strings.Contains(htmlPreview.Document, expected) {
			t.Fatalf("nine-slice texture HTML is missing %q", expected)
		}
	}
	staticPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeStatic})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staticPreview.Document, `class="ck3-texture ck3-texture-a0 ck3-mirror-horizontal"`) || !strings.Contains(staticPreview.Document, `data-ck3-mirror="horizontal"`) {
		t.Fatal("static HTML did not preserve texture mirroring")
	}
	if strings.Count(htmlPreview.Document, ref.dataURI) != 1 {
		t.Fatal("embedded texture data was not emitted exactly once")
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), filePath) || strings.Contains(htmlPreview.Document, filePath) {
		t.Fatal("indexed filesystem path leaked through texture metadata or HTML")
	}
}

func TestEmbedGUIPreviewTexturesPreservesProgressFillAndEmptyLayers(t *testing.T) {
	block := make([]byte, 16)
	block[0] = 255
	binary.LittleEndian.PutUint16(block[8:10], 0x07e0)
	filePath := filepath.Join(t.TempDir(), "progress.dds")
	if err := os.WriteFile(filePath, makeGUIDDSTestData("DXT5", block, 4, 4), 0600); err != nil {
		t.Fatal(err)
	}
	preview := GUIPreviewResult{
		Width: 200, Height: 80,
		Nodes: []GUIPreviewNode{{
			Index: 0, Parent: -1, Kind: "progressbar", Bounds: GUIPreviewRect{Width: 100, Height: 20},
			Semantics:  &GUISemantics{Min: "0", Max: "100", Value: "35", NoProgressTexture: "gfx/interface/empty.dds"},
			TextureRef: &GUITextureRef{Path: "gfx/interface/fill.dds", Resolved: true, Kind: "dds", filePath: filePath},
			NoProgressTextureRef: &GUITextureRef{
				Path: "gfx/interface/empty.dds", Resolved: true, Kind: "dds", filePath: filePath,
			},
		}},
		Textures: GUIPreviewTextures{Total: 2, Resolved: 2},
	}
	if err := prepareGUIPreviewRuntime(&preview, nil); err != nil {
		t.Fatal(err)
	}
	if err := embedGUIPreviewTextures(context.Background(), &preview); err != nil {
		t.Fatal(err)
	}
	node := preview.Nodes[0]
	if preview.Textures.Embedded != 2 || !node.TextureRef.Embedded || !node.NoProgressTextureRef.Embedded {
		t.Fatalf("progress texture layers were not embedded: stats=%+v node=%+v", preview.Textures, node)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`ck3-no-progress`,
		`ck3-progress-fill`,
		`data-ck3-no-progress-texture-embedded="true"`,
		`--ck3-progress-inverse:65.000%`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Fatalf("progress layer HTML is missing %q", expected)
		}
	}
}

func TestEmbedGUIPreviewTexturesDownsamplesToLargestRenderedBounds(t *testing.T) {
	width, height := 64, 32
	topMip := make([]byte, width*height*4)
	for offset := 0; offset < len(topMip); offset += 4 {
		topMip[offset+0] = uint8(offset / 4)
		topMip[offset+1] = 80
		topMip[offset+2] = 160
		topMip[offset+3] = 255
	}
	data := makeGUIDDSTestData("\x00\x00\x00\x00", topMip, width, height)
	binary.LittleEndian.PutUint32(data[20:24], uint32(width*4))
	binary.LittleEndian.PutUint32(data[80:84], 0x41)
	binary.LittleEndian.PutUint32(data[88:92], 32)
	binary.LittleEndian.PutUint32(data[92:96], 0x00ff0000)
	binary.LittleEndian.PutUint32(data[96:100], 0x0000ff00)
	binary.LittleEndian.PutUint32(data[100:104], 0x000000ff)
	binary.LittleEndian.PutUint32(data[104:108], 0xff000000)
	filePath := filepath.Join(t.TempDir(), "wide.dds")
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	makeRef := func() *GUITextureRef {
		return &GUITextureRef{Path: "gfx/interface/wide.dds", Resolved: true, Kind: "dds", filePath: filePath}
	}
	preview := GUIPreviewResult{
		Nodes: []GUIPreviewNode{
			{Index: 0, Parent: -1, Kind: "icon", Bounds: GUIPreviewRect{Width: 10, Height: 10}, TextureRef: makeRef()},
			{Index: 1, Parent: -1, Kind: "icon", Bounds: GUIPreviewRect{Width: 20, Height: 12}, TextureRef: makeRef()},
		},
		Textures: GUIPreviewTextures{Total: 2, Resolved: 2},
	}
	if err := embedGUIPreviewTextures(context.Background(), &preview); err != nil {
		t.Fatal(err)
	}
	for _, node := range preview.Nodes {
		ref := node.TextureRef
		if !ref.Embedded || !ref.Resized || ref.SourceW != 64 || ref.SourceH != 32 || ref.Width != 20 || ref.Height != 10 {
			t.Fatalf("texture was not deterministically resized to the largest rendered box: %+v", ref)
		}
	}
	if preview.Nodes[0].TextureRef.dataURI != preview.Nodes[1].TextureRef.dataURI {
		t.Fatal("shared texture was resized or encoded more than once")
	}
}

func TestEmbedGUIPreviewTexturePreservesSpriteSheetFrames(t *testing.T) {
	const sourceW, sourceH = 12, 4
	topMip := make([]byte, sourceW*sourceH*4)
	for offset := 0; offset < len(topMip); offset += 4 {
		topMip[offset+0] = uint8(offset / 4)
		topMip[offset+1] = 90
		topMip[offset+2] = 180
		topMip[offset+3] = 255
	}
	data := makeGUIDDSTestData("\x00\x00\x00\x00", topMip, sourceW, sourceH)
	binary.LittleEndian.PutUint32(data[20:24], uint32(sourceW*4))
	binary.LittleEndian.PutUint32(data[80:84], 0x41)
	binary.LittleEndian.PutUint32(data[88:92], 32)
	binary.LittleEndian.PutUint32(data[92:96], 0x00ff0000)
	binary.LittleEndian.PutUint32(data[96:100], 0x0000ff00)
	binary.LittleEndian.PutUint32(data[100:104], 0x000000ff)
	binary.LittleEndian.PutUint32(data[104:108], 0xff000000)
	filePath := filepath.Join(t.TempDir(), "frames.dds")
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	up, over, down := 1, 2, 3
	preview := GUIPreviewResult{
		Width: 100, Height: 50,
		Nodes: []GUIPreviewNode{{
			Index: 0, Parent: -1, Kind: "button", Bounds: GUIPreviewRect{Width: 2, Height: 2},
			TextureFrames: &GUITextureFrames{Width: 4, Height: 4, UpFrame: &up, OverFrame: &over, DownFrame: &down},
			TextureRef:    &GUITextureRef{Path: "gfx/interface/frames.dds", Resolved: true, Kind: "dds", filePath: filePath},
		}},
		Textures: GUIPreviewTextures{Total: 1, Resolved: 1},
	}
	if err := embedGUIPreviewTextures(context.Background(), &preview); err != nil {
		t.Fatal(err)
	}
	ref := preview.Nodes[0].TextureRef
	if !ref.Embedded || !ref.Resized || ref.Width != 6 || ref.Height != 2 || ref.FrameW != 2 || ref.FrameH != 2 || ref.FrameCols != 3 || ref.FrameRows != 1 {
		t.Fatalf("sprite sheet was not resized per frame: %+v", ref)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`class="ck3-texture ck3-texture-a0 ck3-framed"`,
		`data-ck3-frame-size="4x4"`,
		`data-ck3-texture-frame-grid="3x1"`,
		`background-size:300% 100%`,
		`--ck3-frame-up-x:0.000%`,
		`--ck3-frame-over-x:50.000%`,
		`--ck3-frame-down-x:100.000%`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Fatalf("framed texture HTML is missing %q", expected)
		}
	}
}

func TestEmbedGUIPreviewTextureCombinesSpriteFramesAndNineSlice(t *testing.T) {
	const sourceW, sourceH = 12, 4
	topMip := make([]byte, sourceW*sourceH*4)
	for y := 0; y < sourceH; y++ {
		for x := 0; x < sourceW; x++ {
			offset := (y*sourceW + x) * 4
			frame := x / 4
			topMip[offset+0] = uint8(30 + frame*70)
			topMip[offset+1] = uint8(60 + frame*40)
			topMip[offset+2] = uint8(180 - frame*50)
			topMip[offset+3] = 255
		}
	}
	data := makeGUIDDSTestData("\x00\x00\x00\x00", topMip, sourceW, sourceH)
	binary.LittleEndian.PutUint32(data[20:24], uint32(sourceW*4))
	binary.LittleEndian.PutUint32(data[80:84], 0x41)
	binary.LittleEndian.PutUint32(data[88:92], 32)
	binary.LittleEndian.PutUint32(data[92:96], 0x00ff0000)
	binary.LittleEndian.PutUint32(data[96:100], 0x0000ff00)
	binary.LittleEndian.PutUint32(data[100:104], 0x000000ff)
	binary.LittleEndian.PutUint32(data[104:108], 0xff000000)
	filePath := filepath.Join(t.TempDir(), "framed-slice.dds")
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	up, over, down := 1, 2, 3
	preview := GUIPreviewResult{
		Width: 100, Height: 50,
		Nodes: []GUIPreviewNode{{
			Index: 0, Parent: -1, Kind: "button", Bounds: GUIPreviewRect{Width: 2, Height: 2},
			TextureFrames: &GUITextureFrames{Width: 4, Height: 4, UpFrame: &up, OverFrame: &over, DownFrame: &down},
			TextureSlice:  &GUITextureSlice{SpriteType: "corneredtiled", BorderX: 1, BorderY: 1, TextureDensity: 1},
			TextureRef:    &GUITextureRef{Path: "gfx/interface/framed-slice.dds", Resolved: true, Kind: "dds", filePath: filePath},
		}},
		Textures: GUIPreviewTextures{Total: 1, Resolved: 1},
	}
	if err := embedGUIPreviewTextures(context.Background(), &preview); err != nil {
		t.Fatal(err)
	}
	ref := preview.Nodes[0].TextureRef
	if !ref.Embedded || !ref.Resized || ref.Width != 2 || ref.Height != 2 || ref.FrameW != 2 || ref.FrameH != 2 || ref.FrameCols != 3 || ref.FrameRows != 1 || ref.FrameImages != 3 || len(ref.frameDataURIs) != 3 {
		t.Fatalf("framed nine-slice texture was not split per frame: %+v", ref)
	}
	if ref.dataURI != ref.frameDataURIs[0] || ref.frameDataURIs[0] == ref.frameDataURIs[1] || ref.frameDataURIs[1] == ref.frameDataURIs[2] {
		t.Fatal("framed nine-slice texture did not preserve distinct deterministic frame images")
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`class="ck3-texture ck3-texture-a0 ck3-framed-images ck3-nine-slice ck3-nine-slice-tiled"`,
		`data-ck3-texture-frame-images="3"`,
		`--ck3-frame-up-image:var(--ck3-texture-image-a0)`,
		`--ck3-frame-over-image:var(--ck3-texture-image-a1)`,
		`--ck3-frame-down-image:var(--ck3-texture-image-a2)`,
		`--ck3-source-slice-x:0.500`,
		`border-image-source:var(--ck3-active-frame-image,var(--ck3-texture-image))`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Fatalf("framed nine-slice HTML is missing %q", expected)
		}
	}
	for _, dataURI := range ref.frameDataURIs {
		if strings.Count(result.Document, dataURI) != 1 {
			t.Fatal("a framed nine-slice data URI was not emitted exactly once")
		}
	}
	for _, warning := range preview.Warnings {
		if strings.Contains(warning, "does not yet combine sprite-sheet selection") {
			t.Fatalf("framed nine-slice support still emitted the obsolete limitation: %q", warning)
		}
	}
}

func TestDecodeGUIDDSRejectsUnsupportedOrTruncatedInput(t *testing.T) {
	if _, _, err := decodeGUIDDS([]byte("DDS ")); err == nil {
		t.Fatal("truncated DDS was accepted")
	}
	unsupported := makeGUIDDSTestData("ATI2", make([]byte, 16), 4, 4)
	if _, _, err := decodeGUIDDS(unsupported); err == nil {
		t.Fatal("unsupported DDS encoding was accepted")
	}
	truncated := makeGUIDDSTestData("DXT5", make([]byte, 8), 4, 4)
	if _, _, err := decodeGUIDDS(truncated); err == nil {
		t.Fatal("truncated BC3 block was accepted")
	}
}

func BenchmarkEmbedGUIPreviewTexture(b *testing.B) {
	benchmarkEmbedGUIPreviewTexture(b, 288, 140)
}

func BenchmarkEmbedGUIPreviewTextureDownsampled(b *testing.B) {
	benchmarkEmbedGUIPreviewTexture(b, 144, 70)
}

func BenchmarkEmbedGUIPreviewTextureFramedNineSlice(b *testing.B) {
	const sourceW, sourceH = 288, 140
	topMip := make([]byte, sourceW*sourceH*4)
	for offset := 0; offset < len(topMip); offset += 4 {
		topMip[offset+0] = uint8(offset / 4)
		topMip[offset+1] = uint8(offset / 16)
		topMip[offset+2] = 96
		topMip[offset+3] = 255
	}
	data := makeGUIDDSTestData("\x00\x00\x00\x00", topMip, sourceW, sourceH)
	binary.LittleEndian.PutUint32(data[20:24], uint32(sourceW*4))
	binary.LittleEndian.PutUint32(data[80:84], 0x41)
	binary.LittleEndian.PutUint32(data[88:92], 32)
	binary.LittleEndian.PutUint32(data[92:96], 0x00ff0000)
	binary.LittleEndian.PutUint32(data[96:100], 0x0000ff00)
	binary.LittleEndian.PutUint32(data[100:104], 0x000000ff)
	binary.LittleEndian.PutUint32(data[104:108], 0xff000000)
	filePath := filepath.Join(b.TempDir(), "framed-panel.dds")
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		b.Fatal(err)
	}
	up, over, down := 1, 2, 3
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		preview := GUIPreviewResult{
			Nodes: []GUIPreviewNode{{
				Bounds:        GUIPreviewRect{Width: 48, Height: 70},
				TextureFrames: &GUITextureFrames{Width: 96, Height: 140, UpFrame: &up, OverFrame: &over, DownFrame: &down},
				TextureSlice:  &GUITextureSlice{SpriteType: "corneredtiled", BorderX: 8, BorderY: 8, TextureDensity: 1},
				TextureRef:    &GUITextureRef{Path: "gfx/interface/framed-panel.dds", Resolved: true, Kind: "dds", filePath: filePath},
			}},
			Textures: GUIPreviewTextures{Total: 1, Resolved: 1},
		}
		if err := embedGUIPreviewTextures(context.Background(), &preview); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkEmbedGUIPreviewTexture(b *testing.B, targetW, targetH int) {
	const sourceW, sourceH = 288, 140
	topMip := make([]byte, sourceW*sourceH*4)
	for offset := 0; offset < len(topMip); offset += 4 {
		topMip[offset+0] = uint8(offset / 4)
		topMip[offset+1] = uint8(offset / 16)
		topMip[offset+2] = 96
		topMip[offset+3] = 255
	}
	data := makeGUIDDSTestData("\x00\x00\x00\x00", topMip, sourceW, sourceH)
	binary.LittleEndian.PutUint32(data[20:24], uint32(sourceW*4))
	binary.LittleEndian.PutUint32(data[80:84], 0x41)
	binary.LittleEndian.PutUint32(data[88:92], 32)
	binary.LittleEndian.PutUint32(data[92:96], 0x00ff0000)
	binary.LittleEndian.PutUint32(data[96:100], 0x0000ff00)
	binary.LittleEndian.PutUint32(data[100:104], 0x000000ff)
	binary.LittleEndian.PutUint32(data[104:108], 0xff000000)
	filePath := filepath.Join(b.TempDir(), "panel.dds")
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		preview := GUIPreviewResult{
			Nodes:    []GUIPreviewNode{{Bounds: GUIPreviewRect{Width: targetW, Height: targetH}, TextureRef: &GUITextureRef{Path: "gfx/interface/panel.dds", Resolved: true, Kind: "dds", filePath: filePath}}},
			Textures: GUIPreviewTextures{Total: 1, Resolved: 1},
		}
		if err := embedGUIPreviewTextures(context.Background(), &preview); err != nil {
			b.Fatal(err)
		}
	}
}

func makeGUIDDSTestData(fourCC string, topMip []byte, width, height int) []byte {
	result := make([]byte, 128+len(topMip))
	copy(result[0:4], "DDS ")
	binary.LittleEndian.PutUint32(result[4:8], 124)
	binary.LittleEndian.PutUint32(result[8:12], 0x00081007)
	binary.LittleEndian.PutUint32(result[12:16], uint32(height))
	binary.LittleEndian.PutUint32(result[16:20], uint32(width))
	binary.LittleEndian.PutUint32(result[20:24], uint32(len(topMip)))
	binary.LittleEndian.PutUint32(result[76:80], 32)
	binary.LittleEndian.PutUint32(result[80:84], 4)
	copy(result[84:88], fourCC)
	binary.LittleEndian.PutUint32(result[108:112], 0x1000)
	copy(result[128:], topMip)
	return result
}
