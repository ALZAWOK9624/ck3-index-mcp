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
}

func Parse(text string) File {
	p := &parser{tokens: Lex(text), nextID: 1}
	nodes := p.parseBlock(0, 0)
	return File{Nodes: nodes, Errors: p.errors}
}

func (p *parser) parseBlock(parent int64, depth int) []*Node {
	var nodes []*Node
	for {
		tok := p.peek()
		switch tok.Kind {
		case TokenEOF:
			return nodes
		case TokenRBrace:
			p.advance()
			return nodes
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
	n.Children = p.parseBlock(n.ID, depth+1)
	if len(n.Children) > 0 {
		last := n.Children[len(n.Children)-1]
		n.EndLine, n.EndCol = last.EndLine, last.EndCol
	} else {
		n.EndLine, n.EndCol = start.Line, start.Col+1
	}
	return n
}

func (p *parser) statement(parent int64, depth int) *Node {
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
			n.Children = p.parseBlock(n.ID, depth+1)
			if len(n.Children) > 0 {
				last := n.Children[len(n.Children)-1]
				n.EndLine, n.EndCol = last.EndLine, last.EndCol
			}
		case TokenIdent, TokenString, TokenOperator:
			// TokenOperator as value: OPERATOR = <=, COUNT = 1
			p.advance()
			n.Value = val.Text
			n.Kind = "atom"
			n.EndLine, n.EndCol = val.Line, val.Col+len([]rune(val.Text))
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
		n.Children = p.parseBlock(n.ID, depth+1)
	} else {
		n.Kind = "bare"
		n.EndLine, n.EndCol = key.Line, key.Col+len([]rune(key.Text))
	}
	if n.EndLine == 0 {
		n.EndLine, n.EndCol = n.Line, n.Col+len([]rune(n.Key))
	}
	return n
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
