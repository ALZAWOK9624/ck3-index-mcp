package indexer

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestSearchProfile is an opt-in real-index profiler used by release checks.
func TestSearchProfile(t *testing.T) {
	path := os.Getenv("CK3_INDEX_PROFILE_DB")
	if path == "" {
		t.Skip("set CK3_INDEX_PROFILE_DB")
	}
	db, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	q := "Abs_CFixedPoint"
	p := escapeLike(q) + "%"
	opts := SearchOptions{}
	limit := 8
	checks := []struct {
		name string
		fn   func() error
	}{
		{"objects", func() error { _, e := db.searchObjects(ctx, q, p, opts, limit); return e }},
		{"refs", func() error { _, e := db.searchRefs(ctx, q, p, opts, limit); return e }},
		{"loc_keys", func() error { _, e := db.searchLocalizationKeys(ctx, q, p, opts, limit); return e }},
		{"resources", func() error { _, e := db.searchResources(ctx, q, p, opts, limit); return e }},
		{"diagnostics", func() error { _, e := db.searchDiagnostics(ctx, q, p, opts, limit); return e }},
		{"fields", func() error { _, e := db.searchScriptKeys(ctx, q, p, opts, limit); return e }},
		{"datatypes", func() error { _, e := db.searchDatatypes(ctx, q, p, opts, limit); return e }},
		{"fts", func() error { _, e := db.searchFTS(ctx, q, opts, limit); return e }},
	}
	for _, c := range checks {
		s := time.Now()
		if err := c.fn(); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s=%s", c.name, time.Since(s))
	}
}
