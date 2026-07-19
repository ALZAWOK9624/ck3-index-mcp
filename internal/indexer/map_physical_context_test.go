package indexer

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhysicalContextSeparatesOceanLakeAndMajorRiver(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	defaultMap := filepath.Join(dir, "project", "map_data", "default.map")
	if err := os.WriteFile(defaultMap, []byte("sea_zones = { 4 }\nlakes = { 3 }\nriver_provinces = { 5 }\n"), 0644); err != nil {
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

	result, err := db.LLMMapPhysicalContext(context.Background(), MapPhysicalContextSpec{TargetType: "all", Operation: "summary"}, LLMOptions{Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "degraded" || result.Sidecar == nil || result.Sidecar.Available {
		t.Fatalf("expected strict no-sidecar degradation, got status=%s sidecar=%+v", result.Status, result.Sidecar)
	}
	if result.Aggregate.OceanProvinces != 1 || result.Aggregate.LakeProvinces != 1 || result.Aggregate.MajorRiverProvinces != 1 {
		t.Fatalf("unexpected water/river aggregate: %+v", result.Aggregate)
	}
	if result.Aggregate.WaterBodyCount != 2 || len(result.Aggregate.WaterBodyIDs) != 2 {
		t.Fatalf("unexpected bounded water-body summary: %+v", result.Aggregate)
	}
	if result.Surface == nil || !result.Surface.Available || result.Surface.DominantMaterialID == "" {
		t.Fatalf("physical summary omitted observed surface materials: %+v", result.Surface)
	}
	if !strings.Contains(strings.ToLower(result.Summary), "normalized") {
		t.Fatalf("physical summary omitted relative-unit warning: %q", result.Summary)
	}
	for _, source := range result.Sources {
		if strings.Contains(strings.ToLower(source.Unit), "metre") || strings.Contains(source.Unit, "米") {
			t.Fatalf("physical source claimed real vertical units: %+v", source)
		}
	}
	surface, err := db.LLMMapPhysicalContext(context.Background(), MapPhysicalContextSpec{TargetType: "province", Target: "1", Operation: "surface"}, LLMOptions{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if surface.Status != "ready" || surface.Sidecar != nil || surface.Aggregate != nil || surface.Surface == nil || surface.Surface.DominantMaterialID != "fixture_grass" || len(surface.Provinces) != 0 {
		t.Fatalf("surface-only context should use cached observed material facts without WhiteboxTools: %+v", surface)
	}
	surfaceJSON, err := json.Marshal(surface)
	if err != nil {
		t.Fatal(err)
	}
	surfaceText := string(surfaceJSON)
	for _, unrelated := range []string{`"aggregate"`, `"sidecar"`, `"provinces"`, `"terrain"`, `"hydrology"`, `"oceanography"`, `"heightmap.png"`, `"map_gis_v1"`} {
		if strings.Contains(surfaceText, unrelated) {
			t.Fatalf("surface-only JSON retained unrelated GIS field %s: %s", unrelated, surfaceText)
		}
	}
	if len(surface.Sources) != 1 || surface.Sources[0].Provenance != "observed" {
		t.Fatalf("surface-only sources should contain only observed paint evidence: %+v", surface.Sources)
	}

	ocean, err := db.LLMMapPhysicalContext(context.Background(), MapPhysicalContextSpec{TargetType: "province", Target: "4", Operation: "oceanography"}, LLMOptions{Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(ocean.Provinces) != 1 || ocean.Provinces[0].Oceanography == nil || ocean.Provinces[0].Oceanography.RelativeDepthMean == nil || ocean.Provinces[0].Oceanography.WaterBodyKind != "ocean" {
		t.Fatalf("expected relative ocean bathymetry, got %+v", ocean.Provinces)
	}
	allOcean, err := db.LLMMapPhysicalContext(context.Background(), MapPhysicalContextSpec{TargetType: "all", Operation: "oceanography"}, LLMOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(allOcean.Provinces) != 1 || allOcean.Provinces[0].Oceanography == nil || allOcean.Provinces[0].Oceanography.RelativeDepthMean == nil {
		t.Fatalf("all-oceanography query returned an irrelevant land representative: %+v", allOcean.Provinces)
	}
	lake, err := db.LLMMapPhysicalContext(context.Background(), MapPhysicalContextSpec{TargetType: "province", Target: "3", Operation: "oceanography"}, LLMOptions{Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if got := lake.Provinces[0].Oceanography.SurfaceMethod; got != "local_lake_boundary_median" {
		t.Fatalf("lake surface method = %q, want local reference", got)
	}
	metric, err := db.LLMMapBuildMetric(context.Background(), MapMetricSpec{Recipe: "relative_bathymetry", Target: "4", Level: "province"}, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(metric.Values) != 1 || metric.Provenance != "derived" {
		t.Fatalf("relative bathymetry metric did not use indexed GIS facts: %+v", metric)
	}
	allDepths, err := db.LLMMapBuildMetric(context.Background(), MapMetricSpec{Recipe: "relative_bathymetry", Target: "all", Level: "province"}, LLMOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(allDepths.Values) != 2 || allDepths.Stats.Missing != 3 {
		t.Fatalf("relative bathymetry should leave non-water provinces missing, got stats=%+v values=%+v", allDepths.Stats, allDepths.Values)
	}
	river, err := db.LLMMapPhysicalContext(context.Background(), MapPhysicalContextSpec{TargetType: "province", Target: "5", Operation: "hydrology"}, LLMOptions{Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if !river.Provinces[0].Hydrology.MajorRiver || river.Provinces[0].Oceanography != nil {
		t.Fatalf("major river was not isolated from oceanography: %+v", river.Provinces[0])
	}
	var memberships int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM map_physical_water_body_provinces WHERE province_id=5`).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 0 {
		t.Fatalf("river_provinces member appeared in bathymetric water body")
	}

	province, err := db.LLMMapProvinceInfo(context.Background(), "4", 1, LLMOptions{Limit: 8})
	if err != nil || province.Physical == nil {
		t.Fatalf("map_province_info did not expose physical summary: physical=%+v err=%v", province.Physical, err)
	}
	title, err := db.LLMMapTitleContext(context.Background(), "k_empty", 1, LLMOptions{Limit: 8})
	if err != nil || title.Physical == nil || title.Physical.OceanProvinces != 1 {
		t.Fatalf("map_title_context did not aggregate physical summary: physical=%+v err=%v", title.Physical, err)
	}
}

func TestPhysicalContextRegionIncludesAdjacentWaterInOneQuery(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "project", "map_data", "default.map"), []byte("sea_zones = { 4 }\nlakes = { 3 }\nriver_provinces = { 5 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	regions := `coast_region = { provinces = { 2 } }
nested_coast_region = { regions = { coast_region } }
water_region = { provinces = { 3 4 5 } }
test_region = { provinces = { 1 } }
`
	if err := os.WriteFile(filepath.Join(dir, "project", "map_data", "geographical_regions.txt"), []byte(regions), 0644); err != nil {
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

	query := func(spec MapPhysicalContextSpec) MapPhysicalContextResult {
		t.Helper()
		result, err := db.LLMMapPhysicalContext(context.Background(), spec, LLMOptions{Limit: 6})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	coast := query(MapPhysicalContextSpec{TargetType: "region", Target: "coast_region", Operation: "oceanography", IncludeAdjacentWater: true})
	if coast.Aggregate.ProvinceCount != 1 || coast.AdjacentWater == nil {
		t.Fatalf("region target did not preserve selected aggregate and adjacent result: %+v", coast)
	}
	adjacent := coast.AdjacentWater
	if adjacent.SelectionMode != "adjacent_to_selected_land" || adjacent.CoastalLandCount != 1 {
		t.Fatalf("unexpected coastal selection: %+v", adjacent)
	}
	if adjacent.WaterProvinceCount != 3 || adjacent.Ocean.ProvinceCount != 1 || adjacent.LakeProvinceCount != 1 || adjacent.MajorRiverCount != 1 {
		t.Fatalf("ocean, lake, and major river were not isolated: %+v", adjacent)
	}
	if adjacent.DepthClassification.Verdict != "predominantly_shallow" || adjacent.DepthClassification.Unit != "relative map depth" {
		t.Fatalf("unexpected relative-depth verdict: %+v", adjacent.DepthClassification)
	}
	if len(adjacent.Representatives) != 1 || adjacent.Representatives[0].ProvinceID != 4 {
		t.Fatalf("representatives should contain only the adjacent ocean: %+v", adjacent.Representatives)
	}

	prefixed := query(MapPhysicalContextSpec{Target: "region:nested_coast_region", Operation: "oceanography", IncludeAdjacentWater: true})
	if prefixed.TargetType != "region" || prefixed.AdjacentWater == nil || prefixed.AdjacentWater.Ocean.ProvinceCount != 1 {
		t.Fatalf("region prefix or nested region expansion failed: %+v", prefixed)
	}
	mixed := query(MapPhysicalContextSpec{TargetType: "targets", Targets: []string{"region:coast_region", "1"}, Operation: "summary", IncludeAdjacentWater: true})
	if mixed.Aggregate.ProvinceCount != 2 || mixed.AdjacentWater == nil || mixed.AdjacentWater.Ocean.ProvinceCount != 1 {
		t.Fatalf("mixed target selection failed: %+v", mixed)
	}
	selectedWater := query(MapPhysicalContextSpec{TargetType: "region", Target: "water_region", Operation: "oceanography", IncludeAdjacentWater: true})
	if selectedWater.AdjacentWater == nil || selectedWater.AdjacentWater.SelectionMode != "selected_water" || selectedWater.AdjacentWater.WaterProvinceCount != 3 {
		t.Fatalf("water-only region did not use selected-water fallback: %+v", selectedWater.AdjacentWater)
	}

	// The fixture has an explicit adjacencies.csv sea route from province 1 to 4,
	// but no pixel border. It must remain a gameplay passage, not a physical coast.
	nonCoast := query(MapPhysicalContextSpec{TargetType: "region", Target: "test_region", Operation: "oceanography", IncludeAdjacentWater: true})
	if nonCoast.AdjacentWater == nil || nonCoast.AdjacentWater.WaterProvinceCount != 0 {
		t.Fatalf("strategic adjacency was incorrectly treated as physical coastline: %+v", nonCoast.AdjacentWater)
	}
}

func TestPhysicalAdjacentWaterUsesSampleWeightedTertiles(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "project", "map_data", "geographical_regions.txt"), []byte("coast_region = { provinces = { 2 } }\n"), 0644); err != nil {
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
	if _, err := db.sql.Exec(`UPDATE map_provinces SET block_kind='water',water_kind='sea' WHERE province_id IN (3,4,5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE map_province_physical SET major_river=0,
		sample_count=CASE province_id WHEN 3 THEN 70 WHEN 4 THEN 20 ELSE 10 END,
		relative_depth_mean=CASE province_id WHEN 3 THEN 0.1 WHEN 4 THEN 0.5 ELSE 0.9 END,
		relative_depth_max=CASE province_id WHEN 3 THEN 0.2 WHEN 4 THEN 0.6 ELSE 1.0 END
		WHERE province_id IN (3,4,5)`); err != nil {
		t.Fatal(err)
	}
	result, err := db.LLMMapPhysicalContext(context.Background(), MapPhysicalContextSpec{
		TargetType: "region", Target: "coast_region", Operation: "oceanography", IncludeAdjacentWater: true,
	}, LLMOptions{Limit: 6})
	if err != nil {
		t.Fatal(err)
	}
	if result.AdjacentWater == nil {
		t.Fatal("missing adjacent-water result")
	}
	if result.AdjacentWater.DepthClassification.Verdict != "predominantly_shallow" || math.Abs(result.AdjacentWater.DepthClassification.ShallowShare-0.70) > 1e-9 {
		t.Fatalf("area-weighted coast verdict is wrong: %+v", result.AdjacentWater.DepthClassification)
	}
	if result.AdjacentWater.Ocean.RelativeDepthMean == nil || math.Abs(*result.AdjacentWater.Ocean.RelativeDepthMean-0.26) > 1e-9 {
		t.Fatalf("area-weighted relative depth is wrong: %+v", result.AdjacentWater.Ocean)
	}
	values := []mapWeightedPhysicalValue{{Value: 0.1, Weight: 70}, {Value: 0.5, Weight: 20}, {Value: 0.9, Weight: 10}}
	if got := weightedPhysicalMean(values); got == nil || math.Abs(*got-0.26) > 1e-9 {
		t.Fatalf("weighted mean = %v, want 0.26", got)
	}
	if got := weightedPhysicalQuantile(values, 0.33); got == nil || *got != 0.1 {
		t.Fatalf("weighted P33 = %v, want 0.1", got)
	}
	if got := weightedPhysicalQuantile(values, 0.90); got == nil || *got != 0.5 {
		t.Fatalf("weighted P90 = %v, want 0.5", got)
	}
}

func TestPhysicalRegionCoastQueryPlanUsesAdjacencyIndex(t *testing.T) {
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
	details := explainDetails(t, db, `EXPLAIN QUERY PLAN
		WITH selected(province_id) AS (
			SELECT province_id FROM map_province_regions WHERE region_id='test_region'
		), selected_land(province_id) AS (
			SELECT s.province_id FROM selected s
			JOIN map_provinces p ON p.province_id=s.province_id
			JOIN map_province_physical g ON g.province_id=s.province_id
			WHERE COALESCE(p.block_kind,'')<>'water' AND g.major_river=0
		)
		SELECT a.neighbor_id,SUM(a.border_len)
		FROM selected_land sl
		JOIN map_adjacencies a ON a.province_id=sl.province_id
		JOIN map_provinces wp ON wp.province_id=a.neighbor_id
		JOIN map_province_physical wg ON wg.province_id=a.neighbor_id
		WHERE COALESCE(wp.block_kind,'')='water' OR wg.major_river=1
		GROUP BY a.neighbor_id`)
	if strings.Contains(details, "SCAN a") {
		t.Fatalf("coast query should use the province-first adjacency index, got %s", details)
	}
	if !strings.Contains(details, "map_adjacencies") && !strings.Contains(details, "sqlite_autoindex_map_adjacencies") {
		t.Fatalf("coast query plan omitted indexed adjacency lookup: %s", details)
	}
}

func TestPhysicalMetricCatalogAndUnavailableHydrology(t *testing.T) {
	catalog := MapRecipeCatalog()
	for _, required := range []string{"elevation", "relative_depth", "seabed_slope", "catchment_area", "ocean_basin_id"} {
		if !containsString(catalog.Fields, required) {
			t.Fatalf("physical metric field %q missing from catalog", required)
		}
	}
	found := map[string]bool{}
	for _, recipe := range catalog.Recipes {
		found[recipe.ID] = true
	}
	for _, required := range []string{"elevation_relief", "composite_rivers", "relative_bathymetry", "continental_shelf", "ocean_basins"} {
		if !found[required] {
			t.Fatalf("physical recipe %q missing", required)
		}
	}
}

func TestGISSidecarRequiresPinnedHash(t *testing.T) {
	status := InspectGISSidecar(context.Background(), Config{GISEnabled: true, GISAnalysis: "full", GISSidecarPath: filepath.Join(t.TempDir(), "missing")})
	if status.Available || !strings.Contains(status.Reason, "SHA-256") {
		t.Fatalf("sidecar without pinned hash must be unavailable: %+v", status)
	}
	status = InspectGISSidecar(context.Background(), Config{GISEnabled: false, GISAnalysis: "terrain"})
	if status.Available || status.Reason == "" {
		t.Fatalf("disabled sidecar must explain unavailability: %+v", status)
	}
}

func TestGISSidecarStatusReportsPersistedAnalysisCache(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	cfg.GISEnabled = true
	cfg.GISAnalysis = "full"
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.sql.Exec(`INSERT INTO meta(key,value) VALUES
		('map_gis_analysis','full'),
		('map_gis_advanced_status','ready')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		t.Fatal(err)
	}
	status := db.GISSidecarStatus(context.Background(), cfg)
	if status.Analysis != "full" || status.AnalysisStatus != "ready" {
		t.Fatalf("health status did not expose the persisted GIS cache: %+v", status)
	}
	cfg.GISAnalysis = "terrain"
	status = db.GISSidecarStatus(context.Background(), cfg)
	if status.AnalysisStatus != "stale" {
		t.Fatalf("mismatched configured and cached GIS modes should be stale: %+v", status)
	}
}

func TestWhiteboxRasterRoundTripAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.dep")
	values := []float32{0.1, 0.2, whiteboxNoData, 0.4, 0.5, 0.6}
	if err := writeWhiteboxRaster(path, 3, 2, values, whiteboxNoData); err != nil {
		t.Fatal(err)
	}
	raster, err := readWhiteboxRaster(path)
	if err != nil {
		t.Fatal(err)
	}
	if raster.Cols != 3 || raster.Rows != 2 || len(raster.Values) != len(values) || raster.Values[2] != float64(whiteboxNoData) {
		t.Fatalf("unexpected Whitebox raster round trip: %+v", raster)
	}
	tasPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".tas"
	extra, err := os.OpenFile(tasPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extra.Write([]byte{0}); err != nil {
		_ = extra.Close()
		t.Fatal(err)
	}
	if err := extra.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readWhiteboxRaster(path); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("oversized Whitebox raster was not rejected before decoding: %v", err)
	}
	root := t.TempDir()
	if err := ensureContainedGISDir(root, filepath.Join(root, "safe")); err != nil {
		t.Fatal(err)
	}
	if err := ensureContainedGISDir(root, filepath.Join(root, "..", "escape")); err == nil {
		t.Fatal("GIS cache containment accepted path traversal")
	}
}

func TestWhiteboxToolsIntegration(t *testing.T) {
	if os.Getenv("CK3_INDEX_WBT_INTEGRATION") != "1" {
		t.Skip("set CK3_INDEX_WBT_INTEGRATION=1 with a pinned local WhiteboxTools binary")
	}
	path := os.Getenv("CK3_INDEX_GIS_SIDECAR_PATH")
	hash := os.Getenv("CK3_INDEX_GIS_SIDECAR_SHA256")
	if path == "" || hash == "" {
		t.Fatal("integration test requires CK3_INDEX_GIS_SIDECAR_PATH and CK3_INDEX_GIS_SIDECAR_SHA256")
	}
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	cfg.GISEnabled = true
	cfg.GISAnalysis = "full"
	cfg.GISSidecarPath = path
	cfg.GISSidecarSHA256 = hash
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if status := db.cachedGISSidecarStatus(context.Background()); !status.Available || status.AnalysisStatus != "ready" {
		t.Fatalf("verified WhiteboxTools analysis did not complete: %+v", status)
	}
	var populated int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM map_province_physical WHERE catchment_pixels IS NOT NULL`).Scan(&populated); err != nil {
		t.Fatal(err)
	}
	if populated == 0 {
		t.Fatal("WhiteboxTools flow accumulation produced no province facts")
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM map_province_physical WHERE river_order IS NOT NULL`).Scan(&populated); err != nil {
		t.Fatal(err)
	}
	if populated == 0 {
		t.Fatal("verified D8 catchments produced no bounded river-order proxies")
	}
}
