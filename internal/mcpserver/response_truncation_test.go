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

	trimmed, err := enforceResponseBudget(result, budget)
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

	returned, err := enforceResponseBudget(result, full*2)
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
	if _, err := enforceResponseBudget(result, 512); err == nil {
		t.Fatal("an oversize result carrying an image must be rejected, not trimmed")
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
