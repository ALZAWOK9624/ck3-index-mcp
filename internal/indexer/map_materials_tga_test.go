package indexer

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeTGA builds a minimal 32-bit true-color TGA. imageType picks 2
// (uncompressed) or 10 (RLE); body is the already-encoded pixel payload.
func writeTGA(t *testing.T, path string, imageType byte, width, height int, topOrigin bool, body []byte) {
	t.Helper()
	header := make([]byte, 18)
	header[2] = imageType
	binary.LittleEndian.PutUint16(header[12:14], uint16(width))
	binary.LittleEndian.PutUint16(header[14:16], uint16(height))
	header[16] = 32
	if topOrigin {
		header[17] = 0x20
	}
	if err := os.WriteFile(path, append(header, body...), 0644); err != nil {
		t.Fatal(err)
	}
}

// rawPixels returns a deterministic BGRA sequence for count pixels.
func rawPixels(count int) []byte {
	out := make([]byte, count*4)
	for i := 0; i < count; i++ {
		out[i*4+0] = byte(i * 7 % 251)
		out[i*4+1] = byte(i * 13 % 241)
		out[i*4+2] = byte(i * 29 % 239)
		out[i*4+3] = 0xFF
	}
	return out
}

func readAllRows(t *testing.T, path string) [][]byte {
	t.Helper()
	reader, err := openTGA(path)
	if err != nil {
		t.Fatalf("openTGA(%s): %v", filepath.Base(path), err)
	}
	defer reader.Close()
	rows := make([][]byte, reader.Height)
	for y := 0; y < reader.Height; y++ {
		buffer := make([]byte, reader.Width*4)
		if err := reader.ReadRow(y, buffer); err != nil {
			t.Fatalf("ReadRow(%d): %v", y, err)
		}
		rows[y] = buffer
	}
	return rows
}

// TestOpenTGADecodesRLEIdenticallyToUncompressed is the core guarantee: an RLE
// image and its uncompressed twin must read back byte-identical, including the
// bottom-origin row flip. The RLE stream deliberately uses packets that span
// row boundaries, since CK3's own detail_index.tga does.
func TestOpenTGADecodesRLEIdenticallyToUncompressed(t *testing.T) {
	const width, height = 5, 4
	total := width * height

	// A run of 12 identical pixels crosses rows 0-2 at width 5; the remaining
	// 8 pixels are literal and end mid-row, so no packet aligns to a row.
	solid := []byte{0x11, 0x22, 0x33, 0xFF}
	literal := rawPixels(8)

	expanded := make([]byte, 0, total*4)
	for i := 0; i < 12; i++ {
		expanded = append(expanded, solid...)
	}
	expanded = append(expanded, literal...)

	encoded := []byte{0x80 | 11} // RLE packet, count = 12
	encoded = append(encoded, solid...)
	encoded = append(encoded, 7) // raw packet, count = 8
	encoded = append(encoded, literal...)

	for _, topOrigin := range []bool{false, true} {
		dir := t.TempDir()
		rlePath := filepath.Join(dir, "rle.tga")
		rawPath := filepath.Join(dir, "raw.tga")
		writeTGA(t, rlePath, 10, width, height, topOrigin, encoded)
		writeTGA(t, rawPath, 2, width, height, topOrigin, expanded)

		rleRows := readAllRows(t, rlePath)
		rawRows := readAllRows(t, rawPath)
		for y := range rawRows {
			if string(rleRows[y]) != string(rawRows[y]) {
				t.Errorf("topOrigin=%v row %d mismatch:\n rle=%v\n raw=%v", topOrigin, y, rleRows[y], rawRows[y])
			}
		}
	}
}

func TestOpenTGARejectsTruncatedRLEStream(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.tga")
	// Claims a 12-pixel run but supplies only 2 bytes of the 4-byte pixel.
	writeTGA(t, path, 10, 5, 4, false, []byte{0x80 | 11, 0x01, 0x02})
	if _, err := openTGA(path); err == nil {
		t.Fatal("expected an error for a truncated RLE stream, got nil")
	}
}

func TestOpenTGARejectsOverrunningRLEPacket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overrun.tga")
	// A 128-pixel run in a 2x2 image must not write past the frame.
	body := append([]byte{0x80 | 127}, 0x01, 0x02, 0x03, 0x04)
	writeTGA(t, path, 10, 2, 2, false, body)
	if _, err := openTGA(path); err == nil {
		t.Fatal("expected an error for an overrunning RLE packet, got nil")
	}
}

func TestOpenTGAStillRejectsColorMapped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cmap.tga")
	header := make([]byte, 18)
	header[1] = 1 // color map present
	header[2] = 1
	binary.LittleEndian.PutUint16(header[12:14], 2)
	binary.LittleEndian.PutUint16(header[14:16], 2)
	header[16] = 32
	if err := os.WriteFile(path, header, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := openTGA(path); err == nil {
		t.Fatal("expected an error for a color-mapped TGA, got nil")
	}
}
