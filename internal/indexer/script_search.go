package indexer

import (
	"strings"
	"unicode"

	"ck3-index/internal/script"
)

// buildScriptSearchText turns one parsed script into a compact, ordered FTS
// document. Braces, operators, comments, and source formatting are structural
// noise for discovery; every non-empty AST key and scalar value remains
// searchable, including values nested below object_fields' direct-field view.
func buildScriptSearchText(nodes []*script.Node) string {
	var document strings.Builder
	var visit func([]*script.Node)
	visit = func(children []*script.Node) {
		for _, node := range children {
			appendScriptSearchTerm(&document, node.Key)
			appendScriptSearchTerm(&document, node.Value)
			visit(node.Children)
		}
	}
	visit(nodes)
	return document.String()
}

func appendScriptSearchTerm(document *strings.Builder, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if document.Len() > 0 {
		document.WriteByte(' ')
	}
	space := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space {
			document.WriteByte(' ')
			space = false
		}
		document.WriteRune(r)
	}
}

type scriptTextLocation struct {
	Line    int
	Column  int
	Snippet string
}

// locateScriptText resolves an FTS candidate back to source evidence using
// only lexer tokens. LexBytes omits comments, so a comment that happens to
// repeat the query can never steal the location from the indexed AST token.
func locateScriptText(data []byte, query string) (scriptTextLocation, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return scriptTextLocation{}, false
	}
	wanted := searchableScriptTokens(script.LexBytes([]byte(query)))
	if len(wanted) == 0 {
		return scriptTextLocation{}, false
	}
	source := script.LexBytes(data)
	phrase := strings.Join(wanted, " ")
	for start := 0; start < len(source); start++ {
		if !isScriptSearchToken(source[start]) {
			continue
		}
		// Quoted CK3 values remain one lexer token even when an unquoted search
		// phrase becomes several tokens. Match only at word boundaries: FTS may
		// find "bar" in a quoted sentence, but an earlier "foobar" must not
		// steal the source location. Bare identifiers always require complete
		// token equality, including punctuation such as dots and colons.
		if source[start].Kind == script.TokenString && containsScriptTextWordFold(source[start].Text, phrase) {
			token := source[start]
			return scriptTextLocation{Line: token.Line, Column: token.Col, Snippet: scriptSearchSnippet(data, token.Line)}, true
		}
		if !strings.EqualFold(source[start].Text, wanted[0]) {
			continue
		}
		matched := 1
		for next := start + 1; next < len(source) && matched < len(wanted); next++ {
			if !isScriptSearchToken(source[next]) {
				continue
			}
			if !strings.EqualFold(source[next].Text, wanted[matched]) {
				break
			}
			matched++
		}
		if matched == len(wanted) {
			token := source[start]
			return scriptTextLocation{Line: token.Line, Column: token.Col, Snippet: scriptSearchSnippet(data, token.Line)}, true
		}
	}
	return scriptTextLocation{}, false
}

func containsScriptTextWordFold(text, query string) bool {
	if query == "" {
		return false
	}
	haystack := []rune(strings.ToLower(text))
	needle := []rune(strings.ToLower(query))
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		matched := true
		for offset := range needle {
			if haystack[start+offset] != needle[offset] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		beforeWord := start > 0 && isScriptTextWordRune(haystack[start-1])
		after := start + len(needle)
		afterWord := after < len(haystack) && isScriptTextWordRune(haystack[after])
		if !beforeWord && !afterWord {
			return true
		}
	}
	return false
}

func isScriptTextWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

func searchableScriptTokens(tokens []script.Token) []string {
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if isScriptSearchToken(token) && strings.TrimSpace(token.Text) != "" {
			out = append(out, token.Text)
		}
	}
	return out
}

func isScriptSearchToken(token script.Token) bool {
	return token.Kind == script.TokenIdent || token.Kind == script.TokenString
}

func scriptSearchSnippet(data []byte, wantedLine int) string {
	if wantedLine <= 0 {
		return ""
	}
	line := 1
	start := 0
	for offset := 0; offset <= len(data); offset++ {
		atEnd := offset == len(data)
		if !atEnd && data[offset] != '\n' && data[offset] != '\r' {
			continue
		}
		if line == wantedLine {
			return trimText(strings.TrimSpace(string(data[start:offset])), 220)
		}
		if atEnd {
			break
		}
		if data[offset] == '\r' && offset+1 < len(data) && data[offset+1] == '\n' {
			offset++
		}
		line++
		start = offset + 1
	}
	return ""
}
