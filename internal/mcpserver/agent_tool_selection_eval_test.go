package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadAgentToolSelectionEvalSuite(t *testing.T) AgentToolSelectionEvalSuite {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "agent_tool_selection", "v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read agent tool-selection fixture: %v", err)
	}
	suite, err := ParseAgentToolSelectionEvalSuite(data)
	if err != nil {
		t.Fatalf("parse agent tool-selection fixture: %v", err)
	}
	return suite
}

func TestAgentToolSelectionEvalManifest(t *testing.T) {
	suite := loadAgentToolSelectionEvalSuite(t)
	if got := len(suite.Cases); got < 12 {
		t.Fatalf("agent tool-selection cases = %d, want at least 12", got)
	}
	if err := ValidateAgentToolSelectionEvalSuite(suite); err != nil {
		t.Fatalf("validate agent tool-selection fixture: %v", err)
	}
}

func TestAgentToolSelectionEvalScoresCanonicalFixturePath(t *testing.T) {
	suite := loadAgentToolSelectionEvalSuite(t)
	plans := AgentToolSelectionPlanSet{SchemaVersion: AgentToolSelectionEvalSchemaVersion}
	for _, item := range suite.Cases {
		plans.Plans = append(plans.Plans, AgentToolSelectionPlan{CaseID: item.ID, Calls: item.ExpectedPath})
	}
	report, err := ScoreAgentToolSelectionPlans(suite, plans)
	if err != nil {
		t.Fatalf("score canonical fixture path: %v", err)
	}
	if !report.Passed {
		data, _ := json.Marshal(report)
		t.Fatalf("canonical fixture path did not pass: %s", data)
	}
}

func TestAgentToolSelectionEvalRejectsLegacyAliasAndExtraCall(t *testing.T) {
	suite := loadAgentToolSelectionEvalSuite(t)
	plans := AgentToolSelectionPlanSet{SchemaVersion: AgentToolSelectionEvalSchemaVersion}
	for _, item := range suite.Cases {
		plans.Plans = append(plans.Plans, AgentToolSelectionPlan{CaseID: item.ID, Calls: append([]AgentToolSelectionEvalCall(nil), item.ExpectedPath...)})
	}
	plans.Plans[0].Calls[0].Tool = "architecture_overview"
	report, err := ScoreAgentToolSelectionPlans(suite, plans)
	if err != nil {
		t.Fatalf("score legacy alias candidate: %v", err)
	}
	if report.Passed || report.Cases[0].Passed {
		t.Fatalf("legacy alias candidate unexpectedly passed: %+v", report)
	}

	plans.Plans[0].Calls = append(plans.Plans[0].Calls, suite.Cases[0].ExpectedPath[0])
	report, err = ScoreAgentToolSelectionPlans(suite, plans)
	if err != nil {
		t.Fatalf("score extra-call candidate: %v", err)
	}
	if report.Passed || report.Cases[0].Passed {
		t.Fatalf("extra-call candidate unexpectedly passed: %+v", report)
	}
}

func loadAgentToolSelectionEvalSuiteV2(t *testing.T) AgentToolSelectionEvalSuiteV2 {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "agent_tool_selection", "v2.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read agent tool-selection v2 fixture: %v", err)
	}
	suite, err := ParseAgentToolSelectionEvalSuiteV2(data)
	if err != nil {
		t.Fatalf("parse agent tool-selection v2 fixture: %v", err)
	}
	return suite
}

func TestAgentToolSelectionEvalV2Manifest(t *testing.T) {
	suite := loadAgentToolSelectionEvalSuiteV2(t)
	if got := len(suite.Cases); got < 6 {
		t.Fatalf("agent tool-selection v2 cases = %d, want at least 6", got)
	}
	if err := ValidateAgentToolSelectionEvalSuiteV2(suite); err != nil {
		t.Fatalf("validate agent tool-selection v2 fixture: %v", err)
	}
}

func TestAgentToolSelectionEvalV2ScoresSemanticRecoveryPlans(t *testing.T) {
	suite := loadAgentToolSelectionEvalSuiteV2(t)
	plans := validAgentToolSelectionPlansV2(t, suite)
	report, err := ScoreAgentToolSelectionPlansV2(suite, plans)
	if err != nil {
		t.Fatalf("score v2 semantic recovery plans: %v", err)
	}
	if !report.Passed {
		data, _ := json.Marshal(report)
		t.Fatalf("valid v2 semantic recovery plans did not pass: %s", data)
	}
}

func TestAgentToolSelectionEvalV2RejectsUnchangedOversizeRetry(t *testing.T) {
	suite := loadAgentToolSelectionEvalSuiteV2(t)
	plans := validAgentToolSelectionPlansV2(t, suite)
	plan := findAgentToolSelectionPlanV2(t, &plans, "response_too_large_narrows")
	plan.Calls[1].Arguments = append(json.RawMessage(nil), plan.Calls[0].Arguments...)

	report, err := ScoreAgentToolSelectionPlansV2(suite, plans)
	if err != nil {
		t.Fatalf("score unchanged RESPONSE_TOO_LARGE retry: %v", err)
	}
	result := findAgentToolSelectionCaseResult(t, report, plan.CaseID)
	if result.Passed || !strings.Contains(result.Reason, "did not narrow") {
		t.Fatalf("unchanged RESPONSE_TOO_LARGE retry result = %+v, want narrowing failure", result)
	}
}

func TestAgentToolSelectionEvalV2RejectsRetryAfterGISUnavailable(t *testing.T) {
	suite := loadAgentToolSelectionEvalSuiteV2(t)
	plans := validAgentToolSelectionPlansV2(t, suite)
	plan := findAgentToolSelectionPlanV2(t, &plans, "gis_unavailable_stops")
	plan.Calls = append(plan.Calls, AgentToolSelectionEvalCallV2{
		Tool:      "map_physical_context",
		Arguments: json.RawMessage(`{"target_type":"province","target":"124","operation":"terrain"}`),
	})

	report, err := ScoreAgentToolSelectionPlansV2(suite, plans)
	if err != nil {
		t.Fatalf("score GIS_UNAVAILABLE retry: %v", err)
	}
	result := findAgentToolSelectionCaseResult(t, report, plan.CaseID)
	if result.Passed || !strings.Contains(result.Reason, "requires the plan to stop") {
		t.Fatalf("GIS_UNAVAILABLE retry result = %+v, want stop failure", result)
	}
}

func validAgentToolSelectionPlansV2(t *testing.T, suite AgentToolSelectionEvalSuiteV2) AgentToolSelectionPlanSetV2 {
	t.Helper()
	plans := AgentToolSelectionPlanSetV2{SchemaVersion: AgentToolSelectionEvalV2SchemaVersion}
	for _, item := range suite.Cases {
		plan := AgentToolSelectionPlanV2{
			CaseID:              item.ID,
			Evidence:            append([]string(nil), item.RequiredEvidence...),
			SatisfiedConditions: append([]string(nil), item.SuccessConditions...),
		}
		switch item.ID {
		case "cancelled_refresh_stops":
			plan.Calls = []AgentToolSelectionEvalCallV2{{
				Tool:       "ck3_refresh",
				Arguments:  json.RawMessage(`{"operation":"full"}`),
				ResultCode: ErrorOperationCancelled,
			}}
		case "gis_unavailable_stops":
			plan.Calls = []AgentToolSelectionEvalCallV2{{
				Tool:       "map_physical_context",
				Arguments:  json.RawMessage(`{"target_type":"province","target":"123","operation":"terrain"}`),
				ResultCode: ErrorGISUnavailable,
			}}
		case "index_stale_refreshes":
			plan.Calls = []AgentToolSelectionEvalCallV2{
				{Tool: "ck3_inspect", Arguments: json.RawMessage(`{"id":"event:k10_river_flood.100","operation":"definition"}`), ResultCode: ErrorIndexStale},
				{Tool: "ck3_refresh", Arguments: json.RawMessage(`{"operation":"full"}`)},
			}
		case "object_ambiguous_searches":
			plan.Calls = []AgentToolSelectionEvalCallV2{
				{Tool: "ck3_inspect", Arguments: json.RawMessage(`{"id":"k10_river_flood","operation":"definition"}`), ResultCode: ErrorObjectAmbiguous},
				{Tool: "ck3_search", Arguments: json.RawMessage(`{"query":"k10_river_flood","kind":"object"}`)},
			}
		case "public_visibility_avoids_private_mutation":
			plan.Calls = []AgentToolSelectionEvalCallV2{{
				Tool:      "ck3_search",
				Arguments: json.RawMessage(`{"query":"k10_river_flood","kind":"object","visibility":"public"}`),
			}}
		case "response_too_large_narrows":
			plan.Calls = []AgentToolSelectionEvalCallV2{
				{Tool: "ck3_search", Arguments: json.RawMessage(`{"query":"event","kind":"object","limit":20}`), ResultCode: ErrorResponseTooLarge},
				{Tool: "ck3_search", Arguments: json.RawMessage(`{"query":"event","kind":"object","limit":5,"page":2}`)},
			}
		default:
			t.Fatalf("v2 fixture case %q has no valid-plan test data", item.ID)
		}
		plans.Plans = append(plans.Plans, plan)
	}
	return plans
}

func findAgentToolSelectionPlanV2(t *testing.T, plans *AgentToolSelectionPlanSetV2, caseID string) *AgentToolSelectionPlanV2 {
	t.Helper()
	for index := range plans.Plans {
		if plans.Plans[index].CaseID == caseID {
			return &plans.Plans[index]
		}
	}
	t.Fatalf("v2 plan %q not found", caseID)
	return nil
}

func findAgentToolSelectionCaseResult(t *testing.T, report AgentToolSelectionEvalReport, caseID string) AgentToolSelectionEvalCaseResult {
	t.Helper()
	for _, result := range report.Cases {
		if result.ID == caseID {
			return result
		}
	}
	t.Fatalf("v2 result %q not found", caseID)
	return AgentToolSelectionEvalCaseResult{}
}
