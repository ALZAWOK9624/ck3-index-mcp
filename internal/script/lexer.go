package script

import "unicode"

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

type Lexer struct {
	input []rune
	pos   int
	line  int
	col   int
}

func Lex(text string) []Token {
	l := &Lexer{input: []rune(text), line: 1, col: 1}
	// Skip UTF-8 BOM (U+FEFF) that some CK3 script files carry.
	if len(l.input) > 0 && l.input[0] == '\uFEFF' {
		l.pos = 1
	}
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

func (l *Lexer) Next() Token {
	l.skipSpace()
	startLine, startCol := l.line, l.col
	r, ok := l.peek()
	if !ok {
		return Token{Kind: TokenEOF, Line: startLine, Col: startCol}
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

func (l *Lexer) skipSpace() {
	for {
		r, ok := l.peek()
		if !ok || !unicode.IsSpace(r) {
			return
		}
		l.advance()
	}
}

func (l *Lexer) comment(line, col int) Token {
	var buf []rune
	for {
		r, ok := l.peek()
		if !ok || r == '\n' || r == '\r' {
			break
		}
		buf = append(buf, r)
		l.advance()
	}
	return Token{Kind: TokenComment, Text: string(buf), Line: line, Col: col}
}

func (l *Lexer) string(line, col int) Token {
	var buf []rune
	l.advance()
	escaped := false
	for {
		r, ok := l.peek()
		if !ok {
			return Token{Kind: TokenError, Text: "unterminated string", Line: line, Col: col}
		}
		l.advance()
		if escaped {
			buf = append(buf, r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			return Token{Kind: TokenString, Text: string(buf), Line: line, Col: col}
		}
		buf = append(buf, r)
	}
}

func (l *Lexer) operator(line, col int) Token {
	first, ok := l.peek()
	if !ok {
		return Token{Kind: TokenError, Text: "unexpected end of file", Line: line, Col: col}
	}
	l.advance()
	if l.peekN(0) == '=' {
		l.advance()
		return Token{Kind: TokenOperator, Text: string([]rune{first, '='}), Line: line, Col: col}
	}
	return Token{Kind: TokenOperator, Text: string(first), Line: line, Col: col}
}

func (l *Lexer) ident(line, col int) Token {
	var buf []rune
	for {
		r, ok := l.peek()
		if !ok || unicode.IsSpace(r) || r == '{' || r == '}' || r == '#' || r == '=' || r == '<' || r == '>' {
			break
		}
		buf = append(buf, r)
		l.advance()
	}
	if len(buf) == 0 {
		r, ok := l.peek()
		if !ok {
			return Token{Kind: TokenError, Text: "unexpected end of file", Line: line, Col: col}
		}
		l.advance()
		return Token{Kind: TokenError, Text: "unexpected character " + string(r), Line: line, Col: col}
	}
	return Token{Kind: TokenIdent, Text: string(buf), Line: line, Col: col}
}

func (l *Lexer) peek() (rune, bool) {
	if l.pos >= len(l.input) {
		return 0, false
	}
	return l.input[l.pos], true
}

func (l *Lexer) peekN(n int) rune {
	if l.pos+n >= len(l.input) {
		return 0
	}
	return l.input[l.pos+n]
}

func (l *Lexer) advance() {
	if l.pos >= len(l.input) {
		return
	}
	r := l.input[l.pos]
	l.pos++
	if r == '\n' {
		l.line++
		l.col = 1
		return
	}
	if r == '\r' {
		l.line++
		l.col = 1
		return
	}
	l.col++
}
