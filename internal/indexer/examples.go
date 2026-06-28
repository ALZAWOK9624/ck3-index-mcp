package indexer

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
)

type ExampleQuery struct {
	Type     string       `json:"type"`
	Contains string       `json:"contains,omitempty"`
	Examples []ExampleHit `json:"examples"`
}

type ExampleHit struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Rank      int    `json:"rank"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	MatchLine int    `json:"match_line,omitempty"`
	Match     string `json:"match,omitempty"`
	Snippet   string `json:"snippet"`
}

func (db *DB) QueryExamples(ctx context.Context, typ, contains string, limit int) (ExampleQuery, error) {
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	q := ExampleQuery{Type: typ, Contains: contains}
	args := []any{typ}
	where := "o.object_type=?"
	sqlLimit := limit
	if contains != "" {
		sqlLimit = 600
	}
	args = append(args, sqlLimit)
	rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0 AND `+where+`
		ORDER BY CASE o.source_name WHEN 'game' THEN 0 WHEN 'godherja' THEN 1 WHEN 'project' THEN 2 ELSE 3 END,
			LENGTH(o.name), o.path, o.line
		LIMIT ?`, args...)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var h ExampleHit
		if err := rows.Scan(&h.Type, &h.Name, &h.Source, &h.Rank, &h.Path, &h.Line); err != nil {
			return q, err
		}
		if contains != "" {
			needle := strings.ToLower(contains)
			if !strings.Contains(strings.ToLower(h.Name), needle) && !strings.Contains(strings.ToLower(h.Path), needle) {
				matchLine, match, snippet, ok := readSnippetContaining(h.Path, h.Line, contains, 160, 20)
				if !ok {
					continue
				}
				h.MatchLine = matchLine
				h.Match = match
				h.Snippet = snippet
			} else {
				h.Snippet = readSnippet(h.Path, h.Line, 20)
			}
		} else {
			h.Snippet = readSnippet(h.Path, h.Line, 20)
		}
		key := h.Type + "\x00" + h.Name + "\x00" + h.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		q.Examples = append(q.Examples, h)
		if len(q.Examples) >= limit {
			break
		}
	}
	return q, rows.Err()
}

func readSnippet(path string, line, maxLines int) string {
	f, err := openMaybeRelative(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	start := line
	if start < 1 {
		start = 1
	}
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		if n < start {
			continue
		}
		out = append(out, sc.Text())
		if len(out) >= maxLines {
			break
		}
	}
	return strings.Join(out, "\n")
}

func readSnippetContaining(path string, startLine int, term string, scanLines int, maxLines int) (int, string, string, bool) {
	f, err := openMaybeRelative(path)
	if err != nil {
		return 0, "", "", false
	}
	defer f.Close()
	if startLine < 1 {
		startLine = 1
	}
	if scanLines <= 0 {
		scanLines = 160
	}
	if maxLines <= 0 {
		maxLines = 20
	}
	needle := strings.ToLower(term)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	lineNo := 0
	var window []string
	var windowStart int
	depth := 0
	started := false
	for sc.Scan() {
		lineNo++
		if lineNo < startLine {
			continue
		}
		if lineNo >= startLine+scanLines {
			break
		}
		line := sc.Text()
		depth += strings.Count(line, "{")
		depth -= strings.Count(line, "}")
		if strings.Contains(line, "{") {
			started = true
		}
		if started && lineNo > startLine && depth <= 0 {
			break
		}
		window = append(window, line)
		if windowStart == 0 {
			windowStart = lineNo
		}
		if len(window) > maxLines {
			window = window[1:]
			windowStart++
		}
		if strings.Contains(strings.ToLower(line), needle) {
			after := 0
			for after < maxLines/2 && sc.Scan() {
				lineNo++
				window = append(window, sc.Text())
				after++
			}
			if len(window) > maxLines {
				window = window[len(window)-maxLines:]
				windowStart = lineNo - len(window) + 1
			}
			return lineNo - after, strings.TrimSpace(line), strings.Join(window, "\n"), true
		}
	}
	return 0, "", "", false
}

func openMaybeRelative(path string) (*os.File, error) {
	if f, err := os.Open(path); err == nil {
		return f, nil
	}
	if filepath.IsAbs(path) {
		return nil, os.ErrNotExist
	}
	if exe, err := os.Executable(); err == nil {
		if f, err := os.Open(filepath.Join(filepath.Dir(exe), path)); err == nil {
			return f, nil
		}
	}
	return os.Open(filepath.Clean(path))
}

func SplitExampleID(id string) (string, string) {
	if typ, contains, ok := strings.Cut(id, ":"); ok {
		return typ, contains
	}
	return id, ""
}
