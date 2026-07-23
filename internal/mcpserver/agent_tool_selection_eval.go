package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// AgentToolSelectionEvalSchemaVersion is the local, deterministic manifest
// format for tool-selection regression cases. It deliberately evaluates plans
// only; it neither calls a model nor executes an MCP handler.
const AgentToolSelectionEvalSchemaVersion = 1

// AgentToolSelectionEvalSuite describes real CK3 authoring and investigation
// tasks together with the shortest canonical MCP call path expected for each.
// Keep cases data-only so external agents can be scored without a live CK3
// workspace, model provider, or network access.
type AgentToolSelectionEvalSuite struct {
	SchemaVersion int                          `json:"schema_version"`
	SuiteID       string                       `json:"suite_id"`
	Cases         []AgentToolSelectionEvalCase `json:"cases"`
}

type AgentToolSelectionEvalCase struct {
	ID           string                       `json:"id"`
	Task         string                       `json:"task"`
	WhyThisPath  string                       `json:"why_this_path"`
	ExpectedPath []AgentToolSelectionEvalCall `json:"expected_path"`
}

// AgentToolSelectionEvalCall is an MCP call asserted by an evaluation case.
// Arguments remain raw JSON so the same fixture format can be emitted by an
// external planning harness without an extra translation layer.
type AgentToolSelectionEvalCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Reason    string          `json:"reason,omitempty"`
}

// AgentToolSelectionPlan is a candidate plan for one case. Plans are matched
// exactly to protect the fixture's stated minimal call path from accidental
// extra discovery or legacy-tool regressions.
type AgentToolSelectionPlan struct {
	CaseID string                       `json:"case_id"`
	Calls  []AgentToolSelectionEvalCall `json:"calls"`
}

type AgentToolSelectionPlanSet struct {
	SchemaVersion int                      `json:"schema_version"`
	Plans         []AgentToolSelectionPlan `json:"plans"`
}

type AgentToolSelectionEvalCaseResult struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

type AgentToolSelectionEvalReport struct {
	SuiteID string                             `json:"suite_id"`
	Passed  bool                               `json:"passed"`
	Cases   []AgentToolSelectionEvalCaseResult `json:"cases"`
}

func ParseAgentToolSelectionEvalSuite(data []byte) (AgentToolSelectionEvalSuite, error) {
	var suite AgentToolSelectionEvalSuite
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return AgentToolSelectionEvalSuite{}, fmt.Errorf("decode agent tool-selection suite: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return AgentToolSelectionEvalSuite{}, fmt.Errorf("decode agent tool-selection suite: expected one JSON object")
	}
	if err := ValidateAgentToolSelectionEvalSuite(suite); err != nil {
		return AgentToolSelectionEvalSuite{}, err
	}
	return suite, nil
}

func ParseAgentToolSelectionPlanSet(data []byte) (AgentToolSelectionPlanSet, error) {
	var plans AgentToolSelectionPlanSet
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plans); err != nil {
		return AgentToolSelectionPlanSet{}, fmt.Errorf("decode agent tool-selection plans: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return AgentToolSelectionPlanSet{}, fmt.Errorf("decode agent tool-selection plans: expected one JSON object")
	}
	if plans.SchemaVersion != AgentToolSelectionEvalSchemaVersion {
		return AgentToolSelectionPlanSet{}, fmt.Errorf("agent tool-selection plans schema_version = %d, want %d", plans.SchemaVersion, AgentToolSelectionEvalSchemaVersion)
	}
	return plans, nil
}

// ValidateAgentToolSelectionEvalSuite verifies fixture hygiene against the
// current canonical tool registry. It is the no-model/no-network regression
// gate run by go test and by cmd/tool-eval before scoring external plans.
func ValidateAgentToolSelectionEvalSuite(suite AgentToolSelectionEvalSuite) error {
	if suite.SchemaVersion != AgentToolSelectionEvalSchemaVersion {
		return fmt.Errorf("agent tool-selection suite schema_version = %d, want %d", suite.SchemaVersion, AgentToolSelectionEvalSchemaVersion)
	}
	if strings.TrimSpace(suite.SuiteID) == "" {
		return fmt.Errorf("agent tool-selection suite has no suite_id")
	}
	if len(suite.Cases) < 12 {
		return fmt.Errorf("agent tool-selection suite has %d cases, want at least 12", len(suite.Cases))
	}
	seen := make(map[string]bool, len(suite.Cases))
	previousID := ""
	for _, item := range suite.Cases {
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("agent tool-selection suite contains a case without id")
		}
		if seen[item.ID] {
			return fmt.Errorf("agent tool-selection suite has duplicate case id %q", item.ID)
		}
		if previousID != "" && item.ID < previousID {
			return fmt.Errorf("agent tool-selection cases must be sorted by id: %q appears after %q", item.ID, previousID)
		}
		previousID = item.ID
		seen[item.ID] = true
		if strings.TrimSpace(item.Task) == "" || strings.TrimSpace(item.WhyThisPath) == "" {
			return fmt.Errorf("agent tool-selection case %q requires task and why_this_path", item.ID)
		}
		if len(item.ExpectedPath) == 0 {
			return fmt.Errorf("agent tool-selection case %q has no expected_path", item.ID)
		}
		for index, call := range item.ExpectedPath {
			if err := validateAgentToolSelectionCall(call); err != nil {
				return fmt.Errorf("agent tool-selection case %q call %d: %w", item.ID, index+1, err)
			}
			if index > 0 && sameAgentToolSelectionCall(item.ExpectedPath[index-1], call) {
				return fmt.Errorf("agent tool-selection case %q repeats identical consecutive call %q", item.ID, call.Tool)
			}
		}
	}
	return nil
}

// ScoreAgentToolSelectionPlans checks deterministic candidate plans against
// the fixture's exact canonical path. A false report is an evaluation miss;
// malformed data is returned as an error so CI can distinguish broken inputs
// from a planner that simply chose the wrong tool.
func ScoreAgentToolSelectionPlans(suite AgentToolSelectionEvalSuite, plans AgentToolSelectionPlanSet) (AgentToolSelectionEvalReport, error) {
	if err := ValidateAgentToolSelectionEvalSuite(suite); err != nil {
		return AgentToolSelectionEvalReport{}, err
	}
	if plans.SchemaVersion != AgentToolSelectionEvalSchemaVersion {
		return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection plans schema_version = %d, want %d", plans.SchemaVersion, AgentToolSelectionEvalSchemaVersion)
	}
	byID := make(map[string]AgentToolSelectionPlan, len(plans.Plans))
	for _, plan := range plans.Plans {
		if strings.TrimSpace(plan.CaseID) == "" {
			return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection plan has no case_id")
		}
		if _, exists := byID[plan.CaseID]; exists {
			return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection plans duplicate case_id %q", plan.CaseID)
		}
		byID[plan.CaseID] = plan
	}

	report := AgentToolSelectionEvalReport{SuiteID: suite.SuiteID, Passed: true, Cases: make([]AgentToolSelectionEvalCaseResult, 0, len(suite.Cases))}
	for _, item := range suite.Cases {
		plan, exists := byID[item.ID]
		result := AgentToolSelectionEvalCaseResult{ID: item.ID, Passed: false}
		switch {
		case !exists:
			result.Reason = "missing candidate plan"
		case len(plan.Calls) != len(item.ExpectedPath):
			result.Reason = fmt.Sprintf("call count = %d, want minimal path of %d", len(plan.Calls), len(item.ExpectedPath))
		default:
			result.Passed = true
			for index, call := range plan.Calls {
				if err := validateAgentToolSelectionCall(call); err != nil {
					result.Passed = false
					result.Reason = fmt.Sprintf("call %d is invalid: %v", index+1, err)
					break
				}
				if !sameAgentToolSelectionCall(item.ExpectedPath[index], call) {
					result.Passed = false
					result.Reason = fmt.Sprintf("call %d does not match expected canonical tool and arguments", index+1)
					break
				}
			}
		}
		if !result.Passed {
			report.Passed = false
		}
		report.Cases = append(report.Cases, result)
	}
	// Reject plans that claim a nonexistent case instead of silently ignoring a
	// typo in an external planner's output.
	known := make(map[string]bool, len(suite.Cases))
	for _, item := range suite.Cases {
		known[item.ID] = true
	}
	for caseID := range byID {
		if !known[caseID] {
			return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection plan references unknown case_id %q", caseID)
		}
	}
	sort.Slice(report.Cases, func(i, j int) bool { return report.Cases[i].ID < report.Cases[j].ID })
	return report, nil
}

func validateAgentToolSelectionCall(call AgentToolSelectionEvalCall) error {
	name := strings.TrimSpace(call.Tool)
	if name == "" {
		return fmt.Errorf("tool is required")
	}
	definition, found := findCanonicalTool(name)
	if !found {
		return fmt.Errorf("tool %q is not a registered canonical tool", name)
	}
	if err := validateArguments(call.Arguments, definition.InputSchema, definition.CompatibilityProperties); err != nil {
		return fmt.Errorf("arguments for %q: %w", name, err)
	}
	return nil
}

func sameAgentToolSelectionCall(left, right AgentToolSelectionEvalCall) bool {
	if left.Tool != right.Tool {
		return false
	}
	leftJSON, leftErr := canonicalAgentToolSelectionJSON(left.Arguments)
	rightJSON, rightErr := canonicalAgentToolSelectionJSON(right.Arguments)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func canonicalAgentToolSelectionJSON(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return json.Marshal(value)
}
