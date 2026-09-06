package indexer

import (
	"regexp"
	"strings"
)

var localizationHeader = regexp.MustCompile(`^l_[A-Za-z0-9_]+:\s*(?:#.*)?$`)

// The standalone tool accepts complete files, so reject headerless prose and
// malformed entries that the indexer's tolerant extractor would simply skip.
func checkLocalizationDocument(rel string, data []byte) []ctxDiag {
	out := checkLocalizationSyntax(rel, data)
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	header := false
	for i, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if localizationHeader.MatchString(s) {
			header = true
			continue
		}
		if !header {
			out = append(out, ctxDiag{severity: "error", code: "localization_missing_header", msg: "complete localization file requires a language header such as l_english:", line: i + 1, col: firstNonSpaceColumn(line)})
			header = true // Report the missing header once; continue checking entries.
		}
		if _, ok := localizationLineValue(line); !ok && !localizationUnterminatedEntry(line) {
			out = append(out, ctxDiag{severity: "error", code: "localization_entry_syntax", msg: "expected localization key:version followed by a quoted value", line: i + 1, col: firstNonSpaceColumn(line)})
		}
	}
	return out
}
