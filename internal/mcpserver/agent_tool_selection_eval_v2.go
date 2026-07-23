package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const AgentToolSelectionEvalV2SchemaVersion = 2

// AgentToolSelectionEvalSuiteV2 scores semantic constraints instead of one
// exact golden call sequence. V1 remains the canonical-plan contract.
type AgentToolSelectionEvalSuiteV2 struct {
	SchemaVersion int                            `json:"schema_version"`
	SuiteID       string                         `json:"suite_id"`
	Cases         []AgentToolSelectionEvalCaseV2 `json:"cases"`
}

type AgentToolSelectionEvalCaseV2 struct {
	ID                   string                           `json:"id"`
	Task                 string                           `json:"task"`
	WhyThisPath          string                           `json:"why_this_path"`
	AcceptableFirstTools []string                         `json:"acceptable_first_tools"`
	RequiredEvidence     []string                         `json:"required_evidence"`
	ForbiddenTools       []string                         `json:"forbidden_tools"`
	MaxCalls             int                              `json:"max_calls"`
	AllowedAlternatives  [][]string                       `json:"allowed_alternatives"`
	SuccessConditions    []string                         `json:"success_conditions"`
	Recovery             []AgentToolSelectionRecoveryRule `json:"recovery,omitempty"`
}

type AgentToolSelectionRecoveryRule struct {
	AfterCode               string   `json:"after_code"`
	AllowedNextTools        []string `json:"allowed_next_tools,omitempty"`
	ForbiddenNextTools      []string `json:"forbidden_next_tools,omitempty"`
	Stop                    bool     `json:"stop,omitempty"`
	RequireChangedArguments bool     `json:"require_changed_arguments,omitempty"`
}

type AgentToolSelectionEvalCallV2 struct {
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	Reason     string          `json:"reason,omitempty"`
	ResultCode string          `json:"result_code,omitempty"`
}

type AgentToolSelectionPlanV2 struct {
	CaseID              string                         `json:"case_id"`
	Calls               []AgentToolSelectionEvalCallV2 `json:"calls"`
	Evidence            []string                       `json:"evidence"`
	SatisfiedConditions []string                       `json:"satisfied_conditions"`
}

type AgentToolSelectionPlanSetV2 struct {
	SchemaVersion int                        `json:"schema_version"`
	Plans         []AgentToolSelectionPlanV2 `json:"plans"`
}

func ParseAgentToolSelectionEvalSuiteV2(data []byte) (AgentToolSelectionEvalSuiteV2, error) {
	var suite AgentToolSelectionEvalSuiteV2
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return AgentToolSelectionEvalSuiteV2{}, fmt.Errorf("decode agent tool-selection v2 suite: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return AgentToolSelectionEvalSuiteV2{}, fmt.Errorf("decode agent tool-selection v2 suite: expected one JSON object")
	}
	if err := ValidateAgentToolSelectionEvalSuiteV2(suite); err != nil {
		return AgentToolSelectionEvalSuiteV2{}, err
	}
	return suite, nil
}

func ParseAgentToolSelectionPlanSetV2(data []byte) (AgentToolSelectionPlanSetV2, error) {
	var plans AgentToolSelectionPlanSetV2
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plans); err != nil {
		return AgentToolSelectionPlanSetV2{}, fmt.Errorf("decode agent tool-selection v2 plans: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return AgentToolSelectionPlanSetV2{}, fmt.Errorf("decode agent tool-selection v2 plans: expected one JSON object")
	}
	if plans.SchemaVersion != AgentToolSelectionEvalV2SchemaVersion {
		return AgentToolSelectionPlanSetV2{}, fmt.Errorf("agent tool-selection v2 plans schema_version = %d, want %d", plans.SchemaVersion, AgentToolSelectionEvalV2SchemaVersion)
	}
	return plans, nil
}

func ValidateAgentToolSelectionEvalSuiteV2(suite AgentToolSelectionEvalSuiteV2) error {
	if suite.SchemaVersion != AgentToolSelectionEvalV2SchemaVersion {
		return fmt.Errorf("agent tool-selection v2 suite schema_version = %d, want %d", suite.SchemaVersion, AgentToolSelectionEvalV2SchemaVersion)
	}
	if strings.TrimSpace(suite.SuiteID) == "" {
		return fmt.Errorf("agent tool-selection v2 suite has no suite_id")
	}
	if len(suite.Cases) < 6 {
		return fmt.Errorf("agent tool-selection v2 suite has %d cases, want at least 6", len(suite.Cases))
	}
	seen := map[string]bool{}
	previousID := ""
	for _, item := range suite.Cases {
		if item.ID == "" || item.Task == "" || item.WhyThisPath == "" {
			return fmt.Errorf("agent tool-selection v2 case requires id, task, and why_this_path")
		}
		if seen[item.ID] {
			return fmt.Errorf("agent tool-selection v2 suite has duplicate case id %q", item.ID)
		}
		if previousID != "" && item.ID < previousID {
			return fmt.Errorf("agent tool-selection v2 cases must be sorted by id")
		}
		seen[item.ID] = true
		previousID = item.ID
		if item.MaxCalls <= 0 || len(item.AcceptableFirstTools) == 0 {
			return fmt.Errorf("agent tool-selection v2 case %q requires max_calls and acceptable_first_tools", item.ID)
		}
		for _, name := range append(append([]string{}, item.AcceptableFirstTools...), item.ForbiddenTools...) {
			if _, ok := findCanonicalTool(name); !ok {
				return fmt.Errorf("agent tool-selection v2 case %q references non-canonical tool %q", item.ID, name)
			}
		}
		for _, alternative := range item.AllowedAlternatives {
			if len(alternative) == 0 || len(alternative) > item.MaxCalls {
				return fmt.Errorf("agent tool-selection v2 case %q has invalid allowed alternative", item.ID)
			}
			for _, name := range alternative {
				if _, ok := findCanonicalTool(name); !ok {
					return fmt.Errorf("agent tool-selection v2 case %q alternative references non-canonical tool %q", item.ID, name)
				}
			}
		}
		for _, recovery := range item.Recovery {
			if recovery.AfterCode == "" {
				return fmt.Errorf("agent tool-selection v2 case %q has recovery without after_code", item.ID)
			}
			if recovery.Stop && len(recovery.AllowedNextTools) > 0 {
				return fmt.Errorf("agent tool-selection v2 case %q stop recovery cannot allow a next tool", item.ID)
			}
			for _, name := range append(append([]string{}, recovery.AllowedNextTools...), recovery.ForbiddenNextTools...) {
				if _, ok := findCanonicalTool(name); !ok {
					return fmt.Errorf("agent tool-selection v2 case %q recovery references non-canonical tool %q", item.ID, name)
				}
			}
		}
	}
	return nil
}

func ScoreAgentToolSelectionPlansV2(suite AgentToolSelectionEvalSuiteV2, plans AgentToolSelectionPlanSetV2) (AgentToolSelectionEvalReport, error) {
	if err := ValidateAgentToolSelectionEvalSuiteV2(suite); err != nil {
		return AgentToolSelectionEvalReport{}, err
	}
	if plans.SchemaVersion != AgentToolSelectionEvalV2SchemaVersion {
		return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection v2 plans schema_version = %d, want %d", plans.SchemaVersion, AgentToolSelectionEvalV2SchemaVersion)
	}
	byID := map[string]AgentToolSelectionPlanV2{}
	for _, plan := range plans.Plans {
		if plan.CaseID == "" {
			return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection v2 plan has no case_id")
		}
		if _, exists := byID[plan.CaseID]; exists {
			return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection v2 plans duplicate case_id %q", plan.CaseID)
		}
		byID[plan.CaseID] = plan
	}
	report := AgentToolSelectionEvalReport{SuiteID: suite.SuiteID, Passed: true}
	known := map[string]bool{}
	for _, item := range suite.Cases {
		known[item.ID] = true
		plan, exists := byID[item.ID]
		result := AgentToolSelectionEvalCaseResult{ID: item.ID}
		if !exists {
			result.Reason = "missing candidate plan"
		} else {
			result.Reason = scoreAgentToolSelectionPlanV2(item, plan)
			result.Passed = result.Reason == ""
		}
		if !result.Passed {
			report.Passed = false
		}
		report.Cases = append(report.Cases, result)
	}
	for caseID := range byID {
		if !known[caseID] {
			return AgentToolSelectionEvalReport{}, fmt.Errorf("agent tool-selection v2 plan references unknown case_id %q", caseID)
		}
	}
	sort.Slice(report.Cases, func(i, j int) bool { return report.Cases[i].ID < report.Cases[j].ID })
	return report, nil
}

func scoreAgentToolSelectionPlanV2(item AgentToolSelectionEvalCaseV2, plan AgentToolSelectionPlanV2) string {
	if len(plan.Calls) == 0 {
		return "candidate plan has no calls"
	}
	if len(plan.Calls) > item.MaxCalls {
		return fmt.Sprintf("call count = %d, exceeds max_calls %d", len(plan.Calls), item.MaxCalls)
	}
	if !containsAgentEvalString(item.AcceptableFirstTools, plan.Calls[0].Tool) {
		return fmt.Sprintf("first tool %q is not acceptable", plan.Calls[0].Tool)
	}
	for index, call := range plan.Calls {
		if containsAgentEvalString(item.ForbiddenTools, call.Tool) {
			return fmt.Sprintf("call %d uses forbidden tool %q", index+1, call.Tool)
		}
		if err := validateAgentToolSelectionCall(AgentToolSelectionEvalCall{Tool: call.Tool, Arguments: call.Arguments, Reason: call.Reason}); err != nil {
			return fmt.Sprintf("call %d is invalid: %v", index+1, err)
		}
	}
	if len(item.AllowedAlternatives) > 0 {
		matched := false
		for _, alternative := range item.AllowedAlternatives {
			if len(alternative) != len(plan.Calls) {
				continue
			}
			equal := true
			for index := range alternative {
				equal = equal && alternative[index] == plan.Calls[index].Tool
			}
			matched = matched || equal
		}
		if !matched {
			return "tool sequence is not one of the allowed alternatives"
		}
	}
	for _, required := range item.RequiredEvidence {
		if !containsAgentEvalString(plan.Evidence, required) {
			return fmt.Sprintf("required evidence %q was not collected", required)
		}
	}
	for _, condition := range item.SuccessConditions {
		if !containsAgentEvalString(plan.SatisfiedConditions, condition) {
			return fmt.Sprintf("success condition %q was not satisfied", condition)
		}
	}
	for _, recovery := range item.Recovery {
		found := false
		for index, call := range plan.Calls {
			if call.ResultCode != recovery.AfterCode {
				continue
			}
			found = true
			if recovery.Stop {
				if index+1 != len(plan.Calls) {
					return fmt.Sprintf("%s requires the plan to stop", recovery.AfterCode)
				}
				break
			}
			if index+1 >= len(plan.Calls) {
				return fmt.Sprintf("%s requires a recovery call", recovery.AfterCode)
			}
			next := plan.Calls[index+1]
			if len(recovery.AllowedNextTools) > 0 && !containsAgentEvalString(recovery.AllowedNextTools, next.Tool) {
				return fmt.Sprintf("%s recovery tool %q is not allowed", recovery.AfterCode, next.Tool)
			}
			if containsAgentEvalString(recovery.ForbiddenNextTools, next.Tool) {
				return fmt.Sprintf("%s recovery uses forbidden tool %q", recovery.AfterCode, next.Tool)
			}
			if recovery.RequireChangedArguments {
				left, _ := canonicalAgentToolSelectionJSON(call.Arguments)
				right, _ := canonicalAgentToolSelectionJSON(next.Arguments)
				if bytes.Equal(left, right) {
					return fmt.Sprintf("%s recovery did not narrow or page arguments", recovery.AfterCode)
				}
			}
			break
		}
		if !found {
			return fmt.Sprintf("plan did not expose expected recovery code %s", recovery.AfterCode)
		}
	}
	return ""
}

func containsAgentEvalString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
