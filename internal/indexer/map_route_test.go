package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
)

var oraispolAsiupoliSeaRoute = []int{8142, 8293, 8139, 8137, 8136, 8135, 8127, 8134, 8189, 8192, 8363, 8226, 8208, 8209, 8210, 8211, 8212, 8217, 8218}

func openMapRouteFixture(t testing.TB) (*DB, Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "route.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(1,'fixture',1,'loc.yml','localization/loc.yml','localization',0,'fixture',0)`); err != nil {
		t.Fatal(err)
	}
	insertProvince := func(id int, x, y float64, water, barony, county, duchy string, capital int) {
		t.Helper()
		_, err := db.sql.ExecContext(ctx, `INSERT INTO map_provinces(province_id,center_x,center_y,min_x,min_y,max_x,max_y,area,blocked,block_kind,water_kind,terrain,barony,county,duchy,kingdom,empire,is_county_capital)
			VALUES(?,?,?,?,?,?,?,100,0,'',?,'plains',?,?,?,'k_fixture','e_fixture',?)`, id, x, y, int(x), int(y), int(x)+4, int(y)+4, water, barony, county, duchy, capital)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO map_province_geometry(province_id,fill_rle,boundary_rle) VALUES(?,?,?)`, id, []byte{1}, []byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	insertProvince(1911, 0, 200, "", "b_oraispol", "c_oraispol", "d_origin", 1)
	insertProvince(1302, 1163.64, 200, "", "b_asiupoli", "c_asiupoli", "d_destination", 1)
	step := 1850.43 / float64(len(oraispolAsiupoliSeaRoute)-1)
	for index, id := range oraispolAsiupoliSeaRoute {
		insertProvince(id, float64(index)*step, 0, "sea", "", "", "", 0)
	}
	for _, title := range []struct {
		id, kind, parent string
		province         int
	}{
		{"b_oraispol", "b", "c_oraispol", 1911}, {"c_oraispol", "c", "d_origin", 1911},
		{"b_asiupoli", "b", "c_asiupoli", 1302}, {"c_asiupoli", "c", "d_destination", 1302},
		{"d_origin", "d", "k_fixture", 1911}, {"d_destination", "d", "k_fixture", 1302},
		{"k_fixture", "k", "e_fixture", 1911}, {"e_fixture", "e", "", 1911},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO map_titles(title_id,title_type,parent_id,province_id,province_count) VALUES(?,?,?,?,1)`, title.id, title.kind, title.parent, title.province); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.ExecContext(ctx, `INSERT OR IGNORE INTO map_title_provinces(title_id,province_id) VALUES(?,?)`, title.id, title.province); err != nil {
			t.Fatal(err)
		}
	}
	for _, loc := range []struct{ key, language, value string }{
		{"b_oraispol", "english", "Oraispol"}, {"b_oraispol", "simp_chinese", "俄赖斯波尔"},
		{"c_oraispol", "english", "Oraispol"}, {"c_oraispol", "simp_chinese", "俄赖斯波尔"},
		{"b_asiupoli", "english", "Asiupoli"}, {"b_asiupoli", "simp_chinese", "阿西乌波利"},
		{"c_asiupoli", "english", "Asiupoli"}, {"c_asiupoli", "simp_chinese", "阿西乌波利"},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,?,1,'fixture',1,'loc.yml',1,0)`, loc.key, loc.language, loc.value); err != nil {
			t.Fatal(err)
		}
	}
	insertEdge := func(from, to int) {
		t.Helper()
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO map_adjacencies(province_id,neighbor_id,border_len,blocked) VALUES(?,?,10,0)`, from, to); err != nil {
			t.Fatal(err)
		}
	}
	// Store one direction only to verify that route loading treats the topology
	// as undirected even when a cache generation stores one edge per pair.
	insertEdge(1911, oraispolAsiupoliSeaRoute[0])
	for index := 1; index < len(oraispolAsiupoliSeaRoute); index++ {
		insertEdge(oraispolAsiupoliSeaRoute[index-1], oraispolAsiupoliSeaRoute[index])
	}
	insertEdge(oraispolAsiupoliSeaRoute[len(oraispolAsiupoliSeaRoute)-1], 1302)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES
		('map_width','2048'),('map_height','512'),('map_geometry_fingerprint','route-fixture'),
		('index_rule_version',?),('scan_status','ready')`, indexRuleVersion); err != nil {
		t.Fatal(err)
	}
	return db, Config{ConfigPath: filepath.Join(filepath.Dir(path), "ck3-index.toml"), Database: filepath.Base(path)}
}

func TestResolveMapSubjectVariants(t *testing.T) {
	db, _ := openMapRouteFixture(t)
	ctx := context.Background()
	for _, input := range []string{"俄赖斯波尔", "Oraispol", "b_oraispol", "c_oraispol", "1911"} {
		resolved, err := db.ResolveMapSubject(ctx, input, 6254)
		if err != nil {
			t.Fatalf("ResolveMapSubject(%q): %v", input, err)
		}
		if resolved.ProvinceID != 1911 {
			t.Fatalf("ResolveMapSubject(%q) province = %d, want 1911", input, resolved.ProvinceID)
		}
	}
	for _, input := range []string{"阿西乌波利", "Asiupoli", "b_asiupoli", "c_asiupoli", "1302"} {
		resolved, err := db.ResolveMapSubject(ctx, input, 6254)
		if err != nil {
			t.Fatalf("ResolveMapSubject(%q): %v", input, err)
		}
		if resolved.ProvinceID != 1302 {
			t.Fatalf("ResolveMapSubject(%q) province = %d, want 1302", input, resolved.ProvinceID)
		}
	}
}

func TestResolveMapSubjectAmbiguityReturnsCandidates(t *testing.T) {
	db, _ := openMapRouteFixture(t)
	ctx := context.Background()
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO map_provinces(province_id,center_x,center_y,min_x,min_y,max_x,max_y,area,blocked,block_kind,water_kind,terrain,barony,county,duchy,kingdom,empire,is_county_capital)
		VALUES(2000,40,240,38,238,42,242,100,0,'','','plains','b_other_oraispol','c_other_oraispol','d_origin','k_fixture','e_fixture',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO map_province_geometry(province_id,fill_rle,boundary_rle) VALUES(2000,?,?)`, []byte{1}, []byte{1}); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"b_other_oraispol", "c_other_oraispol"} {
		kind := string(title[0])
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO map_titles(title_id,title_type,province_id,province_count) VALUES(?,?,2000,1)`, title, kind); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,'english','Oraispol',1,'fixture',1,'loc.yml',1,0)`, title); err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.ResolveMapSubject(ctx, "Oraispol", 6254)
	var resolution *MapSubjectResolutionError
	if !errors.As(err, &resolution) || resolution.Code != MapSubjectAmbiguousCode || len(resolution.Candidates) != 2 {
		t.Fatalf("ambiguous subject error = %#v, want two candidates", err)
	}
}

func TestMapRouteOraispolToAsiupoliSea(t *testing.T) {
	db, _ := openMapRouteFixture(t)
	result, err := db.LLMMapRoute(context.Background(), MapRouteSpec{From: "俄赖斯波尔", To: "Asiupoli", Year: 6254, Mode: "sea", Objective: "shortest"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || result.Error != nil {
		t.Fatalf("route not ready: %+v", result.Error)
	}
	if result.ResolvedFrom.ProvinceID != 1911 || result.ResolvedTo.ProvinceID != 1302 {
		t.Fatalf("resolved endpoints = %d -> %d", result.ResolvedFrom.ProvinceID, result.ResolvedTo.ProvinceID)
	}
	got := make([]int, len(result.Path))
	for index, point := range result.Path {
		got[index] = point.ProvinceID
		if point.WaterKind != "sea" {
			t.Fatalf("path point %d is not sea: %+v", index, point)
		}
		if index > 0 && point.AdjacencyFromPrevious != "water_boundary" {
			t.Fatalf("path edge %d kind = %q", index, point.AdjacencyFromPrevious)
		}
	}
	if !reflect.DeepEqual(got, oraispolAsiupoliSeaRoute) {
		t.Fatalf("route = %v, want %v", got, oraispolAsiupoliSeaRoute)
	}
	if math.Abs(result.DistancePixels-1850.43) > 0.02 {
		t.Fatalf("distance = %.2f, want 1850.43", result.DistancePixels)
	}
	if math.Abs(result.DistancePixels-1163.64) < 1 {
		t.Fatal("route used the city-to-city straight-line distance")
	}
}

func TestMapRouteIncompleteDatabaseIsNotNoPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := db.LLMMapRoute(context.Background(), MapRouteSpec{From: "1", To: "2", Mode: "land"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Code != MapDatabaseIncompleteCode {
		t.Fatalf("route error = %+v, want %s", result.Error, MapDatabaseIncompleteCode)
	}
	if result.Error.Code == MapRouteNoPathCode {
		t.Fatal("incomplete database was mislabeled as no path")
	}
	health, err := db.HealthConfigured(context.Background(), Config{ConfigPath: filepath.Join(filepath.Dir(path), "ck3-index.toml"), Database: filepath.Base(path)})
	if err != nil {
		t.Fatal(err)
	}
	if health.MapDatabase.Complete || health.Status != "error" {
		t.Fatalf("health did not report incomplete map data: %+v", health.MapDatabase)
	}
	data, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || health.Database == "" {
		t.Fatal("health fixture did not retain its internal path")
	}
	if containsJSONPath(string(data), health.Database) {
		t.Fatal("health JSON leaked the database absolute path")
	}
	for _, sidecar := range health.WALFiles {
		if containsJSONPath(string(data), sidecar.Path) {
			t.Fatal("health JSON leaked a database sidecar absolute path")
		}
	}
}

func TestHealthReportsBareSQLiteAsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bare.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	health, err := db.HealthConfigured(context.Background(), Config{ConfigPath: filepath.Join(filepath.Dir(path), "ck3-index.toml"), Database: filepath.Base(path)})
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "error" || health.MapDatabase.ErrorCode != MapDatabaseIncompleteCode || len(health.MapDatabase.MissingTables) < len(mapCoreTables) {
		t.Fatalf("bare SQLite health = %+v", health)
	}
}

func TestMapDatabaseRejectsStaleIncompleteAndFinalizingCaches(t *testing.T) {
	ctx := context.Background()
	db, _ := openMapRouteFixture(t)

	if _, err := db.sql.ExecContext(ctx, `UPDATE meta SET value='old-index-version' WHERE key='index_rule_version'`); err != nil {
		t.Fatal(err)
	}
	status, err := db.MapDatabaseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Complete || !status.Stale || status.ExpectedVersion != indexRuleVersion {
		t.Fatalf("stale cache was accepted: %+v", status)
	}
	route, err := db.LLMMapRoute(ctx, MapRouteSpec{From: "1911", To: "1302", Mode: "sea"}, LLMOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if route.Error == nil || route.Error.Code != MapDatabaseIncompleteCode {
		t.Fatalf("stale cache route error = %+v", route.Error)
	}

	if _, err := db.sql.ExecContext(ctx, `UPDATE meta SET value=? WHERE key='index_rule_version'`, indexRuleVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE meta SET value='finalizing' WHERE key='scan_status'`); err != nil {
		t.Fatal(err)
	}
	status, err = db.MapDatabaseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Complete || !status.Finalizing {
		t.Fatalf("unfinished finalization was accepted: %+v", status)
	}

	if _, err := db.sql.ExecContext(ctx, `UPDATE meta SET value='ready' WHERE key='scan_status'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `DROP TABLE map_strategic_adjacencies`); err != nil {
		t.Fatal(err)
	}
	status, err = db.MapDatabaseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Complete || !containsString(status.MissingTables, "map_strategic_adjacencies") {
		t.Fatalf("missing route schema was accepted: %+v", status)
	}
}

func containsJSONPath(jsonText, path string) bool {
	return len(path) > 0 && len(jsonText) >= len(path) && stringContainsFold(jsonText, path)
}

func stringContainsFold(value, needle string) bool {
	return len(needle) > 0 && len(value) >= len(needle) && func() bool {
		for index := 0; index+len(needle) <= len(value); index++ {
			if value[index:index+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}

func BenchmarkMapRouteOraispolToAsiupoli(b *testing.B) {
	db, _ := openMapRouteFixture(b)
	spec := MapRouteSpec{From: "1911", To: "1302", Year: 6254, Mode: "sea", Objective: "shortest"}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result, err := db.LLMMapRoute(context.Background(), spec, LLMOptions{})
		if err != nil || result.Status != "ready" {
			b.Fatalf("route benchmark failed: status=%s err=%v route_error=%+v", result.Status, err, result.Error)
		}
	}
}
