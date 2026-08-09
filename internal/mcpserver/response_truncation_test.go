package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// An oversize result used to be discarded outright, which threw away work the
// server had already done and left the caller to guess a smaller limit. These
// tests pin the trimming behaviour that replaced it, including the two cases
// that must still fail rather than return something misleading.

func encodedResultBytes(t *testing.T, result map[string]any) int {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return len(data)
}

func testJSONInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		return int(number), number == float64(int(number))
	default:
		return 0, false
	}
}

func textResultWithEvidence(items int) map[string]any {
	evidence := make([]any, 0, items)
	for i := 0; i < items; i++ {
		evidence = append(evidence, map[string]any{
			"kind":   "object",
			"name":   strings.Repeat("x", 64),
			"detail": strings.Repeat("y", 128),
		})
	}
	structured := map[string]any{
		"intent":   "search",
		"summary":  "bounded",
		"evidence": evidence,
	}
	data, _ := json.Marshal(structured)
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(data)}},
		"structuredContent": structured,
	}
}

func TestOversizeTextResultIsTrimmedInsteadOfRejected(t *testing.T) {
	result := textResultWithEvidence(400)
	full := encodedResultBytes(t, result)
	budget := full / 4

	trimmed, err := enforceResponseBudget(result, budget, "evidence")
	if err != nil {
		t.Fatalf("oversize text result must be trimmed, not rejected: %v", err)
	}
	if got := encodedResultBytes(t, trimmed); got > budget {
		t.Fatalf("trimmed result is %d bytes, above the %d byte budget", got, budget)
	}

	structured := trimmed["structuredContent"].(map[string]any)
	if structured["truncated"] != true {
		t.Fatalf("a trimmed result must be marked truncated: %+v", structured)
	}
	evidence, ok := structured["evidence"].([]any)
	if !ok || len(evidence) == 0 {
		t.Fatalf("trimming must keep the leading evidence, got %+v", structured["evidence"])
	}
	if len(evidence) >= 400 {
		t.Fatalf("evidence was not actually trimmed: %d items", len(evidence))
	}
	// The caller reads content[0].text, so it has to describe the trimmed
	// payload rather than the discarded original.
	content := trimmed["content"].([]map[string]any)
	var echoed map[string]any
	if err := json.Unmarshal([]byte(content[0]["text"].(string)), &echoed); err != nil {
		t.Fatalf("trimmed content text is not valid JSON: %v", err)
	}
	if len(echoed["evidence"].([]any)) != len(evidence) {
		t.Fatalf("content text and structuredContent disagree after trimming: %d vs %d",
			len(echoed["evidence"].([]any)), len(evidence))
	}
}

func TestResultWithinBudgetIsReturnedUntouched(t *testing.T) {
	result := textResultWithEvidence(3)
	full := encodedResultBytes(t, result)

	returned, err := enforceResponseBudget(result, full*2, "evidence")
	if err != nil {
		t.Fatalf("in-budget result must not error: %v", err)
	}
	structured := returned["structuredContent"].(map[string]any)
	if _, marked := structured["truncated"]; marked {
		t.Fatalf("an in-budget result must not be marked truncated: %+v", structured)
	}
	if len(structured["evidence"].([]any)) != 3 {
		t.Fatalf("in-budget evidence was modified: %+v", structured["evidence"])
	}
}

func TestOversizeImageResultIsStillRejected(t *testing.T) {
	// A PNG has no meaningful tail to drop, so trimming it would hand back a
	// corrupt image. This case must keep failing loudly.
	result := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": "{}"},
			{"type": "image", "data": strings.Repeat("A", 4096), "mimeType": "image/png"},
		},
		"structuredContent": map[string]any{"intent": "render"},
	}
	if _, err := enforceResponseBudget(result, 512, "evidence"); err == nil {
		t.Fatal("an oversize result carrying an image must be rejected, not trimmed")
	}
}

func TestResponseTrimmingNeverTouchesUndeclaredContractArrays(t *testing.T) {
	patchFiles := make([]any, 0, 20)
	for i := 0; i < 20; i++ {
		patchFiles = append(patchFiles, map[string]any{"path": strings.Repeat("p", 200), "content": strings.Repeat("x", 800)})
	}
	structured := map[string]any{
		"intent":      "review",
		"evidence":    []any{map[string]any{"kind": "diagnostic", "detail": strings.Repeat("d", 512)}},
		"patch_files": patchFiles,
	}
	data, _ := json.Marshal(structured)
	result := map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(data)}},
		"structuredContent": structured,
	}
	if _, err := enforceResponseBudget(result, 2048, "evidence"); err == nil {
		t.Fatal("oversize patch_files must produce RESPONSE_TOO_LARGE instead of a partial file list")
	}
	if got := len(result["structuredContent"].(map[string]any)["patch_files"].([]any)); got != len(patchFiles) {
		t.Fatalf("undeclared patch_files was mutated: got %d want %d", got, len(patchFiles))
	}
}

func TestResponseTrimmingSynchronizesDerivedMetadata(t *testing.T) {
	result := textResultWithEvidence(64)
	structured := result["structuredContent"].(map[string]any)
	structured["count"] = 64
	structured["served"] = 64
	structured["evidence_count"] = 64
	structured["pagination"] = map[string]any{"page": 3, "limit": 64, "returned": 64, "has_more": false}
	data, _ := json.Marshal(structured)
	result["content"].([]map[string]any)[0]["text"] = string(data)

	trimmed, err := enforceResponseBudget(result, encodedResultBytes(t, result)/3, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	body := trimmed["structuredContent"].(map[string]any)
	returned := len(body["evidence"].([]any))
	count, countOK := testJSONInt(body["count"])
	served, servedOK := testJSONInt(body["served"])
	if !countOK || !servedOK || count != returned || served != returned {
		t.Fatalf("single-collection derived counts disagree with evidence=%d: %+v", returned, body)
	}
	if fieldCount, ok := testJSONInt(body["evidence_count"]); !ok || fieldCount != returned {
		t.Fatalf("field-specific count disagrees with evidence=%d: %+v", returned, body)
	}
	pagination := body["pagination"].(map[string]any)
	pageReturned, returnedOK := testJSONInt(pagination["returned"])
	if !returnedOK || pageReturned != returned || pagination["has_more"] != false {
		t.Fatalf("pagination was not synchronized: %+v", pagination)
	}
	if _, exists := pagination["next_page"]; exists {
		t.Fatalf("byte trimming invented a semantic next page that would skip the omitted tail: %+v", pagination)
	}
	truncation := body["truncation"].(map[string]any)["evidence"].(map[string]any)
	original, originalOK := testJSONInt(truncation["original"])
	truncatedReturned, truncatedOK := testJSONInt(truncation["returned"])
	if !originalOK || !truncatedOK || original != 64 || truncatedReturned != returned {
		t.Fatalf("truncation metadata = %+v", truncation)
	}
}

func TestSuggestionTrimmingDoesNotMutateEvidencePagination(t *testing.T) {
	result := textResultWithEvidence(0)
	structured := result["structuredContent"].(map[string]any)
	suggestions := textResultWithEvidence(64)["structuredContent"].(map[string]any)["evidence"]
	structured["suggestions"] = suggestions
	structured["pagination"] = map[string]any{"page": 1, "limit": 8, "returned": 0, "has_more": false}
	structured["suggestion_pagination"] = map[string]any{"page": 2, "limit": 64, "returned": 64, "has_more": false}
	data, _ := json.Marshal(structured)
	result["content"].([]map[string]any)[0]["text"] = string(data)

	trimmed, err := enforceResponseBudget(result, encodedResultBytes(t, result)/3, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	body := trimmed["structuredContent"].(map[string]any)
	evidencePage := body["pagination"].(map[string]any)
	if returned, _ := testJSONInt(evidencePage["returned"]); returned != 0 || evidencePage["has_more"] != false {
		t.Fatalf("suggestion trimming changed evidence pagination: %+v", evidencePage)
	}
	kept := len(body["suggestions"].([]any))
	suggestionPage := body["suggestion_pagination"].(map[string]any)
	if returned, ok := testJSONInt(suggestionPage["returned"]); !ok || returned != kept || suggestionPage["has_more"] != false {
		t.Fatalf("suggestion pagination was not synchronized without inventing a page: %+v", suggestionPage)
	}
}

func TestResponseTrimmingPreservesRuntimeEnvelope(t *testing.T) {
	result := textResultWithEvidence(64)
	result["database"] = map[string]any{"name": "secondary", "epoch": 7}
	result["indexState"] = map[string]any{"scan_generation": 12, "scan_status": "ready"}

	trimmed, err := enforceResponseBudget(result, encodedResultBytes(t, result)/3, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"database", "indexState"} {
		want, _ := json.Marshal(result[field])
		got, exists := trimmed[field]
		if !exists {
			t.Fatalf("response trimming dropped the runtime %s envelope", field)
		}
		encoded, _ := json.Marshal(got)
		if string(encoded) != string(want) {
			t.Fatalf("response trimming changed %s: got %s want %s", field, encoded, want)
		}
	}
}

func TestDatabaseAndArtifactToolsDeclareNoTrimmableFields(t *testing.T) {
	for _, name := range []string{"ck3_database", "ck3_package", "map_artifact", "map_assignment_plan"} {
		definition, ok := findCanonicalTool(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if len(definition.TrimmableFields) != 0 {
			t.Fatalf("%s can silently truncate contract fields: %v", name, definition.TrimmableFields)
		}
	}
}

func TestTrimmableFieldDeclarationsUseOnlyRelevanceOrderedEvidence(t *testing.T) {
	allowed := map[string]bool{"evidence": true, "suggestions": true}
	for _, definition := range registry() {
		for _, field := range definition.TrimmableFields {
			if !allowed[field] {
				t.Fatalf("%s declares unsafe trimmable field %q", definition.Name, field)
			}
		}
	}
}

func TestUntrimmableOversizeResultStillErrors(t *testing.T) {
	// Nothing to halve: one enormous string field. Reporting RESPONSE_TOO_LARGE
	// is better than returning a result the caller cannot act on.
	result := map[string]any{
		"content":           []map[string]any{{"type": "text", "text": "{}"}},
		"structuredContent": map[string]any{"summary": strings.Repeat("z", 8192)},
	}
	if _, err := enforceResponseBudget(result, 1024); err == nil {
		t.Fatal("an oversize result with no trimmable array must still error")
	}
}
