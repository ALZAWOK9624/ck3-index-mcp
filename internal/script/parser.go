package script

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
	Nodes  []*Node
	Errors []ParseError
}

type ParseError struct {
	Message string
	Line    int
	Col     int
}

type parser struct {
	tokens []Token
	pos    int
	nextID int64
	errors []ParseError
	gui    bool
}

func Parse(text string) File {
	return parse(text, false)
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
	return parse(text, true)
}

func parse(text string, gui bool) File {
	p := &parser{tokens: Lex(text), nextID: 1, gui: gui}
	nodes, _ := p.parseBlock(0, 0)
	return File{Nodes: nodes, Errors: p.errors}
}

func (p *parser) parseBlock(parent int64, depth int) ([]*Node, *Token) {
	var nodes []*Node
	for {
		tok := p.peek()
		switch tok.Kind {
		case TokenEOF:
			return nodes, nil
		case TokenRBrace:
			close := p.advance()
			return nodes, &close
		case TokenError:
			p.err(tok.Text, tok)
			p.advance()
		case TokenLBrace:
			nodes = append(nodes, p.anonymousBlock(parent, depth))
		case TokenIdent, TokenString:
			nodes = append(nodes, p.statement(parent, depth))
		default:
			p.err("expected statement", tok)
			p.advance()
		}
	}
}

func (p *parser) anonymousBlock(parent int64, depth int) *Node {
	start := p.advance()
	n := &Node{ID: p.nextID, Parent: parent, Depth: depth, Kind: "block", Line: start.Line, Col: start.Col}
	p.nextID++
	var close *Token
	n.Children, close = p.parseBlock(n.ID, depth+1)
	if close != nil {
		n.EndLine, n.EndCol = close.Line, close.Col+1
	} else if len(n.Children) > 0 {
		last := n.Children[len(n.Children)-1]
		n.EndLine, n.EndCol = last.EndLine, last.EndCol
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
	n := &Node{ID: p.nextID, Parent: parent, Depth: depth, Key: key.Text, Line: key.Line, Col: key.Col}
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
			var close *Token
			n.Children, close = p.parseBlock(n.ID, depth+1)
			if close != nil {
				n.EndLine, n.EndCol = close.Line, close.Col+1
			} else if len(n.Children) > 0 {
				last := n.Children[len(n.Children)-1]
				n.EndLine, n.EndCol = last.EndLine, last.EndCol
			}
		case TokenIdent, TokenString, TokenOperator:
			// TokenOperator as value: OPERATOR = <=, COUNT = 1
			p.advance()
			n.Value = val.Text
			n.Kind = "atom"
			n.EndLine, n.EndCol = val.Line, val.Col+len([]rune(val.Text))
			// Jomini GUI also permits inheritance/instantiation followed by a
			// body: child = parent { ... }. Preserve parent in Value and attach
			// the following block to the same node.
			if p.gui && p.peek().Kind == TokenLBrace {
				p.advance()
				n.Kind = "block"
				var close *Token
				n.Children, close = p.parseBlock(n.ID, depth+1)
				if close != nil {
					n.EndLine, n.EndCol = close.Line, close.Col+1
				}
				break
			}
			// CK3 GUI: type = A = B  (name = parent_type)
			if p.peek().Kind == TokenOperator {
				nextOp := p.advance()
				nextVal := p.peek()
				if nextVal.Kind == TokenIdent || nextVal.Kind == TokenString {
					p.advance()
					n.Value = n.Value + " " + nextOp.Text + " " + nextVal.Text
					n.EndLine, n.EndCol = nextVal.Line, nextVal.Col+len([]rune(nextVal.Text))
				}
			}
		default:
			p.err("expected value or block after operator", val)
			p.advance()
		}
	} else if op.Kind == TokenLBrace {
		p.advance()
		n.Operator = "="
		n.Kind = "block"
		var close *Token
		n.Children, close = p.parseBlock(n.ID, depth+1)
		if close != nil {
			n.EndLine, n.EndCol = close.Line, close.Col+1
		}
	} else {
		n.Kind = "bare"
		n.EndLine, n.EndCol = key.Line, key.Col+len([]rune(key.Text))
	}
	if n.EndLine == 0 {
		n.EndLine, n.EndCol = n.Line, n.Col+len([]rune(n.Key))
	}
	return n
}

func (p *parser) guiPrefixedStatement(parent int64, depth int) (*Node, bool) {
	first := p.peek()
	if first.Kind != TokenIdent && first.Kind != TokenString {
		return nil, false
	}

	newNode := func(key, operator, value string) *Node {
		n := &Node{
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
		var close *Token
		n.Children, close = p.parseBlock(n.ID, depth+1)
		setBlockEnd(n, close)
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
		var close *Token
		n.Children, close = p.parseBlock(n.ID, depth+1)
		setBlockEnd(n, close)
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
			var close *Token
			n.Children, close = p.parseBlock(n.ID, depth+1)
			setBlockEnd(n, close)
		} else {
			n.Kind = "atom"
			n.EndLine, n.EndCol = base.Line, base.Col+len([]rune(base.Text))
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
		var close *Token
		n.Children, close = p.parseBlock(n.ID, depth+1)
		setBlockEnd(n, close)
		return n, true
	}

	return nil, false
}

func setBlockEnd(n *Node, close *Token) {
	if close != nil {
		n.EndLine, n.EndCol = close.Line, close.Col+1
		return
	}
	if len(n.Children) > 0 {
		last := n.Children[len(n.Children)-1]
		n.EndLine, n.EndCol = last.EndLine, last.EndCol
		return
	}
	n.EndLine, n.EndCol = n.Line, n.Col+len([]rune(n.Key))
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

func (p *parser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{Kind: TokenEOF}
	}
	return p.tokens[p.pos]
}

func (p *parser) peekAt(offset int) Token {
	pos := p.pos + offset
	if pos < 0 || pos >= len(p.tokens) {
		return Token{Kind: TokenEOF}
	}
	return p.tokens[pos]
}

func (p *parser) advance() Token {
	t := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return t
}

func (p *parser) err(msg string, tok Token) {
	p.errors = append(p.errors, ParseError{Message: msg, Line: tok.Line, Col: tok.Col})
}
