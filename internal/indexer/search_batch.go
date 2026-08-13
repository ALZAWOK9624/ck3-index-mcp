package indexer

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// A session audit found 163 calls spent walking one id family a term at a
// time -- fifty consecutive searches for innovation_alchemical_gunpowder,
// innovation_beast_cavalry, innovation_night_vision_potion and so on, each
// succeeding, each costing a full round trip and a model turn. The evidence
// per call was never the problem: the median search returns one item and only
// sixteen percent reach the limit. The cost was the number of turns.
//
// LLMSearchBatch answers several terms in one call. It is deliberately not a
// different search: each term runs through LLMSearch exactly as it would
// alone, so ranking, public-source filtering, near-miss recovery and no-match
// guidance stay identical, and a batch result can be reasoned about as the
// concatenation of the calls it replaces.
const (
	maxBatchSearchQueries    = 8
	maxBatchSearchPerQuery   = 8
	batchSearchEvidenceCeil  = 48
	batchSearchQueryMaxRunes = 200
)

// LLMBatchQuery reports one term's outcome. A term that matched nothing still
// gets a row: "which of the things I asked about are absent" is usually the
// point of asking for several at once, and it cannot be read off an evidence
// list that simply lacks them.
type LLMBatchQuery struct {
	Query string `json:"query"`
	// Returned is what this response carries for the term, and equals Emitted.
	// Available is what the search found before the shared evidence ceiling
	// was applied, so a caller can tell "absent" from "crowded out".
	Returned  int  `json:"returned"`
	Available int  `json:"available,omitempty"`
	Emitted   int  `json:"emitted,omitempty"`
	HasMore   bool `json:"has_more,omitempty"`
	// Suggested counts low-confidence candidates. Without it a row reading
	// available=0 alongside a recovered spelling looks like a contradiction,
	// when it means "nothing matched, but here is something to look at".
	Suggested int    `json:"suggested,omitempty"`
	Spelling  string `json:"recovered_spelling,omitempty"`
}

func normalizeBatchQueries(queries []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(queries))
	for _, raw := range queries {
		query := strings.TrimSpace(raw)
		if query == "" {
			continue
		}
		if len([]rune(query)) > batchSearchQueryMaxRunes {
			return nil, fmt.Errorf("ck3_search queries entries must be at most %d characters", batchSearchQueryMaxRunes)
		}
		if seen[query] {
			continue
		}
		seen[query] = true
		out = append(out, query)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ck3_search requires at least one non-empty query")
	}
	if len(out) > maxBatchSearchQueries {
		return nil, fmt.Errorf("ck3_search accepts at most %d queries per call", maxBatchSearchQueries)
	}
	return out, nil
}

// LLMSearchBatch runs each term and returns one result carrying all of them.
// Terms run concurrently so an eight-term batch costs the slowest term, not
// the sum. The real-world telemetry this fixes: batch calls with near-miss
// terms serialized at ~0.7s each (p90 6.1s), and contended sessions at up to
// 36s. Concurrency is bounded across all batch calls on this DB, leaving pool
// capacity for ordinary reads even when several clients submit batches at
// once.
func (db *DB) LLMSearchBatch(ctx context.Context, queries []string, opts SearchOptions) (LLMResult, error) {
	terms, err := normalizeBatchQueries(queries)
	if err != nil {
		return LLMResult{}, err
	}
	perQuery := opts.Limit
	if perQuery <= 0 || perQuery > maxBatchSearchPerQuery {
		perQuery = maxBatchSearchPerQuery
	}

	result := LLMResult{Intent: "ck3_search", Counts: map[string]int{}}
	matched := 0
	type batchOutcome struct {
		row   LLMBatchQuery
		one   LLMResult
		err   error
		index int
	}
	outcomes := make([]batchOutcome, len(terms))
	var wg sync.WaitGroup
	for i, term := range terms {
		single := opts
		single.Query = term
		// Paging a batch would ask which term the page belongs to; a caller who
		// needs depth on one term should ask for that term alone.
		single.Page = 0
		single.Limit = perQuery

		wg.Add(1)
		go func(index int, term string, single SearchOptions) {
			defer wg.Done()
			select {
			case db.batchSearchSem <- struct{}{}:
				defer func() { <-db.batchSearchSem }()
			case <-ctx.Done():
				outcomes[index].err = ctx.Err()
				return
			}
			outcomes[index].index = index
			one, searchErr := db.LLMSearch(ctx, single)
			if searchErr != nil {
				outcomes[index].err = searchErr
				return
			}
			outcomes[index].one = one
			outcomes[index].row = LLMBatchQuery{Query: term, Returned: len(one.Evidence)}
			if one.Pagination != nil {
				outcomes[index].row.HasMore = one.Pagination.HasMore
			}
			outcomes[index].row.Suggested = len(one.Suggestions)
			outcomes[index].row.Spelling = one.RecoveredQuery
		}(i, term, single)
	}
	wg.Wait()
	result.Batch = make([]LLMBatchQuery, len(outcomes))
	for i := range outcomes {
		if outcomes[i].err != nil {
			return LLMResult{}, outcomes[i].err
		}
		one := outcomes[i].one
		row := outcomes[i].row
		if len(one.Evidence) > 0 {
			matched++
		}
		for kind, count := range one.Counts {
			result.Counts[kind] += count
		}
		row.Available = len(one.Evidence)
		result.Batch[i] = row
	}

	// Merge one item per term per round. Sequential concatenation lets the
	// first six wide terms consume all 48 slots and gives the final two no
	// evidence despite successful searches.
	for evidenceIndex := 0; len(result.Evidence) < batchSearchEvidenceCeil; evidenceIndex++ {
		emittedThisRound := false
		for termIndex := range outcomes {
			if evidenceIndex >= len(outcomes[termIndex].one.Evidence) {
				continue
			}
			item := outcomes[termIndex].one.Evidence[evidenceIndex]
			item.Query = terms[termIndex]
			result.Evidence = append(result.Evidence, item)
			result.Batch[termIndex].Emitted++
			emittedThisRound = true
			if len(result.Evidence) >= batchSearchEvidenceCeil {
				break
			}
		}
		if !emittedThisRound {
			break
		}
	}
	for index := range result.Batch {
		row := &result.Batch[index]
		row.Returned = row.Emitted
		if row.Emitted < row.Available {
			row.HasMore = true
			result.Truncated = true
		}
	}

	result.Summary = fmt.Sprintf("Batch search over %d term(s); %d matched; %d evidence item(s) emitted.",
		len(terms), matched, len(result.Evidence))
	result.Guidance = []string{
		"Every term is reported in batch, including the ones that matched nothing.",
		"Use ck3_inspect on an exact id from this evidence rather than searching for it again.",
	}
	if matched < len(terms) {
		result.Guidance = append(result.Guidance,
			"A term with available=0 is absent from the index under that spelling; search it alone to see the recovery guidance for it.")
	}
	if result.Truncated {
		result.Guidance = append(result.Guidance,
			fmt.Sprintf("Evidence stopped at %d items; ask for fewer terms per call to see the rest.", batchSearchEvidenceCeil))
	}
	if opts.publicMode() {
		result = result.withPublicBatchGuidance()
	}
	return result, nil
}

// withPublicBatchGuidance restores the batch-specific guidance that
// withPublicFilter replaces wholesale on each per-term result.
func (r LLMResult) withPublicBatchGuidance() LLMResult {
	r.Counts = nil
	r.Guidance = append([]string{"Public visibility omits private-source findings and aggregate counts."}, r.Guidance...)
	return r
}
