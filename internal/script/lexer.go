package script

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type TokenKind int

const (
	TokenEOF TokenKind = iota
	TokenIdent
	TokenString
	TokenLBrace
	TokenRBrace
	TokenOperator
	TokenComment
	TokenError
)

type Token struct {
	Kind TokenKind
	Text string
	Line int
	Col  int
}

// Lexer scans UTF-8 source directly. A lexer created for []byte input never
// retains that input through returned token strings: token text is copied as
// it is materialized. This lets ParseBytes callers reuse their read buffer as
// soon as parsing returns.
type Lexer struct {
	bytes      []byte
	text       string
	fromBytes  bool
	borrowText bool
	tokenCache [1024]string
	pos        int
	line       int
	col        int
}

// Lex preserves the original string API while using the byte-oriented lexer.
func Lex(text string) []Token {
	return lexAll(newStringLexer(text))
}

// LexBytes lexes a UTF-8 byte slice without first converting the whole input
// to string or []rune.
func LexBytes(input []byte) []Token {
	return lexAll(newByteLexer(input))
}

func lexAll(l *Lexer) []Token {
	var out []Token
	for {
		tok := l.Next()
		if tok.Kind != TokenComment {
			out = append(out, tok)
		}
		if tok.Kind == TokenEOF {
			return out
		}
	}
}

func newStringLexer(text string) *Lexer {
	l := &Lexer{text: text, line: 1, col: 1}
	l.skipBOM()
	return l
}

func newByteLexer(input []byte) *Lexer {
	l := &Lexer{bytes: input, fromBytes: true, line: 1, col: 1}
	l.skipBOM()
	return l
}

func newParserStringLexer(text string) *Lexer {
	// Keep one compact owned source instead of allocating every key and scalar
	// separately. Node strings may safely borrow slices from this immutable
	// copy for exactly as long as the returned AST remains live.
	l := &Lexer{text: strings.Clone(text), borrowText: true, line: 1, col: 1}
	l.skipBOM()
	return l
}

func newParserByteLexer(input []byte) *Lexer {
	// One conversion owns the caller's mutable buffer. Scanning remains a
	// byte-oriented UTF-8 state machine, while AST strings borrow from this
	// immutable copy instead of copying each token independently.
	l := &Lexer{text: string(input), borrowText: true, line: 1, col: 1}
	l.skipBOM()
	return l
}

func (l *Lexer) skipBOM() {
	if l.sourceLen() >= 3 && l.byteAt(0) == 0xef && l.byteAt(1) == 0xbb && l.byteAt(2) == 0xbf {
		// Match the old []rune lexer: a leading BOM is ignored without moving
		// the first real token away from column one.
		l.pos = 3
	}
}

func (l *Lexer) Next() Token {
	l.skipSpace()
	startLine, startCol := l.line, l.col
	r, ok := l.peek()
	if !ok {
		return Token{Kind: TokenEOF, Line: startLine, Col: startCol}
	}
	if r == '@' && l.peekN(1) == '[' {
		return l.arithmeticExpression(startLine, startCol)
	}
	switch r {
	case '#':
		return l.comment(startLine, startCol)
	case '{':
		l.advance()
		return Token{Kind: TokenLBrace, Text: "{", Line: startLine, Col: startCol}
	case '}':
		l.advance()
		return Token{Kind: TokenRBrace, Text: "}", Line: startLine, Col: startCol}
	case '"':
		return l.string(startLine, startCol)
	case '=', '<', '>', '!':
		return l.operator(startLine, startCol)
	case '?':
		if l.peekN(1) == '=' {
			l.advance()
			l.advance()
			return Token{Kind: TokenOperator, Text: "?=", Line: startLine, Col: startCol}
		}
		l.advance()
		return Token{Kind: TokenError, Text: "unexpected character ?", Line: startLine, Col: startCol}
	}
	return l.ident(startLine, startCol)
}

// arithmeticExpression keeps Jomini's @[ ... ] value syntax as one token.
// Expressions may contain whitespace and nested brackets, so ordinary
// identifier scanning would otherwise split a valid value into unrelated
// bare statements and corrupt every following source node on the line.
func (l *Lexer) arithmeticExpression(line, col int) Token {
	start := l.pos
	depth := 0
	for l.pos < l.sourceLen() {
		b := l.byteAt(l.pos)
		if b >= utf8.RuneSelf {
			_, size := l.decodeRune(l.pos)
			l.pos += size
			l.col++
			continue
		}
		l.pos++
		switch b {
		case '\r':
			if l.pos < l.sourceLen() && l.byteAt(l.pos) == '\n' {
				l.pos++
			}
			l.line++
			l.col = 1
		case '\n':
			l.line++
			l.col = 1
		case '[':
			depth++
			l.col++
		case ']':
			depth--
			l.col++
			if depth == 0 {
				return Token{Kind: TokenIdent, Text: l.tokenString(start, l.pos), Line: line, Col: col}
			}
		default:
			l.col++
		}
	}
	return Token{Kind: TokenError, Text: "unterminated arithmetic expression", Line: line, Col: col}
}

func (l *Lexer) skipSpace() {
	for l.pos < l.sourceLen() {
		b := l.byteAt(l.pos)
		if b < utf8.RuneSelf {
			switch b {
			case ' ', '\t', '\v', '\f':
				l.pos++
				l.col++
			case '\r':
				l.pos++
				if l.pos < l.sourceLen() && l.byteAt(l.pos) == '\n' {
					l.pos++
				}
				l.line++
				l.col = 1
			case '\n':
				l.pos++
				l.line++
				l.col = 1
			default:
				return
			}
			continue
		}
		r, size := l.decodeRune(l.pos)
		if !unicode.IsSpace(r) {
			return
		}
		l.pos += size
		l.col++
	}
}

func (l *Lexer) comment(line, col int) Token {
	start := l.pos
	for l.pos < l.sourceLen() {
		b := l.byteAt(l.pos)
		if b == '\n' || b == '\r' {
			break
		}
		if b < utf8.RuneSelf {
			l.pos++
		} else {
			_, size := l.decodeRune(l.pos)
			l.pos += size
		}
		l.col++
	}
	return Token{Kind: TokenComment, Text: l.sourceString(start, l.pos), Line: line, Col: col}
}

func (l *Lexer) string(line, col int) Token {
	l.pos++ // opening ASCII quote
	l.col++
	contentStart := l.pos
	segmentStart := contentStart
	var decoded []byte
	for l.pos < l.sourceLen() {
		charStart := l.pos
		b := l.byteAt(l.pos)
		if b >= utf8.RuneSelf {
			_, size := l.decodeRune(l.pos)
			l.pos += size
			l.col++
			continue
		}
		l.pos++
		switch b {
		case '"':
			l.col++
			if decoded == nil {
				return Token{Kind: TokenString, Text: l.tokenString(contentStart, charStart), Line: line, Col: col}
			}
			decoded = l.appendSource(decoded, segmentStart, charStart)
			return Token{Kind: TokenString, Text: string(decoded), Line: line, Col: col}
		case '\\':
			l.col++
			if decoded == nil {
				decoded = make([]byte, 0, charStart-contentStart+16)
			}
			decoded = l.appendSource(decoded, segmentStart, charStart)
			escapedStart := l.pos
			if escapedStart >= l.sourceLen() {
				return Token{Kind: TokenError, Text: "unterminated string", Line: line, Col: col}
			}
			escaped := l.byteAt(escapedStart)
			if escaped == '\r' {
				l.pos++
				if l.pos < l.sourceLen() && l.byteAt(l.pos) == '\n' {
					l.pos++
				}
				l.line++
				l.col = 1
				// advance consumes a CRLF pair. The previous rune lexer kept only
				// the CR in a string value, so retain that compatibility detail.
				decoded = append(decoded, '\r')
			} else if escaped == '\n' {
				l.pos++
				l.line++
				l.col = 1
				decoded = append(decoded, '\n')
			} else {
				if escaped < utf8.RuneSelf {
					l.pos++
				} else {
					_, size := l.decodeRune(l.pos)
					l.pos += size
				}
				l.col++
				decoded = l.appendSource(decoded, escapedStart, l.pos)
			}
			segmentStart = l.pos
		case '\r':
			if l.pos < l.sourceLen() && l.byteAt(l.pos) == '\n' {
				l.pos++
			}
			l.line++
			l.col = 1
			// The old rune lexer consumed CRLF as one newline and appended the
			// leading CR to string values. Normalize identically.
			if decoded == nil {
				decoded = make([]byte, 0, charStart-contentStart+16)
			}
			decoded = l.appendSource(decoded, segmentStart, charStart)
			decoded = append(decoded, '\r')
			segmentStart = l.pos
		case '\n':
			l.line++
			l.col = 1
		default:
			l.col++
		}
	}
	return Token{Kind: TokenError, Text: "unterminated string", Line: line, Col: col}
}

func (l *Lexer) operator(line, col int) Token {
	first, ok := l.peek()
	if !ok {
		return Token{Kind: TokenError, Text: "unexpected end of file", Line: line, Col: col}
	}
	l.advance()
	if l.peekN(0) == '=' {
		l.advance()
		switch first {
		case '=':
			return Token{Kind: TokenOperator, Text: "==", Line: line, Col: col}
		case '<':
			return Token{Kind: TokenOperator, Text: "<=", Line: line, Col: col}
		case '>':
			return Token{Kind: TokenOperator, Text: ">=", Line: line, Col: col}
		case '!':
			return Token{Kind: TokenOperator, Text: "!=", Line: line, Col: col}
		}
	}
	switch first {
	case '=':
		return Token{Kind: TokenOperator, Text: "=", Line: line, Col: col}
	case '<':
		return Token{Kind: TokenOperator, Text: "<", Line: line, Col: col}
	case '>':
		return Token{Kind: TokenOperator, Text: ">", Line: line, Col: col}
	case '!':
		return Token{Kind: TokenOperator, Text: "!", Line: line, Col: col}
	default:
		return Token{Kind: TokenOperator, Text: string(first), Line: line, Col: col}
	}
}

func (l *Lexer) ident(line, col int) Token {
	start := l.pos
	for l.pos < l.sourceLen() {
		b := l.byteAt(l.pos)
		if b < utf8.RuneSelf {
			if isASCIIIdentDelimiter(b) {
				break
			}
			l.pos++
			l.col++
			continue
		}
		r, size := l.decodeRune(l.pos)
		if unicode.IsSpace(r) {
			break
		}
		l.pos += size
		l.col++
	}
	if l.pos == start {
		r, ok := l.peek()
		if !ok {
			return Token{Kind: TokenError, Text: "unexpected end of file", Line: line, Col: col}
		}
		l.advance()
		return Token{Kind: TokenError, Text: "unexpected character " + string(r), Line: line, Col: col}
	}
	return Token{Kind: TokenIdent, Text: l.tokenString(start, l.pos), Line: line, Col: col}
}

func isASCIIIdentDelimiter(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f', '{', '}', '#', '=', '<', '>', '!', '?':
		return true
	default:
		return false
	}
}

func (l *Lexer) peek() (rune, bool) {
	if l.pos >= l.sourceLen() {
		return 0, false
	}
	b := l.byteAt(l.pos)
	if b < utf8.RuneSelf {
		return rune(b), true
	}
	r, _ := l.decodeRune(l.pos)
	return r, true
}

// peekN returns the rune at a rune offset from the current byte position.
func (l *Lexer) peekN(n int) rune {
	pos := l.pos
	for offset := 0; ; offset++ {
		if pos >= l.sourceLen() {
			return 0
		}
		r, size := l.decodeRune(pos)
		if offset == n {
			return r
		}
		pos += size
	}
}

func (l *Lexer) advance() {
	if l.pos >= l.sourceLen() {
		return
	}
	r, size := l.decodeRune(l.pos)
	l.pos += size
	// Treat Windows CRLF as one newline, matching CK3 source positions.
	if r == '\r' && l.pos < l.sourceLen() && l.byteAt(l.pos) == '\n' {
		l.pos++
		l.line++
		l.col = 1
		return
	}
	if r == '\n' || r == '\r' {
		l.line++
		l.col = 1
		return
	}
	l.col++
}

func (l *Lexer) sourceLen() int {
	if l.fromBytes {
		return len(l.bytes)
	}
	return len(l.text)
}

func (l *Lexer) byteAt(pos int) byte {
	if l.fromBytes {
		return l.bytes[pos]
	}
	return l.text[pos]
}

func (l *Lexer) decodeRune(pos int) (rune, int) {
	b := l.byteAt(pos)
	if b < utf8.RuneSelf {
		return rune(b), 1
	}
	if l.fromBytes {
		return utf8.DecodeRune(l.bytes[pos:])
	}
	return utf8.DecodeRuneInString(l.text[pos:])
}

func (l *Lexer) sourceString(start, end int) string {
	if l.borrowText {
		return l.text[start:end]
	}
	if l.fromBytes {
		return string(l.bytes[start:end])
	}
	// Avoid retaining a multi-megabyte source string through a short-lived
	// token or AST node while still avoiding a whole-input conversion.
	return strings.Clone(l.text[start:end])
}

func (l *Lexer) tokenString(start, end int) string {
	if l.borrowText {
		return l.text[start:end]
	}
	length := end - start
	index := uint(length * 131)
	if length > 0 {
		index ^= uint(l.byteAt(start)) * 17
		index ^= uint(l.byteAt(end-1)) * 31
		index ^= uint(l.byteAt(start+length/2)) * 47
	}
	index %= uint(len(l.tokenCache))
	if cached := l.tokenCache[index]; cached != "" && l.sourceEqualsString(start, end, cached) {
		return cached
	}
	value := l.sourceString(start, end)
	l.tokenCache[index] = value
	return value
}

func (l *Lexer) sourceEqualsString(start, end int, value string) bool {
	if end-start != len(value) {
		return false
	}
	for index := 0; index < len(value); index++ {
		if l.byteAt(start+index) != value[index] {
			return false
		}
	}
	return true
}

func (l *Lexer) appendSource(dst []byte, start, end int) []byte {
	if l.fromBytes {
		return append(dst, l.bytes[start:end]...)
	}
	return append(dst, l.text[start:end]...)
}
