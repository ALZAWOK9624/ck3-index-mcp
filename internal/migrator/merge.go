package migrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func conflictID(code, path string, line, source int, message string) string {
	payload := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s", code, strings.ToLower(path), line, source, message)
	sum := sha256.Sum256([]byte(payload))
	return "migration-conflict-" + hex.EncodeToString(sum[:8])
}

func mergeConflict(path, message string) Conflict {
	c := Conflict{Code: "three_way_text_conflict", Path: path, Severity: "error", Message: message, SuggestedAction: "prefer_project"}
	c.ID = conflictID(c.Code, c.Path, 0, 0, c.Message)
	return c
}

// mergeText applies a conservative line-preserving three-way merge. Unique
// unchanged anchors split independent edits; ambiguous overlapping regions are
// deliberately returned as conflicts instead of receiving marker text.
func mergeText(path, oldText, projectText, targetText string, resolutions map[string]Resolution) (string, *Conflict) {
	if projectText == oldText {
		return targetText, nil
	}
	if targetText == oldText || projectText == targetText {
		return projectText, nil
	}
	merged, ok := mergeLineRanges(splitLinesKeepEndings(oldText), splitLinesKeepEndings(projectText), splitLinesKeepEndings(targetText))
	if ok {
		return strings.Join(merged, ""), nil
	}
	conflict := mergeConflict(path, "project and target changed the same text region differently")
	if resolution, exists := resolutions[conflict.ID]; exists {
		switch strings.ToLower(strings.TrimSpace(resolution.Action)) {
		case "prefer_project":
			return projectText, nil
		case "prefer_target", "drop":
			return targetText, nil
		}
	}
	return "", &conflict
}

func mergeLineRanges(oldLines, projectLines, targetLines []string) ([]string, bool) {
	if linesEqual(projectLines, oldLines) {
		return append([]string(nil), targetLines...), true
	}
	if linesEqual(targetLines, oldLines) || linesEqual(projectLines, targetLines) {
		return append([]string(nil), projectLines...), true
	}

	// Strip shared outer anchors before searching the middle. This keeps the
	// common case cheap and preserves every original byte, including newlines.
	prefix := 0
	for prefix < len(oldLines) && prefix < len(projectLines) && prefix < len(targetLines) &&
		oldLines[prefix] == projectLines[prefix] && oldLines[prefix] == targetLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(projectLines)-prefix && suffix < len(targetLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == projectLines[len(projectLines)-1-suffix] &&
		oldLines[len(oldLines)-1-suffix] == targetLines[len(targetLines)-1-suffix] {
		suffix++
	}
	if prefix > 0 || suffix > 0 {
		middle, ok := mergeLineRanges(
			oldLines[prefix:len(oldLines)-suffix],
			projectLines[prefix:len(projectLines)-suffix],
			targetLines[prefix:len(targetLines)-suffix],
		)
		if ok {
			out := append([]string(nil), oldLines[:prefix]...)
			out = append(out, middle...)
			out = append(out, oldLines[len(oldLines)-suffix:]...)
			return out, true
		}
	}

	oi, pi, ti, found := uniqueTripleAnchor(oldLines, projectLines, targetLines)
	if !found {
		return nil, false
	}
	left, ok := mergeLineRanges(oldLines[:oi], projectLines[:pi], targetLines[:ti])
	if !ok {
		return nil, false
	}
	right, ok := mergeLineRanges(oldLines[oi+1:], projectLines[pi+1:], targetLines[ti+1:])
	if !ok {
		return nil, false
	}
	out := append(left, oldLines[oi])
	out = append(out, right...)
	return out, true
}

func uniqueTripleAnchor(oldLines, projectLines, targetLines []string) (int, int, int, bool) {
	type occurrence struct{ count, index int }
	count := func(lines []string) map[string]occurrence {
		out := map[string]occurrence{}
		for i, line := range lines {
			item := out[line]
			item.count++
			item.index = i
			out[line] = item
		}
		return out
	}
	o, p, t := count(oldLines), count(projectLines), count(targetLines)
	bestDistance := int(^uint(0) >> 1)
	bo, bp, bt := 0, 0, 0
	found := false
	for oldIndex, line := range oldLines {
		oo := o[line]
		pp, pok := p[line]
		tt, tok := t[line]
		if oo.count != 1 || !pok || !tok || pp.count != 1 || tt.count != 1 {
			continue
		}
		distance := absInt(oo.index-pp.index) + absInt(oo.index-tt.index)
		if !found || distance < bestDistance || (distance == bestDistance && (oldIndex < bo || (oldIndex == bo && (pp.index < bp || (pp.index == bp && tt.index < bt))))) {
			found, bestDistance = true, distance
			bo, bp, bt = oo.index, pp.index, tt.index
		}
	}
	return bo, bp, bt, found
}

func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
