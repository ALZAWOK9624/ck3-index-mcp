package indexer

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMapContextFixture(t *testing.T, dir string) Config {
	t.Helper()
	project := filepath.Join(dir, "project")
	mustMkdir := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(project, filepath.FromSlash(rel)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustMkdir("map_data")
	mustMkdir("map_data/geographical_regions")
	mustMkdir("common/landed_titles")
	mustMkdir("common/province_terrain")
	mustMkdir("common/religion/holy_sites")
	mustMkdir("common/religion/religions")
	mustMkdir("history/provinces")
	mustMkdir("history/titles")
	mustMkdir("history/characters")
	mustMkdir("gfx/map/map_object_data/generated")
	mustMkdir("gfx/map/terrain")

	mustWrite("map_data/definition.csv", "province;red;green;blue\n1;255;0;0\n2;0;255;0\n3;0;0;255\n4;255;255;0\n5;0;255;255\n")
	mustWrite("map_data/default.map", "lakes = { 3 }\nimpassable_mountains = { 5 }\n")
	mustWrite("map_data/adjacencies.csv", "From;To;Type;Through;start_x;start_y;stop_x;stop_y;Comment\n1;4;sea;3;-1;-1;-1;-1;fixture strait\n1;5;sea;-1;-1;-1;-1;-1;fixture underground\n-1;-1;;-1;-1;-1;-1;-1;\n")
	mustWrite("map_data/geographical_regions.txt", "test_region = { provinces = { 1 } counties = { c_c200 } }\nparent_region = { regions = { test_region } }\n")
	mustWrite("map_data/geographical_regions/governorates.txt", "nested_region = { duchies = { d_d33 } }\n")
	mustWrite("common/province_terrain/00_province_terrain.txt", "default_land=plains\ndefault_sea=sea\n1=hills\n2=forest\n3=coastal_sea\n5=mayik_chamber\n")
	img := image.NewRGBA(image.Rect(0, 0, 6, 3))
	colors := map[int]color.RGBA{
		1: {R: 255, A: 255},
		2: {G: 255, A: 255},
		3: {B: 255, A: 255},
		4: {R: 255, G: 255, A: 255},
		5: {G: 255, B: 255, A: 255},
	}
	grid := [][]int{{1, 1, 2, 3, 4, 4}, {1, 2, 2, 5, 4, 4}, {1, 2, 2, 4, 4, 4}}
	for y := range grid {
		for x, id := range grid[y] {
			img.SetRGBA(x, y, colors[id])
		}
	}
	pngPath := filepath.Join(project, "map_data", "provinces.png")
	f, err := os.Create(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	heightmap := image.NewGray16(image.Rect(0, 0, 6, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 6; x++ {
			heightmap.SetGray16(x, y, color.Gray16{Y: uint16((x + y*2) * 5000)})
		}
	}
	heightPath := filepath.Join(project, "map_data", "heightmap.png")
	heightFile, err := os.Create(heightPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(heightFile, heightmap); err != nil {
		heightFile.Close()
		t.Fatal(err)
	}
	if err := heightFile.Close(); err != nil {
		t.Fatal(err)
	}
	rivers := image.NewRGBA(image.Rect(0, 0, 6, 3))
	draw.Draw(rivers, rivers.Bounds(), &image.Uniform{color.RGBA{255, 0, 128, 255}}, image.Point{}, draw.Src)
	rivers.SetRGBA(1, 1, color.RGBA{0, 100, 255, 255})
	rivers.SetRGBA(2, 1, color.RGBA{0, 200, 255, 255})
	rivers.SetRGBA(3, 1, color.RGBA{255, 0, 0, 255})
	rivers.SetRGBA(4, 1, color.RGBA{0, 255, 0, 255})
	rivers.SetRGBA(5, 1, color.RGBA{255, 255, 255, 255})
	riverPath := filepath.Join(project, "map_data", "rivers.png")
	riverFile, err := os.Create(riverPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(riverFile, rivers); err != nil {
		riverFile.Close()
		t.Fatal(err)
	}
	if err := riverFile.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite("gfx/map/terrain/materials.settings", `{
	{
		name = "Fixture Grass"
		id = "fixture_grass"
		diffuse = "grass.dds"
		mask = "grass_mask.png"
	}
	{
		name = "Fixture Rock"
		id = "fixture_rock"
		diffuse = "rock.dds"
		mask = "rock_mask.png"
	}
}`)
	mustWrite("gfx/map/terrain/grass.dds", "fixture diffuse")
	mustWrite("gfx/map/terrain/grass_normal.dds", "fixture normal")
	mustWrite("gfx/map/terrain/grass_properties.dds", "fixture properties")
	mustWrite("gfx/map/terrain/grass_mask.png", "fixture mask")
	materialIndexes := make([]byte, 6*3*4)
	materialStrengths := make([]byte, 6*3*4)
	for y := 0; y < 3; y++ {
		for x := 0; x < 6; x++ {
			offset := (y*6 + x) * 4
			if x >= 3 {
				materialIndexes[offset+2] = 1
			}
			materialStrengths[offset+2] = 255
		}
	}
	writeTestTGA32(t, filepath.Join(project, "gfx", "map", "terrain", "detail_index.tga"), 6, 3, materialIndexes)
	writeTestTGA32(t, filepath.Join(project, "gfx", "map", "terrain", "detail_intensity.tga"), 6, 3, materialStrengths)

	mustWrite("common/landed_titles/00_landed_titles.txt", `
e_test = {
	k_k11 = {
		d_d33 = {
			color = { 12 34 56 }
			c_c114 = {
				capital = b_b420
				b_b420 = { province = 1 }
				b_b421 = { province = 2 }
			}
			c_c115 = {
				b_b422 = { province = 3 }
			}
		}
	}
	k_empty = {
		d_empty = {
			c_c200 = {
				b_b500 = { province = 4 }
			}
		}
	}
}`)
	mustWrite("history/provinces/test_provinces.txt", `
1 = { culture = culture_a religion = faith_old holding = castle_holding development_level = 12 special_building_slot = slot_a buildings = { farm_01 barracks_01 } }
2 = { culture = culture_a holding = city_holding 6240.1.1 = { special_building = special_market_01 } }
3 = { culture = culture_b religion = faith_sea }
4 = { culture = culture_a holding = wasteland_empty_holding }
`)
	mustWrite("common/religion/holy_sites/test_holy_sites.txt", `
test_holy_site = {
	county = c_c114
	barony = b_b420
}
`)
	mustWrite("common/religion/religions/test_religion.txt", `
test_religion = {
	faiths = {
		faith_old = {
			holy_site = test_holy_site
		}
	}
}
`)
	mustWrite("history/titles/test_titles.txt", `
k_k11 = {
	6248.1.1 = { holder = char72 }
}
c_c114 = {
	6240.1.1 = { change_development_level = 12 }
}
`)
	mustWrite("history/characters/test_chars.txt", `
char72 = {
	name = "Holder"
	culture = culture_a
	religion = faith_old
}
`)
	mustWrite("gfx/map/map_object_data/building_locators.txt", `
game_object_locator = {
	name = "buildings"
	instances = {
		{ id = 1 position = { 0.25 0 0.75 } rotation = { 0 0 0 1 } scale = { 1 1 1 } }
		{ id = 2 position = { 2.00 0 1.25 } rotation = { 0 0.382683 0 0.923880 } scale = { 1 1 1 } }
	}
}
`)
	mustWrite("gfx/map/map_object_data/generated/tree_leaf_high_generator_1.txt", `
object = {
	name = "tree_leaf_high_generator_1_0"
	generated_content = yes
	transform = "2 0 1 0 0 0 1 1 1 1"
}
`)
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte(`database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeTestTGA32(t *testing.T, path string, width, height int, topOriginBGRA []byte) {
	t.Helper()
	header := make([]byte, 18)
	header[2] = 2
	binary.LittleEndian.PutUint16(header[12:14], uint16(width))
	binary.LittleEndian.PutUint16(header[14:16], uint16(height))
	header[16] = 32
	header[17] = 8
	data := append([]byte(nil), header...)
	rowBytes := width * 4
	for y := height - 1; y >= 0; y-- {
		data = append(data, topOriginBGRA[y*rowBytes:(y+1)*rowBytes]...)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMapContextScanAndQueries(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	province, err := db.LLMMapProvinceInfo(context.Background(), "1", 6253, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if province.Province.County != "c_c114" || province.Province.Holder != "char72" || province.Province.Terrain != "hills" {
		t.Fatalf("expected c_c114 inherited holder char72 and hills terrain, got county=%s holder=%s terrain=%s", province.Province.County, province.Province.Holder, province.Province.Terrain)
	}
	if !province.Province.Building.IsCountyCapital || province.Province.Building.HoldingType != "castle_holding" || province.Province.Building.SlotStatus != "empty_special_slot" {
		t.Fatalf("expected county-capital castle with empty special slot, got %+v", province.Province.Building)
	}
	if len(province.Province.Building.Buildings) != 2 || len(province.Province.HolySites) != 1 || province.Province.HolySites[0].ID != "test_holy_site" {
		t.Fatalf("expected building list and holy site on province 1, got buildings=%+v holy_sites=%+v", province.Province.Building.Buildings, province.Province.HolySites)
	}
	if !containsString(province.Province.Regions, "test_region") || !containsString(province.Province.Regions, "parent_region") {
		t.Fatalf("expected region propagation on province 1, got %+v", province.Province.Regions)
	}
	if !containsString(province.Province.Regions, "nested_region") {
		t.Fatalf("expected nested map_data/geographical_regions file to be indexed, got %+v", province.Province.Regions)
	}
	if province.Province.Area != 4 || province.Province.BBox.MaxX != 1 || len(province.Neighbors) == 0 {
		t.Fatalf("unexpected province geometry/neighbors: %+v", province.Province)
	}
	if province.Neighbors[0].Direction != "east" || province.Neighbors[0].DistancePixels <= 0 || province.Neighbors[0].Center.X <= province.Province.Center.X {
		t.Fatalf("expected precision eastward neighbor geometry, got %+v", province.Neighbors[0])
	}

	water, err := db.LLMMapProvinceInfo(context.Background(), "3", 6253, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !water.Province.Blocked || water.Province.BlockKind != "water" || water.Province.Terrain != "coastal_sea" {
		t.Fatalf("expected province 3 to be water-blocked, got %+v", water.Province)
	}

	barriers, err := db.LLMMapProvinceInfo(context.Background(), "2", 6253, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if barriers.AdjacentFeatures.WaterNeighbors != 1 || barriers.AdjacentFeatures.ImpassableMountainNeighbors != 1 {
		t.Fatalf("expected one water and one impassable-mountain neighbor around province 2, got %+v", barriers.AdjacentFeatures)
	}

	relation, err := db.LLMMapSpatialRelation(context.Background(), "2", "5", 6253, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !relation.Adjacent || relation.Direction != "east" || relation.AdjacencyKind != "impassable_mountain_boundary" || relation.DistancePixels <= 0 || relation.BearingDegrees < 0 || relation.BearingDegrees >= 360 {
		t.Fatalf("unexpected precision spatial relation: %+v", relation)
	}
	if relation.MapFrame.WidthPixels != 6 || relation.MapFrame.HeightPixels != 3 || relation.ReverseDirection != "west" {
		t.Fatalf("unexpected map frame/reverse direction: %+v", relation)
	}
	strategic, err := db.LLMMapSpatialRelation(context.Background(), "1", "5", 6253, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strategic.Adjacent || !strategic.ExplicitConnection || strategic.AdjacencyKind != "underground_gateway" || len(strategic.StrategicPassages) != 1 {
		t.Fatalf("expected separate underground strategic gateway, got %+v", strategic)
	}
	var lakeBodies, lakeMembers int
	if err := db.sql.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM map_water_bodies WHERE kind='lake'`).Scan(&lakeBodies); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM map_water_body_provinces WHERE province_id=3`).Scan(&lakeMembers); err != nil {
		t.Fatal(err)
	}
	if lakeBodies != 1 || lakeMembers != 1 {
		t.Fatalf("expected one connected fixture lake body containing province 3, got bodies=%d members=%d", lakeBodies, lakeMembers)
	}
	var dominant string
	if err := db.sql.QueryRowContext(context.Background(), `SELECT m.material_id FROM map_province_materials p JOIN map_surface_materials m ON m.material_index=p.material_index WHERE p.province_id=1 AND p.material_rank=1`).Scan(&dominant); err != nil {
		t.Fatal(err)
	}
	if dominant != "fixture_grass" {
		t.Fatalf("expected fixture_grass as province 1 dominant surface material, got %q", dominant)
	}
	provinceInfo, err := db.LLMMapProvinceInfo(context.Background(), "1", 6253, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if provinceInfo.Surface == nil || !provinceInfo.Surface.Available || provinceInfo.Surface.DominantMaterialID != "fixture_grass" || len(provinceInfo.Surface.Materials) != 1 {
		t.Fatalf("province info did not expose its observed surface material: %+v", provinceInfo.Surface)
	}
	material := provinceInfo.Surface.Materials[0]
	if material.WeightShare != 1 || !material.Diffuse.Resolved || material.Diffuse.ResolvedPath != "gfx/map/terrain/grass.dds" || !material.Mask.Resolved || material.Mask.ResolvedPath != "gfx/map/terrain/grass_mask.png" {
		t.Fatalf("surface material resources or weights were not resolved: %+v", material)
	}
	if provinceInfo.Surface.Source.Provenance != "observed" || !strings.Contains(provinceInfo.Summary, "fixture_grass") {
		t.Fatalf("surface provenance or summary missing: %+v summary=%q", provinceInfo.Surface.Source, provinceInfo.Summary)
	}
	tinySurface, err := db.mapSurfaceProvinceContext(context.Background(), 5, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !tinySurface.Available || tinySurface.SampleCount < 1 || tinySurface.DominantMaterialID != "fixture_rock" {
		t.Fatalf("tiny province fallback sampling failed: %+v", tinySurface)
	}
	if tinySurface.Source.Confidence >= 1 || len(tinySurface.Guidance) < 3 {
		t.Fatalf("sparse surface sampling was not disclosed: %+v", tinySurface)
	}

	title, err := db.LLMMapTitleContext(context.Background(), "k_k11", 6253, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if title.Title.Holder != "char72" || title.Counts["provinces"] != 3 || len(title.Terrains) == 0 {
		t.Fatalf("unexpected k_k11 context: %+v", title)
	}
	if title.Buildings.EmptySpecialSlots != 1 || title.Buildings.OccupiedSpecialSlots != 1 || len(title.HolySites) != 1 || len(title.Visual.Points) == 0 {
		t.Fatalf("expected building/holy-site/visual title context, got buildings=%+v holy_sites=%+v visual=%+v", title.Buildings, title.HolySites, title.Visual)
	}

	candidates, err := db.LLMMapBuildingCandidates(context.Background(), "k_k11", 6253, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates.Candidates) == 0 || candidates.Candidates[0].Province.ProvinceID != 1 || !candidates.Candidates[0].EmptySpecialSlot {
		t.Fatalf("expected province 1 to lead building candidates via empty special slot, got %+v", candidates.Candidates)
	}

	relPlan, err := db.LLMMapAssignmentPlan(context.Background(), "religion", "k_empty", 6253, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(relPlan.PatchFiles) != 1 || !strings.Contains(relPlan.PatchFiles[0].Content, "4 = {") || !strings.Contains(relPlan.PatchFiles[0].Content, "faith_old") {
		t.Fatalf("expected religion patch preview for province 4, got %+v", relPlan.PatchFiles)
	}

	charPlan, err := db.LLMMapAssignmentPlan(context.Background(), "characters", "k_empty", 6253, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(charPlan.PatchFiles) != 2 || !strings.Contains(charPlan.PatchFiles[0].Content, "map_context_c_c200_6253") {
		t.Fatalf("expected character patch preview for c_c200, got %+v", charPlan.PatchFiles)
	}
	if _, err := os.Stat(filepath.Join(dir, "project", "history", "characters", "zz_map_context_generated_characters.txt")); !os.IsNotExist(err) {
		t.Fatalf("map_assignment_plan must not write generated character files")
	}
}

func TestMapIntegrityWarningsAreDeterministicAndRendered(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	path := filepath.Join(dir, "project", "common", "landed_titles", "00_landed_titles.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), "b_b420 = { province = 1 }", "b_b420 = { province = 1 }\n\t\t\t\tb_b420 = { province = 2 }\n\t\t\t\tb_conflict = { province = 1 }", 1)
	if err := os.WriteFile(path, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var selected string
	if err := db.sql.QueryRowContext(context.Background(), `SELECT barony FROM map_provinces WHERE province_id=1`).Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if selected != "b_b420" {
		t.Fatalf("map cache selected %q, want earliest deterministic b_b420", selected)
	}
	title, err := db.LLMMapTitleContext(context.Background(), "c_c114", 6253, LLMOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if title.IntegrityStatus != "warning" || len(title.IntegrityIssues) == 0 {
		t.Fatalf("map title context hid integrity conflicts: %+v", title)
	}
	spec := MapRenderSpec{Title: "Integrity fixture", Target: "e_test", Year: 6253, Width: 640,
		Layers: []MapRenderLayer{{Type: "fill", Metric: &MapMetricSpec{Recipe: "development_network"}, Palette: "development", Classes: 5}}}
	first, err := db.LLMMapRender(context.Background(), spec, LLMOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.LLMMapRender(context.Background(), spec, LLMOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if first.IntegrityStatus != "warning" || first.LayerCounts["integrity_conflicts"] == 0 || len(first.IntegrityIssues) == 0 {
		t.Fatalf("render did not expose integrity overlay: %+v", first)
	}
	if !bytes.Equal(first.PNG, second.PNG) {
		t.Fatal("integrity overlay must remain deterministic")
	}
	foundLegend := false
	for _, item := range first.Legend {
		foundLegend = foundLegend || item.Color == "#ff00ff"
	}
	if !foundLegend {
		t.Fatalf("integrity conflict legend missing: %+v", first.Legend)
	}
}
