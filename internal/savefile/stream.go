package savefile

import (
	"errors"
	"io"
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
	src    io.Reader
	buf    []byte
	start  int
	end    int
	eof    bool
	limits Limits
	tokens int64
	depth  int
	read   int64
}

// NewStreamDecoder returns a decoder over one decompressed section.
func NewStreamDecoder(src io.Reader, limits Limits) *StreamDecoder {
	return &StreamDecoder{
		src:    src,
		buf:    make([]byte, streamBufferBytes),
		limits: limits,
	}
}

// Depth reports the current container nesting level.
func (d *StreamDecoder) Depth() int { return d.depth }

// Consumed reports how many section bytes have been decoded.
func (d *StreamDecoder) Consumed() int64 { return d.read }

// Done reports whether the section has been fully consumed.
func (d *StreamDecoder) Done() bool {
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

// Next decodes the next token.
func (d *StreamDecoder) Next() (Token, error) {
	if d.Done() {
		return Token{}, newError(ErrTruncated, "the section ended where a token was expected")
	}
	return decodeToken(d.take, d.limits, &d.tokens, &d.depth)
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
