package savefile

import "encoding/binary"

// Lexeme ids of the Paradox binary token stream.
//
// These mirror jomini 0.35.0's table exactly. Any id outside this set is an
// identifier whose meaning comes from a version-matched token map.
const (
	lexEqual    uint16 = 0x0001
	lexOpen     uint16 = 0x0003
	lexClose    uint16 = 0x0004
	lexI32      uint16 = 0x000c
	lexF32      uint16 = 0x000d
	lexBool     uint16 = 0x000e
	lexQuoted   uint16 = 0x000f
	lexU32      uint16 = 0x0014
	lexUnquoted uint16 = 0x0017
	lexF64      uint16 = 0x0167
	lexRGB      uint16 = 0x0243
	lexU64      uint16 = 0x029c
	lexI64      uint16 = 0x0317

	lexLookupU16 uint16 = 0x0d3e
	lexLookupU32 uint16 = 0x0d3f
	lexLookupU8  uint16 = 0x0d40
	lexLookupU24 uint16 = 0x0d41

	// The compact block below is documented by jomini as EU5-only:
	// EMPTY_STRING, the alternate LOOKUP_* forms, and the FIXED5_*
	// fixed-point encodings. CK3 does not emit them, and they sit inside
	// CK3's identifier value range, so reading one means either a save this
	// package does not understand or a future patch that reassigned the id.
	// Either way, guessing would silently corrupt the answer.
	lexCompactFirst uint16 = 0x0d42
	lexCompactLast  uint16 = 0x0d55
)

// TokenKind is the decoded category of one binary token.
type TokenKind uint8

// Token kinds produced by Decoder.Next.
const (
	KindInvalid TokenKind = iota
	KindOpen
	KindClose
	KindEqual
	KindID
	KindU32
	KindU64
	KindI32
	KindI64
	KindBool
	KindF32
	KindF64
	KindQuoted
	KindUnquoted
	KindRGB
	KindLookup
)

// Token is one decoded binary token.
//
// Text points into the caller's buffer and stays valid only as long as that
// buffer does; callers that retain a value must copy it.
type Token struct {
	Kind TokenKind
	// ID is the raw identifier, for KindID.
	ID uint16
	// Text is the scalar payload, for KindQuoted and KindUnquoted.
	Text []byte
	// Unsigned holds U32 and U64 values.
	Unsigned uint64
	// Signed holds I32 and I64 values.
	Signed int64
	// Bits holds the raw little-endian payload of F32 (4 bytes) and F64.
	Bits [8]byte
	// Bool holds the boolean value.
	Bool bool
	// Lookup holds the lookup-table index.
	Lookup uint32
	// RGB holds red, green, blue and, when RGBHasAlpha, alpha.
	RGB         [4]uint32
	RGBHasAlpha bool
}

// IsScalar reports whether the token is a value rather than punctuation.
func (t Token) IsScalar() bool {
	switch t.Kind {
	case KindU32, KindU64, KindI32, KindI64, KindBool, KindF32, KindF64,
		KindQuoted, KindUnquoted, KindRGB, KindLookup, KindID:
		return true
	default:
		return false
	}
}

// Decoder walks one binary section within a fixed resource budget.
type Decoder struct {
	data   []byte
	pos    int
	limits Limits
	tokens int64
	depth  int
}

// NewDecoder returns a decoder over one already-extracted section.
func NewDecoder(data []byte, limits Limits) *Decoder {
	return &Decoder{data: data, limits: limits}
}

// Offset reports how many bytes have been consumed.
func (d *Decoder) Offset() int { return d.pos }

// Done reports whether the whole section has been consumed.
func (d *Decoder) Done() bool { return d.pos >= len(d.data) }

// Depth reports the current container nesting level.
func (d *Decoder) Depth() int { return d.depth }

func (d *Decoder) take(n int) ([]byte, error) {
	if n < 0 || len(d.data)-d.pos < n {
		return nil, newError(ErrTruncated, "token payload extends past the end of the section")
	}
	out := d.data[d.pos : d.pos+n]
	d.pos += n
	return out, nil
}

// takeFunc yields exactly n bytes, or reports why it cannot.
//
// The returned slice stays valid only until the next call, which is what lets
// the streaming decoder reuse one buffer for a save of any size.
type takeFunc func(n int) ([]byte, error)

func readU16(take takeFunc) (uint16, error) {
	raw, err := take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(raw), nil
}

// readString reads a u16-length-prefixed byte string.
func readString(take takeFunc, limits Limits) ([]byte, error) {
	length, err := readU16(take)
	if err != nil {
		return nil, err
	}
	if int(length) > limits.MaxStringBytes {
		return nil, newError(ErrTooLarge, "a scalar string exceeds the configured limit")
	}
	return take(int(length))
}

// Next decodes the next token.
//
// It returns io-free errors only: a truncated stream, an exhausted budget, or
// a token this package refuses to interpret.
func (d *Decoder) Next() (Token, error) {
	if d.Done() {
		return Token{}, newError(ErrTruncated, "the section ended where a token was expected")
	}
	return decodeToken(d.take, d.limits, &d.tokens, &d.depth)
}

// decodeToken is the one implementation of the binary token grammar, shared by
// the in-memory and streaming decoders so the two can never drift apart.
func decodeToken(take takeFunc, limits Limits, tokens *int64, depth *int) (Token, error) {
	*tokens++
	if *tokens > limits.MaxTokens {
		return Token{}, newError(ErrTooLarge, "the section exceeds the configured token budget")
	}

	id, err := readU16(take)
	if err != nil {
		return Token{}, err
	}

	switch id {
	case lexOpen:
		*depth++
		if *depth > limits.MaxDepth {
			return Token{}, newError(ErrTooLarge, "container nesting exceeds the configured depth limit")
		}
		return Token{Kind: KindOpen}, nil
	case lexClose:
		if *depth == 0 {
			return Token{}, newError(ErrMalformedToken, "a container close appeared outside any container")
		}
		*depth--
		return Token{Kind: KindClose}, nil
	case lexEqual:
		return Token{Kind: KindEqual}, nil
	case lexU32:
		raw, err := take(4)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindU32, Unsigned: uint64(binary.LittleEndian.Uint32(raw))}, nil
	case lexU64:
		raw, err := take(8)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindU64, Unsigned: binary.LittleEndian.Uint64(raw)}, nil
	case lexI32:
		raw, err := take(4)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindI32, Signed: int64(int32(binary.LittleEndian.Uint32(raw)))}, nil
	case lexI64:
		raw, err := take(8)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindI64, Signed: int64(binary.LittleEndian.Uint64(raw))}, nil
	case lexBool:
		raw, err := take(1)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindBool, Bool: raw[0] != 0}, nil
	case lexQuoted, lexUnquoted:
		text, err := readString(take, limits)
		if err != nil {
			return Token{}, err
		}
		kind := KindQuoted
		if id == lexUnquoted {
			kind = KindUnquoted
		}
		return Token{Kind: kind, Text: text}, nil
	case lexF32:
		raw, err := take(4)
		if err != nil {
			return Token{}, err
		}
		token := Token{Kind: KindF32}
		copy(token.Bits[:4], raw)
		return token, nil
	case lexF64:
		raw, err := take(8)
		if err != nil {
			return Token{}, err
		}
		token := Token{Kind: KindF64}
		copy(token.Bits[:], raw)
		return token, nil
	case lexRGB:
		return decodeRGB(take)
	case lexLookupU8:
		raw, err := take(1)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindLookup, Lookup: uint32(raw[0])}, nil
	case lexLookupU16:
		value, err := readU16(take)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindLookup, Lookup: uint32(value)}, nil
	case lexLookupU24:
		raw, err := take(3)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindLookup, Lookup: binary.LittleEndian.Uint32([]byte{raw[0], raw[1], raw[2], 0})}, nil
	case lexLookupU32:
		raw, err := take(4)
		if err != nil {
			return Token{}, err
		}
		return Token{Kind: KindLookup, Lookup: binary.LittleEndian.Uint32(raw)}, nil
	}

	if id >= lexCompactFirst && id <= lexCompactLast {
		return Token{}, newError(ErrMalformedToken,
			"the save uses a compact token encoding that CK3 does not emit; refusing to guess its meaning")
	}
	return Token{Kind: KindID, ID: id}, nil
}

// readRGB decodes the fixed `{ u32 u32 u32 [u32] }` shape that follows an RGB
// lexeme. The braces are part of the value, not the surrounding structure, so
// they are consumed here and never affect the decoder's depth.
func decodeRGB(take takeFunc) (Token, error) {
	open, err := readU16(take)
	if err != nil {
		return Token{}, err
	}
	if open != lexOpen {
		return Token{}, newError(ErrMalformedToken, "an rgb value is not followed by a container")
	}
	token := Token{Kind: KindRGB}
	for channel := 0; channel < 3; channel++ {
		marker, err := readU16(take)
		if err != nil {
			return Token{}, err
		}
		if marker != lexU32 {
			return Token{}, newError(ErrMalformedToken, "an rgb channel is not a 32-bit unsigned value")
		}
		raw, err := take(4)
		if err != nil {
			return Token{}, err
		}
		token.RGB[channel] = binary.LittleEndian.Uint32(raw)
	}
	next, err := readU16(take)
	if err != nil {
		return Token{}, err
	}
	switch next {
	case lexClose:
		return token, nil
	case lexU32:
		raw, err := take(4)
		if err != nil {
			return Token{}, err
		}
		token.RGB[3] = binary.LittleEndian.Uint32(raw)
		token.RGBHasAlpha = true
		end, err := readU16(take)
		if err != nil {
			return Token{}, err
		}
		if end != lexClose {
			return Token{}, newError(ErrMalformedToken, "an rgb value does not close after its alpha channel")
		}
		return token, nil
	default:
		return Token{}, newError(ErrMalformedToken, "an rgb value has an unexpected fourth component")
	}
}

// SkipValue consumes whatever remains of the value that begins with token.
//
// Scalar payloads are already consumed by Next, so only a container needs
// work. Skipping is what keeps the portrait gene blobs out of a save card.
func (d *Decoder) SkipValue(token Token) error {
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
