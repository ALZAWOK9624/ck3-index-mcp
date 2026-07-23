package indexer

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// A localization entry has a key, an optional numeric version, and a quoted
// value. parseLocBytes intentionally remains permissive so one malformed line
// does not hide every later key; these checks report the malformed line
// separately instead of silently dropping it from the index.
var localizationEntryPrefix = regexp.MustCompile(`^\s*[A-Za-z0-9_.:\-]+:\d*\s+`)

// These are the call-like localization expressions that CK3 evaluates inside
// square-bracket text. Ordinary prose parentheses are not checked, which
// keeps this rule narrow enough for translated text.
var localizationCallPattern = regexp.MustCompile(`(?i)(?:Concept|SelectLocalization|Select_CString|AddLocalizationIf|AddTextIf|Get[A-Za-z0-9_]*|SCOPE\.[A-Za-z0-9_]+)\s*\(`)

func checkLocalizationSyntax(relPath string, data []byte) []ctxDiag {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	var out []ctxDiag
	for lineIndex, line := range lines {
		lineNumber := lineIndex + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		characterCheckLine := line
		if _, closingQuote, ok := localizationLineValueSpan(line); ok {
			// CK3 ignores trailing localization comments. Vanilla Chinese-name
			// files contain a control byte in one such annotation, so checking
			// the entire physical line creates a baseline false positive.
			characterCheckLine = line[:closingQuote+1]
		}
		if col, ok := firstInvalidLocalizationRune(characterCheckLine); ok {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "localization_invalid_character",
				msg:      fmt.Sprintf("localization entry contains illegal control or replacement character %q", string([]rune(characterCheckLine)[col-1])),
				line:     lineNumber,
				col:      col,
			})
		}

		value, hasEntry := localizationLineValue(line)
		if !hasEntry {
			if localizationUnterminatedEntry(line) {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "localization_entry_syntax",
					msg:      "localization entry has an unterminated quoted value",
					line:     lineNumber,
					col:      firstNonSpaceColumn(line),
				})
			}
			continue
		}

		if col, ok := unbalancedLocalizationBrackets(value); ok {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "localization_macro_syntax",
				msg:      "localization square-bracket expression is not balanced",
				line:     lineNumber,
				col:      valueColumn(line, value, col),
			})
		}
		for _, match := range localizationCallPattern.FindAllStringIndex(value, -1) {
			open := strings.IndexByte(value[match[0]:match[1]], '(')
			if open < 0 {
				continue
			}
			open += match[0]
			if !balancedLocalizationCall(value, open) {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "localization_macro_syntax",
					msg:      "localization function expression has an unterminated quote or parenthesis",
					line:     lineNumber,
					col:      valueColumn(line, value, open+1),
				})
				break
			}
		}
	}
	return out
}

func localizationLineValue(line string) (string, bool) {
	valueStart, closingQuote, ok := localizationLineValueSpan(line)
	if !ok {
		return "", false
	}
	return line[valueStart:closingQuote], true
}

func localizationLineValueSpan(line string) (int, int, bool) {
	match := localizationEntryPrefix.FindStringIndex(line)
	if match == nil {
		return 0, 0, false
	}
	valueStart := match[1]
	for valueStart < len(line) && (line[valueStart] == ' ' || line[valueStart] == '\t') {
		valueStart++
	}
	if valueStart >= len(line) || (line[valueStart] != '"' && line[valueStart] != '\'') {
		return 0, 0, false
	}
	quote := line[valueStart]
	escaped := false
	closingQuote := -1
	for index := valueStart + 1; index < len(line); index++ {
		if escaped {
			escaped = false
			continue
		}
		if line[index] == '\\' {
			escaped = true
			continue
		}
		if line[index] == quote {
			// CK3 localization macros may contain an empty quoted argument
			// such as Select_CString(..., "", ...). Keep the last unescaped
			// quote as the outer value delimiter instead of treating that
			// inner pair as the end of the entry.
			closingQuote = index
		}
	}
	if closingQuote < 0 {
		return 0, 0, false
	}
	return valueStart + 1, closingQuote, true
}

func localizationUnterminatedEntry(line string) bool {
	match := localizationEntryPrefix.FindStringIndex(line)
	if match == nil {
		return false
	}
	rest := strings.TrimSpace(line[match[1]:])
	if len(rest) < 1 || (rest[0] != '"' && rest[0] != '\'') {
		return false
	}
	_, closed := localizationLineValue(line)
	return !closed
}

func firstInvalidLocalizationRune(line string) (int, bool) {
	for index, r := range []rune(line) {
		if r == '\uFFFD' || (unicode.IsControl(r) && r != '\t') {
			return index + 1, true
		}
	}
	return 0, false
}

func firstNonSpaceColumn(line string) int {
	for index, r := range []rune(line) {
		if !unicode.IsSpace(r) {
			return index + 1
		}
	}
	return 1
}

func unbalancedLocalizationBrackets(value string) (int, bool) {
	depth := 0
	for index, r := range []rune(value) {
		switch r {
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return index, true
			}
			depth--
		}
	}
	if depth != 0 {
		return len([]rune(value)), true
	}
	return 0, false
}

func balancedLocalizationCall(value string, open int) bool {
	runes := []rune(value)
	if open < 0 || open >= len(runes) || runes[open] != '(' {
		return true
	}
	depth := 0
	var quote rune
	escaped := false
	for index := open; index < len(runes); index++ {
		r := runes[index]
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			// Some CK3 localization files contain a doubled backslash
			// before an apostrophe inside a single-quoted macro argument.
			// Treat any immediately preceding backslash as escaping that
			// apostrophe; otherwise the final argument quote is mistaken for
			// an opening quote.
			if r == quote && !localizationQuoteEscaped(runes, index, open) {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func localizationQuoteEscaped(runes []rune, index, start int) bool {
	if index <= start {
		return false
	}
	return runes[index-1] == '\\'
}

func valueColumn(line, value string, valueIndex int) int {
	if valueIndex < 0 {
		valueIndex = 0
	}
	byteIndex := strings.Index(line, value)
	if byteIndex < 0 {
		return firstNonSpaceColumn(line)
	}
	return len([]rune(line[:byteIndex])) + valueIndex + 1
}
