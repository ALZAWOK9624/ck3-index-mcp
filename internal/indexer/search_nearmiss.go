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
// nothing. It returns the evidence it recovered plus the spelling that
// recovered it, so the caller can tell the requester which query actually
// worked.
//
// Every variant is looked up through the same indexed prefix searchers the
// ordinary path uses, never through a substring scan. That matters: measured
// against the 2 GB production index, one instr() pass over objects and
// localization costs about 1.5 s because neither column can serve a substring
// predicate from an index. Doing that for four variants and a token turned a
// miss from milliseconds into a p50 of 4.7 s and a worst case of 11.6 s. The
// prefix range is indexed, so the normalized spellings now cost roughly what
// the original query cost -- and the normalization cases this exists for
// ("Pale Knight" for pale_knight) land on an exact name anyway.
func (db *DB) searchNearMiss(ctx context.Context, query string, opts SearchOptions, limit int) ([]LLMEvidence, string, error) {
	if limit <= 0 {
		return nil, "", nil
	}
	for _, variant := range nearMissQueryVariants(query) {
		evidence, err := db.searchIndexedSpelling(ctx, variant, opts, limit)
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
	evidence, err := db.searchIndexedSpelling(ctx, token, opts, limit)
	if err != nil {
		return nil, "", err
	}
	if len(evidence) == 0 {
		return nil, "", nil
	}
	return evidence, token, nil
}

// searchIndexedSpelling looks one alternative spelling up through the indexed
// identifier searchers. Object names and localization keys are lower_snake_case
// by convention, so a lowercased variant meets the corpus on its own terms and
// the existing half-open prefix range does the work.
func (db *DB) searchIndexedSpelling(ctx context.Context, query string, opts SearchOptions, limit int) ([]LLMEvidence, error) {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil, nil
	}
	prefix := escapeLike(query) + "%"
	var out []LLMEvidence
	for _, search := range []struct {
		kind string
		fn   func(context.Context, string, string, SearchOptions, int) ([]LLMEvidence, error)
	}{
		{"object", db.searchObjects},
		{"localization", db.searchLocalizationKeys},
		{"reference", db.searchRefs},
	} {
		if opts.Kind != "" && opts.Kind != search.kind {
			continue
		}
		if len(out) >= limit {
			break
		}
		evidence, err := search.fn(ctx, query, prefix, opts, limit-len(out))
		if err != nil {
			return nil, err
		}
		out = appendUniqueEvidence(out, evidence, limit)
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
