//go:build ck3_native && cgo && windows && amd64

package indexer

/*
#cgo CFLAGS: -O3

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <windows.h>
#include <x86intrin.h>

// ---------------------------------------------------------------------------
// SHA-256 block transform, the Intel SHA-NI intrinsics implementation written
// by Jeffrey Walton (public domain), based on code from Intel and Sean Gulley.
// Verbatim except for the per-function target attribute, which replaces the
// -msha/-mssse3/-msse4.1 command line flags cgo's flag allowlist rejects.
// ---------------------------------------------------------------------------

__attribute__((target("sha,ssse3,sse4.1")))
static void sha256_process_x86(uint32_t state[8], const uint8_t data[], uint32_t length)
{
	__m128i STATE0, STATE1;
	__m128i MSG, TMP;
	__m128i MSG0, MSG1, MSG2, MSG3;
	__m128i ABEF_SAVE, CDGH_SAVE;
	const __m128i MASK = _mm_set_epi64x(0x0c0d0e0f08090a0bULL, 0x0405060700010203ULL);

	// Load initial values
	TMP = _mm_loadu_si128((const __m128i*) &state[0]);
	STATE1 = _mm_loadu_si128((const __m128i*) &state[4]);

	TMP = _mm_shuffle_epi32(TMP, 0xB1);          // CDAB
	STATE1 = _mm_shuffle_epi32(STATE1, 0x1B);    // EFGH
	STATE0 = _mm_alignr_epi8(TMP, STATE1, 8);    // ABEF
	STATE1 = _mm_blend_epi16(STATE1, TMP, 0xF0); // CDGH

	while (length >= 64)
	{
		// Save current state
		ABEF_SAVE = STATE0;
		CDGH_SAVE = STATE1;

		// Rounds 0-3
		MSG = _mm_loadu_si128((const __m128i*) (data+0));
		MSG0 = _mm_shuffle_epi8(MSG, MASK);
		MSG = _mm_add_epi32(MSG0, _mm_set_epi64x(0xE9B5DBA5B5C0FBCFULL, 0x71374491428A2F98ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);

		// Rounds 4-7
		MSG1 = _mm_loadu_si128((const __m128i*) (data+16));
		MSG1 = _mm_shuffle_epi8(MSG1, MASK);
		MSG = _mm_add_epi32(MSG1, _mm_set_epi64x(0xAB1C5ED5923F82A4ULL, 0x59F111F13956C25BULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG0 = _mm_sha256msg1_epu32(MSG0, MSG1);

		// Rounds 8-11
		MSG2 = _mm_loadu_si128((const __m128i*) (data+32));
		MSG2 = _mm_shuffle_epi8(MSG2, MASK);
		MSG = _mm_add_epi32(MSG2, _mm_set_epi64x(0x550C7DC3243185BEULL, 0x12835B01D807AA98ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG1 = _mm_sha256msg1_epu32(MSG1, MSG2);

		// Rounds 12-15
		MSG3 = _mm_loadu_si128((const __m128i*) (data+48));
		MSG3 = _mm_shuffle_epi8(MSG3, MASK);
		MSG = _mm_add_epi32(MSG3, _mm_set_epi64x(0xC19BF1749BDC06A7ULL, 0x80DEB1FE72BE5D74ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG3, MSG2, 4);
		MSG0 = _mm_add_epi32(MSG0, TMP);
		MSG0 = _mm_sha256msg2_epu32(MSG0, MSG3);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG2 = _mm_sha256msg1_epu32(MSG2, MSG3);

		// Rounds 16-19
		MSG = _mm_add_epi32(MSG0, _mm_set_epi64x(0x240CA1CC0FC19DC6ULL, 0xEFBE4786E49B69C1ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG0, MSG3, 4);
		MSG1 = _mm_add_epi32(MSG1, TMP);
		MSG1 = _mm_sha256msg2_epu32(MSG1, MSG0);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG3 = _mm_sha256msg1_epu32(MSG3, MSG0);

		// Rounds 20-23
		MSG = _mm_add_epi32(MSG1, _mm_set_epi64x(0x76F988DA5CB0A9DCULL, 0x4A7484AA2DE92C6FULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG1, MSG0, 4);
		MSG2 = _mm_add_epi32(MSG2, TMP);
		MSG2 = _mm_sha256msg2_epu32(MSG2, MSG1);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG0 = _mm_sha256msg1_epu32(MSG0, MSG1);

		// Rounds 24-27
		MSG = _mm_add_epi32(MSG2, _mm_set_epi64x(0xBF597FC7B00327C8ULL, 0xA831C66D983E5152ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG2, MSG1, 4);
		MSG3 = _mm_add_epi32(MSG3, TMP);
		MSG3 = _mm_sha256msg2_epu32(MSG3, MSG2);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG1 = _mm_sha256msg1_epu32(MSG1, MSG2);

		// Rounds 28-31
		MSG = _mm_add_epi32(MSG3, _mm_set_epi64x(0x1429296706CA6351ULL,  0xD5A79147C6E00BF3ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG3, MSG2, 4);
		MSG0 = _mm_add_epi32(MSG0, TMP);
		MSG0 = _mm_sha256msg2_epu32(MSG0, MSG3);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG2 = _mm_sha256msg1_epu32(MSG2, MSG3);

		// Rounds 32-35
		MSG = _mm_add_epi32(MSG0, _mm_set_epi64x(0x53380D134D2C6DFCULL, 0x2E1B213827B70A85ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG0, MSG3, 4);
		MSG1 = _mm_add_epi32(MSG1, TMP);
		MSG1 = _mm_sha256msg2_epu32(MSG1, MSG0);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG3 = _mm_sha256msg1_epu32(MSG3, MSG0);

		// Rounds 36-39
		MSG = _mm_add_epi32(MSG1, _mm_set_epi64x(0x92722C8581C2C92EULL, 0x766A0ABB650A7354ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG1, MSG0, 4);
		MSG2 = _mm_add_epi32(MSG2, TMP);
		MSG2 = _mm_sha256msg2_epu32(MSG2, MSG1);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG0 = _mm_sha256msg1_epu32(MSG0, MSG1);

		// Rounds 40-43
		MSG = _mm_add_epi32(MSG2, _mm_set_epi64x(0xC76C51A3C24B8B70ULL, 0xA81A664BA2BFE8A1ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG2, MSG1, 4);
		MSG3 = _mm_add_epi32(MSG3, TMP);
		MSG3 = _mm_sha256msg2_epu32(MSG3, MSG2);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG1 = _mm_sha256msg1_epu32(MSG1, MSG2);

		// Rounds 44-47
		MSG = _mm_add_epi32(MSG3, _mm_set_epi64x(0x106AA070F40E3585ULL, 0xD6990624D192E819ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG3, MSG2, 4);
		MSG0 = _mm_add_epi32(MSG0, TMP);
		MSG0 = _mm_sha256msg2_epu32(MSG0, MSG3);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG2 = _mm_sha256msg1_epu32(MSG2, MSG3);

		// Rounds 48-51
		MSG = _mm_add_epi32(MSG0, _mm_set_epi64x(0x34B0BCB52748774CULL, 0x1E376C0819A4C116ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG0, MSG3, 4);
		MSG1 = _mm_add_epi32(MSG1, TMP);
		MSG1 = _mm_sha256msg2_epu32(MSG1, MSG0);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG3 = _mm_sha256msg1_epu32(MSG3, MSG0);

		// Rounds 52-55
		MSG = _mm_add_epi32(MSG1, _mm_set_epi64x(0x682E6FF35B9CCA4FULL, 0x4ED8AA4A391C0CB3ULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG1, MSG0, 4);
		MSG2 = _mm_add_epi32(MSG2, TMP);
		MSG2 = _mm_sha256msg2_epu32(MSG2, MSG1);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG0 = _mm_sha256msg1_epu32(MSG0, MSG1);

		// Rounds 56-59
		MSG = _mm_add_epi32(MSG2, _mm_set_epi64x(0x8CC7020884C87814ULL, 0x78A5636F748F82EEULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		TMP = _mm_alignr_epi8(MSG2, MSG1, 4);
		MSG3 = _mm_add_epi32(MSG3, TMP);
		MSG3 = _mm_sha256msg2_epu32(MSG3, MSG2);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);
		MSG1 = _mm_sha256msg1_epu32(MSG1, MSG2);

		// Rounds 60-63
		MSG = _mm_add_epi32(MSG3, _mm_set_epi64x(0xC67178F2BEF9A3F7ULL, 0xA4506CEB90BEFFFAULL));
		STATE1 = _mm_sha256rnds2_epu32(STATE1, STATE0, MSG);
		MSG = _mm_shuffle_epi32(MSG, 0x0E);
		STATE0 = _mm_sha256rnds2_epu32(STATE0, STATE1, MSG);

		// Combine state
		STATE0 = _mm_add_epi32(STATE0, ABEF_SAVE);
		STATE1 = _mm_add_epi32(STATE1, CDGH_SAVE);

		data += 64;
		length -= 64;
	}

	TMP = _mm_shuffle_epi32(STATE0, 0x1B);       // FEBA
	STATE1 = _mm_shuffle_epi32(STATE1, 0xB1);    // DCHG
	STATE0 = _mm_blend_epi16(TMP, STATE1, 0xF0); // DCBA
	STATE1 = _mm_alignr_epi8(STATE1, TMP, 8);    // ABEF

	// Save state
	_mm_storeu_si128((__m128i*) &state[0], STATE0);
	_mm_storeu_si128((__m128i*) &state[4], STATE1);
}

// ---------------------------------------------------------------------------
// Streaming and in-memory hashing wrappers. Both produce the same lowercase
// hex form as hex.EncodeToString(sha256.Sum256(data)) in Go.
// ---------------------------------------------------------------------------

static const char gh_hexd[] = "0123456789abcdef";

static void gh_digest_hex(const uint32_t state[8], char out[65])
{
	uint8_t digest[32];
	for (int i = 0; i < 8; i++) {
		digest[4*i]   = (uint8_t)(state[i] >> 24);
		digest[4*i+1] = (uint8_t)(state[i] >> 16);
		digest[4*i+2] = (uint8_t)(state[i] >> 8);
		digest[4*i+3] = (uint8_t)state[i];
	}
	for (int i = 0; i < 32; i++) {
		out[2*i]   = gh_hexd[digest[i] >> 4];
		out[2*i+1] = gh_hexd[digest[i] & 15];
	}
	out[64] = 0;
}

static void gh_sha256_iv(uint32_t state[8])
{
	static const uint32_t iv[8] = {
		0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
		0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
	};
	memcpy(state, iv, sizeof(iv));
}

// gh_sha256_finish applies SHA-256 padding for a message whose total length
// is total_bytes and whose remaining bytes (0..63) sit in tail[0..tail_len).
static void gh_sha256_finish(uint32_t state[8], uint8_t tail[64], size_t tail_len, uint64_t total_bytes)
{
	tail[tail_len++] = 0x80;
	if (tail_len > 56) {
		memset(tail + tail_len, 0, 64 - tail_len);
		sha256_process_x86(state, tail, 64);
		tail_len = 0;
	}
	memset(tail + tail_len, 0, 56 - tail_len);
	uint64_t bits = total_bytes * 8;
	for (int i = 0; i < 8; i++) {
		tail[56 + i] = (uint8_t)(bits >> (56 - 8 * i));
	}
	sha256_process_x86(state, tail, 64);
}

// gh_sha256_available reports whether this CPU implements the SHA extensions
// the transform above is compiled for. sha256_process_x86 is built with a
// target attribute, not a runtime dispatch, so calling it on a CPU without
// SHA-NI raises SIGILL; every entry point is gated on this and the Go side
// falls back to crypto/sha256 when it returns 0.
static int gh_sha256_available(void)
{
	__builtin_cpu_init();
	return __builtin_cpu_supports("sha") && __builtin_cpu_supports("ssse3") && __builtin_cpu_supports("sse4.1");
}

// shaProcessChunk is the largest span handed to sha256_process_x86 in one
// call. Its length parameter is a uint32_t, so a buffer of 4 GiB or more must
// be fed in pieces or the count silently truncates.
#define GH_SHA_CHUNK (1u << 30)

// gh_sha256_blocks feeds whole 64-byte blocks through the transform in spans
// small enough for its uint32_t length.
static void gh_sha256_blocks(uint32_t state[8], const uint8_t *data, uint64_t bytes)
{
	while (bytes > 0) {
		uint32_t span = bytes > GH_SHA_CHUNK ? GH_SHA_CHUNK : (uint32_t)bytes;
		sha256_process_x86(state, data, span);
		data += span;
		bytes -= span;
	}
}

// gh_sha256_update absorbs one arbitrary run of bytes, carrying the partial
// block across calls. Feeding each read's whole blocks straight to the
// transform is only correct while tail is empty: ReadFile is allowed to return
// a short count that is not end of file (network redirectors, filter drivers,
// virtual filesystems), and hashing the next read's blocks before completing
// the block the previous read left behind reorders the message. The digest is
// then wrong, and nothing reports an error.
static void gh_sha256_update(uint32_t state[8], uint8_t tail[64], size_t *tail_len,
	const uint8_t *data, size_t len)
{
	if (*tail_len != 0) {
		size_t need = 64 - *tail_len;
		size_t take = len < need ? len : need;
		memcpy(tail + *tail_len, data, take);
		*tail_len += take;
		data += take;
		len -= take;
		if (*tail_len == 64) {
			sha256_process_x86(state, tail, 64);
			*tail_len = 0;
		}
	}
	size_t full = len & ~(size_t)63;
	if (full != 0) {
		gh_sha256_blocks(state, data, (uint64_t)full);
		data += full;
		len -= full;
	}
	if (len != 0) {
		memcpy(tail, data, len);
		*tail_len = len;
	}
}

// gh_sha_ctx is the streaming state: whatever the caller has absorbed so far,
// plus the bytes of the block it has not completed. The file path and the
// chunk-sequence tests both drive this, so the ordering the tests prove is the
// ordering production uses.
typedef struct {
	uint32_t state[8];
	uint8_t tail[64];
	size_t tail_len;
	uint64_t total;
} gh_sha_ctx;

static void gh_sha256_ctx_init(gh_sha_ctx *c)
{
	gh_sha256_iv(c->state);
	c->tail_len = 0;
	c->total = 0;
}

static void gh_sha256_ctx_update(gh_sha_ctx *c, const uint8_t *data, uint64_t len)
{
	c->total += len;
	gh_sha256_update(c->state, c->tail, &c->tail_len, data, (size_t)len);
}

static void gh_sha256_ctx_final(gh_sha_ctx *c, char out[65])
{
	gh_sha256_finish(c->state, c->tail, c->tail_len, c->total);
	gh_digest_hex(c->state, out);
}

// gh_sha256_bytes_hex hashes an in-memory buffer.
static void gh_sha256_bytes_hex(const uint8_t *data, uint64_t len, char out[65])
{
	uint32_t state[8];
	gh_sha256_iv(state);
	uint64_t full = (uint64_t)(len / 64);
	if (full) {
		gh_sha256_blocks(state, data, full * 64);
	}
	uint8_t tail[64];
	size_t tail_len = (size_t)(len % 64);
	if (tail_len) {
		memcpy(tail, data + full * 64, tail_len);
	}
	gh_sha256_finish(state, tail, tail_len, len);
	gh_digest_hex(state, out);
}

// gh_sha256_file_hex hashes a file by UTF-8 path. The file is opened with
// FILE_FLAG_SEQUENTIAL_SCAN, which lets Windows prefetch the read stream;
// measured on the Godherja gfx tree (11k files, 5.9 GB) this path is ~3.3x
// faster than Go's os.Open + io.CopyBuffer equivalent on the same corpus.
// Returns 0 on success, a Win32 error code otherwise (in errout).
#define GH_SHA_READ_BUFFER (4 << 20)

static int gh_sha256_file_hex(const char *path_utf8, char out[65], uint32_t *errout)
{
	int wlen = MultiByteToWideChar(CP_UTF8, 0, path_utf8, -1, NULL, 0);
	if (wlen <= 0) {
		*errout = (uint32_t)GetLastError();
		return -1;
	}
	wchar_t *wpath = (wchar_t *)malloc((size_t)wlen * sizeof(wchar_t));
	if (!wpath) {
		*errout = (uint32_t)ERROR_NOT_ENOUGH_MEMORY;
		return -1;
	}
	if (MultiByteToWideChar(CP_UTF8, 0, path_utf8, -1, wpath, wlen) <= 0) {
		*errout = (uint32_t)GetLastError();
		free(wpath);
		return -1;
	}
	// Share write and delete as well as read. Denying them made hashing fail
	// outright whenever an editor held the file open, and editors that save
	// through a temporary file plus rename need DELETE sharing too. The torn
	// read that sharing admits is detected below by comparing the file's size
	// and last-write time across the read.
	HANDLE h = CreateFileW(wpath, GENERIC_READ,
		FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, NULL, OPEN_EXISTING,
		FILE_FLAG_SEQUENTIAL_SCAN, NULL);
	free(wpath);
	if (h == INVALID_HANDLE_VALUE) {
		*errout = (uint32_t)GetLastError();
		return -1;
	}
	BY_HANDLE_FILE_INFORMATION beforeInfo;
	int haveBefore = GetFileInformationByHandle(h, &beforeInfo) != 0;
	// The read buffer is per call. A single static buffer would be shared by
	// every scan worker -- parseOneFile runs on up to sixteen goroutines --
	// and they would overwrite each other's bytes, producing digests that are
	// wrong rather than merely slow.
	uint8_t *buf = (uint8_t *)malloc(GH_SHA_READ_BUFFER);
	if (!buf) {
		CloseHandle(h);
		*errout = (uint32_t)ERROR_NOT_ENOUGH_MEMORY;
		return -1;
	}
	gh_sha_ctx ctx;
	gh_sha256_ctx_init(&ctx);
	int failed = 0;
	for (;;) {
		DWORD got = 0;
		// ReadFile's return value is the only reliable failure signal: on
		// error it may leave got at zero, which is indistinguishable from a
		// clean end of file. Testing GetLastError() after the loop instead
		// would report a truncated read as a successful hash.
		if (!ReadFile(h, buf, GH_SHA_READ_BUFFER, &got, NULL)) {
			*errout = (uint32_t)GetLastError();
			failed = 1;
			break;
		}
		if (got == 0) {
			break;
		}
		gh_sha256_ctx_update(&ctx, buf, (uint64_t)got);
	}
	free(buf);
	BY_HANDLE_FILE_INFORMATION afterInfo;
	int haveAfter = GetFileInformationByHandle(h, &afterInfo) != 0;
	CloseHandle(h);
	if (failed) {
		return -1;
	}
	// A digest of a file that was rewritten mid-read is not a digest of any
	// version of that file. Report it instead of storing it as this file's
	// identity.
	if (haveBefore && haveAfter) {
		int changed = beforeInfo.nFileSizeHigh != afterInfo.nFileSizeHigh ||
			beforeInfo.nFileSizeLow != afterInfo.nFileSizeLow ||
			beforeInfo.ftLastWriteTime.dwLowDateTime != afterInfo.ftLastWriteTime.dwLowDateTime ||
			beforeInfo.ftLastWriteTime.dwHighDateTime != afterInfo.ftLastWriteTime.dwHighDateTime;
		if (changed) {
			return -2;
		}
	}
	gh_sha256_ctx_final(&ctx, out);
	return 0;
}
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"unsafe"
)

// shaFileStreamBuffer is the read buffer of the pure-Go fallback below. It
// matches the buffer sha_stub.go uses so both builds make the same number of
// read syscalls.
const shaFileStreamBuffer = 4 << 20

// Win32 status codes worth translating: callers such as patch.go and the
// on-action evidence audit branch on os.IsNotExist, which only works if the
// native path keeps returning an error that unwraps to fs.ErrNotExist.
const (
	winErrorFileNotFound = 2
	winErrorPathNotFound = 3
	winErrorAccessDenied = 5
)

var nativeSHAOnce sync.Once
var nativeSHAUsable bool

// nativeSHAAvailable reports whether the SHA-NI transform may be called on
// this CPU. The transform is compiled with a target attribute rather than a
// runtime dispatch, so on a CPU without the SHA extensions calling it is an
// illegal instruction, not a slow path.
func nativeSHAAvailable() bool {
	if override := nativeSHAOverride; override != nil {
		return *override
	}
	nativeSHAOnce.Do(func() {
		nativeSHAUsable = C.gh_sha256_available() != 0
	})
	return nativeSHAUsable
}

// nativeSHAOverride forces the capability answer. Only tests set it: the
// fallback has to be provable on a machine that does have SHA-NI, because the
// machines that do not are exactly the ones nobody runs the suite on.
var nativeSHAOverride *bool

// sha256FileHex hashes the file at path with the native SHA-NI
// implementation using Windows sequential-scan reads. ok reports whether the
// native path served the request; it is false when the CPU has no SHA
// extensions and the pure-Go fallback answered instead.
func sha256FileHex(path string) (sum string, ok bool, err error) {
	if !nativeSHAAvailable() {
		sum, err = goSHA256FileHex(path)
		return sum, false, err
	}
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var out [65]C.char
	var errout C.uint32_t
	rc := C.gh_sha256_file_hex(cPath, &out[0], &errout)
	if rc == -2 {
		return "", true, &fs.PathError{Op: "hash", Path: path, Err: errFileChangedWhileHashing}
	}
	if rc != 0 {
		return "", true, nativeFileHashError(path, uint32(errout))
	}
	return C.GoString(&out[0]), true, nil
}

// errFileChangedWhileHashing reports a file rewritten between the first and
// last read of one hash. The caller can retry; storing the digest would record
// an identity no version of the file ever had.
var errFileChangedWhileHashing = errors.New("file changed while it was being hashed")

// nativeFileHashError keeps the Win32 status code and maps the two codes
// callers actually test for onto the standard filesystem errors.
func nativeFileHashError(path string, code uint32) error {
	switch code {
	case winErrorFileNotFound, winErrorPathNotFound:
		return &fs.PathError{Op: "hash", Path: path, Err: fs.ErrNotExist}
	case winErrorAccessDenied:
		return &fs.PathError{Op: "hash", Path: path, Err: fs.ErrPermission}
	}
	return &fs.PathError{Op: "hash", Path: path, Err: fmt.Errorf("native file hash failed, Win32 error %d", code)}
}

// sha256BytesHex hashes an in-memory buffer with the native SHA-NI
// implementation.
func sha256BytesHex(data []byte) (sum string, ok bool) {
	if !nativeSHAAvailable() {
		digest := sha256.Sum256(data)
		return hex.EncodeToString(digest[:]), false
	}
	if len(data) == 0 {
		// Empty input still needs the C path for byte-identical results;
		// pass a valid pointer via the slice's data pointer (nil is fine too,
		// but keep the length explicit).
		return nativeHashBytesEmpty(), true
	}
	var out [65]C.char
	C.gh_sha256_bytes_hex((*C.uint8_t)(unsafe.Pointer(&data[0])), C.uint64_t(len(data)), &out[0])
	return C.GoString(&out[0]), true
}

func nativeHashBytesEmpty() string {
	var out [65]C.char
	C.gh_sha256_bytes_hex(nil, 0, &out[0])
	return C.GoString(&out[0])
}

// nativeSHA256Chunked drives the streaming path over caller-chosen chunk
// boundaries. Real reads rarely produce a partial block that is not the last
// one, so the ordering bug this exists to catch cannot be reached reliably
// through the filesystem; the tests feed the boundaries directly.
func nativeSHA256Chunked(chunks [][]byte) (string, bool) {
	if !nativeSHAAvailable() {
		return "", false
	}
	var ctx C.gh_sha_ctx
	C.gh_sha256_ctx_init(&ctx)
	for _, chunk := range chunks {
		if len(chunk) == 0 {
			continue
		}
		C.gh_sha256_ctx_update(&ctx, (*C.uint8_t)(unsafe.Pointer(&chunk[0])), C.uint64_t(len(chunk)))
	}
	var out [65]C.char
	C.gh_sha256_ctx_final(&ctx, &out[0])
	return C.GoString(&out[0]), true
}

// goSHA256FileHex is the fallback for CPUs without the SHA extensions. It is
// the same implementation sha_stub.go compiles for the pure-Go build.
func goSHA256FileHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, shaFileStreamBuffer)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
