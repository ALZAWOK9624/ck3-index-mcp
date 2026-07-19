package indexer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

func TestMapFontWarningDoesNotLeakConfiguredPath(t *testing.T) {
	privateDir := t.TempDir()
	privatePath := filepath.Join(privateDir, "missing-font.ttf")
	renderer, warnings := loadMapTextRenderer(privatePath)
	defer renderer.Close()
	if len(warnings) == 0 {
		t.Fatal("missing configured font did not produce a warning")
	}
	if strings.Contains(strings.Join(warnings, "\n"), privateDir) {
		t.Fatalf("map font warning leaked its configured path: %v", warnings)
	}
}

func openMapFixtureDB(t *testing.T) (Config, *DB, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, db, dir
}

func TestMapGeometryRLEColorAndTitleAdjacency(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()

	var rgb, perimeter int
	if err := db.sql.QueryRowContext(ctx, `SELECT color_rgb,perimeter FROM map_provinces WHERE province_id=1`).Scan(&rgb, &perimeter); err != nil {
		t.Fatal(err)
	}
	if rgb != 0xff0000 || perimeter <= 0 {
		t.Fatalf("unexpected province color/perimeter: rgb=%06x perimeter=%d", rgb, perimeter)
	}
	var titleRGB int
	if err := db.sql.QueryRowContext(ctx, `SELECT color_rgb FROM map_titles WHERE title_id='d_d33'`).Scan(&titleRGB); err != nil {
		t.Fatal(err)
	}
	if titleRGB != 0x0c2238 {
		t.Fatalf("unexpected indexed title color: rgb=%06x", titleRGB)
	}
	political, err := db.politicalEntityColors(ctx, MapMetricResult{Level: "duchy", Values: []MapMetricValue{{ID: "d_d33", Category: "d_d33"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := political["d_d33"]; got.R != 12 || got.G != 34 || got.B != 56 {
		t.Fatalf("political fill must preserve native title RGB, got %+v", got)
	}
	muted := harmonizePoliticalColor(color.RGBA{255, 0, 0, 255})
	if muted.R <= muted.G || muted.R <= muted.B || muted.R >= 230 || muted.G == 0 {
		t.Fatalf("muted political color should preserve red hue while reducing extremes, got %+v", muted)
	}

	grid := make([]int, 6*3)
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id,fill_rle FROM map_province_geometry ORDER BY province_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var data []byte
		if err := rows.Scan(&pid, &data); err != nil {
			t.Fatal(err)
		}
		runs, err := DecodeMapRuns(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			for x := int(run.X0); x <= int(run.X1); x++ {
				grid[int(run.Y)*6+x] = pid
			}
		}
	}
	expected := []int{1, 1, 2, 3, 4, 4, 1, 2, 2, 5, 4, 4, 1, 2, 2, 4, 4, 4}
	for i := range expected {
		if grid[i] != expected[i] {
			t.Fatalf("RLE mismatch at pixel %d: got %d want %d", i, grid[i], expected[i])
		}
	}

	var border, blocked, water int
	if err := db.sql.QueryRowContext(ctx, `SELECT border_len,blocked_border_len,water_border_len FROM map_title_adjacencies WHERE level='c' AND title_id='c_c114' AND neighbor_id='c_c200'`).Scan(&border, &blocked, &water); err != nil {
		t.Fatal(err)
	}
	if border != 1 || blocked != 0 || water != 0 {
		t.Fatalf("unexpected county adjacency: border=%d blocked=%d water=%d", border, blocked, water)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT border_len,blocked_border_len,water_border_len FROM map_title_adjacencies WHERE level='c' AND title_id='c_c115' AND neighbor_id='c_c200'`).Scan(&border, &blocked, &water); err != nil {
		t.Fatal(err)
	}
	if border != 1 || blocked != 1 || water != 1 {
		t.Fatalf("unexpected water county adjacency: border=%d blocked=%d water=%d", border, blocked, water)
	}
}

func TestStrategicWaterwaysAtlasUsesEmpireBlocksAndLakeSurface(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	resolved, err := resolveMapRenderSpec(MapRenderSpec{
		Recipe: "strategic_waterways_atlas", Target: "e_test", Level: "duchy", Width: 640,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Level != "empire" || len(resolved.Layers) == 0 || resolved.Layers[0].Level != "empire" || resolved.Layers[0].Metric == nil || resolved.Layers[0].Metric.Level != "empire" {
		t.Fatalf("strategic atlas must resolve to empire fill, got level=%q layers=%+v", resolved.Level, resolved.Layers)
	}
	for _, layer := range resolved.Layers {
		if layer.Type == "markers" && layer.Source == "lakes" {
			t.Fatalf("strategic atlas must encode lakes as water surfaces, got lake marker layer %+v", layer)
		}
		if layer.Type == "labels" && (layer.Level != "empire" || layer.Source != "entities") {
			t.Fatalf("strategic atlas must label empire entities, got %+v", layer)
		}
	}
	result, err := db.LLMMapRender(context.Background(), MapRenderSpec{
		Recipe: "strategic_waterways_atlas", Target: "e_test", Level: "duchy", Width: 640,
	}, LLMOptions{AllowProject: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.LayerCounts["flow_edges"] < 1 || result.LayerCounts["strategic_portals"] < 1 || result.LayerCounts["lake_symbols"] != 0 {
		t.Fatalf("expected strategic flow and portal layers without lake icons, got %+v", result.LayerCounts)
	}
	if result.LayerCounts["cached_physical_rasters"] < 1 {
		t.Fatalf("expected cached physical/material overlays, got %+v", result.LayerCounts)
	}
	if _, err := png.Decode(bytes.NewReader(result.PNG)); err != nil {
		t.Fatalf("invalid strategic atlas PNG: %v", err)
	}
	lake := mapPhysicalBaseColor("water", "lake", "historical_atlas")
	sea := mapPhysicalBaseColor("water", "sea", "historical_atlas")
	if lake.B <= lake.G || int(lake.R)+int(lake.G)+int(lake.B) <= int(sea.R)+int(sea.G)+int(sea.B)+120 {
		t.Fatalf("lake water must be visibly brighter and bluer than sea: lake=%+v sea=%+v", lake, sea)
	}
}

func TestMapGeometryFingerprintIncrementalInvalidation(t *testing.T) {
	cfg, db, dir := openMapFixtureDB(t)
	ctx := context.Background()
	var before int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_geometry_build_count'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var physicalBefore int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_physical_build_count'`).Scan(&physicalBefore); err != nil {
		t.Fatal(err)
	}
	db.Close()

	provinceHistory := filepath.Join(dir, "project", "history", "provinces", "test_provinces.txt")
	f, err := os.OpenFile(provinceHistory, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n# history-only refresh\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err := ScanFiles(ctx, cfg, []string{"history/provinces/test_provinces.txt"}); err != nil {
		t.Fatal(err)
	}
	db, err = Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var afterHistory int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_geometry_build_count'`).Scan(&afterHistory); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if afterHistory != before {
		db.Close()
		t.Fatalf("history refresh rebuilt geometry: before=%d after=%d", before, afterHistory)
	}
	var physicalAfterHistory int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_physical_build_count'`).Scan(&physicalAfterHistory); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if physicalAfterHistory != physicalBefore {
		db.Close()
		t.Fatalf("history refresh rebuilt physical rasters: before=%d after=%d", physicalBefore, physicalAfterHistory)
	}
	db.Close()

	pngPath := filepath.Join(dir, "project", "map_data", "provinces.png")
	file, err := os.Open(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	rgba := image.NewRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	rgba.Set(0, 0, color.RGBA{G: 255, A: 255})
	out, err := os.Create(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(out, rgba); err != nil {
		out.Close()
		t.Fatal(err)
	}
	out.Close()
	if _, err := ScanFiles(ctx, cfg, []string{"map_data/provinces.png"}); err != nil {
		t.Fatal(err)
	}
	db, err = Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var afterMap int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_geometry_build_count'`).Scan(&afterMap); err != nil {
		t.Fatal(err)
	}
	if afterMap != before+1 {
		t.Fatalf("map pixel refresh did not rebuild geometry: before=%d after=%d", before, afterMap)
	}
}

func TestMapPhysicalRasterCacheAndPalette(t *testing.T) {
	cfg, db, dir := openMapFixtureDB(t)
	ctx := context.Background()
	var before int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_physical_build_count'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	hillshade, err := db.loadMapPhysicalRaster(ctx, "hillshade")
	if err != nil {
		t.Fatal(err)
	}
	rivers, err := db.loadMapPhysicalRaster(ctx, "rivers")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := db.loadMapPhysicalRaster(ctx, "terrain_detail")
	if err != nil {
		t.Fatal(err)
	}
	elevation, err := db.loadMapPhysicalRaster(ctx, "elevation")
	if err != nil {
		t.Fatal(err)
	}
	if hillshade == nil || detail == nil || elevation == nil || rivers == nil || hillshade.Width != 6 || hillshade.Height != 3 {
		t.Fatalf("unexpected physical cache: hillshade=%+v detail=%+v elevation=%+v rivers=%+v", hillshade, detail, elevation, rivers)
	}
	if rivers.Image.GrayAt(1, 1).Y == 0 || rivers.Image.GrayAt(2, 1).Y == 0 {
		t.Fatal("blue and cyan river pixels must enter the river mask")
	}
	for _, point := range []image.Point{{0, 0}, {3, 1}, {4, 1}, {5, 1}} {
		if rivers.Image.GrayAt(point.X, point.Y).Y != 0 {
			t.Fatalf("background/control marker entered river mask at %v", point)
		}
	}
	if bytes.Equal(hillshade.Image.Pix, make([]byte, len(hillshade.Image.Pix))) {
		t.Fatal("hillshade must contain deterministic nonzero relief")
	}
	db.Close()

	heightPath := filepath.Join(dir, "project", "map_data", "heightmap.png")
	f, err := os.Open(heightPath)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	gray := image.NewGray16(img.Bounds())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			gray.Set(x, y, img.At(x, y))
		}
	}
	gray.SetGray16(0, 0, color.Gray16{Y: 65535})
	out, err := os.Create(heightPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(out, gray); err != nil {
		out.Close()
		t.Fatal(err)
	}
	out.Close()
	if _, err := ScanFiles(ctx, cfg, []string{"map_data/heightmap.png"}); err != nil {
		t.Fatal(err)
	}
	db, err = Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var after int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_physical_build_count'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("heightmap edit did not invalidate physical cache: before=%d after=%d", before, after)
	}
}

func TestMultiScaleReliefAndTerrainAnchors(t *testing.T) {
	heightmap := image.NewGray16(image.Rect(0, 0, 9, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 9; x++ {
			distance := math.Hypot(float64(x-4), float64(y-4))
			value := uint16(math.Max(0, 56000-distance*9000))
			heightmap.SetGray16(x, y, color.Gray16{Y: value})
		}
	}
	hillshade, detail, elevation := buildMultiScaleRelief(heightmap)
	if hillshade.Bounds() != heightmap.Bounds() || detail.Bounds() != heightmap.Bounds() || elevation.Bounds() != heightmap.Bounds() {
		t.Fatalf("physical rasters changed dimensions: hill=%v detail=%v elevation=%v", hillshade.Bounds(), detail.Bounds(), elevation.Bounds())
	}
	if elevation.GrayAt(4, 4).Y <= elevation.GrayAt(0, 0).Y {
		t.Fatal("elevation raster lost the fixture summit")
	}
	if detail.GrayAt(4, 4).Y == 128 || bytes.Equal(hillshade.Pix, make([]byte, len(hillshade.Pix))) {
		t.Fatal("multi-scale relief did not retain curvature and directional shade")
	}

	anchors := parseTerrainAnchors([]byte(`object={
		transform="4 0 4 0 0.382683 0 0.923880 1.5 1.5 1.5
		6 0 3 0 0 0 1 1 1 1"
	}`), "mountain")
	if len(anchors) != 2 || math.Abs(anchors[0].Scale-1.5) > 0.001 || math.Abs(anchors[0].Rotation-math.Pi/4) > 0.001 {
		t.Fatalf("unexpected parsed terrain anchors: %+v", anchors)
	}
	mask := image.NewGray(image.Rect(0, 0, 9, 9))
	drawTerrainAnchor(mask, anchors[0].X, anchors[0].Z, anchors[0])
	if mask.GrayAt(4, 4).Y == 0 {
		t.Fatal("terrain anchor did not produce a ridge mask")
	}
}

func TestMapObjectInstancesAndAtlasSymbols(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()

	var vegetation, holdings int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_object_instances WHERE object_kind='vegetation'`).Scan(&vegetation); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_object_instances WHERE object_kind='holding'`).Scan(&holdings); err != nil {
		t.Fatal(err)
	}
	if vegetation != 1 || holdings != 2 {
		t.Fatalf("unexpected indexed map symbols: vegetation=%d holdings=%d", vegetation, holdings)
	}
	var subtype, sourcePath string
	if err := db.sql.QueryRowContext(ctx, `SELECT subtype,source_path FROM map_object_instances WHERE object_kind='vegetation'`).Scan(&subtype, &sourcePath); err != nil {
		t.Fatal(err)
	}
	if subtype != "broadleaf" || !strings.Contains(sourcePath, "tree_leaf") {
		t.Fatalf("unexpected vegetation classification: subtype=%q source=%q", subtype, sourcePath)
	}
	for holding, want := range map[string]string{
		"castle_holding": "castle", "city_holding": "city", "church_holding": "church", "tribal_holding": "tribal",
		"nomad_holding": "nomad", "ruin_holding": "ruins", "necropolis_holding": "necropolis", "wasteland_empty_holding": "",
	} {
		if got := holdingSymbolKind(holding); got != want {
			t.Fatalf("holding symbol %q: got %q want %q", holding, got, want)
		}
	}

	resolved, err := resolveMapRenderSpec(MapRenderSpec{Recipe: "political_atlas", Target: "e_test", Level: "county", Width: 400})
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]bool{}
	for _, layer := range resolved.Layers {
		if layer.Type == "markers" {
			sources[layer.Source] = true
		}
	}
	if !sources["vegetation"] || !sources["holdings"] {
		t.Fatalf("adaptive atlas omitted symbol layers: %+v", sources)
	}
	result, err := db.LLMMapRender(ctx, resolved, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.LayerCounts["vegetation_symbols"] == 0 || result.LayerCounts["holding_symbols"] == 0 {
		t.Fatalf("atlas did not render indexed symbols: %+v warnings=%+v", result.LayerCounts, result.Warnings)
	}
	if _, err := png.Decode(bytes.NewReader(result.PNG)); err != nil {
		t.Fatalf("symbol atlas PNG did not decode: %v", err)
	}
}

func TestMapObjectCacheInvalidation(t *testing.T) {
	cfg, db, dir := openMapFixtureDB(t)
	ctx := context.Background()
	var before int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_object_build_count'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	db.Close()

	historyPath := filepath.Join(dir, "project", "history", "provinces", "test_provinces.txt")
	history, err := os.OpenFile(historyPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.WriteString("\n# map object history-only check\n"); err != nil {
		history.Close()
		t.Fatal(err)
	}
	history.Close()
	if _, err := ScanFiles(ctx, cfg, []string{"history/provinces/test_provinces.txt"}); err != nil {
		t.Fatal(err)
	}
	db, err = Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var afterHistory int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_object_build_count'`).Scan(&afterHistory); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if afterHistory != before {
		db.Close()
		t.Fatalf("history refresh rebuilt map objects: before=%d after=%d", before, afterHistory)
	}
	db.Close()

	treePath := filepath.Join(dir, "project", "gfx", "map", "map_object_data", "generated", "tree_leaf_high_generator_1.txt")
	tree, err := os.OpenFile(treePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.WriteString("\n# object-cache refresh\n"); err != nil {
		tree.Close()
		t.Fatal(err)
	}
	tree.Close()
	if _, err := ScanFiles(ctx, cfg, []string{"gfx/map/map_object_data/generated/tree_leaf_high_generator_1.txt"}); err != nil {
		t.Fatal(err)
	}
	db, err = Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var afterObject int
	if err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_object_build_count'`).Scan(&afterObject); err != nil {
		t.Fatal(err)
	}
	if afterObject != before+1 {
		t.Fatalf("map object edit did not invalidate object cache: before=%d after=%d", before, afterObject)
	}
}

func TestMapRenderRouteTransformAcrossOutputSizes(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	for _, test := range []struct {
		name          string
		width, height int
		supersample   int
	}{
		{name: "2200x1300", width: 2200, height: 1300, supersample: 1},
		{name: "2000x1100_supersampled", width: 2000, height: 1100, supersample: 2},
		{name: "automatic", supersample: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := db.LLMMapRender(ctx, MapRenderSpec{
				RouteProvinceIDs: []int{1, 2}, AutoContext: true, CorridorRadiusPixels: 120, ContextLevel: "duchy",
				Width: test.width, Height: test.height, Supersample: test.supersample,
				Layers: []MapRenderLayer{{Type: "borders", Level: "county"}},
			}, LLMOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.RoutePointsOutput) != 2 {
				t.Fatalf("route_points_output = %d, want 2", len(result.RoutePointsOutput))
			}
			for _, point := range result.RoutePointsOutput {
				if point.X < 0 || point.Y < 0 || point.X >= float64(result.Width) || point.Y >= float64(result.Height) {
					t.Fatalf("route point outside %dx%d output: %+v", result.Width, result.Height, point)
				}
				var sourceX, sourceY float64
				if err := db.sql.QueryRowContext(ctx, `SELECT center_x,center_y FROM map_provinces WHERE province_id=?`, point.ProvinceID).Scan(&sourceX, &sourceY); err != nil {
					t.Fatal(err)
				}
				wantX := sourceX*result.Transform.ScaleX + result.Transform.OffsetX
				wantY := sourceY*result.Transform.ScaleY + result.Transform.OffsetY
				if math.Abs(point.X-wantX) > 0.02 || math.Abs(point.Y-wantY) > 0.02 {
					t.Fatalf("point transform mismatch: got=(%.2f,%.2f) want=(%.2f,%.2f)", point.X, point.Y, wantX, wantY)
				}
			}
			if result.Output.Width != result.Width || result.Output.Height != result.Height || result.SourceMap.Width <= 0 || result.SourceMap.Height <= 0 {
				t.Fatalf("incomplete coordinate metadata: %+v", result)
			}
		})
	}
}

func TestMapRenderFullRouteCarriesEndpointsLegsAndLabels(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	var fileID int
	if err := db.sql.QueryRowContext(ctx, `SELECT id FROM files ORDER BY id LIMIT 1`).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ language, value, path string }{
		{"english", "Route Duchy", "localization/test_l_english.yml"},
		{"simp_chinese", "路线公国", "localization/test_l_simp_chinese.yml"},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES('d_d33',?,?,?,?,?,?,?,0)`, item.language, item.value, fileID, "project", 1, item.path, 1); err != nil {
			t.Fatal(err)
		}
	}
	fontPath := filepath.Join(t.TempDir(), "route-label.ttf")
	if err := os.WriteFile(fontPath, goregular.TTF, 0644); err != nil {
		t.Fatal(err)
	}
	route := &MapRouteResult{
		Status:       "ready",
		Intent:       "map_route",
		ResolvedFrom: MapResolvedSubject{Input: "Origin", ProvinceID: 1, NameEN: "Origin", NameZH: "起点"},
		ResolvedTo:   MapResolvedSubject{Input: "Destination", ProvinceID: 2, NameEN: "Destination", NameZH: "终点"},
		Path:         []MapRoutePoint{{ProvinceID: 1}, {ProvinceID: 2, AdjacencyFromPrevious: "land_boundary"}},
		Legs:         []MapRouteLeg{{Kind: "land", StartIndex: 0, EndIndex: 1}},
	}
	result, err := db.LLMMapRender(ctx, MapRenderSpec{
		Route: route, AutoContext: true, CorridorRadiusPixels: 120, ContextLevel: "duchy",
		LabelLanguage: "bilingual", FontPath: fontPath, Width: 600, Height: 360,
		Layers: []MapRenderLayer{{Type: "borders", Level: "duchy"}, {Type: "labels", Source: "titles", Level: "duchy", Limit: 10}},
	}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResolvedFrom == nil || result.ResolvedTo == nil || result.ResolvedFrom.ProvinceID != 1 || result.ResolvedTo.ProvinceID != 2 {
		t.Fatalf("render omitted resolved route endpoints: from=%+v to=%+v", result.ResolvedFrom, result.ResolvedTo)
	}
	if len(result.RouteLegs) != 1 || result.RouteLegs[0].Kind != "land" || result.LayerCounts["label_items"] == 0 {
		t.Fatalf("render omitted route legs or bilingual context labels: legs=%+v counts=%+v warnings=%v", result.RouteLegs, result.LayerCounts, result.Warnings)
	}
	if len(result.RoutePointsOutput) != 2 || result.RoutePointsOutput[0].Role != "origin" || result.RoutePointsOutput[1].Role != "destination" {
		t.Fatalf("render route endpoint coordinate roles = %+v", result.RoutePointsOutput)
	}
}

func TestMapRenderHistoryYearCompatibilityAlias(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	result, err := db.LLMMapRender(ctx, MapRenderSpec{HistoryYear: 6254, Width: 400, Layers: []MapRenderLayer{{Type: "borders", Level: "county"}}}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Year != 6254 || !containsString(result.Warnings, "history_year is deprecated; use year") {
		t.Fatalf("deprecated alias was not normalized: year=%d warnings=%v", result.Year, result.Warnings)
	}
	if _, err := db.LLMMapRender(ctx, MapRenderSpec{Year: 6254, HistoryYear: 6253, Width: 400, Layers: []MapRenderLayer{{Type: "borders", Level: "county"}}}, LLMOptions{}); err == nil {
		t.Fatal("conflicting year and history_year were accepted")
	}
}

func TestCoordinatedPoliticalColorAnchorBounds(t *testing.T) {
	input := map[string]color.RGBA{
		"d_a": {R: 150, G: 45, B: 70, A: 255},
		"d_b": {R: 153, G: 47, B: 72, A: 255},
	}
	neighbors := map[string]map[string]bool{"d_a": {"d_b": true}, "d_b": {"d_a": true}}
	got := coordinatePoliticalColors(input, neighbors)
	for id, original := range input {
		anchor := okLabToLCH(rgbaToOKLab(original))
		adjusted := okLabToLCH(rgbaToOKLab(got[id]))
		if math.Abs(hueDelta(adjusted.H, anchor.H)) > 8.5 || math.Abs(adjusted.L-anchor.L) > 0.125 {
			t.Fatalf("coordinated color escaped native anchor for %s: anchor=%+v adjusted=%+v", id, anchor, adjusted)
		}
	}
	if distance := okLabDistance(got["d_a"], got["d_b"]); distance < 0.065 {
		t.Fatalf("adjacent colors remain too similar: distance=%.4f colors=%+v", distance, got)
	}
}

func TestMapMetricRecipesCustomValidationAndDiffusion(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	metric, err := db.LLMMapBuildMetric(ctx, MapMetricSpec{Recipe: "development_network", Target: "e_test", Year: 6253}, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if metric.Kind != "numeric" || len(metric.Values) != 2 || metric.Stats.Coverage != 1 {
		t.Fatalf("unexpected development metric: %+v", metric)
	}
	values := map[string]float64{}
	for _, item := range metric.Values {
		values[item.ID] = item.Value
	}
	if values["c_c114"] <= values["c_c200"] || values["c_c200"] <= 1 {
		t.Fatalf("expected c_c114 core and positive diffusion into c_c200, got %+v", values)
	}

	_, err = db.LLMMapBuildMetric(ctx, MapMetricSpec{Target: "e_test", Level: "county", Kind: "numeric", Values: []MapMetricValue{{ID: "c_c114", Value: 5}}}, LLMOptions{})
	if err == nil {
		t.Fatal("custom metric without source_note must fail")
	}
	custom, err := db.LLMMapBuildMetric(ctx, MapMetricSpec{Target: "e_test", Level: "county", Kind: "numeric", SourceNote: "Test model hypothesis.", Values: []MapMetricValue{{ID: "c_c114", Value: 5}, {ID: "c_c200", Value: 2}}}, LLMOptions{})
	if err != nil || custom.Provenance != "model" {
		t.Fatalf("expected source-noted model metric, result=%+v err=%v", custom, err)
	}
	invalidCustomValues := []struct {
		name   string
		values []MapMetricValue
	}{
		{name: "unknown id", values: []MapMetricValue{{ID: "c_missing", Value: 1}}},
		{name: "duplicate id", values: []MapMetricValue{{ID: "c_c114", Value: 1}, {ID: "c_c114", Value: 2}}},
		{name: "non-finite value", values: []MapMetricValue{{ID: "c_c114", Value: math.Inf(1)}}},
	}
	for _, test := range invalidCustomValues {
		t.Run(test.name, func(t *testing.T) {
			_, err := db.LLMMapBuildMetric(ctx, MapMetricSpec{Target: "e_test", Level: "county", Kind: "numeric", SourceNote: "Invalid test input.", Values: test.values}, LLMOptions{})
			if err == nil {
				t.Fatalf("custom metric with %s must fail", test.name)
			}
		})
	}
}

func TestAdaptiveAtlasLevelsAndIndexedDevelopment(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()

	baronies, err := db.LLMMapBuildMetric(ctx, MapMetricSpec{Target: "e_test", Level: "barony", Year: 6253, Kind: "category", Field: "entity_id", Aggregate: "majority", Provenance: "indexed", SourceNote: "fixture"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(baronies.Values) != 3 {
		t.Fatalf("expected three playable baronies, got %+v", baronies.Values)
	}
	var baronyBorders int
	if err := db.sql.QueryRowContext(ctx, `SELECT border_len FROM map_title_adjacencies WHERE level='b' AND title_id='b_b420' AND neighbor_id='b_b421'`).Scan(&baronyBorders); err != nil || baronyBorders <= 0 {
		t.Fatalf("expected cached barony adjacency, border=%d err=%v", baronyBorders, err)
	}

	development, err := db.LLMMapBuildMetric(ctx, MapMetricSpec{Target: "e_test", Level: "county", Year: 6253, Kind: "numeric", Field: "development", Aggregate: "max", Provenance: "indexed", SourceNote: "Indexed fixture development."}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(development.Values) != 1 || development.Values[0].ID != "c_c114" || development.Values[0].Value != 12 {
		t.Fatalf("expected undiluted indexed development level 12, got %+v", development.Values)
	}

	for _, test := range []struct {
		recipe, theme, level, field string
	}{
		{"political_atlas", "political", "barony", "entity_id"},
		{"thematic_atlas", "culture", "county", "culture"},
		{"thematic_atlas", "faith", "duchy", "religion"},
		{"thematic_atlas", "development", "county", "development"},
		{"thematic_atlas", "terrain", "kingdom", "terrain"},
	} {
		resolved, err := resolveMapRenderSpec(MapRenderSpec{Recipe: test.recipe, Theme: test.theme, Level: test.level, Target: "e_test"})
		if err != nil {
			t.Fatalf("resolve %s/%s: %v", test.theme, test.level, err)
		}
		if len(resolved.Layers) < 4 || resolved.Layers[0].Metric == nil || resolved.Layers[0].Metric.Field != test.field || resolved.Layers[0].Metric.Level != test.level {
			t.Fatalf("unexpected adaptive layers for %s/%s: %+v", test.theme, test.level, resolved.Layers)
		}
	}
	political, err := resolveMapRenderSpec(MapRenderSpec{Recipe: "political_atlas", Level: "barony", Target: "e_test"})
	if err != nil {
		t.Fatal(err)
	}
	legendText := ""
	for _, item := range buildAtlasLegend(political, nil) {
		legendText += item.Label + "\n"
	}
	for _, expected := range []string{"男爵领 / Barony", "伯爵领边界 / County boundary", "公国边界 / Duchy boundary", "王国边界 / Kingdom boundary", "帝国外框 / Empire outline"} {
		if !strings.Contains(legendText, expected) {
			t.Fatalf("adaptive legend omitted %q: %s", expected, legendText)
		}
	}
}

func TestMapRenderDeterministicPNG(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	spec := MapRenderSpec{
		Title: "Fixture development network", Target: "e_test", Year: 6253, Width: 640,
		Layers: []MapRenderLayer{
			{Type: "fill", Metric: &MapMetricSpec{Recipe: "development_network"}, Palette: "development", Classes: 5},
			{Type: "borders", Level: "county", Color: "#111111", LineWidth: 1},
			{Type: "borders", Source: "outer", Color: "#ded6c4c8", LineWidth: 2},
			{Type: "markers", Source: "capitals", Color: "#ffcc55", LineWidth: 4},
			{Type: "flows", Source: "metric", Color: "#ef9d45", Threshold: 0.1, Limit: 10},
			{Type: "labels", Source: "top_metric", Limit: 2},
		},
	}
	first, err := db.LLMMapRender(ctx, spec, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.LLMMapRender(ctx, spec, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.PNG, second.PNG) {
		t.Fatal("map rendering must be deterministic")
	}
	decoded, err := png.Decode(bytes.NewReader(first.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != first.Width || decoded.Bounds().Dy() != first.Height || first.Bytes > 5*1024*1024 {
		t.Fatalf("unexpected PNG result: bounds=%v metadata=%dx%d bytes=%d", decoded.Bounds(), first.Width, first.Height, first.Bytes)
	}
	if first.LayerCounts["physical_features"] == 0 {
		t.Fatalf("expected automatic water or impassable terrain base, counts=%+v", first.LayerCounts)
	}
	nonBackground := false
	background := decoded.At(0, decoded.Bounds().Dy()-1)
	for y := 0; y < decoded.Bounds().Dy() && !nonBackground; y++ {
		for x := 0; x < decoded.Bounds().Dx(); x++ {
			if decoded.At(x, y) != background {
				nonBackground = true
				break
			}
		}
	}
	if !nonBackground {
		t.Fatal("rendered map is blank")
	}
}

func TestAtlasLegendGridKeepsAllItems(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		items, height, top, bottom    int
		rowHeight, wantRows, wantCols int
	}{
		{name: "culture atlas fits one column", items: 19, height: 1522, top: 146, bottom: 108, rowHeight: 34, wantRows: 19, wantCols: 1},
		{name: "short atlas balances two columns", items: 19, height: 500, top: 73, bottom: 54, rowHeight: 34, wantRows: 10, wantCols: 2},
		{name: "empty legend remains drawable", items: 0, height: 500, top: 73, bottom: 54, rowHeight: 34, wantRows: 1, wantCols: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, cols := atlasLegendGrid(tc.items, tc.height, tc.top, tc.bottom, tc.rowHeight)
			if rows != tc.wantRows || cols != tc.wantCols {
				t.Fatalf("atlasLegendGrid() = %d rows x %d columns, want %d x %d", rows, cols, tc.wantRows, tc.wantCols)
			}
			if tc.items > 0 && rows*cols < tc.items {
				t.Fatalf("legend grid has %d slots for %d items", rows*cols, tc.items)
			}
		})
	}
}

func TestMapRenderOutputSpaceSizing(t *testing.T) {
	if MapRenderMaxWidth != 8192 || MapRenderMaxHeight != 4096 {
		t.Fatalf("map output cap = %dx%d, want 8192x4096", MapRenderMaxWidth, MapRenderMaxHeight)
	}
	for _, tc := range []struct {
		name        string
		width       int
		supersample int
		wantFinal   float64
	}{
		{name: "baseline", width: 1600, supersample: 1, wantFinal: 7},
		{name: "4k supersampled", width: 4096, supersample: 2, wantFinal: 7},
		{name: "8k native", width: 8192, supersample: 1, wantFinal: 7},
		{name: "small output shrinks", width: 800, supersample: 2, wantFinal: 3.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := MapRenderSpec{Width: tc.width, Supersample: tc.supersample}
			spec.uiScale = mapRenderOutputUIScale(spec.Width)
			spec.deviceScale = spec.uiScale * float64(spec.Supersample)
			gotFinal := float64(mapRenderUIPixels(spec, 7)) / float64(spec.Supersample)
			if math.Abs(gotFinal-tc.wantFinal) > 0.01 {
				t.Fatalf("final UI size = %.2fpx, want %.2fpx", gotFinal, tc.wantFinal)
			}
		})
	}
	resolved, err := resolveMapRenderSpec(MapRenderSpec{Recipe: "political_atlas", Width: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Supersample != 1 {
		t.Fatalf("8K recipe default supersample = %d, want 1", resolved.Supersample)
	}
}

func TestMapRenderAutoWidth(t *testing.T) {
	cases := []struct {
		name      string
		provinces int
		spec      MapRenderSpec
		want      int
	}{
		{name: "compact region map", provinces: 180, spec: MapRenderSpec{Level: "region", Layers: []MapRenderLayer{{Type: "fill"}, {Type: "labels", Level: "region"}}}, want: 2560},
		{name: "detailed imperial atlas", provinces: 3225, spec: MapRenderSpec{Level: "region", Layout: "full_atlas", Layers: []MapRenderLayer{{Type: "fill"}, {Type: "markers", Source: "holdings"}, {Type: "markers", Source: "vegetation"}, {Type: "labels", Level: "region"}}}, want: 3840},
		{name: "dense world atlas", provinces: 8309, spec: MapRenderSpec{Level: "empire", Layout: "full_atlas", Layers: []MapRenderLayer{{Type: "fill"}, {Type: "labels", Level: "empire"}}}, want: 8192},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			width, reason := mapRenderAutoWidth(tc.spec, tc.provinces)
			if width != tc.want {
				t.Fatalf("auto width = %d, want %d (%s)", width, tc.want, reason)
			}
			if !strings.Contains(reason, fmt.Sprintf("%d provinces", tc.provinces)) {
				t.Fatalf("auto resolution reason does not explain province count: %q", reason)
			}
		})
	}
}

func TestMapRenderExactCanvasAndWorkingPixelBudget(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	pids, err := db.mapRenderTargetProvinces(ctx, "e_test")
	if err != nil {
		t.Fatal(err)
	}
	regionPIDs, err := db.mapRenderTargetProvinces(ctx, "region:nested_region")
	if err != nil {
		t.Fatal(err)
	}
	if len(regionPIDs) != 3 || regionPIDs[0] != 1 || regionPIDs[1] != 2 || regionPIDs[2] != 3 {
		t.Fatalf("region target provinces = %v, want [1 2 3]", regionPIDs)
	}
	regionEntities, regionGroups, err := db.mapMetricEntities(ctx, "region:nested_region", "county")
	if err != nil {
		t.Fatal(err)
	}
	if len(regionEntities) != 1 || len(regionGroups["c_c114"]) != 2 {
		t.Fatalf("region metric groups = ids:%v groups:%v", regionEntities, regionGroups)
	}
	governorateEntities, governorateGroups, err := db.mapMetricEntities(ctx, "region:nested_region", "region")
	if err != nil {
		t.Fatal(err)
	}
	if len(governorateEntities) != 1 || governorateEntities[0] != "nested_region" || len(governorateGroups["nested_region"]) != 2 {
		t.Fatalf("governorate metric groups = ids:%v groups:%v", governorateEntities, governorateGroups)
	}
	selected := map[int]bool{}
	for _, pid := range pids {
		selected[pid] = true
	}
	viewport, err := db.mapRenderViewport(ctx, selected, 8192, 4096, 72)
	if err != nil {
		t.Fatal(err)
	}
	if viewport.Width != 8192 || viewport.Height != 4096 {
		t.Fatalf("explicit canvas = %dx%d, want 8192x4096", viewport.Width, viewport.Height)
	}
	autoResult, err := db.LLMMapRender(ctx, MapRenderSpec{
		Target: "e_test",
		Layers: []MapRenderLayer{{Type: "borders", Level: "county", LineWidth: 1}},
	}, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if autoResult.ResolutionMode != "auto" || autoResult.Width != 2560 || autoResult.ResolutionReason == "" {
		t.Fatalf("automatic render resolution = mode:%q size:%dx%d reason:%q", autoResult.ResolutionMode, autoResult.Width, autoResult.Height, autoResult.ResolutionReason)
	}
	_, err = db.LLMMapRender(ctx, MapRenderSpec{
		Target: "e_test", Width: 8192, Height: 4096, Supersample: 2,
		Layers: []MapRenderLayer{{Type: "borders", Level: "county", LineWidth: 1}},
	}, LLMOptions{Limit: 8})
	if err == nil || !strings.Contains(err.Error(), "working pixels") {
		t.Fatalf("8K supersample budget error = %v", err)
	}
}

func TestMapRenderLocalizedLabelAndConfiguredFont(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	var fileID int
	if err := db.sql.QueryRowContext(ctx, `SELECT id FROM files ORDER BY id LIMIT 1`).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		language string
		value    string
		path     string
	}{
		{language: "godherja", value: "English County", path: "localization/godherja/test_l_english.yml"},
		{language: "godherja", value: "中文伯爵领", path: "localization/godherja/test_l_simp_chinese.yml"},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,?,?,?,?,?,?,0)`, "c_c114", item.language, item.value, fileID, "project", 1, item.path, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := db.mapRenderLabel(ctx, "c_c114", true); got != "中文伯爵领" {
		t.Fatalf("expected Simplified Chinese localization before English, got %q", got)
	}
	if got := db.mapRenderLabel(ctx, "c_c114", false); got != "C_C114" {
		t.Fatalf("expected id fallback without a configured font, got %q", got)
	}
	fontPath := filepath.Join(t.TempDir(), "map-label.ttf")
	if err := os.WriteFile(fontPath, goregular.TTF, 0644); err != nil {
		t.Fatal(err)
	}
	renderer, warnings := loadMapTextRenderer(fontPath)
	defer renderer.Close()
	if len(warnings) != 0 || !renderer.SupportsLocalizedText() {
		t.Fatalf("expected configured OpenType renderer, warnings=%v", warnings)
	}
}

func TestDuchyPoliticalAtlasRecipe(t *testing.T) {
	_, db, _ := openMapFixtureDB(t)
	defer db.Close()
	ctx := context.Background()
	var fileID int
	if err := db.sql.QueryRowContext(ctx, `SELECT id FROM files ORDER BY id LIMIT 1`).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"e_test", "k_k11", "d_d33"} {
		for _, item := range []struct{ language, value, path string }{
			{"english", "Atlas " + id, "localization/test_l_english.yml"},
			{"simp_chinese", "地图" + id, "localization/test_l_simp_chinese.yml"},
		} {
			if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,?,?,?,?,?,?,0)`, id, item.language, item.value, fileID, "project", 1, item.path, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	fontPath := filepath.Join(t.TempDir(), "atlas.ttf")
	if err := os.WriteFile(fontPath, goregular.TTF, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := db.LLMMapRender(ctx, MapRenderSpec{
		Recipe: "duchy_political_atlas", Target: "e_test", Width: 640, Height: 420,
		Title: "测试地图集", Subtitle: "TEST ATLAS", FontPath: fontPath,
	}, LLMOptions{Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(result.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height || result.Width != 640 || result.Height != 420 {
		t.Fatalf("supersampling changed final dimensions: image=%v metadata=%dx%d", decoded.Bounds(), result.Width, result.Height)
	}
	if result.LayerCounts["cached_physical_rasters"] < 4 || result.LayerCounts["label_items"] == 0 {
		t.Fatalf("atlas omitted physical rasters or localized labels: counts=%+v warnings=%+v", result.LayerCounts, result.Warnings)
	}
	if len(result.Legend) != 7 || strings.Contains(result.Summary, "d_d33") {
		t.Fatalf("atlas legend or summary leaked script ids: legend=%+v summary=%q", result.Legend, result.Summary)
	}
}
