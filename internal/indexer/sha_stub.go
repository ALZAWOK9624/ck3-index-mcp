//go:build !ck3_native || !cgo || !windows || !amd64

package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// shaFileStreamBuffer is the read buffer for the streaming resource hash
// path in the pure-Go build. io.Copy's default 32 KiB buffer costs one read
// syscall per 32 KiB on large rasters; 4 MiB keeps the same call count for a
// 4 GB tree at ~1000 syscalls while staying cheap for the small files the
// path also serves.
const shaFileStreamBuffer = 4 << 20

// sha256FileHex hashes the file at path using the pure-Go implementation.
// ok is always false: the native build supplies the fast path.
func sha256FileHex(path string) (sum string, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, shaFileStreamBuffer)); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), false, nil
}

// sha256BytesHex hashes an in-memory buffer with the pure-Go implementation.
func sha256BytesHex(data []byte) (sum string, ok bool) {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), false
}
