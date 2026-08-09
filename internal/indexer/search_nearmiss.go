package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// A total miss used to return only "returned 0 evidence item(s)". The session
// audit of ~2100 real bot calls found 222 such misses out of 919 searches, and
// half of all ck3-index traffic sat inside back-to-back retry chains -- runs of
// up to 42 consecutive searches where the caller had nothing to correct toward
// and could only guess another spelling. Three failure shapes dominated:
//
//	pale knight / Pale Knight / pale_knight   separator and case
//	province:3602                             an id form borrowed from ck3_inspect
//	外乡人军团 / Outsider Legion / 26th Legion   a term with no indexed spelling
//
// The first two are answerable: the corpus stores pale_knight and 3602, so the
// query only needed normalizing. The third is not, but the caller still needs
// to be told that so it stops rephrasing. searchNearMiss handles both by
// retrying normalized spellings and, failing that, offering the vocabulary that
// shares the query's longest token.
const (
	nearMissVariantLimit = 4
	nearMissTokenMinimum = 4
)

// nearMissQueryVariants returns alternative spellings worth one more lookup,
// most-likely first and never including the original. Order matters: the caller
// stops at the first variant that produces evidence, so the cheap mechanical
// rewrites come before the lossy ones.
func nearMissQueryVariants(query string) []string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil
	}
	candidates := []string{
		strings.ToLower(trimmed),
		strings.ToLower(strings.NewReplacer(" ", "_", "-", "_").Replace(trimmed)),
		strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(trimmed)),
	}
	// ck3_inspect addresses objects as type:name and the audit shows callers
	// carrying that form into ck3_search, where the colon matches nothing.
	if _, name, found := strings.Cut(trimmed, ":"); found && strings.TrimSpace(name) != "" {
		candidates = append([]string{strings.TrimSpace(name)}, candidates...)
	}
	seen := map[string]bool{trimmed: true, strings.ToLower(trimmed): false}
	out := make([]string, 0, nearMissVariantLimit)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || candidate == trimmed || seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
		if len(out) >= nearMissVariantLimit {
			break
		}
	}
	return out
}

// longestQueryToken picks the token most likely to be the indexed identifier.
// "Djinn-Kings of Sarradon" indexes under sarradon far more often than under
// the whole phrase, so a phrase that matches nothing is retried on its most
// distinctive word rather than abandoned.
func longestQueryToken(query string) string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	best := ""
	for _, field := range fields {
		if isCommonQueryWord(field) {
			continue
		}
		if len([]rune(field)) > len([]rune(best)) {
			best = field
		}
	}
	if len([]rune(best)) < nearMissTokenMinimum {
		return ""
	}
	return strings.ToLower(best)
}

func isCommonQueryWord(word string) bool {
	switch strings.ToLower(word) {
	case "the", "of", "and", "for", "with", "from", "into", "a", "an":
		return true
	default:
		return false
	}
}

// searchNearMiss is only reached once every ordinary searcher has returned
// nothing, so it is allowed one more bounded pass over the identifier columns.
// It returns the evidence it recovered plus the spelling that recovered it, so
// the caller can tell the requester which query actually worked.
func (db *DB) searchNearMiss(ctx context.Context, query string, opts SearchOptions, limit int) ([]LLMEvidence, string, error) {
	if limit <= 0 {
		return nil, "", nil
	}
	for _, variant := range nearMissQueryVariants(query) {
		evidence, err := db.searchInsensitiveContains(ctx, variant, opts, limit)
		if err != nil {
			return nil, "", err
		}
		if len(evidence) > 0 {
			return evidence, variant, nil
		}
	}
	token := longestQueryToken(query)
	if token == "" || strings.EqualFold(token, strings.TrimSpace(query)) {
		return nil, "", nil
	}
	evidence, err := db.searchInsensitiveContains(ctx, token, opts, limit)
	if err != nil {
		return nil, "", err
	}
	if len(evidence) == 0 {
		return nil, "", nil
	}
	return evidence, token, nil
}

// searchInsensitiveContains is the case-folded twin of searchContains. Object
// names and localization keys are lower_snake_case by convention while callers
// type prose, and instr() is case-sensitive, which is why "Pale Knight" could
// not find pale_knight through any existing path.
func (db *DB) searchInsensitiveContains(ctx context.Context, query string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}
	var out []LLMEvidence
	lowered := strings.ToLower(query)
	if opts.Kind == "" || opts.Kind == "object" {
		rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,o.name,o.source_name,f.rel_path,o.line FROM objects o JOIN files f ON f.id=o.file_id WHERE f.overridden=0 AND instr(lower(o.name),?)>0 AND (?='' OR o.source_name=?) AND (?='' OR f.rel_path LIKE ?) ORDER BY o.source_rank,length(o.name),o.name LIMIT ?`,
			lowered, opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix)+"%", limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ev LLMEvidence
			ev.Kind = "object"
			if err := rows.Scan(&ev.Type, &ev.Name, &ev.Source, &ev.Path, &ev.Line); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, ev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(out) < limit && (opts.Kind == "" || opts.Kind == "localization") {
		rows, err := db.sql.QueryContext(ctx, `SELECT l.key,l.source_name,l.path,l.line,l.language,l.value FROM localization l JOIN files f ON f.id=l.file_id WHERE f.overridden=0 AND instr(lower(l.key),?)>0 AND (?='' OR l.source_name=?) AND (?='' OR f.rel_path LIKE ? ESCAPE '\') ORDER BY l.source_rank,length(l.key),l.key LIMIT ?`,
			lowered, opts.Source, opts.Source, opts.PathPrefix, escapeLike(opts.PathPrefix)+"%", limit-len(out))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ev LLMEvidence
			var language, value string
			ev.Kind = "localization"
			if err := rows.Scan(&ev.Name, &ev.Source, &ev.Path, &ev.Line, &language, &value); err != nil {
				rows.Close()
				return nil, err
			}
			ev.Path = evidencePath(ev.Path)
			ev.Detail = language + ": " + trimText(value, 180)
			out = append(out, ev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// noMatchGuidance is what the caller receives when even the near-miss pass
// found nothing. It replaces a bare zero count with the two facts that decide
// the next move: which spellings were already tried on the caller's behalf, and
// which tool answers the question that ck3_search cannot.
func noMatchGuidance(query string, opts SearchOptions) []string {
	attempted := append([]string{query}, nearMissQueryVariants(query)...)
	if token := longestQueryToken(query); token != "" {
		attempted = append(attempted, token)
	}
	sort.Strings(attempted[1:])
	guidance := []string{
		fmt.Sprintf("No indexed identifier, localization key, or script text matched any of: %s.", strings.Join(attempted, ", ")),
		"Rephrasing the same concept rarely helps once these spellings have missed; the term is probably not indexed under any of them.",
	}
	if opts.Kind != "" {
		guidance = append(guidance, fmt.Sprintf("kind=%q restricted this search; retry without kind before concluding the term is absent.", opts.Kind))
	}
	if opts.Source != "" || opts.PathPrefix != "" {
		guidance = append(guidance, "source/path_prefix restricted this search; retry without them before concluding the term is absent.")
	}
	guidance = append(guidance,
		"For a display name rather than an identifier, search the localization value with kind=localization.",
		"For a term that only appears in prose, use rg over the evidence paths returned by a neighbouring query.")
	return guidance
}
