package indexer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func terrainEditWorkspace(t *testing.T, width, height, bitDepth int) (Config, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	mapDir := filepath.Join(project, "map_data")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeText := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(mapDir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeText("definition.csv", "province;red;green;blue\n1;255;0;0\n2;0;0;255\n3;0;255;0\n")
	writeText("default.map", "sea_zones = { 2 }\nlakes = { 3 }\n")

	provinces := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value := color.RGBA{R: 255, A: 255}
			if x >= width*2/3 {
				value = color.RGBA{B: 255, A: 255}
			} else if x >= width/2 && y >= height*3/4 {
				value = color.RGBA{G: 255, A: 255}
			}
			provinces.SetRGBA(x, y, value)
		}
	}
	writePNG(t, filepath.Join(mapDir, "provinces.png"), provinces)

	switch bitDepth {
	case 8:
		heightmap := image.NewGray(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				heightmap.SetGray(x, y, color.Gray{Y: uint8(40 + y/2)})
			}
		}
		writePNG(t, filepath.Join(mapDir, "heightmap.png"), heightmap)
	case 16:
		heightmap := image.NewGray16(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				heightmap.SetGray16(x, y, color.Gray16{Y: uint16(9000 + x*37 + y*53)})
			}
		}
		writePNG(t, filepath.Join(mapDir, "heightmap.png"), heightmap)
	default:
		t.Fatalf("unsupported fixture bit depth %d", bitDepth)
	}

	rivers := image.NewPaletted(image.Rect(0, 0, width, height), ck3RiverPalette())
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			index := uint8(riverLand)
			if x >= width*2/3 {
				index = riverSea
			}
			rivers.SetColorIndex(x, y, index)
		}
	}
	if width > 4 && height > 4 {
		rivers.SetColorIndex(2, 2, 5)
		rivers.SetColorIndex(2, 3, 5)
	}
	writePNG(t, filepath.Join(mapDir, "rivers.png"), rivers)

	cfg := Config{
		ArtifactRoot: filepath.Join(root, "artifacts"),
		Sources:      []Source{{Name: "project", Path: project, Rank: 1}},
	}
	return cfg, filepath.Join(root, "out")
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func composeRangeSpec() MapTerrainEditSpec {
	return MapTerrainEditSpec{
		Operation: MapTerrainOpCompose,
		Layers: []MapTerrainLayer{{
			ID: "main-range", Kind: TerrainRange, Domain: TerrainDomainLand,
			Geometry: MapTerrainGeometry{Type: TerrainGeometryPolyline, Coordinates: []MapTerrainPoint{
				{X: 12, Y: 24}, {X: 44, Y: 28},
			}},
			WidthPx: 12, FeatherPx: 3, Strength: 0.18, Roughness: 0.65, Detail: 1.2, Seed: 17,
		}},
		Region: &MapTerrainRegion{X: 8, Y: 8, Width: 48, Height: 48},
	}
}

func TestTerrainComposePreservesGray16AndEveryPixelOutsideRegion(t *testing.T) {
	cfg, out := terrainEditWorkspace(t, 64, 64, 16)
	sourcePath := filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png")
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceImage, err := decodeMapImage(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceImage.(*image.Gray16)

	result, err := EditMapTerrain(cfg, composeRangeSpec(), out)
	if err != nil {
		t.Fatal(err)
	}
	if result.BitDepth != 16 || result.HydrologyStatus != HydrologyStale || !result.RequiresCK3Repack {
		t.Fatalf("wrong compose metadata: %+v", result)
	}
	if len(result.Layers) != 1 || result.Layers[0].ChangedPixels == 0 {
		t.Fatalf("compose returned no layer changes: %+v", result.Layers)
	}
	heightPath := outputPathForKind(t, result, "heightmap")
	decoded, err := decodeMapImage(heightPath)
	if err != nil {
		t.Fatal(err)
	}
	written, ok := decoded.(*image.Gray16)
	if !ok {
		t.Fatalf("heightmap decoded as %T, want Gray16", decoded)
	}
	region := composeRangeSpec().Region.rectangle()
	changedInside := 0
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			before := source.Gray16At(x, y).Y
			after := written.Gray16At(x, y).Y
			if !image.Pt(x, y).In(region) && before != after {
				t.Fatalf("outside pixel %d,%d changed from %d to %d", x, y, before, after)
			}
			if image.Pt(x, y).In(region) && before != after {
				changedInside++
			}
		}
	}
	if changedInside == 0 {
		t.Fatal("no in-region Gray16 pixel changed")
	}
	afterSource, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sourceBytes, afterSource) {
		t.Fatal("source heightmap was modified")
	}
}

func TestGray8AndGray16NoOpRoundTripKeepsEverySample(t *testing.T) {
	for _, bitDepth := range []int{8, 16} {
		t.Run(fmt.Sprintf("gray%d", bitDepth), func(t *testing.T) {
			cfg, out := terrainEditWorkspace(t, 32, 32, bitDepth)
			sourcePath := filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png")
			source, err := decodeMapImage(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := EditMapTerrain(cfg, MapTerrainEditSpec{
				Operation: MapTerrainOpCompose,
				Layers: []MapTerrainLayer{{
					ID: "no-op", Kind: TerrainHills, Domain: TerrainDomainAny,
					Geometry: MapTerrainGeometry{Type: TerrainGeometryPoint, Coordinates: []MapTerrainPoint{{X: 16, Y: 16}}},
					WidthPx:  12, Strength: 0,
				}},
			}, out)
			if err != nil {
				t.Fatal(err)
			}
			written, err := decodeMapImage(outputPathForKind(t, result, "heightmap"))
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < 32; y++ {
				for x := 0; x < 32; x++ {
					sr, sg, sb, _ := source.At(x, y).RGBA()
					wr, wg, wb, _ := written.At(x, y).RGBA()
					if sr != wr || sg != wg || sb != wb {
						t.Fatalf("sample %d,%d changed in no-op Gray%d round trip", x, y, bitDepth)
					}
				}
			}
		})
	}
}

func TestTerrainComposeRejectsColourHeightmapInsteadOfFlatteningIt(t *testing.T) {
	cfg, out := terrainEditWorkspace(t, 64, 64, 8)
	rgba := image.NewRGBA(image.Rect(0, 0, 64, 64))
	writePNG(t, filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png"), rgba)
	if _, err := EditMapTerrain(cfg, composeRangeSpec(), out); err == nil || !strings.Contains(err.Error(), "Gray8 or Gray16") {
		t.Fatalf("colour heightmap was not rejected clearly: %v", err)
	}
}

func TestTerrainLayerOrderDomainAndGeometryValidation(t *testing.T) {
	cfg, root := terrainEditWorkspace(t, 64, 64, 8)
	targetLow, targetHigh := 0.25, 0.55
	layer := func(id string, target float64) MapTerrainLayer {
		return MapTerrainLayer{
			ID: id, Kind: TerrainPlain, Domain: TerrainDomainLand,
			Geometry: MapTerrainGeometry{Type: TerrainGeometryPolygon, Coordinates: []MapTerrainPoint{
				{X: 8, Y: 8}, {X: 48, Y: 8}, {X: 48, Y: 48}, {X: 8, Y: 48},
			}},
			WidthPx: 4, Strength: 1, TargetHeight: &target,
		}
	}
	first, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpCompose, Layers: []MapTerrainLayer{layer("low", targetLow), layer("high", targetHigh)},
	}, filepath.Join(root, "ordered"))
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpCompose, Layers: []MapTerrainLayer{layer("high", targetHigh), layer("low", targetLow)},
	}, filepath.Join(root, "reversed"))
	if err != nil {
		t.Fatal(err)
	}
	firstImage, _ := decodeMapImage(outputPathForKind(t, first, "heightmap"))
	reversedImage, _ := decodeMapImage(outputPathForKind(t, reversed, "heightmap"))
	if firstImage.(*image.Gray).GrayAt(20, 20).Y <= reversedImage.(*image.Gray).GrayAt(20, 20).Y {
		t.Fatal("later layer did not build on and override the earlier layer")
	}
	// Land-domain layers cannot alter the ocean province on the right.
	source, _ := decodeMapImage(filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png"))
	if firstImage.(*image.Gray).GrayAt(55, 20).Y != source.(*image.Gray).GrayAt(55, 20).Y {
		t.Fatal("land-domain layer changed an ocean pixel")
	}

	bowTie := layer("bad-polygon", targetLow)
	bowTie.Geometry.Coordinates = []MapTerrainPoint{{X: 8, Y: 8}, {X: 40, Y: 40}, {X: 8, Y: 40}, {X: 40, Y: 8}}
	if _, err := EditMapTerrain(cfg, MapTerrainEditSpec{Operation: MapTerrainOpCompose, Layers: []MapTerrainLayer{bowTie}}, filepath.Join(root, "bad")); err == nil {
		t.Fatal("self-intersecting polygon was accepted")
	}
	island := MapTerrainLayer{
		ID: "bad-island", Kind: TerrainIsland, Domain: TerrainDomainOcean,
		Geometry: MapTerrainGeometry{Type: TerrainGeometryPoint, Coordinates: []MapTerrainPoint{{X: 55, Y: 20}}},
		WidthPx:  8, Strength: 0.2,
	}
	if _, err := EditMapTerrain(cfg, MapTerrainEditSpec{Operation: MapTerrainOpCompose, Layers: []MapTerrainLayer{island}}, filepath.Join(root, "island")); err == nil {
		t.Fatal("island was allowed to create land in an ocean province")
	}
}

func TestEveryTerrainKindIsDeterministicAndMovesInItsDeclaredDirection(t *testing.T) {
	cfg, root := terrainEditWorkspace(t, 64, 64, 8)
	target := 0.3
	kinds := []string{
		TerrainPlain, TerrainHills, TerrainRange, TerrainPlateau, TerrainBasin,
		TerrainValley, TerrainCanyon, TerrainVolcano, TerrainContinentalShelf,
		TerrainContinentalSlope, TerrainAbyssalPlain, TerrainTrench,
		TerrainOceanRidge, TerrainSeamount, TerrainIsland,
		TerrainFoldBelt, TerrainMassif, TerrainKarst, TerrainDome,
		TerrainDunes, TerrainBadlands, TerrainFloodplain, TerrainWetland,
		TerrainSteppe, TerrainTerraced,
		TerrainFaultBlock, TerrainCuesta, TerrainVolcanicChain, TerrainRiverTerrace,
	}
	goldens := map[string]string{
		TerrainPlain:            "35eeeeeea8dcbc0f2be63e958bdc8206e4862f212520483dc54ba039a2ac62e8",
		TerrainHills:            "99ea981d0122a8ddffdf51d979da83a76dfcd0367a54c605fe88aedc9ae3f9df",
		TerrainRange:            "3da6da00e0d5b067431e38bf4973d122d9a257d903178a1e68e785a2b2544d77",
		TerrainPlateau:          "740160ce1d9f2404957c818ec644233ef4274e45e7d5e8bb79cbaed9d8a6e4a1",
		TerrainBasin:            "bb7d23e6b5c4896f048519b685b4a2f567a3efcd0895a43053eb3a73588512a1",
		TerrainValley:           "79d298592b6976cde64821bb09521cf7ca8539c237fdfedfc94dae5bd53729c6",
		TerrainCanyon:           "86577e99d644b16f61b3711f6446c593d74f71d7f82931bf077424f0868f3995",
		TerrainVolcano:          "f09a493669a5ef8e22e1a636027f834aef9d431759e4234b14c475ca4934d33b",
		TerrainContinentalShelf: "cea795be666b74cb434d55d8b28a8ecf2a10eccd89be0967352a06a22d39b41c",
		TerrainContinentalSlope: "f7fdf7ad31e4e68706c2960096863447e3d71f87e3e36a39023b5f12e8316a8d",
		TerrainAbyssalPlain:     "8c6114c0a2644e04898aeeecea88018c244d5732bf24f06f7534aa3b3c79d284",
		TerrainTrench:           "b7f603b6c7c9a0999b864fefe2a628db191543aa4077910a9fb4c91c3552ba0f",
		TerrainOceanRidge:       "3da6da00e0d5b067431e38bf4973d122d9a257d903178a1e68e785a2b2544d77",
		TerrainSeamount:         "797ba40d1dfb598095bb27fd76a2abd9b4ae1e9ceb1c6bae78d6631069fe4e5b",
		TerrainIsland:           "b63f6045ad3a244b614c58967b9f7ed9feb8f71a42f063244692182b0c144162",
		TerrainFoldBelt:         "cb71288d99ede534ac3be13544e2a480333121d0f5788ea96f636b26caca0a78",
		TerrainMassif:           "b86dace74164048bdd1f1c2ab6521b391c0d906e6c74469cd552863e2b2c59ad",
		TerrainKarst:            "86432ce3c01d10415e32e90953cbb4abaceb5448f95e694f61d4ae9cc07f2fb5",
		TerrainDome:             "d90e79c63b158eeb4305c033f015d4c6cb6a38dcd9427ff86b40515503ae10c7",
		TerrainDunes:            "fbaa62414f2b18b5c2299e271bd0fd1cabb04deca4386e919dda640a7e5a8491",
		TerrainBadlands:         "4d94c6e3035ccc1a3161a6582c06c892a1072cf441963d4c6248eb03045c6641",
		TerrainFloodplain:       "66b08dabba63cfa1fd81a9e649811a22f3f5adeba5e215a55861993e99ec4aef",
		TerrainWetland:          "d4cae9c5ac54a24e51952e3b6e853c094bd3c8ed2dc7a2e9ad5e4724f0b557a4",
		TerrainSteppe:           "9275deecb93212f7b6bcb7f331e0e11cb2bfd3f5617bec865d041dde75ec538c",
		TerrainTerraced:         "604dc60394ee67a6ec6143f04cd2a2a1d9cca4bb31f5618e4294186a1ab29d9b",
		TerrainFaultBlock:       "dd614be2e2be52f25685f085e4e9c3c67b3bca5e7a139ef691c441ed5423b1c0",
		TerrainCuesta:           "936c6b81a50a7f80e6d96a65ea3264dc4c108bc78a72d830ce012b049a95ccaa",
		TerrainVolcanicChain:    "a47613adcda1af0637d5a13b1022adae09740eeb71385798e840a1b6553d65c7",
		TerrainRiverTerrace:     "aded18358fa868fd50ae8293a311fda60c787174cf0a91e1c0007c4dcd771300",
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			layer := MapTerrainLayer{
				ID: kind, Kind: kind, Domain: TerrainDomainAny,
				Geometry: MapTerrainGeometry{Type: TerrainGeometryPoint, Coordinates: []MapTerrainPoint{{X: 24, Y: 24}}},
				WidthPx:  16, FeatherPx: 2, Strength: 0.12, Roughness: 0.4, Detail: 1, Seed: 99,
			}
			switch kind {
			case TerrainPlain, TerrainContinentalShelf, TerrainContinentalSlope, TerrainAbyssalPlain,
				TerrainFloodplain, TerrainWetland:
				layer.TargetHeight = &target
			}
			if kind == TerrainIsland {
				layer.Domain = TerrainDomainLand
			}
			switch kind {
			case TerrainFaultBlock, TerrainCuesta, TerrainVolcanicChain, TerrainRiverTerrace:
				// Defined by side of path and distance along it, so a point
				// cannot express them.
				layer.Geometry = MapTerrainGeometry{Type: TerrainGeometryPolyline, Coordinates: []MapTerrainPoint{
					{X: 12, Y: 24}, {X: 34, Y: 28}, {X: 52, Y: 22},
				}}
			}
			spec := MapTerrainEditSpec{Operation: MapTerrainOpCompose, Layers: []MapTerrainLayer{layer}}
			a, err := EditMapTerrain(cfg, spec, filepath.Join(root, kind+"-a"))
			if err != nil {
				t.Fatal(err)
			}
			b, err := EditMapTerrain(cfg, spec, filepath.Join(root, kind+"-b"))
			if err != nil {
				t.Fatal(err)
			}
			got := outputForKind(t, a, "heightmap").SHA256
			if got != outputForKind(t, b, "heightmap").SHA256 {
				t.Fatal("same spec was not deterministic")
			}
			if got != goldens[kind] {
				t.Fatalf("heightmap golden changed: got %s want %s", got, goldens[kind])
			}
			stats := a.Layers[0]
			switch kind {
			case TerrainBasin, TerrainValley, TerrainCanyon, TerrainTrench:
				if stats.LoweredPixels == 0 {
					t.Fatalf("%s did not lower terrain: %+v", kind, stats)
				}
			default:
				if stats.ChangedPixels == 0 {
					t.Fatalf("%s did not change terrain: %+v", kind, stats)
				}
			}
		})
	}
}

func TestTerrainRangeReliefCreatesMassifsAndSaddlesInsteadOfAWall(t *testing.T) {
	peak := terrainRangeRelief(1, 1, 1)
	saddle := terrainRangeRelief(1, 0, 0)
	if peak < 0.95 {
		t.Fatalf("range peak lost most of the requested relief: %.6f", peak)
	}
	if saddle >= 0.01 {
		t.Fatalf("range saddle retains a visible wall-like floor: %.6f", saddle)
	}
	if edge := terrainRangeRelief(0, 1, 1); edge != 0 {
		t.Fatalf("range relief escaped its rasterized profile: %.6f", edge)
	}
	if broad := terrainRangeRelief(0.5, 1, 1); broad >= peak*0.5 {
		t.Fatalf("range cross-section is too broad: midpoint %.6f peak %.6f", broad, peak)
	}
}

func TestTerrainDomainsComeFromProvinceDefinitionsNotElevation(t *testing.T) {
	cfg, root := terrainEditWorkspace(t, 64, 64, 8)
	sourceImage, _ := decodeMapImage(filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png"))
	source := sourceImage.(*image.Gray)

	seamount, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpCompose,
		Layers: []MapTerrainLayer{{
			ID: "ocean-only", Kind: TerrainSeamount,
			Geometry: MapTerrainGeometry{Type: TerrainGeometryPoint, Coordinates: []MapTerrainPoint{{X: 50, Y: 20}}},
			WidthPx:  24, Strength: 0.2, Roughness: 0.2,
		}},
	}, filepath.Join(root, "ocean-domain"))
	if err != nil {
		t.Fatal(err)
	}
	oceanImage, _ := decodeMapImage(outputPathForKind(t, seamount, "heightmap"))
	ocean := oceanImage.(*image.Gray)
	if ocean.GrayAt(50, 20).Y == source.GrayAt(50, 20).Y {
		t.Fatal("default ocean-domain seamount did not alter an ocean province")
	}
	if ocean.GrayAt(40, 20).Y != source.GrayAt(40, 20).Y {
		t.Fatal("default ocean-domain seamount altered a land province with similar elevation")
	}

	lakeLayer := MapTerrainLayer{
		ID: "lake-only", Kind: TerrainBasin, Domain: TerrainDomainLake,
		Geometry: MapTerrainGeometry{Type: TerrainGeometryPoint, Coordinates: []MapTerrainPoint{{X: 36, Y: 54}}},
		WidthPx:  18, Strength: 0.1,
	}
	lakeResult, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpCompose, Layers: []MapTerrainLayer{lakeLayer},
	}, filepath.Join(root, "lake-domain"))
	if err != nil {
		t.Fatal(err)
	}
	lakeImage, _ := decodeMapImage(outputPathForKind(t, lakeResult, "heightmap"))
	lake := lakeImage.(*image.Gray)
	if lake.GrayAt(36, 54).Y == source.GrayAt(36, 54).Y {
		t.Fatal("lake-domain basin did not alter the default.map lake province")
	}
	if lake.GrayAt(36, 40).Y != source.GrayAt(36, 40).Y {
		t.Fatal("lake-domain basin altered adjacent land")
	}
}

func TestSmallRiversPreserveOutsidePaletteIndicesAndGray16Pixels(t *testing.T) {
	cfg, out := terrainEditWorkspace(t, 64, 64, 16)
	sourceHeightImage, _ := decodeMapImage(filepath.Join(cfg.Sources[0].Path, "map_data", "heightmap.png"))
	sourceHeight := sourceHeightImage.(*image.Gray16)
	sourceRiversImage, _ := decodeMapImage(filepath.Join(cfg.Sources[0].Path, "map_data", "rivers.png"))
	sourceRivers := sourceRiversImage.(*image.Paletted)
	region := MapTerrainRegion{X: 12, Y: 12, Width: 36, Height: 36}
	result, err := EditMapTerrain(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpSmallRivers, Region: &region,
		SmallRivers: &MapTerrainSmallRiverSettings{
			MinDrainage: 4, MaxDrainage: 1200, BedDepth: 0.01, SeaLevel: 0.05, MaxWidthIndex: 6,
		},
	}, out)
	if err != nil {
		t.Fatal(err)
	}
	writtenRiversImage, _ := decodeMapImage(outputPathForKind(t, result, "rivers"))
	writtenRivers := writtenRiversImage.(*image.Paletted)
	writtenHeightImage, _ := decodeMapImage(outputPathForKind(t, result, "heightmap"))
	writtenHeight := writtenHeightImage.(*image.Gray16)
	rect := region.rectangle()
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if image.Pt(x, y).In(rect) {
				continue
			}
			if sourceRivers.ColorIndexAt(x, y) != writtenRivers.ColorIndexAt(x, y) {
				t.Fatalf("outside river index at %d,%d changed", x, y)
			}
			if sourceHeight.Gray16At(x, y).Y != writtenHeight.Gray16At(x, y).Y {
				t.Fatalf("outside Gray16 height at %d,%d changed", x, y)
			}
		}
	}
	if writtenRivers.ColorIndexAt(2, 2) != 5 {
		t.Fatal("existing river outside the region was cleared")
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			index := writtenRivers.ColorIndexAt(x, y)
			if index != riverSource && index != riverConfluence && (index < riverBodyMin || index > riverBodyMax) {
				continue
			}
			connected := false
			for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+step[0], y+step[1]
				if !image.Pt(nx, ny).In(writtenRivers.Bounds()) {
					continue
				}
				neighbor := writtenRivers.ColorIndexAt(nx, ny)
				if neighbor == riverSea || neighbor == riverSource || neighbor == riverConfluence ||
					(neighbor >= riverBodyMin && neighbor <= riverBodyMax) {
					connected = true
					break
				}
			}
			if !connected {
				t.Fatalf("new isolated river pixel at %d,%d", x, y)
			}
		}
	}
}

func TestBoundaryRiverSegmentsRequireAnOrthogonalInheritedOutlet(t *testing.T) {
	rect := image.Rect(2, 2, 6, 6)
	base := image.NewPaletted(image.Rect(0, 0, 8, 8), ck3RiverPalette())
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			base.SetColorIndex(x, y, riverLand)
		}
	}
	base.SetColorIndex(6, 4, riverSea)

	w, h := rect.Dx(), rect.Dy()
	index := func(x, y int) int { return y*w + x }
	inStream := make([]bool, w*h)
	painted := []int{
		index(0, 1), index(1, 1),
		index(2, 2), index(3, 2),
	}
	for _, cell := range painted {
		inStream[cell] = true
	}
	kept, blocked := blockUnconnectedBoundaryStreams(painted, inStream, rect, base)
	if blocked != 2 {
		t.Fatalf("blocked %d cells, want the two-cell orphan boundary segment", blocked)
	}
	if len(kept) != 2 || inStream[index(0, 1)] || inStream[index(1, 1)] {
		t.Fatalf("orphan boundary component survived: kept=%v membership=%v", kept, inStream)
	}
	if !inStream[index(2, 2)] || !inStream[index(3, 2)] {
		t.Fatalf("component with an orthogonal inherited-sea outlet was blocked: %v", inStream)
	}
}

func TestPreviewAndArtifactLineageValidateHashesAndSourceFingerprint(t *testing.T) {
	cfg, _ := terrainEditWorkspace(t, 64, 64, 16)
	spec := composeRangeSpec()
	preview, err := PreviewMapTerrainEdit(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.PreviewPNG) == 0 {
		t.Fatal("preview returned no PNG")
	}
	if entries, err := os.ReadDir(cfg.ArtifactRoot); err == nil && len(entries) != 0 {
		t.Fatal("preview created an artifact")
	}
	first, err := CreateMapTerrainEditArtifact(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := CreateMapTerrainEditArtifact(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if outputForKind(t, first, "heightmap").SHA256 != outputForKind(t, peer, "heightmap").SHA256 {
		t.Fatal("two public entry points sharing the same normalized spec would not receive identical raster hashes")
	}
	if !terrainArtifactIDPattern.MatchString(first.ArtifactID) {
		t.Fatalf("invalid artifact id %q", first.ArtifactID)
	}
	second, err := CreateMapTerrainEditArtifact(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpLargeRivers, BaseArtifactID: first.ArtifactID,
		LargeRivers: &MapTerrainLargeRiverSettings{MinDrainage: 4, Depth: 0.01, ValleyWidth: 1, SeaLevel: 0.05},
		Region:      &MapTerrainRegion{X: 8, Y: 8, Width: 48, Height: 48},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ParentArtifactID != first.ArtifactID || second.HydrologyStatus != HydrologySmallRiversStale {
		t.Fatalf("artifact lineage/status missing: %+v", second)
	}
	manifestPath := filepath.Join(cfg.ArtifactRoot, "map-edits", second.ArtifactID, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest MapTerrainArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil || !manifest.RequiresCK3Repack {
		t.Fatalf("bad manifest: %v %+v", err, manifest)
	}
	if _, err := PreviewMapTerrainEdit(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpLargeRivers, BaseArtifactID: "../forged",
	}); err == nil {
		t.Fatal("path-traversal artifact id was accepted")
	}

	heightPath := filepath.Join(cfg.ArtifactRoot, "map-edits", first.ArtifactID, filepath.FromSlash(mapHeightmapRel))
	file, err := os.OpenFile(heightPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0}, 0); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, err := PreviewMapTerrainEdit(cfg, MapTerrainEditSpec{
		Operation: MapTerrainOpLargeRivers, BaseArtifactID: first.ArtifactID,
	}); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered parent was not rejected by hash: %v", err)
	}
}

func TestArtifactRejectsStaleSourceDamagedManifestAndPathTraversal(t *testing.T) {
	t.Run("stale source", func(t *testing.T) {
		cfg, _ := terrainEditWorkspace(t, 64, 64, 16)
		artifact, err := CreateMapTerrainEditArtifact(cfg, composeRangeSpec())
		if err != nil {
			t.Fatal(err)
		}
		defaultMap := filepath.Join(cfg.Sources[0].Path, "map_data", "default.map")
		if err := os.WriteFile(defaultMap, []byte("sea_zones = { 2 }\nlakes = { 3 }\nimpassable_seas = { 99 }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = PreviewMapTerrainEdit(cfg, MapTerrainEditSpec{
			Operation: MapTerrainOpLargeRivers, BaseArtifactID: artifact.ArtifactID,
		})
		if err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("source fingerprint drift was not rejected: %v", err)
		}
	})

	t.Run("damaged manifest", func(t *testing.T) {
		cfg, _ := terrainEditWorkspace(t, 64, 64, 16)
		artifact, err := CreateMapTerrainEditArtifact(cfg, composeRangeSpec())
		if err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(cfg.ArtifactRoot, "map-edits", artifact.ArtifactID, "manifest.json")
		if err := os.WriteFile(manifestPath, []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = PreviewMapTerrainEdit(cfg, MapTerrainEditSpec{
			Operation: MapTerrainOpLargeRivers, BaseArtifactID: artifact.ArtifactID,
		})
		if err == nil || !strings.Contains(err.Error(), "damaged manifest") {
			t.Fatalf("damaged manifest was not rejected: %v", err)
		}
	})

	t.Run("manifest path traversal", func(t *testing.T) {
		cfg, _ := terrainEditWorkspace(t, 64, 64, 16)
		artifact, err := CreateMapTerrainEditArtifact(cfg, composeRangeSpec())
		if err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(cfg.ArtifactRoot, "map-edits", artifact.ArtifactID, "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest MapTerrainArtifactManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, MapTerrainArtifactFile{
			Kind: "escape", Rel: "../escape.png", SHA256: strings.Repeat("0", 64), Bytes: 1,
		})
		data, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = PreviewMapTerrainEdit(cfg, MapTerrainEditSpec{
			Operation: MapTerrainOpLargeRivers, BaseArtifactID: artifact.ArtifactID,
		})
		if err == nil || !strings.Contains(err.Error(), "unsafe") && !strings.Contains(err.Error(), "escapes") && !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("manifest path traversal was not rejected: %v", err)
		}
	})
}

func TestEditMapTerrainRejectsDuplicateIDsBadRegionAndSourceOutput(t *testing.T) {
	cfg, out := terrainEditWorkspace(t, 64, 64, 8)
	spec := composeRangeSpec()
	spec.Layers = append(spec.Layers, spec.Layers[0])
	if _, err := EditMapTerrain(cfg, spec, out); err == nil {
		t.Fatal("duplicate layer ids were accepted")
	}
	spec = composeRangeSpec()
	spec.Region = &MapTerrainRegion{X: 500, Y: 500, Width: 4, Height: 4}
	if _, err := EditMapTerrain(cfg, spec, filepath.Join(out, "region")); err == nil {
		t.Fatal("off-map region was accepted")
	}
	if _, err := EditMapTerrain(cfg, composeRangeSpec(), filepath.Join(cfg.Sources[0].Path, "generated")); err == nil {
		t.Fatal("output inside configured source was accepted")
	}
}

func TestTerrainCoreMatchesPublishedBoundsAndZeroStrength(t *testing.T) {
	cfg, _ := terrainEditWorkspace(t, 64, 64, 16)
	target := 0.2
	zeroStrength := MapTerrainEditSpec{
		Operation: MapTerrainOpCompose,
		Layers: []MapTerrainLayer{{
			ID: "zero-slope", Kind: TerrainContinentalSlope, Domain: TerrainDomainAny,
			Geometry: MapTerrainGeometry{
				Type: TerrainGeometryPoint, Coordinates: []MapTerrainPoint{{X: 32, Y: 32}},
			},
			WidthPx: 32, Strength: 0, TargetHeight: &target,
		}},
	}
	result, err := PreviewMapTerrainEdit(cfg, zeroStrength)
	if err != nil {
		t.Fatal(err)
	}
	if result.Layers[0].ChangedPixels != 0 || result.ModifiedBounds != nil {
		t.Fatalf("zero-strength target layer changed terrain: %+v", result.Layers[0])
	}

	badDetail := composeRangeSpec()
	badDetail.Layers[0].Detail = -1
	if _, err := PreviewMapTerrainEdit(cfg, badDetail); err == nil {
		t.Fatal("negative detail bypassed the CLI/MCP contract")
	}

	tooMany := composeRangeSpec()
	tooMany.Layers = make([]MapTerrainLayer, 129)
	for index := range tooMany.Layers {
		tooMany.Layers[index] = composeRangeSpec().Layers[0]
		tooMany.Layers[index].ID = fmt.Sprintf("layer-%03d", index)
	}
	if _, err := PreviewMapTerrainEdit(cfg, tooMany); err == nil {
		t.Fatal("more than 128 layers bypassed the CLI/MCP contract")
	}

	badErosion := composeRangeSpec()
	badErosion.Erosion = &MapErosionSettings{Droplets: 1, Capacity: -1}
	if _, err := PreviewMapTerrainEdit(cfg, badErosion); err == nil {
		t.Fatal("negative erosion capacity bypassed the CLI/MCP contract")
	}
}

func outputForKind(t *testing.T, result MapTerrainEditResult, kind string) MapTerrainEditOutput {
	t.Helper()
	for _, output := range result.Outputs {
		if output.Kind == kind {
			return output
		}
	}
	t.Fatalf("no %s output in %+v", kind, result.Outputs)
	return MapTerrainEditOutput{}
}

func outputPathForKind(t *testing.T, result MapTerrainEditResult, kind string) string {
	t.Helper()
	output := outputForKind(t, result, kind)
	if output.Path == "" {
		t.Fatalf("%s output has no local path", kind)
	}
	return output.Path
}
