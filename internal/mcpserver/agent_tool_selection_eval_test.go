package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
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
