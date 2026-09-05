//go:build ck3_native && cgo && amd64

package indexer

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/png"
	"os"
	"testing"
)

// Explicit opt-in: ordinary tests never depend on a developer's game install.
func nativeBenchmarkHeightmap(tb testing.TB) image.Image {
	tb.Helper()
	path := os.Getenv("CK3_INDEX_BENCH_HEIGHTMAP")
	if path == "" {
		tb.Skip("set CK3_INDEX_BENCH_HEIGHTMAP to a Gray16 PNG")
	}
	f, err := os.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Logf("heightmap: %v (%T)", img.Bounds(), img)
	return img
}

func TestNativeRealHeightmapMatchesGo(t *testing.T) {
	img := nativeBenchmarkHeightmap(t)
	wh, wd, we := buildMultiScaleReliefGo(img)
	gh, gd, ge := buildMultiScaleRelief(img)
	if !bytes.Equal(wh.Pix, gh.Pix) || !bytes.Equal(wd.Pix, gd.Pix) || !bytes.Equal(we.Pix, ge.Pix) {
		t.Fatal("real heightmap differs from Go")
	}
	t.Logf("hill=%x detail=%x elevation=%x", sha256.Sum256(gh.Pix), sha256.Sum256(gd.Pix), sha256.Sum256(ge.Pix))
}

func BenchmarkNativeRealHeightmap(b *testing.B) {
	img := nativeBenchmarkHeightmap(b)
	b.ReportAllocs()
	b.SetBytes(int64(img.Bounds().Dx() * img.Bounds().Dy() * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildMultiScaleRelief(img)
	}
}
