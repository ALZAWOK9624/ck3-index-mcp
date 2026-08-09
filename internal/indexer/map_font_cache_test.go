package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

func writeTestFont(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A CJK font is tens of megabytes and every map_render re-read and re-parsed
// it. Parsing once per file identity is the point of the cache.
func TestMapFontIsParsedOncePerIdentity(t *testing.T) {
	mapFontCache.Lock()
	mapFontCache.identity, mapFontCache.parsed = "", nil
	mapFontCache.Unlock()

	dir := t.TempDir()
	path := writeTestFont(t, dir, "test.ttf")

	first, warnings := parseMapFont(path)
	if first == nil {
		t.Fatalf("font did not parse: %v", warnings)
	}
	second, _ := parseMapFont(path)
	if second != first {
		t.Fatal("a second load of the same font reparsed it instead of reusing the cache")
	}
}

// Replacing the font on disk has to be picked up without a restart, or a
// deployment that swaps the font silently keeps rendering with the old one.
func TestMapFontCacheFollowsTheFileIdentity(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFont(t, dir, "test.ttf")

	identity, ok := mapFontIdentity(path)
	if !ok {
		t.Fatal("an existing font has no identity")
	}
	if stable, _ := mapFontIdentity(path); stable != identity {
		t.Fatal("identity is not stable for an unchanged file")
	}

	// A different path is a different identity even with identical bytes.
	other := writeTestFont(t, dir, "other.ttf")
	if otherIdentity, _ := mapFontIdentity(other); otherIdentity == identity {
		t.Fatal("two different font paths share one identity")
	}

	if _, missing := mapFontIdentity(filepath.Join(dir, "absent.ttf")); missing {
		t.Fatal("a missing font reported an identity")
	}
}

// A font that cannot be parsed must not be cached as a success, and must still
// degrade to hidden labels rather than failing the render.
func TestUnparseableMapFontIsNotCached(t *testing.T) {
	mapFontCache.Lock()
	mapFontCache.identity, mapFontCache.parsed = "", nil
	mapFontCache.Unlock()

	dir := t.TempDir()
	path := filepath.Join(dir, "broken.ttf")
	if err := os.WriteFile(path, []byte("this is not a font"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, warnings := parseMapFont(path)
	if parsed != nil {
		t.Fatal("a broken font parsed successfully")
	}
	if len(warnings) == 0 {
		t.Fatal("a broken font produced no warning")
	}
	mapFontCache.Lock()
	cached := mapFontCache.parsed
	mapFontCache.Unlock()
	if cached != nil {
		t.Fatal("a failed parse was pinned in the cache")
	}

	renderer, rendererWarnings := loadMapTextRenderer(path)
	if renderer == nil || renderer.parsed != nil {
		t.Fatal("a broken font did not degrade to hidden labels")
	}
	if len(rendererWarnings) == 0 {
		t.Fatal("a broken font produced no renderer warning")
	}
}
