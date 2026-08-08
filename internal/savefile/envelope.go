package savefile

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
)

// Layout is one canonical CK3 binary save shape.
type Layout string

// The three writable binary layouts. Text layouts are refused outright.
const (
	// LayoutBinaryUncompressed is header kind 1: both sections are plain
	// bytes with no archive at all.
	LayoutBinaryUncompressed Layout = "binary_uncompressed"
	// LayoutUnifiedBinaryZip is header kind 3: inline metadata immediately
	// followed by the archive holding the gamestate.
	LayoutUnifiedBinaryZip Layout = "unified_binary_zip"
	// LayoutSplitBinaryZip is header kind 5: the archive holds both the
	// meta and gamestate entries.
	LayoutSplitBinaryZip Layout = "split_binary_zip"
)

// Archive entry names used by every ZIP-bearing layout.
const (
	metaEntry      = "meta"
	gamestateEntry = "gamestate"
)

// maxHeaderBytes is the widest header line the format defines.
const maxHeaderBytes = 33

var zipLocalSignature = []byte{'P', 'K', 0x03, 0x04}

// Reader pairs a random-access source with its size.
//
// Reading a save card touches only the header and the metadata, so a save is
// never loaded whole. That matters because saves arrive from untrusted
// uploads and a late-game file can be hundreds of megabytes.
type Reader struct {
	At   io.ReaderAt
	Size int64
}

// Bytes wraps an in-memory save.
func Bytes(data []byte) Reader {
	return Reader{At: bytes.NewReader(data), Size: int64(len(data))}
}

// Open opens a save read-only. The caller closes the returned file.
func Open(path string) (Reader, *os.File, error) {
	handle, err := os.Open(path)
	if err != nil {
		return Reader{}, nil, wrapError(ErrBounds, "the save could not be opened", err)
	}
	info, err := handle.Stat()
	if err != nil {
		handle.Close()
		return Reader{}, nil, wrapError(ErrBounds, "the save could not be inspected", err)
	}
	if !info.Mode().IsRegular() {
		handle.Close()
		return Reader{}, nil, newError(ErrBounds, "the save is not a regular file")
	}
	return Reader{At: handle, Size: info.Size()}, handle, nil
}

// read returns exactly n bytes starting at offset.
func (r Reader) read(offset int64, n int) ([]byte, error) {
	if n < 0 || offset < 0 || offset > r.Size || int64(n) > r.Size-offset {
		return nil, newError(ErrBounds, "a read extends past the end of the save")
	}
	buffer := make([]byte, n)
	if _, err := io.ReadFull(io.NewSectionReader(r.At, offset, int64(n)), buffer); err != nil {
		return nil, wrapError(ErrBounds, "the save could not be read", err)
	}
	return buffer, nil
}

// peek returns up to n bytes starting at offset, without demanding all of them.
func (r Reader) peek(offset int64, n int) ([]byte, error) {
	if offset < 0 || offset > r.Size {
		return nil, newError(ErrBounds, "a read starts past the end of the save")
	}
	available := r.Size - offset
	if int64(n) > available {
		n = int(available)
	}
	return r.read(offset, n)
}

// Header is the parsed 24- or 32-byte save header line.
type Header struct {
	Version               uint16
	KindCode              uint16
	HeaderBytes           int
	DeclaredMetadataBytes uint64
}

// Envelope is a validated save shape: which layout it is, and where the
// metadata physically lives.
type Envelope struct {
	Header Header
	Layout Layout

	metadataStart int64
	metadataEnd   int64
	archiveStart  int64
	hasArchive    bool
}

// ParseHeader decodes the leading save header line.
func ParseHeader(data []byte) (Header, error) {
	if len(data) < 24 {
		return Header{}, newError(ErrHeader, "the file is shorter than a save header")
	}
	if !bytes.HasPrefix(data, []byte("SAV")) {
		return Header{}, newError(ErrHeader, "the file does not begin with a SAV magic")
	}
	version, err := strconv.ParseUint(string(data[3:5]), 16, 16)
	if err != nil {
		return Header{}, newError(ErrHeader, "the header version is not hexadecimal")
	}
	kind, err := strconv.ParseUint(string(data[5:7]), 16, 16)
	if err != nil {
		return Header{}, newError(ErrHeader, "the header kind is not hexadecimal")
	}
	metadataLen, err := strconv.ParseUint(string(data[15:23]), 16, 64)
	if err != nil {
		return Header{}, newError(ErrHeader, "the declared metadata length is not hexadecimal")
	}

	// The header line ends at the first newline within its maximum width,
	// which is what distinguishes the 24-byte form from the 32-byte one.
	limit := min(len(data), maxHeaderBytes)
	newline := bytes.IndexByte(data[:limit], '\n')
	if newline < 0 {
		return Header{}, newError(ErrHeader, "the header line is not terminated")
	}
	paddingEnd := newline
	if newline > 0 && data[newline-1] == '\r' {
		paddingEnd--
	}
	if paddingEnd != 23 && paddingEnd != 31 {
		return Header{}, newError(ErrHeader, "the header line has an unsupported width")
	}
	return Header{
		Version:               uint16(version),
		KindCode:              uint16(kind),
		HeaderBytes:           newline + 1,
		DeclaredMetadataBytes: metadataLen,
	}, nil
}

// Analyze validates one save and resolves its layout.
//
// It reads only the header and, at most, the four bytes that follow the inline
// metadata, so it stays cheap for any save size.
func Analyze(src Reader, limits Limits) (*Envelope, error) {
	if src.Size > limits.MaxFileBytes {
		return nil, newError(ErrTooLarge, "the save exceeds the configured file size limit")
	}
	front, err := src.peek(0, maxHeaderBytes)
	if err != nil {
		return nil, err
	}
	header, err := ParseHeader(front)
	if err != nil {
		return nil, err
	}

	var layout Layout
	switch header.KindCode {
	case 1:
		layout = LayoutBinaryUncompressed
	case 3:
		layout = LayoutUnifiedBinaryZip
	case 5:
		layout = LayoutSplitBinaryZip
	case 0, 2, 4:
		return nil, newError(ErrUnsupportedLayout, "this is a text save; only binary saves are read")
	default:
		return nil, newError(ErrUnsupportedLayout,
			fmt.Sprintf("unsupported save header kind 0x%02x", header.KindCode))
	}
	if int64(header.HeaderBytes) > src.Size {
		return nil, newError(ErrBounds, "the save is shorter than its declared header")
	}

	envelope := &Envelope{Header: header, Layout: layout}
	if layout == LayoutSplitBinaryZip {
		if header.DeclaredMetadataBytes != 0 {
			return nil, newError(ErrContainerMismatch,
				"a split save declares inline metadata, but its metadata belongs to the archive")
		}
		archive, err := src.peek(int64(header.HeaderBytes), len(zipLocalSignature))
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(archive, zipLocalSignature) {
			return nil, newError(ErrContainerMismatch,
				"a split save has no archive immediately after its header")
		}
		envelope.archiveStart = int64(header.HeaderBytes)
		envelope.hasArchive = true
		return envelope, nil
	}

	end, err := inlineMetadataEnd(src, header)
	if err != nil {
		return nil, err
	}
	envelope.metadataStart = int64(header.HeaderBytes)
	envelope.metadataEnd = end

	following, err := src.peek(end, len(zipLocalSignature))
	if err != nil {
		return nil, err
	}
	followedByArchive := bytes.Equal(following, zipLocalSignature)
	if layout == LayoutUnifiedBinaryZip {
		if !followedByArchive {
			return nil, newError(ErrContainerMismatch,
				"a unified save has no archive immediately after its inline metadata")
		}
		envelope.archiveStart = end
		envelope.hasArchive = true
		return envelope, nil
	}
	// An uncompressed save's gamestate is plain bytes. Finding an archive
	// where those bytes belong means the header kind and the real container
	// disagree, so the save is refused rather than reinterpreted.
	if followedByArchive {
		return nil, newError(ErrContainerMismatch,
			"an uncompressed save has an archive where its inline gamestate should start")
	}
	if end == src.Size {
		return nil, newError(ErrBounds,
			"the declared metadata consumes the whole save, leaving no gamestate")
	}
	return envelope, nil
}

func inlineMetadataEnd(src Reader, header Header) (int64, error) {
	if header.DeclaredMetadataBytes > uint64(src.Size) {
		return 0, newError(ErrBounds, "the declared metadata extends beyond the save")
	}
	end := int64(header.HeaderBytes) + int64(header.DeclaredMetadataBytes)
	if end < int64(header.HeaderBytes) || end > src.Size {
		return 0, newError(ErrBounds, "the declared metadata extends beyond the save")
	}
	return end, nil
}

// Metadata returns the decoded metadata section.
func (e *Envelope) Metadata(src Reader, limits Limits) ([]byte, error) {
	if e.Layout != LayoutSplitBinaryZip {
		length := e.metadataEnd - e.metadataStart
		if length > limits.MaxSectionBytes {
			return nil, newError(ErrTooLarge, "the metadata exceeds the configured section limit")
		}
		return src.read(e.metadataStart, int(length))
	}
	return e.archiveEntry(src, metaEntry, limits)
}

// GamestateReader streams the decompressed gamestate.
//
// The gamestate is never materialised: it is orders of magnitude larger than
// the metadata and grows without bound over a long campaign, so every caller
// reads it through a StreamDecoder rather than a byte slice. The caller closes
// the returned reader.
func (e *Envelope) GamestateReader(src Reader, limits Limits) (io.ReadCloser, error) {
	if e.Layout == LayoutBinaryUncompressed {
		length := src.Size - e.metadataEnd
		if length <= 0 {
			return nil, newError(ErrBounds, "the save has no inline gamestate")
		}
		if length > limits.gamestateCeiling() {
			return nil, newError(ErrTooLarge, "the gamestate exceeds the configured limit")
		}
		return io.NopCloser(io.NewSectionReader(src.At, e.metadataEnd, length)), nil
	}
	return e.archiveEntryReader(src, gamestateEntry, limits)
}

func (e *Envelope) archiveEntryReader(src Reader, name string, limits Limits) (io.ReadCloser, error) {
	reader, err := e.openArchive(src)
	if err != nil {
		return nil, err
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		if file.UncompressedSize64 > uint64(limits.gamestateCeiling()) {
			return nil, newError(ErrTooLarge, "the gamestate exceeds the configured limit")
		}
		handle, err := file.Open()
		if err != nil {
			return nil, wrapError(ErrArchive, "the gamestate entry could not be opened", err)
		}
		return handle, nil
	}
	return nil, newError(ErrArchive, fmt.Sprintf("the archive has no %q entry", name))
}

// ArchiveEntryNames lists the archive entries in central-directory order, or
// nil for a layout without an archive.
func (e *Envelope) ArchiveEntryNames(src Reader) ([]string, error) {
	if !e.hasArchive {
		return nil, nil
	}
	reader, err := e.openArchive(src)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	return names, nil
}

func (e *Envelope) openArchive(src Reader) (*zip.Reader, error) {
	if !e.hasArchive {
		return nil, newError(ErrArchive, "this layout has no embedded archive")
	}
	// Archive offsets are relative to the archive's own start, so the
	// section reader is what makes the CK3 prefix invisible to the reader.
	section := io.NewSectionReader(src.At, e.archiveStart, src.Size-e.archiveStart)
	reader, err := zip.NewReader(section, section.Size())
	if err != nil {
		return nil, wrapError(ErrArchive, "the embedded archive could not be opened", err)
	}
	return reader, nil
}

func (e *Envelope) archiveEntry(src Reader, name string, limits Limits) ([]byte, error) {
	reader, err := e.openArchive(src)
	if err != nil {
		return nil, err
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		if file.UncompressedSize64 > uint64(limits.MaxSectionBytes) {
			return nil, newError(ErrTooLarge, "an archive entry exceeds the configured section limit")
		}
		handle, err := file.Open()
		if err != nil {
			return nil, wrapError(ErrArchive, "an archive entry could not be opened", err)
		}
		defer handle.Close()
		// One byte past the limit distinguishes an exact-size entry from a
		// declaration that understates the real payload.
		section, err := io.ReadAll(io.LimitReader(handle, limits.MaxSectionBytes+1))
		if err != nil {
			return nil, wrapError(ErrArchive, "an archive entry could not be read", err)
		}
		if int64(len(section)) > limits.MaxSectionBytes {
			return nil, newError(ErrTooLarge, "an archive entry exceeds the configured section limit")
		}
		if uint64(len(section)) != file.UncompressedSize64 {
			return nil, newError(ErrArchive, "an archive entry is not the size it declares")
		}
		return section, nil
	}
	return nil, newError(ErrArchive, fmt.Sprintf("the archive has no %q entry", name))
}
