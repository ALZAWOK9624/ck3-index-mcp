package savefile

import (
	"encoding/binary"
	"math"
	"strconv"
)

// Encoding is the grammar a save's sections are written in.
//
// CK3 writes the same document in two forms. The binary form names its fields
// with numeric identifiers that only a version-matched token map can resolve;
// the text form spells them out. Everything above the lexer — the metadata
// projection and the gamestate scan — is written against the token stream, so
// the difference is confined to this file and to which decoder is selected.
type Encoding string

const (
	// EncodingBinary is the tokenized form CK3 writes by default.
	EncodingBinary Encoding = "binary"
	// EncodingText is the plain Paradox form CK3 writes in debug mode, and
	// the form a melted save takes.
	EncodingText Encoding = "text"
)

// textSource hands unconsumed bytes to the lexer without committing to how
// they are held: the in-memory decoder slices the whole section, the streaming
// decoder refills a fixed window. A returned slice stays valid only until the
// next call, which is what lets the streaming decoder reuse one buffer.
type textSource interface {
	peekAhead(n int) ([]byte, error)
	discard(n int)
}

// textScanChunk is how far ahead the lexer looks for a scalar's end before
// asking for more. Nearly every scalar in a save is shorter than this, so one
// look usually settles it.
const textScanChunk = 64

// isTextSpace reports the bytes CK3 writes between tokens.
func isTextSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// isTextDelimiter reports the bytes that end an unquoted scalar.
func isTextDelimiter(b byte) bool {
	return isTextSpace(b) || b == '{' || b == '}' || b == '=' || b == '"' || b == '#'
}

// skipTextFiller consumes whitespace and comments, and reports whether any
// byte at all remains.
//
// It is separate from token decoding because Done has to answer the same
// question: a section that ends in a newline is finished, not truncated.
func skipTextFiller(src textSource) (bool, error) {
	for {
		window, err := src.peekAhead(textScanChunk)
		if err != nil {
			return false, err
		}
		if len(window) == 0 {
			return false, nil
		}
		consumed := 0
		for consumed < len(window) && isTextSpace(window[consumed]) {
			consumed++
		}
		if consumed == len(window) {
			src.discard(consumed)
			continue
		}
		if window[consumed] != '#' {
			src.discard(consumed)
			return true, nil
		}
		// A comment runs to the end of its line, which may be further than
		// this window reaches.
		src.discard(consumed)
		for {
			line, err := src.peekAhead(textScanChunk)
			if err != nil {
				return false, err
			}
			if len(line) == 0 {
				return false, nil
			}
			end := 0
			for end < len(line) && line[end] != '\n' {
				end++
			}
			if end < len(line) {
				src.discard(end + 1)
				break
			}
			src.discard(end)
		}
	}
}

// decodeTextToken is the one implementation of the text grammar, shared by the
// in-memory and streaming decoders so the two can never drift apart.
//
// It produces the same Token kinds the binary decoder does, so a scalar is
// read the same way whichever form the save is in. The one shape with no
// binary counterpart is a decimal literal: the binary form's F64 is fixed
// point, and reusing it here would scale every text float by 100000.
func decodeTextToken(src textSource, limits Limits, tokens *int64, depth *int) (Token, error) {
	*tokens++
	if *tokens > limits.MaxTokens {
		return Token{}, newError(ErrTooLarge, "the section exceeds the configured token budget")
	}
	remaining, err := skipTextFiller(src)
	if err != nil {
		return Token{}, err
	}
	if !remaining {
		return Token{}, newError(ErrTruncated, "the section ended where a token was expected")
	}

	window, err := src.peekAhead(1)
	if err != nil {
		return Token{}, err
	}
	switch window[0] {
	case '{':
		src.discard(1)
		*depth++
		if *depth > limits.MaxDepth {
			return Token{}, newError(ErrTooLarge, "container nesting exceeds the configured depth limit")
		}
		return Token{Kind: KindOpen}, nil
	case '}':
		if *depth == 0 {
			return Token{}, newError(ErrMalformedToken, "a container close appeared outside any container")
		}
		src.discard(1)
		*depth--
		return Token{Kind: KindClose}, nil
	case '=':
		src.discard(1)
		return Token{Kind: KindEqual}, nil
	case '"':
		return decodeTextQuoted(src, limits)
	}
	token, err := decodeTextUnquoted(src, limits)
	if err != nil {
		return Token{}, err
	}
	// `rgb { 255 207 51 }` is one value whose braces belong to it, exactly as
	// the binary form's single RGB lexeme is. Left split, the colour name
	// becomes a field's value and the channels become stray list items.
	if token.Kind == KindUnquoted && isColourName(token.Text) {
		return decodeTextColour(src, limits)
	}
	return token, nil
}

// isColourName reports the colour spaces CK3 writes with a brace payload.
func isColourName(raw []byte) bool {
	switch string(raw) {
	case "rgb", "hsv", "hsv360":
		return true
	default:
		return false
	}
}

// decodeTextColour reads the `{ n n n [n] }` payload that follows a colour
// name, if one follows at all. A bare `rgb` with no braces stays text.
func decodeTextColour(src textSource, limits Limits) (Token, error) {
	remaining, err := skipTextFiller(src)
	if err != nil {
		return Token{}, err
	}
	if !remaining {
		return Token{Kind: KindUnquoted, Text: []byte("rgb")}, nil
	}
	window, err := src.peekAhead(1)
	if err != nil {
		return Token{}, err
	}
	if len(window) == 0 || window[0] != '{' {
		return Token{Kind: KindUnquoted, Text: []byte("rgb")}, nil
	}
	src.discard(1)

	token := Token{Kind: KindRGB}
	channel := 0
	for {
		if _, err := skipTextFiller(src); err != nil {
			return Token{}, err
		}
		window, err := src.peekAhead(1)
		if err != nil {
			return Token{}, err
		}
		if len(window) == 0 {
			return Token{}, newError(ErrTruncated, "a colour value is not closed before the section ends")
		}
		if window[0] == '}' {
			src.discard(1)
			token.RGBHasAlpha = channel > 3
			return token, nil
		}
		component, err := decodeTextUnquoted(src, limits)
		if err != nil {
			return Token{}, err
		}
		if channel < len(token.RGB) {
			// A channel may be written as a fraction in the hsv form.
			switch component.Kind {
			case KindI64:
				token.RGB[channel] = uint32(component.Signed)
			case KindDecimal:
				token.RGB[channel] = uint32(textDecimal(component) * 255)
			default:
				return Token{}, newError(ErrMalformedToken,
					"a colour channel is neither a number nor a fraction")
			}
		}
		channel++
		if channel > len(token.RGB) {
			return Token{}, newError(ErrMalformedToken, "a colour value has too many channels")
		}
	}
}

// decodeTextQuoted reads a `"..."` scalar, honouring backslash escapes.
func decodeTextQuoted(src textSource, limits Limits) (Token, error) {
	// The closing quote is found by growing the look-ahead rather than by
	// copying, so the payload is still one slice of the decoder's own buffer.
	size := textScanChunk
	for {
		window, err := src.peekAhead(size)
		if err != nil {
			return Token{}, err
		}
		if len(window) == 0 || window[0] != '"' {
			return Token{}, newError(ErrMalformedToken, "a quoted scalar does not begin with a quote")
		}
		end, escapes, closed := scanQuotedBody(window)
		if closed {
			if end-1 > limits.MaxStringBytes {
				return Token{}, newError(ErrTooLarge, "a scalar string exceeds the configured limit")
			}
			text := window[1 : end-1]
			if escapes {
				text = unescapeTextScalar(text)
			}
			src.discard(end)
			return Token{Kind: KindQuoted, Text: text}, nil
		}
		if len(window) < size {
			return Token{}, newError(ErrTruncated, "a quoted scalar is not closed before the section ends")
		}
		if size > limits.MaxStringBytes {
			return Token{}, newError(ErrTooLarge, "a scalar string exceeds the configured limit")
		}
		size *= 2
	}
}

// scanQuotedBody locates the closing quote of window[0]=='"'. It reports the
// index one past that quote, whether any escape was seen, and whether the
// scalar closed inside the window at all.
func scanQuotedBody(window []byte) (end int, escapes bool, closed bool) {
	for index := 1; index < len(window); index++ {
		switch window[index] {
		case '\\':
			escapes = true
			index++
		case '"':
			return index + 1, escapes, true
		}
	}
	return 0, escapes, false
}

// unescapeTextScalar resolves backslash escapes in place.
//
// The result is written over the source slice, which is the decoder's own
// window and is already treated as valid only until the next token.
func unescapeTextScalar(text []byte) []byte {
	out := text[:0]
	for index := 0; index < len(text); index++ {
		if text[index] == '\\' && index+1 < len(text) {
			index++
		}
		out = append(out, text[index])
	}
	return out
}

// decodeTextUnquoted reads a bare scalar and classifies it.
func decodeTextUnquoted(src textSource, limits Limits) (Token, error) {
	size := textScanChunk
	for {
		window, err := src.peekAhead(size)
		if err != nil {
			return Token{}, err
		}
		end := 0
		for end < len(window) && !isTextDelimiter(window[end]) {
			end++
		}
		if end < len(window) || len(window) < size {
			if end == 0 {
				return Token{}, newError(ErrMalformedToken, "an unquoted scalar is empty")
			}
			if end > limits.MaxStringBytes {
				return Token{}, newError(ErrTooLarge, "a scalar string exceeds the configured limit")
			}
			raw := window[:end]
			src.discard(end)
			return classifyTextScalar(raw), nil
		}
		if size > limits.MaxStringBytes {
			return Token{}, newError(ErrTooLarge, "a scalar string exceeds the configured limit")
		}
		size *= 2
	}
}

// classifyTextScalar decides which token kind a bare scalar is.
//
// Getting this wrong is silent, so the classification is deliberately narrow:
// only what parses exactly as an integer, a decimal or a boolean becomes one.
// Everything else stays text, which is what keeps a date like 1066.10.1 — two
// dots, not one — out of the decimal branch and readable as the date it is.
func classifyTextScalar(raw []byte) Token {
	switch string(raw) {
	case "yes":
		return Token{Kind: KindBool, Bool: true, Text: raw}
	case "no":
		return Token{Kind: KindBool, Bool: false, Text: raw}
	}
	if value, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		return Token{Kind: KindI64, Signed: value, Text: raw}
	}
	if isTextDecimal(raw) {
		if value, err := strconv.ParseFloat(string(raw), 64); err == nil {
			token := Token{Kind: KindDecimal, Text: raw}
			binary.LittleEndian.PutUint64(token.Bits[:], math.Float64bits(value))
			return token
		}
	}
	return Token{Kind: KindUnquoted, Text: raw}
}

// isTextDecimal reports the `-?digits.digits` shape, and only that shape.
// ParseFloat alone would also accept 1e5 and, more damagingly, "Infinity" and
// "NaN", which are ordinary identifiers in a save.
func isTextDecimal(raw []byte) bool {
	index := 0
	if index < len(raw) && (raw[index] == '-' || raw[index] == '+') {
		index++
	}
	digits, dots := 0, 0
	for ; index < len(raw); index++ {
		switch {
		case raw[index] >= '0' && raw[index] <= '9':
			digits++
		case raw[index] == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return dots == 1 && digits > 0
}

// textDecimal returns the value of a KindDecimal token.
func textDecimal(token Token) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(token.Bits[:]))
}

// textDate accepts the `year.month.day[.hour]` literal a text save writes, and
// only that. Anything else is an ordinary identifier that happened to reach a
// date field, and answering with it would be worse than answering with
// nothing.
//
// The component ranges are what separate a date from a version: "1.19.0.6" is
// four dotted numbers too, and only month 19 and day 0 tell it apart.
func textDate(raw []byte) string {
	var components [4]int
	count, digits := 0, 0
	for index := 0; index <= len(raw); index++ {
		if index == len(raw) || raw[index] == '.' {
			if digits == 0 || count == len(components) {
				return ""
			}
			count++
			digits = 0
			continue
		}
		if raw[index] < '0' || raw[index] > '9' {
			return ""
		}
		components[count] = components[count]*10 + int(raw[index]-'0')
		if components[count] > 1<<20 {
			return ""
		}
		digits++
	}
	if count < 3 {
		return ""
	}
	if components[1] < 1 || components[1] > 12 || components[2] < 1 || components[2] > 31 {
		return ""
	}
	if count == 4 && components[3] > 23 {
		return ""
	}
	return string(raw)
}

// maxInternedNames bounds the interning table.
//
// A save uses a few hundred distinct field names, so the table is tiny in
// practice. The cap is there because a hostile file could spell a different
// name on every line, and an unbounded table would then scale with the save.
const maxInternedNames = 4096

// nameTable interns text field names.
//
// A binary key resolves through the token map to a string the map already
// owns, so naming a field costs nothing. A text key is bytes in the decoder's
// window and would otherwise allocate a fresh string for every field in the
// save; looking it up as m[string(raw)] does not allocate, so only a name's
// first appearance does.
type nameTable struct {
	names map[string]string
}

func (t *nameTable) intern(raw []byte) string {
	if name, ok := t.names[string(raw)]; ok {
		return name
	}
	name := string(raw)
	if len(t.names) >= maxInternedNames {
		return name
	}
	if t.names == nil {
		t.names = make(map[string]string, 256)
	}
	t.names[name] = name
	return name
}
