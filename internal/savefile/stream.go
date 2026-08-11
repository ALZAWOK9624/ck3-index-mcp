package savefile

import (
	"errors"
	"io"
	"strconv"
)

// streamBufferBytes is the working window the streaming decoder keeps.
//
// It only ever has to hold one token, so it is sized for comfort rather than
// necessity; it grows on demand if a single scalar is larger.
const streamBufferBytes = 1 << 20

// StreamDecoder decodes a binary token stream without holding it in memory.
//
// This is what lets a gamestate of any size be read within a fixed memory
// budget: the decoder keeps one reusable window, so peak memory is set by the
// window and by whatever the caller chooses to retain, never by the save.
//
// Token.Text points into that window and is only valid until the next call to
// Next. Callers that keep a value must copy it.
type StreamDecoder struct {
	src      io.Reader
	buf      []byte
	start    int
	end      int
	eof      bool
	limits   Limits
	tokens   int64
	depth    int
	read     int64
	encoding Encoding
	names    nameTable
}

// NewStreamDecoder returns a binary decoder over one decompressed section.
func NewStreamDecoder(src io.Reader, limits Limits) *StreamDecoder {
	return NewStreamDecoderFor(EncodingBinary, src, limits)
}

// NewStreamDecoderFor returns a decoder for one section in the named encoding.
func NewStreamDecoderFor(encoding Encoding, src io.Reader, limits Limits) *StreamDecoder {
	return &StreamDecoder{
		src:      src,
		buf:      make([]byte, streamBufferBytes),
		limits:   limits,
		encoding: encoding,
	}
}

// Encoding reports which grammar this decoder reads.
func (d *StreamDecoder) Encoding() Encoding { return d.encoding }

// Depth reports the current container nesting level.
func (d *StreamDecoder) Depth() int { return d.depth }

// Consumed reports how many section bytes have been decoded.
func (d *StreamDecoder) Consumed() int64 { return d.read }

// Done reports whether the section has been fully consumed.
func (d *StreamDecoder) Done() bool {
	// A text section ending in whitespace or a comment is finished, not
	// truncated, so the filler has to be consumed before the question can be
	// answered.
	if d.encoding == EncodingText {
		remaining, err := skipTextFiller(d)
		return err != nil || !remaining
	}
	if d.start < d.end {
		return false
	}
	if d.eof {
		return true
	}
	// A single refill settles it: either more data arrives or the source is
	// at EOF, and both answers are stable.
	if err := d.fill(); err != nil {
		return true
	}
	return d.start >= d.end && d.eof
}

// fill pulls one more chunk from the source into the window.
func (d *StreamDecoder) fill() error {
	if d.eof {
		return io.EOF
	}
	if d.start > 0 {
		copy(d.buf, d.buf[d.start:d.end])
		d.end -= d.start
		d.start = 0
	}
	if d.end == len(d.buf) {
		return nil
	}
	n, err := d.src.Read(d.buf[d.end:])
	d.end += n
	if err != nil {
		if errors.Is(err, io.EOF) {
			d.eof = true
			return nil
		}
		return wrapError(ErrTruncated, "the section could not be read", err)
	}
	return nil
}

func (d *StreamDecoder) take(n int) ([]byte, error) {
	if n < 0 {
		return nil, newError(ErrMalformedToken, "a token declared a negative length")
	}
	if n > len(d.buf) {
		// One scalar is wider than the window; grow to fit it exactly once.
		grown := make([]byte, n)
		copy(grown, d.buf[d.start:d.end])
		d.end -= d.start
		d.start = 0
		d.buf = grown
	}
	for d.end-d.start < n {
		if d.eof {
			return nil, newError(ErrTruncated, "token payload extends past the end of the section")
		}
		if err := d.fill(); err != nil {
			return nil, err
		}
	}
	out := d.buf[d.start : d.start+n]
	d.start += n
	d.read += int64(n)
	return out, nil
}

// peekAhead returns up to n unconsumed bytes without consuming them, growing
// the window when a single scalar needs more room than it has.
func (d *StreamDecoder) peekAhead(n int) ([]byte, error) {
	if n > len(d.buf) {
		grown := make([]byte, n)
		copy(grown, d.buf[d.start:d.end])
		d.end -= d.start
		d.start = 0
		d.buf = grown
	}
	for d.end-d.start < n && !d.eof {
		if err := d.fill(); err != nil {
			return nil, err
		}
	}
	available := d.end - d.start
	if available > n {
		available = n
	}
	return d.buf[d.start : d.start+available], nil
}

func (d *StreamDecoder) discard(n int) {
	d.start += n
	d.read += int64(n)
}

// Next decodes the next token.
func (d *StreamDecoder) Next() (Token, error) {
	if d.Done() {
		return Token{}, newError(ErrTruncated, "the section ended where a token was expected")
	}
	if d.encoding == EncodingText {
		return decodeTextToken(d, d.limits, &d.tokens, &d.depth)
	}
	return decodeToken(d.take, d.limits, &d.tokens, &d.depth)
}

// keyName resolves the field name a key token carries. See Decoder.keyName.
func (d *StreamDecoder) keyName(token Token, resolver *TokenMap) (string, bool) {
	if d.encoding == EncodingText {
		return d.names.intern(token.Text), true
	}
	if token.Kind != KindID {
		return "", false
	}
	if resolver == nil {
		return "", true
	}
	name, _ := resolver.Lookup(token.ID)
	return name, true
}

// entryKey is keyName widened to the keys a document walk must address.
//
// The targeted scans only ever look up identifiers, because they know which
// field they want. A generic walk also has to name the numeric keys CK3 uses
// for its entity tables — `4501={ ... }` under landed_titles — so those are
// addressed by their decimal spelling.
func (d *StreamDecoder) entryKey(token Token, resolver *TokenMap) (string, bool) {
	// Punctuation is never a key. keyName is only ever reached from callers
	// that have already established this; a document walk has not, because it
	// meets anonymous containers as list items.
	if !token.IsScalar() {
		return "", false
	}
	switch token.Kind {
	case KindU32, KindU64:
		return strconv.FormatUint(token.Unsigned, 10), true
	case KindI32, KindI64:
		if d.encoding == EncodingText {
			return d.names.intern(token.Text), true
		}
		return strconv.FormatInt(token.Signed, 10), true
	}
	return d.keyName(token, resolver)
}

// SkipValue consumes whatever remains of the value that begins with token.
//
// Skipping is the whole point of a targeted scan: a container the caller did
// not ask for costs only its token count, never memory.
func (d *StreamDecoder) SkipValue(token Token) error {
	if token.Kind != KindOpen {
		return nil
	}
	target := d.depth - 1
	for d.depth > target {
		if _, err := d.Next(); err != nil {
			return err
		}
	}
	return nil
}

// SkipToDepth consumes tokens until the nesting level returns to target.
func (d *StreamDecoder) SkipToDepth(target int) error {
	for d.depth > target {
		if _, err := d.Next(); err != nil {
			return err
		}
	}
	return nil
}
