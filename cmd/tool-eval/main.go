package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"ck3-index/internal/mcpserver"
)

func main() {
	casesPath := flag.String("cases", "testdata/agent_tool_selection/v1.json", "tool-selection suite manifest")
	plansPath := flag.String("plans", "", "optional candidate plans JSON to score")
	flag.Parse()

	casesData, err := os.ReadFile(*casesPath)
	if err != nil {
		fail(err)
	}
	version, err := toolEvalSchemaVersion(casesData)
	if err != nil {
		fail(err)
	}
	var report mcpserver.AgentToolSelectionEvalReport
	switch version {
	case mcpserver.AgentToolSelectionEvalSchemaVersion:
		suite, parseErr := mcpserver.ParseAgentToolSelectionEvalSuite(casesData)
		if parseErr != nil {
			fail(parseErr)
		}
		if *plansPath == "" {
			fmt.Printf("validated %s (%d canonical CK3 tool-selection cases)\n", suite.SuiteID, len(suite.Cases))
			return
		}
		plansData, readErr := os.ReadFile(*plansPath)
		if readErr != nil {
			fail(readErr)
		}
		plans, parseErr := mcpserver.ParseAgentToolSelectionPlanSet(plansData)
		if parseErr != nil {
			fail(parseErr)
		}
		report, err = mcpserver.ScoreAgentToolSelectionPlans(suite, plans)
	case mcpserver.AgentToolSelectionEvalV2SchemaVersion:
		suite, parseErr := mcpserver.ParseAgentToolSelectionEvalSuiteV2(casesData)
		if parseErr != nil {
			fail(parseErr)
		}
		if *plansPath == "" {
			fmt.Printf("validated %s (%d semantic CK3 tool-selection cases)\n", suite.SuiteID, len(suite.Cases))
			return
		}
		plansData, readErr := os.ReadFile(*plansPath)
		if readErr != nil {
			fail(readErr)
		}
		plans, parseErr := mcpserver.ParseAgentToolSelectionPlanSetV2(plansData)
		if parseErr != nil {
			fail(parseErr)
		}
		report, err = mcpserver.ScoreAgentToolSelectionPlansV2(suite, plans)
	default:
		fail(fmt.Errorf("unsupported schema_version %d", version))
	}
	if err != nil {
		fail(err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(data))
	if !report.Passed {
		os.Exit(1)
	}
}

func toolEvalSchemaVersion(data []byte) (int, error) {
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, fmt.Errorf("decode schema_version: %w", err)
	}
	if envelope.SchemaVersion == 0 {
		return 0, fmt.Errorf("schema_version is required")
	}
	return envelope.SchemaVersion, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tool-eval:", err)
	os.Exit(2)
}
