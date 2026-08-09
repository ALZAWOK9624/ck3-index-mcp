package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCachedGISSidecarStatusDoesNotVerifyConfiguredBinary(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "health.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	trusted := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for key, value := range map[string]string{
		"map_gis_sidecar_enabled":   "true",
		"map_gis_sidecar_available": "true",
		"map_gis_sidecar_platform":  gisPlatform(),
		"map_gis_sidecar_version":   "fixture",
		"map_gis_sidecar_sha256":    trusted,
		"map_gis_analysis":          "terrain",
		"map_gis_advanced_status":   "ready",
	} {
		if _, err := db.sql.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		GISEnabled: true, GISAnalysis: "terrain", GISSidecarSHA256: trusted,
		GISSidecarPath: filepath.Join(t.TempDir(), "missing-sidecar"),
	}

	quick := db.CachedGISSidecarStatus(context.Background(), cfg)
	if !quick.Available || quick.AnalysisStatus != "ready" || quick.SHA256 != trusted {
		t.Fatalf("quick persisted GIS status = %+v", quick)
	}
	deep := db.GISSidecarStatus(context.Background(), cfg)
	if deep.Available {
		t.Fatalf("deep verification accepted a missing configured binary: %+v", deep)
	}
}
