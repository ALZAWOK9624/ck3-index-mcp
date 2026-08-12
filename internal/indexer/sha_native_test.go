//go:build ck3_native && cgo && windows

package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func nativeReferenceSum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestNativeSHA256BytesMatchesGo(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("abc"),
		[]byte("The quick brown fox jumps over the lazy dog"),
		bytesOf(55, 'a'),
		bytesOf(56, 'a'),
		bytesOf(63, 'a'),
		bytesOf(64, 'a'),
		bytesOf(65, 'a'),
		bytesOf(127, 'b'),
		bytesOf(128, 'b'),
		bytesOf(1000, 'c'),
	}
	rng := rand.New(rand.NewSource(42))
	for size := 0; size <= 300; size++ {
		buf := make([]byte, size)
		rng.Read(buf)
		cases = append(cases, buf)
	}
	big := make([]byte, 1<<20)
	rng.Read(big)
	cases = append(cases, big)
	for i, data := range cases {
		got, ok := sha256BytesHex(data)
		if !ok {
			t.Fatalf("case %d: native path unavailable", i)
		}
		want := nativeReferenceSum(data)
		if got != want {
			t.Fatalf("case %d (len %d): native %s, want %s", i, len(data), got, want)
		}
	}
}

func TestNativeSHA256FileMatchesGo(t *testing.T) {
	dir := t.TempDir()
	for i, data := range [][]byte{
		nil,
		[]byte("abc"),
		bytesOf(55, 'a'),
		bytesOf(64, 'b'),
		bytesOf(65, 'c'),
		bytesOf(1<<20, 'd'),
	} {
		path := filepath.Join(dir, "file-"+string(rune('0'+i))+".bin")
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		got, ok, err := sha256FileHex(path)
		if err != nil || !ok {
			t.Fatalf("case %d: ok=%v err=%v", i, ok, err)
		}
		want := nativeReferenceSum(data)
		if got != want {
			t.Logf("case %d (len %d): native %s, want %s", i, len(data), got, want)
			t.Fail()
		}
	}
}

// parseOneFile hashes on up to sixteen worker goroutines, so the native file
// path has to be reentrant. A shared read buffer inside the C helper produced
// wrong digests for every concurrent call here, which is indistinguishable
// from a changed file to the rest of the scanner.
func TestNativeSHA256FileIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	const files = 16
	paths := make([]string, files)
	want := make([]string, files)
	rng := rand.New(rand.NewSource(7))
	for i := range paths {
		// Larger than the C read buffer so every call makes several reads and
		// the interleaving window is wide.
		buf := make([]byte, 6<<20+i*1024)
		rng.Read(buf)
		path := filepath.Join(dir, "concurrent-"+string(rune('a'+i))+".bin")
		if err := os.WriteFile(path, buf, 0644); err != nil {
			t.Fatal(err)
		}
		paths[i] = path
		want[i] = nativeReferenceSum(buf)
	}
	for round := 0; round < 4; round++ {
		var wg sync.WaitGroup
		for i := range paths {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				got, _, err := sha256FileHex(paths[i])
				if err != nil {
					t.Errorf("round %d file %d: %v", round, i, err)
					return
				}
				if got != want[i] {
					t.Errorf("round %d file %d: native %s, want %s", round, i, got, want[i])
				}
			}(i)
		}
		wg.Wait()
	}
}

func bytesOf(n int, fill byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = fill
	}
	return out
}
