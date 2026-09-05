//go:build ck3_native && cgo && windows && amd64

package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Keep these fixtures independent of the live index and OS disk-cache state:
// file benchmarks measure repeated (warm) reads, including identity checks.
func BenchmarkNativeFileHashSizes(b *testing.B) {
	if !nativeSHAAvailable() {
		b.Skip("CPU has no SHA-NI")
	}
	for _, size := range []int{0, 4096, 64 << 10, 1 << 20, 32 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			data := bytesOf(size, 0x5a)
			want := nativeReferenceSum(data)
			path := filepath.Join(b.TempDir(), "hash.bin")
			if err := os.WriteFile(path, data, 0600); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, _, err := sha256FileHex(path)
				if err != nil || got != want {
					b.Fatalf("hash mismatch: %s, %v", got, err)
				}
			}
		})
	}
}

func BenchmarkNativeBytesHashSizes(b *testing.B) {
	for _, size := range []int{0, 64, 1024, 16 << 10, 1 << 20} {
		data := bytesOf(size, 0x5a)
		b.Run(fmt.Sprintf("%d/go", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				digest := sha256.Sum256(data)
				benchmarkSHASink = hex.EncodeToString(digest[:])
			}
		})
		b.Run(fmt.Sprintf("%d/native", size), func(b *testing.B) {
			if !nativeSHAAvailable() {
				b.Skip("CPU has no SHA-NI")
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchmarkSHASink, _ = sha256BytesHex(data)
			}
		})
	}
}

func BenchmarkNativeReliefSizes(b *testing.B) {
	for _, size := range []int{16, 512, 2048} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			img := syntheticHeightmap(size, size)
			b.SetBytes(int64(size * size * 2))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buildMultiScaleRelief(img)
			}
		})
	}
}
