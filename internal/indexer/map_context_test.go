package indexer

import (
	"context"
	"image"
	"image/color"
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
	mustMkdir("common/landed_titles")
	mustMkdir("common/province_terrain")
	mustMkdir("common/religion/holy_sites")
	mustMkdir("common/religion/religions")
	mustMkdir("history/provinces")
	mustMkdir("history/titles")
	mustMkdir("history/characters")

	mustWrite("map_data/definition.csv", "province;red;green;blue\n1;255;0;0\n2;0;255;0\n3;0;0;255\n4;255;255;0\n")
	mustWrite("map_data/default.map", "sea_zones = { 3 }\n")
	mustWrite("map_data/geographical_regions.txt", "test_region = { provinces = { 1 } counties = { c_c200 } }\nparent_region = { regions = { test_region } }\n")
	mustWrite("common/province_terrain/00_province_terrain.txt", "default_land=plains\ndefault_sea=sea\n1=hills\n2=forest\n3=coastal_sea\n")
	img := image.NewRGBA(image.Rect(0, 0, 5, 2))
	colors := map[int]color.RGBA{
		1: {R: 255, A: 255},
		2: {G: 255, A: 255},
		3: {B: 255, A: 255},
		4: {R: 255, G: 255, A: 255},
	}
	grid := [][]int{{1, 1, 2, 4, 4}, {1, 2, 2, 3, 4}}
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

	mustWrite("common/landed_titles/00_landed_titles.txt", `
e_test = {
	k_k11 = {
		d_d33 = {
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
1 = { culture = culture_a religion = faith_old holding = castle_holding special_building_slot = slot_a buildings = { farm_01 barracks_01 } }
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
`)
	mustWrite("history/characters/test_chars.txt", `
char72 = {
	name = "Holder"
	culture = culture_a
	religion = faith_old
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
	if province.Province.Area != 3 || province.Province.BBox.MaxX != 1 || len(province.Neighbors) == 0 {
		t.Fatalf("unexpected province geometry/neighbors: %+v", province.Province)
	}

	water, err := db.LLMMapProvinceInfo(context.Background(), "3", 6253, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !water.Province.Blocked || water.Province.BlockKind != "water" || water.Province.Terrain != "coastal_sea" {
		t.Fatalf("expected province 3 to be water-blocked, got %+v", water.Province)
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
