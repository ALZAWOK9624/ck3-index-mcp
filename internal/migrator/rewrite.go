package migrator

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ck3-index/internal/script"
)

type rewriteResult struct {
	Content      string
	Replacements int
	Conflicts    []Conflict
	Diagnostics  []Conflict
}

var (
	numericKeyLine          = regexp.MustCompile(`^(\s*)([0-9]+)(\s*=)`)
	provinceAtom            = regexp.MustCompile(`\bprovince\s*=\s*([0-9]+)\b`)
	typedProvince           = regexp.MustCompile(`\bprovince:([0-9]+)\b`)
	provinceBlock           = regexp.MustCompile(`(?s)\bprovinces\s*=\s*\{(.*?)\}`)
	defaultList             = regexp.MustCompile(`(?s)\b(?:sea_zones|lakes|impassable_seas|impassable_mountains|river_provinces)\s*=\s*(?:LIST\s*)?\{(.*?)\}`)
	uncertainProvinceScalar = regexp.MustCompile(`(?m)\b([A-Za-z_][A-Za-z0-9_]*province[A-Za-z0-9_]*)\s*=\s*([0-9]+)\b`)
)

func rewriteSemantic(rel string, data []byte, policy *migrationPolicy) rewriteResult {
	text := string(data)
	result := rewriteResult{Content: text}
	if !utf8.Valid(data) {
		result.Conflicts = append(result.Conflicts, generalConflict("unsupported_text_encoding", rel, "semantic province migration requires valid UTF-8 text", ""))
		return result
	}
	lower := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	switch {
	case strings.HasPrefix(lower, "history/provinces/") && strings.HasSuffix(lower, ".txt"):
		result = rewriteHistory(rel, text, policy)
	case strings.HasPrefix(lower, "common/province_terrain/") && strings.HasSuffix(lower, ".txt"):
		result = rewriteTerrain(rel, text, policy)
	case strings.HasPrefix(lower, "common/landed_titles/") && strings.HasSuffix(lower, ".txt"):
		result = rewriteScalarPattern(rel, text, provinceAtom, policy, "scalar")
	case lower == "map_data/adjacencies.csv":
		result = rewriteAdjacencies(rel, text, policy)
	case lower == "map_data/default.map":
		result = rewriteCollectionPattern(rel, text, defaultList, policy)
	case strings.Contains(lower, "geographical_region") || lower == "map_data/island_region.txt" || lower == "map_data/climate.txt":
		result = rewriteCollectionPattern(rel, text, provinceBlock, policy)
	}
	if isMigrationScriptPath(lower) {
		typed := rewriteScalarPattern(rel, result.Content, typedProvince, policy, "scalar")
		result.Content = typed.Content
		result.Replacements += typed.Replacements
		result.Conflicts = append(result.Conflicts, typed.Conflicts...)
		result.Diagnostics = append(result.Diagnostics, suspiciousProvinceNumbers(rel, result.Content, policy)...)
	}
	return result
}

func suspiciousProvinceNumbers(rel, text string, policy *migrationPolicy) []Conflict {
	var out []Conflict
	for _, match := range uncertainProvinceScalar.FindAllStringSubmatchIndex(text, -1) {
		if len(match) < 6 || insideCommentOrString(text, match[4]) {
			continue
		}
		key := strings.ToLower(text[match[2]:match[3]])
		if key == "province" {
			continue
		}
		id, _ := strconv.Atoi(text[match[4]:match[5]])
		if _, knownOldProvince := policy.decisions[id]; !knownOldProvince {
			continue
		}
		message := "numeric field " + key + " may be a province reference, but its engine semantics are not proven; value was not changed"
		diagnostic := Conflict{Code: "uncertain_province_number", Path: rel, Line: lineAt(text, match[4]), SourceProvince: id, Message: message, Severity: "warning", SuggestedAction: "select_target"}
		diagnostic.ID = conflictID(diagnostic.Code, diagnostic.Path, diagnostic.Line, diagnostic.SourceProvince, diagnostic.Message)
		out = append(out, diagnostic)
	}
	return dedupeConflicts(out)
}

func rewriteHistory(rel, text string, policy *migrationPolicy) rewriteResult {
	parsed := script.Parse(text)
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	result := rewriteResult{Content: text}
	for _, node := range parsed.Nodes {
		old, err := strconv.Atoi(node.Key)
		start, end, ok := nodeRuneSpan(text, node)
		if err != nil || node.Kind != "block" || !ok {
			continue
		}
		targets, conflict := policy.targetsFor(old, "record", rel, node.Line)
		if conflict != nil {
			result.Conflicts = append(result.Conflicts, *conflict)
			continue
		}
		block := string([]rune(text)[start:end])
		var replacements []string
		for _, target := range targets {
			replacements = append(replacements, replaceLeadingNumericKey(block, old, target))
		}
		value := strings.Join(replacements, recordSeparator(text, start, block))
		if value != block {
			result.Replacements += max(1, len(targets))
			edits = append(edits, edit{start: start, end: end, value: value})
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	runes := []rune(text)
	for _, edit := range edits {
		runes = append(runes[:edit.start], append([]rune(edit.value), runes[edit.end:]...)...)
	}
	result.Content = string(runes)
	result.Content = dedupeIdenticalNumericBlocks(result.Content)
	return result
}

func recordSeparator(text string, start int, block string) string {
	if !strings.ContainsAny(block, "\r\n") {
		return " "
	}
	runes := []rune(text)
	lineStart := start
	for lineStart > 0 && runes[lineStart-1] != '\n' && runes[lineStart-1] != '\r' {
		lineStart--
	}
	if lineStart == 0 && len(runes) > 0 && runes[0] == '\ufeff' {
		lineStart = 1
	}
	indent := string(runes[lineStart:start])
	if strings.Contains(block, "\r\n") {
		return "\r\n" + indent
	}
	return "\n" + indent
}

func dedupeIdenticalNumericBlocks(text string) string {
	parsed := script.Parse(text)
	seen := map[int]string{}
	type removal struct{ start, end int }
	var removals []removal
	for _, node := range parsed.Nodes {
		id, err := strconv.Atoi(node.Key)
		start, end, ok := nodeRuneSpan(text, node)
		if err != nil || node.Kind != "block" || !ok {
			continue
		}
		block := string([]rune(text)[start:end])
		canonical := replaceLeadingNumericKey(block, id, 0)
		if previous, exists := seen[id]; exists && previous == canonical {
			removals = append(removals, removal{start, end})
			continue
		}
		seen[id] = canonical
	}
	sort.Slice(removals, func(i, j int) bool { return removals[i].start > removals[j].start })
	runes := []rune(text)
	for _, item := range removals {
		runes = append(runes[:item.start], runes[item.end:]...)
	}
	return string(runes)
}

func nodeRuneSpan(text string, node *script.Node) (int, int, bool) {
	starts := []int{0}
	runes := []rune(text)
	if len(runes) > 0 && runes[0] == '\ufeff' {
		starts[0] = 1
	}
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\r' {
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			starts = append(starts, i+1)
		} else if runes[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	if node.Line < 1 || node.EndLine < 1 || node.Line > len(starts) || node.EndLine > len(starts) || node.Col < 1 || node.EndCol < 1 {
		return 0, 0, false
	}
	start := starts[node.Line-1] + node.Col - 1
	end := starts[node.EndLine-1] + node.EndCol - 1
	if start < 0 || end < start || end > len(runes) {
		return 0, 0, false
	}
	return start, end, true
}

func replaceLeadingNumericKey(block string, old, target int) string {
	lines := splitLinesKeepEndings(block)
	if len(lines) == 0 {
		return block
	}
	match := numericKeyLine.FindStringSubmatchIndex(lines[0])
	if match == nil {
		return block
	}
	value, _ := strconv.Atoi(lines[0][match[4]:match[5]])
	if value != old {
		return block
	}
	lines[0] = lines[0][:match[4]] + strconv.Itoa(target) + lines[0][match[5]:]
	return strings.Join(lines, "")
}

func rewriteTerrain(rel, text string, policy *migrationPolicy) rewriteResult {
	lines := splitLinesKeepEndings(text)
	result := rewriteResult{Content: text}
	var out []string
	seen := map[int]string{}
	for i, line := range lines {
		match := numericKeyLine.FindStringSubmatchIndex(line)
		if match == nil || commentBefore(line, match[4]) {
			out = append(out, line)
			continue
		}
		old, _ := strconv.Atoi(line[match[4]:match[5]])
		targets, conflict := policy.targetsFor(old, "record", rel, i+1)
		if conflict != nil {
			result.Conflicts = append(result.Conflicts, *conflict)
			out = append(out, line)
			continue
		}
		for _, target := range targets {
			rewritten := line[:match[4]] + strconv.Itoa(target) + line[match[5]:]
			canonical := line[match[5]:]
			if previous, exists := seen[target]; exists && previous == canonical {
				continue
			}
			seen[target] = canonical
			out = append(out, rewritten)
			result.Replacements++
		}
	}
	result.Content = strings.Join(out, "")
	return result
}

func rewriteCollectionPattern(rel, text string, pattern *regexp.Regexp, policy *migrationPolicy) rewriteResult {
	result := rewriteResult{Content: text}
	indices := pattern.FindAllStringSubmatchIndex(text, -1)
	for i := len(indices) - 1; i >= 0; i-- {
		m := indices[i]
		if len(m) < 4 {
			continue
		}
		bodyStart, bodyEnd := m[2], m[3]
		body, count, conflicts := rewriteNumericBody(rel, text[bodyStart:bodyEnd], lineAt(text, bodyStart), policy)
		result.Replacements += count
		result.Conflicts = append(result.Conflicts, conflicts...)
		result.Content = result.Content[:bodyStart] + body + result.Content[bodyEnd:]
	}
	return result
}

func rewriteNumericBody(rel, body string, startLine int, policy *migrationPolicy) (string, int, []Conflict) {
	var b strings.Builder
	count := 0
	var conflicts []Conflict
	seen := map[int]bool{}
	for i := 0; i < len(body); {
		if body[i] == '#' {
			end := strings.IndexByte(body[i:], '\n')
			if end < 0 {
				b.WriteString(body[i:])
				break
			}
			b.WriteString(body[i : i+end])
			i += end
			continue
		}
		if body[i] >= '0' && body[i] <= '9' && (i == 0 || body[i-1] < '0' || body[i-1] > '9') {
			end := i + 1
			for end < len(body) && body[end] >= '0' && body[end] <= '9' {
				end++
			}
			old, _ := strconv.Atoi(body[i:end])
			targets, conflict := policy.targetsFor(old, "collection", rel, startLine+strings.Count(body[:i], "\n"))
			if conflict != nil {
				conflicts = append(conflicts, *conflict)
				b.WriteString(body[i:end])
			} else {
				var values []string
				for _, target := range targets {
					if seen[target] {
						continue
					}
					seen[target] = true
					values = append(values, strconv.Itoa(target))
				}
				b.WriteString(strings.Join(values, " "))
				count += len(values)
			}
			i = end
			continue
		}
		b.WriteByte(body[i])
		i++
	}
	return b.String(), count, conflicts
}

func rewriteScalarPattern(rel, text string, pattern *regexp.Regexp, policy *migrationPolicy, mode string) rewriteResult {
	result := rewriteResult{Content: text}
	indices := pattern.FindAllStringSubmatchIndex(text, -1)
	for i := len(indices) - 1; i >= 0; i-- {
		m := indices[i]
		if len(m) < 4 || insideCommentOrString(text, m[2]) {
			continue
		}
		old, _ := strconv.Atoi(text[m[2]:m[3]])
		targets, conflict := policy.targetsFor(old, mode, rel, lineAt(text, m[2]))
		if conflict != nil {
			result.Conflicts = append(result.Conflicts, *conflict)
			continue
		}
		if len(targets) == 0 {
			c := semanticConflict("scalar_drop_requires_review", rel, lineAt(text, m[2]), old, "dropping a scalar province reference cannot safely remove its owning CK3 structure")
			result.Conflicts = append(result.Conflicts, c)
			continue
		}
		if len(targets) != 1 {
			c := semanticConflict("scalar_split_requires_review", rel, lineAt(text, m[2]), old, "scalar province reference cannot expand to multiple targets")
			result.Conflicts = append(result.Conflicts, c)
			continue
		}
		result.Content = result.Content[:m[2]] + strconv.Itoa(targets[0]) + result.Content[m[3]:]
		result.Replacements++
	}
	return result
}

func rewriteAdjacencies(rel, text string, policy *migrationPolicy) rewriteResult {
	lines := splitLinesKeepEndings(text)
	result := rewriteResult{Content: text}
	seen := map[string]bool{}
	var out []string
	for i, line := range lines {
		ending := ""
		body := line
		if strings.HasSuffix(body, "\r\n") {
			ending, body = "\r\n", strings.TrimSuffix(body, "\r\n")
		} else if strings.HasSuffix(body, "\n") {
			ending, body = "\n", strings.TrimSuffix(body, "\n")
		}
		trimmed := strings.TrimSpace(body)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(strings.ToLower(trimmed), "from;") {
			out = append(out, line)
			continue
		}
		fields := strings.Split(body, ";")
		if len(fields) < 4 {
			out = append(out, line)
			continue
		}
		blocked := false
		for _, column := range []int{0, 1, 3} {
			old, err := strconv.Atoi(strings.TrimSpace(fields[column]))
			if err != nil || old < 0 {
				continue
			}
			targets, conflict := policy.targetsFor(old, "scalar", rel, i+1)
			if conflict != nil || len(targets) != 1 {
				if conflict != nil {
					result.Conflicts = append(result.Conflicts, *conflict)
				} else {
					result.Conflicts = append(result.Conflicts, semanticConflict("adjacency_scalar_resolution_invalid", rel, i+1, old, "adjacency From/To/Through requires exactly one target province"))
				}
				blocked = true
				continue
			}
			prefix := fields[column][:len(fields[column])-len(strings.TrimLeft(fields[column], " \t"))]
			suffix := fields[column][len(strings.TrimRight(fields[column], " \t")):]
			fields[column] = prefix + strconv.Itoa(targets[0]) + suffix
			result.Replacements++
		}
		if blocked {
			out = append(out, line)
			continue
		}
		rewritten := strings.Join(fields, ";")
		key := strings.ToLower(rewritten)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, rewritten+ending)
	}
	result.Content = strings.Join(out, "")
	return result
}

func splitLinesKeepEndings(text string) []string {
	if text == "" {
		return nil
	}
	var lines []string
	for len(text) > 0 {
		index := strings.IndexByte(text, '\n')
		if index < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:index+1])
		text = text[index+1:]
	}
	return lines
}

func insideCommentOrString(text string, offset int) bool {
	lineStart := strings.LastIndex(text[:offset], "\n") + 1
	quoted, escaped := false, false
	for i := lineStart; i < offset; i++ {
		c := text[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quoted {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if c == '#' && !quoted {
			return true
		}
	}
	return quoted
}

func commentBefore(line string, offset int) bool {
	index := strings.IndexByte(line, '#')
	return index >= 0 && index < offset
}

func lineAt(text string, offset int) int { return 1 + strings.Count(text[:offset], "\n") }

func dedupeConflicts(conflicts []Conflict) []Conflict {
	seen := map[string]bool{}
	var out []Conflict
	for _, conflict := range conflicts {
		if !seen[conflict.ID] {
			seen[conflict.ID] = true
			out = append(out, conflict)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
