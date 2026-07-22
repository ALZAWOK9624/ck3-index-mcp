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
	suite, err := mcpserver.ParseAgentToolSelectionEvalSuite(casesData)
	if err != nil {
		fail(err)
	}
	if *plansPath == "" {
		fmt.Printf("validated %s (%d canonical CK3 tool-selection cases)\n", suite.SuiteID, len(suite.Cases))
		return
	}
	plansData, err := os.ReadFile(*plansPath)
	if err != nil {
		fail(err)
	}
	plans, err := mcpserver.ParseAgentToolSelectionPlanSet(plansData)
	if err != nil {
		fail(err)
	}
	report, err := mcpserver.ScoreAgentToolSelectionPlans(suite, plans)
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tool-eval:", err)
	os.Exit(2)
}
