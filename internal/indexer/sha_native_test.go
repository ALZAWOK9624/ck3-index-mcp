//go:build ck3_native && cgo && windows && amd64

package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	requireNativeSHA(t)
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
	requireNativeSHA(t)
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
	requireNativeSHA(t)
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

// requireNativeSHA skips when this CPU has no SHA extensions. Production falls
// back to crypto/sha256 there; a test that treats the fallback as a failure
// would only be reporting the CPU it happened to run on.
func requireNativeSHA(t *testing.T) {
	t.Helper()
	if !nativeSHAAvailable() {
		t.Skip("CPU has no SHA-NI; the pure-Go fallback serves this build")
	}
}

// A read is allowed to return fewer bytes than asked for without being at end
// of file, which leaves a partial block behind. Hashing the next read's whole
// blocks before completing that one reorders the message and produces a wrong
// digest with no error. Filesystem reads almost never produce that boundary,
// so the chunk sequence is fed directly.
func TestNativeSHA256ChunkBoundariesMatchGo(t *testing.T) {
	requireNativeSHA(t)
	payload := make([]byte, 4096)
	rng := rand.New(rand.NewSource(11))
	rng.Read(payload)
	for _, sizes := range [][]int{
		{65, 64},
		{1, 63, 64},
		{63, 1, 65},
		{31, 33, 127, 2},
		{64, 1},
		{1, 1, 1, 61, 64, 64},
		{4096},
		{0, 64, 0, 65, 0},
	} {
		total := 0
		for _, size := range sizes {
			total += size
		}
		if total > len(payload) {
			t.Fatalf("chunk plan %v exceeds the payload", sizes)
		}
		chunks := make([][]byte, 0, len(sizes))
		offset := 0
		for _, size := range sizes {
			chunks = append(chunks, payload[offset:offset+size])
			offset += size
		}
		got, ok := nativeSHA256Chunked(chunks)
		if !ok {
			t.Fatal("native path unavailable after the capability check")
		}
		if want := nativeReferenceSum(payload[:total]); got != want {
			t.Fatalf("chunks %v: native %s, want %s", sizes, got, want)
		}
	}
}

func TestNativeSHA256RandomChunkingMatchesGo(t *testing.T) {
	requireNativeSHA(t)
	rng := rand.New(rand.NewSource(2026))
	for round := 0; round < 2000; round++ {
		payload := make([]byte, rng.Intn(600))
		rng.Read(payload)
		var chunks [][]byte
		for offset := 0; offset < len(payload); {
			size := rng.Intn(70) + 1
			if offset+size > len(payload) {
				size = len(payload) - offset
			}
			chunks = append(chunks, payload[offset:offset+size])
			offset += size
		}
		got, ok := nativeSHA256Chunked(chunks)
		if !ok {
			t.Fatal("native path unavailable after the capability check")
		}
		if want := nativeReferenceSum(payload); got != want {
			t.Fatalf("round %d (len %d, %d chunks): native %s, want %s", round, len(payload), len(chunks), got, want)
		}
	}
}

var benchmarkSHASink string

func BenchmarkSHA256BytesBackends(b *testing.B) {
	payload := make([]byte, 16<<20)
	rand.New(rand.NewSource(2026)).Read(payload)
	b.Run("go", func(b *testing.B) {
		b.SetBytes(int64(len(payload)))
		for i := 0; i < b.N; i++ {
			digest := sha256.Sum256(payload)
			benchmarkSHASink = hex.EncodeToString(digest[:])
		}
	})
	b.Run("native", func(b *testing.B) {
		if !nativeSHAAvailable() {
			b.Skip("CPU has no SHA-NI")
		}
		b.SetBytes(int64(len(payload)))
		for i := 0; i < b.N; i++ {
			benchmarkSHASink, _ = sha256BytesHex(payload)
		}
	})
}

// On a CPU without the SHA extensions the native entry points must answer
// from crypto/sha256 and say so, rather than executing an instruction the CPU
// does not have. That path is unreachable on the machines this suite normally
// runs on, so the capability answer is forced.
func TestSHAFallbackWhenTheCPUHasNoExtensions(t *testing.T) {
	disabled := false
	nativeSHAOverride = &disabled
	t.Cleanup(func() { nativeSHAOverride = nil })

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	for _, size := range []int{0, 1, 63, 64, 65, 1 << 20} {
		payload := make([]byte, size)
		rng.Read(payload)
		want := nativeReferenceSum(payload)

		got, ok := sha256BytesHex(payload)
		if ok {
			t.Fatalf("len %d: bytes path reported the native backend while it was disabled", size)
		}
		if got != want {
			t.Fatalf("len %d: fallback bytes digest %s, want %s", size, got, want)
		}

		path := filepath.Join(dir, fmt.Sprintf("fallback-%d.bin", size))
		if err := os.WriteFile(path, payload, 0644); err != nil {
			t.Fatal(err)
		}
		got, ok, err := sha256FileHex(path)
		if err != nil {
			t.Fatalf("len %d: %v", size, err)
		}
		if ok {
			t.Fatalf("len %d: file path reported the native backend while it was disabled", size)
		}
		if got != want {
			t.Fatalf("len %d: fallback file digest %s, want %s", size, got, want)
		}
	}
}
