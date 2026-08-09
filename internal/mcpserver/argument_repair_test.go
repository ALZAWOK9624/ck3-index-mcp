package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every case below is a real rejected call taken from the Codex session audit,
// where these two bounds alone produced 92 of 173 ck3-index tool errors and
// several runs of four to seven identical retries.
func TestRepairToolArgumentsClampsAdvisoryBounds(t *testing.T) {
	definition, ok := findCanonicalTool("ck3_search")
	if !ok {
		t.Fatal("ck3_search is not registered")
	}
	for _, testCase := range []struct {
		name     string
		in       string
		wantJSON string
		wantNote string
	}{
		{
			name:     "limit above documented maximum",
			in:       `{"query":"innovation_","limit":100}`,
			wantJSON: `"limit":20`,
			wantNote: "limit 100 is above the maximum 20",
		},
		{
			name:     "response budget below documented minimum",
			in:       `{"query":"lesser god","limit":12,"max_response_bytes":16000}`,
			wantJSON: `"max_response_bytes":16384`,
			wantNote: "max_response_bytes 16000 is below the minimum 16384",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repaired, notices := repairToolArguments(definition.InputSchema, json.RawMessage(testCase.in))
			if !strings.Contains(string(repaired), testCase.wantJSON) {
				t.Fatalf("repaired arguments %s do not contain %s", repaired, testCase.wantJSON)
			}
			if err := validateArguments(repaired, definition.InputSchema, definition.CompatibilityProperties); err != nil {
				t.Fatalf("repaired arguments still fail validation: %v", err)
			}
			if len(notices) != 1 || !strings.Contains(notices[0], testCase.wantNote) {
				t.Fatalf("notices %v do not report %q", notices, testCase.wantNote)
			}
		})
	}
}

// A clamp that stayed invisible would teach the caller nothing and quietly
// change the answer, so the repaired bound has to reach the wire.
func TestClampedArgumentIsReportedOnTheResult(t *testing.T) {
	db, cfg := newSearchFixture(t)
	defer db.Close()

	result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{
		"query": "nearmiss_target",
		"limit": 100,
	})
	if result["isError"] == true {
		t.Fatalf("clamped search failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	notices, ok := body["argument_notices"].([]string)
	if !ok || len(notices) == 0 {
		t.Fatalf("result carries no argument_notices: %+v", body)
	}
	if !strings.Contains(notices[0], "limit 100 is above the maximum 20") {
		t.Fatalf("unexpected notice: %v", notices)
	}
	if served, _ := body["pagination"].(map[string]any)["limit"].(float64); served != 20 {
		t.Fatalf("clamped limit was not the limit actually served: %v", served)
	}
	// The text content is the encoding of structuredContent; a client reading
	// only the text must see the same repair.
	items := result["content"].([]map[string]any)
	if !strings.Contains(items[0]["text"].(string), "argument_notices") {
		t.Fatalf("text content lost the argument notice: %v", items[0]["text"])
	}
}

// Out-of-range values on fields that are not advisory knobs still fail: page 99
// clamped to the last page would answer a different question than the one asked.
func TestRepairToolArgumentsLeavesNonAdvisoryBoundsAlone(t *testing.T) {
	definition, _ := findCanonicalTool("ck3_search")
	repaired, notices := repairToolArguments(definition.InputSchema, json.RawMessage(`{"query":"x","page":99}`))
	if len(notices) != 0 {
		t.Fatalf("page was silently repaired: %v", notices)
	}
	if err := validateArguments(repaired, definition.InputSchema, definition.CompatibilityProperties); err == nil {
		t.Fatal("an out-of-range page must still be rejected")
	}
}

// A wrong type must not be masked by the clamp: it has to keep reaching the
// validator with its original value so the caller learns the real problem.
func TestRepairToolArgumentsIgnoresNonNumericKnobs(t *testing.T) {
	definition, _ := findCanonicalTool("ck3_search")
	repaired, notices := repairToolArguments(definition.InputSchema, json.RawMessage(`{"query":"x","limit":"many"}`))
	if len(notices) != 0 {
		t.Fatalf("a string limit was treated as a clamp: %v", notices)
	}
	if err := validateArguments(repaired, definition.InputSchema, definition.CompatibilityProperties); err == nil {
		t.Fatal("a string limit must still be rejected")
	}
}

// map_province_info was called with province_id and with subject; both name the
// documented id. history_year already maps to year on the outbound side in
// canonicalizeNextActions, so the inbound direction now agrees.
func TestRepairToolArgumentsAppliesObservedAliases(t *testing.T) {
	definition, ok := findCanonicalTool("map_province_info")
	if !ok {
		t.Fatal("map_province_info is not registered")
	}
	for _, alias := range []string{"province_id", "subject"} {
		repaired, notices := repairToolArguments(definition.InputSchema, json.RawMessage(`{"`+alias+`":"3602","year":1254}`))
		if !strings.Contains(string(repaired), `"id":"3602"`) {
			t.Fatalf("%s was not read as id: %s", alias, repaired)
		}
		if len(notices) == 0 || !strings.Contains(notices[0], alias) {
			t.Fatalf("%s alias was applied without a notice: %v", alias, notices)
		}
	}
}

// An alias must never overwrite a value the caller stated explicitly.
func TestRepairToolArgumentsKeepsExplicitCanonicalField(t *testing.T) {
	definition, _ := findCanonicalTool("map_province_info")
	repaired, _ := repairToolArguments(definition.InputSchema, json.RawMessage(`{"id":"111","province_id":"222","year":1254}`))
	if !strings.Contains(string(repaired), `"id":"111"`) {
		t.Fatalf("explicit id was overwritten by the alias: %s", repaired)
	}
}

// The audit found eight consecutive ck3_search calls rejected for an
// undocumented "domain" field. The old message named the offending field but
// never said what the tool accepts, leaving nothing to correct toward.
func TestUnknownArgumentErrorNamesTheAcceptedFields(t *testing.T) {
	definition, _ := findCanonicalTool("ck3_search")
	err := validateArguments(json.RawMessage(`{"query":"daukeni","domain":"culture"}`), definition.InputSchema, definition.CompatibilityProperties)
	if err == nil {
		t.Fatal("an undocumented field must still be rejected")
	}
	message := err.Error()
	for _, want := range []string{`unknown argument field "domain"`, "This tool accepts:", "path_prefix", "kind"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not mention %q", message, want)
		}
	}
}

func TestNearestArgumentNameOnlyCorrectsPlausibleTypos(t *testing.T) {
	accepted := []string{"query", "kind", "source", "path_prefix", "page", "limit", "visibility"}
	if nearest, ok := nearestArgumentName("pathprefix", accepted); !ok || nearest != "path_prefix" {
		t.Fatalf("pathprefix should suggest path_prefix, got %q ok=%v", nearest, ok)
	}
	if nearest, ok := nearestArgumentName("zzz", accepted); ok {
		t.Fatalf("an unrelated field must not be corrected, got %q", nearest)
	}
}
