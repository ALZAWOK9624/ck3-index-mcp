package script

import "unicode/utf8"

type Node struct {
	ID       int64
	Parent   int64
	Depth    int
	Key      string
	Operator string
	Value    string
	Kind     string
	Line     int
	Col      int
	EndLine  int
	EndCol   int
	Children []*Node
}

type File struct {
	Nodes   []*Node
	Errors  []ParseError
	Limited bool
}

type ParseError struct {
	Message string
	Line    int
	Col     int
}

const parserLookahead = 4
const maxParserNodeChunk = 1024

// Bound recursive consumers as well as the parser for untrusted tool input.
const MaxDepth = 256
const MaxNodes = 500000

type parser struct {
	lexer         *Lexer
	lookahead     [parserLookahead]Token
	head          int
	buffered      int
	nodeChunks    [][]Node
	nodeChunk     []Node
	nodeChunkSize int
	nodePos       int
	nextID        int64
	errors        []ParseError
	gui           bool
	limited       bool
}

func Parse(text string) File {
	return parseLexer(newParserStringLexer(text), false)
}

// ParseBytes makes one immutable owned copy of UTF-8 CK3 source, then scans it
// byte by byte without a []rune conversion or a complete token tape.
func ParseBytes(input []byte) File {
	return parseLexer(newParserByteLexer(input), false)
}

// ParseGUI parses CK3/Jomini GUI syntax in addition to ordinary PDX script.
// GUI files add several prefix forms that are not key=value statements:
//
//	types Namespace { ... }
//	type child = parent { ... }
//	template Name { ... }
//	local_template Name { ... }
//	block "slot" { ... }
//	blockoverride "slot" { ... }
//
// Keeping these forms behind a GUI mode avoids changing the meaning of
// similarly named keys in events, history, and common script files.
func ParseGUI(text string) File {
	return parseLexer(newParserStringLexer(text), true)
}

// ParseGUIBytes is the byte-oriented equivalent of ParseGUI.
func ParseGUIBytes(input []byte) File {
	return parseLexer(newParserByteLexer(input), true)
}

func parseLexer(lexer *Lexer, gui bool) File {
	p := &parser{lexer: lexer, nodeChunkSize: parserNodeChunkSize(lexer.sourceLen()), nextID: 1, gui: gui}
	p.parseBlock(0, 0)
	return File{Nodes: p.buildTree(), Errors: p.errors, Limited: p.limited}
}

func (p *parser) parseBlock(parent int64, depth int) (Token, bool, *Node) {
	var last *Node
	for {
		tok := p.peek()
		switch tok.Kind {
		case TokenEOF:
			if parent != 0 {
				opening := p.nodeAt(parent)
				p.err("unclosed block: expected } before end of file", Token{Line: opening.Line, Col: opening.Col})
			}
			return Token{}, false, last
		case TokenRBrace:
			close := p.advance()
			if parent == 0 {
				p.err("unexpected closing brace }", close)
				continue
			}
			return close, true, last
		case TokenError:
			p.err(tok.Text, tok)
			p.advance()
		case TokenLBrace:
			if !p.withinLimits(tok, depth) {
				return Token{}, false, last
			}
			last = p.anonymousBlock(parent, depth)
		case TokenIdent, TokenString:
			if !p.withinLimits(tok, depth) {
				return Token{}, false, last
			}
			last = p.statement(parent, depth)
		default:
			p.err("expected statement", tok)
			p.advance()
		}
	}
}

func (p *parser) withinLimits(tok Token, depth int) bool {
	if depth < MaxDepth && p.nextID <= MaxNodes {
		return true
	}
	p.err("script exceeds parser depth or node limit", tok)
	p.limited = true
	// End all recursive frames without scanning or allocating more nodes.
	p.lexer.pos = p.lexer.sourceLen()
	p.buffered = 0
	return false
}

func (p *parser) anonymousBlock(parent int64, depth int) *Node {
	start := p.advance()
	n := p.allocNode()
	*n = Node{ID: p.nextID, Parent: parent, Depth: depth, Kind: "block", Line: start.Line, Col: start.Col}
	p.nextID++
	close, closed, lastChild := p.parseBlock(n.ID, depth+1)
	if closed {
		n.EndLine, n.EndCol = close.Line, close.Col+1
	} else if lastChild != nil {
		n.EndLine, n.EndCol = lastChild.EndLine, lastChild.EndCol
	} else {
		n.EndLine, n.EndCol = start.Line, start.Col+1
	}
	return n
}

func (p *parser) statement(parent int64, depth int) *Node {
	if p.gui {
		if n, ok := p.guiPrefixedStatement(parent, depth); ok {
			return n
		}
	}

	key := p.advance()
	n := p.allocNode()
	*n = Node{ID: p.nextID, Parent: parent, Depth: depth, Key: key.Text, Line: key.Line, Col: key.Col}
	p.nextID++
	op := p.peek()
	if op.Kind == TokenOperator {
		n.Operator = op.Text
		p.advance()
		val := p.peek()
		switch val.Kind {
		case TokenLBrace:
			p.advance()
			n.Kind = "block"
			close, closed, lastChild := p.parseBlock(n.ID, depth+1)
			if closed {
				n.EndLine, n.EndCol = close.Line, close.Col+1
			} else if lastChild != nil {
				n.EndLine, n.EndCol = lastChild.EndLine, lastChild.EndCol
			}
		case TokenIdent, TokenString, TokenOperator:
			// TokenOperator as value: OPERATOR = <=, COUNT = 1
			p.advance()
			n.Value = val.Text
			n.Kind = "atom"
			n.EndLine, n.EndCol = val.EndLine, val.EndCol
			// Jomini GUI also permits inheritance/instantiation followed by a
			// body: child = parent { ... }. Preserve parent in Value and attach
			// the following block to the same node.
			if (p.gui || val.Kind == TokenIdent && isValueTag(val.Text)) && p.peek().Kind == TokenLBrace {
				p.advance()
				n.Kind = "block"
				close, closed, _ := p.parseBlock(n.ID, depth+1)
				if closed {
					n.EndLine, n.EndCol = close.Line, close.Col+1
				}
				break
			}
			// CK3 GUI: type = A = B  (name = parent_type)
			if p.gui && p.peek().Kind == TokenOperator {
				nextOp := p.advance()
				nextVal := p.peek()
				if nextVal.Kind == TokenIdent || nextVal.Kind == TokenString {
					p.advance()
					n.Value = n.Value + " " + nextOp.Text + " " + nextVal.Text
					n.EndLine, n.EndCol = nextVal.EndLine, nextVal.EndCol
				} else {
					p.err("expected value after GUI inheritance operator", nextVal)
				}
			}
		default:
			p.err("expected value or block after operator", val)
			// Let the enclosing block consume its own closing delimiter.
			if val.Kind != TokenRBrace && val.Kind != TokenEOF {
				p.advance()
			}
		}
	} else if op.Kind == TokenLBrace {
		p.advance()
		n.Operator = "="
		n.Kind = "block"
		close, closed, _ := p.parseBlock(n.ID, depth+1)
		if closed {
			n.EndLine, n.EndCol = close.Line, close.Col+1
		}
	} else {
		n.Kind = "bare"
		n.EndLine, n.EndCol = key.EndLine, key.EndCol
	}
	if n.EndLine == 0 {
		n.EndLine, n.EndCol = n.Line, n.Col+runeLen(n.Key)
	}
	return n
}

func isValueTag(value string) bool {
	switch value {
	case "rgb", "hsv", "hsv360":
		return true
	}
	return false
}

func (p *parser) guiPrefixedStatement(parent int64, depth int) (*Node, bool) {
	first := p.peek()
	if first.Kind != TokenIdent && first.Kind != TokenString {
		return nil, false
	}

	newNode := func(key, operator, value string) *Node {
		n := p.allocNode()
		*n = Node{
			ID:       p.nextID,
			Parent:   parent,
			Depth:    depth,
			Key:      key,
			Operator: operator,
			Value:    value,
			Line:     first.Line,
			Col:      first.Col,
		}
		p.nextID++
		return n
	}
	switch first.Text {
	case "types":
		name := p.peekAt(1)
		brace := p.peekAt(2)
		if (name.Kind != TokenIdent && name.Kind != TokenString) || brace.Kind != TokenLBrace {
			return nil, false
		}
		p.advance()
		p.advance()
		p.advance()
		n := newNode("types", "namespace", name.Text)
		n.Kind = "block"
		close, closed, lastChild := p.parseBlock(n.ID, depth+1)
		setBlockEnd(n, close, closed, lastChild)
		return n, true

	case "template", "local_template":
		name := p.peekAt(1)
		brace := p.peekAt(2)
		if (name.Kind != TokenIdent && name.Kind != TokenString) || brace.Kind != TokenLBrace {
			return nil, false
		}
		p.advance()
		p.advance()
		p.advance()
		n := newNode(name.Text, first.Text, "")
		n.Kind = "block"
		close, closed, lastChild := p.parseBlock(n.ID, depth+1)
		setBlockEnd(n, close, closed, lastChild)
		return n, true

	case "type":
		name := p.peekAt(1)
		op := p.peekAt(2)
		base := p.peekAt(3)
		if (name.Kind != TokenIdent && name.Kind != TokenString) || op.Kind != TokenOperator || op.Text != "=" || (base.Kind != TokenIdent && base.Kind != TokenString) {
			return nil, false
		}
		p.advance()
		p.advance()
		p.advance()
		p.advance()
		n := newNode(name.Text, "type", base.Text)
		if p.peek().Kind == TokenLBrace {
			p.advance()
			n.Kind = "block"
			close, closed, lastChild := p.parseBlock(n.ID, depth+1)
			setBlockEnd(n, close, closed, lastChild)
		} else {
			n.Kind = "atom"
			n.EndLine, n.EndCol = base.Line, base.Col+runeLen(base.Text)
		}
		return n, true

	case "block", "blockoverride":
		name := p.peekAt(1)
		brace := p.peekAt(2)
		if (name.Kind != TokenIdent && name.Kind != TokenString) || brace.Kind != TokenLBrace {
			return nil, false
		}
		p.advance()
		p.advance()
		p.advance()
		n := newNode(first.Text, "slot", name.Text)
		n.Kind = "block"
		close, closed, lastChild := p.parseBlock(n.ID, depth+1)
		setBlockEnd(n, close, closed, lastChild)
		return n, true
	}

	return nil, false
}

func setBlockEnd(n *Node, close Token, closed bool, lastChild *Node) {
	if closed {
		n.EndLine, n.EndCol = close.Line, close.Col+1
		return
	}
	if lastChild != nil {
		n.EndLine, n.EndCol = lastChild.EndLine, lastChild.EndCol
		return
	}
	n.EndLine, n.EndCol = n.Line, n.Col+runeLen(n.Key)
}

func tokenValueKind(tok Token) string {
	if tok.Kind == TokenString {
		return "string"
	}
	switch tok.Text {
	case "yes", "no":
		return "bool"
	}
	return "atom"
}

func runeLen(text string) int {
	return utf8.RuneCountInString(text)
}

func (p *parser) allocNode() *Node {
	if p.nodePos == len(p.nodeChunk) {
		p.nodeChunk = make([]Node, p.nodeChunkSize)
		p.nodeChunks = append(p.nodeChunks, p.nodeChunk)
		p.nodePos = 0
	}
	node := &p.nodeChunk[p.nodePos]
	p.nodePos++
	return node
}

func (p *parser) nodeAt(id int64) *Node {
	index := int(id - 1)
	return &p.nodeChunks[index/p.nodeChunkSize][index%p.nodeChunkSize]
}

func parserNodeChunkSize(sourceBytes int) int {
	size := sourceBytes / 64
	if size < 16 {
		return 16
	}
	if size > maxParserNodeChunk {
		return maxParserNodeChunk
	}
	return size
}

func (p *parser) buildTree() []*Node {
	nodeCount := int(p.nextID - 1)
	if nodeCount == 0 {
		return nil
	}
	childSlots := make([]int, nodeCount+1)
	rootCount := 0
	for id := int64(1); id < p.nextID; id++ {
		node := p.nodeAt(id)
		if node.Parent == 0 {
			rootCount++
		} else {
			childSlots[node.Parent]++
		}
	}

	edges := make([]*Node, nodeCount-rootCount)
	offset := 0
	for id := int64(1); id < p.nextID; id++ {
		count := childSlots[id]
		if count == 0 {
			continue
		}
		p.nodeAt(id).Children = edges[offset : offset+count : offset+count]
		childSlots[id] = offset
		offset += count
	}

	roots := make([]*Node, 0, rootCount)
	for id := int64(1); id < p.nextID; id++ {
		node := p.nodeAt(id)
		if node.Parent == 0 {
			roots = append(roots, node)
			continue
		}
		index := childSlots[node.Parent]
		edges[index] = node
		childSlots[node.Parent]++
	}
	return roots
}

func (p *parser) peek() Token {
	return p.peekAt(0)
}

func (p *parser) peekAt(offset int) Token {
	if offset < 0 || offset >= parserLookahead {
		return Token{Kind: TokenEOF}
	}
	for p.buffered <= offset {
		tok := p.lexer.Next()
		for tok.Kind == TokenComment {
			tok = p.lexer.Next()
		}
		index := (p.head + p.buffered) % parserLookahead
		p.lookahead[index] = tok
		p.buffered++
	}
	return p.lookahead[(p.head+offset)%parserLookahead]
}

func (p *parser) advance() Token {
	t := p.peek()
	if p.buffered > 0 {
		p.lookahead[p.head] = Token{}
		p.head = (p.head + 1) % parserLookahead
		p.buffered--
	}
	return t
}

func (p *parser) err(msg string, tok Token) {
	if len(p.errors) >= 1000 {
		p.limited = true
		return
	}
	p.errors = append(p.errors, ParseError{Message: msg, Line: tok.Line, Col: tok.Col})
}
