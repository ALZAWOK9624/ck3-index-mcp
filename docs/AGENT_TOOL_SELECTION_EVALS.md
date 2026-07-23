# Agent Tool-Selection Evals

The agent tool-selection fixtures are deterministic, offline contract suites
for agents that plan CK3-index MCP calls. They do not run a language model,
contact a network service, or require a CK3 installation/database.

`testdata/agent_tool_selection/v1.json` protects exact canonical minimal paths.
`testdata/agent_tool_selection/v2.json` scores semantic constraints: acceptable
first tools, required evidence, forbidden tools, call budgets, allowed
alternatives, success conditions, and code-specific recovery behavior. The v2
recovery cases cover cancellation, unavailable GIS, stale indexes, ambiguous
objects, public visibility, and oversized responses.

Both fixtures are sorted by case id. Every call must use a currently registered
canonical tool and pass its live input schema, so tool renames, schema changes,
invalid arguments, and legacy-alias regressions fail CI immediately.

Run the fixture gate:

```text
go test ./internal/mcpserver -run TestAgentToolSelectionEval
go run ./cmd/tool-eval
go run ./cmd/tool-eval -cases testdata/agent_tool_selection/v2.json
```

To score a planner without adding a model dependency, write a local plan file:

```json
{
  "schema_version": 1,
  "plans": [
    {
      "case_id": "inspect_exact_definition",
      "calls": [
        {
          "tool": "ck3_inspect",
          "arguments": {"id": "tradition:k10_river_keepers", "operation": "definition"}
        }
      ]
    }
  ]
}
```

Then run `go run ./cmd/tool-eval -plans planner-output.json`. For v1, a plan
passes only when its complete call list matches the canonical minimal path. For
v2, a planner output also uses `schema_version: 2` and supplies `evidence`,
`satisfied_conditions`, and optional per-call `result_code` values. It passes
when all semantic and recovery constraints hold; an identical retry after
`RESPONSE_TOO_LARGE`, any retry after `OPERATION_CANCELLED`, or a private
mutation in a public-only case is a selection miss.

When adding a case, keep the task grounded in a CK3 workflow, omit default
arguments unless they disambiguate the operation, use only canonical tools,
and place the case in lexical id order. Keep v1 exact and immutable. Add
behavioral alternatives or new recovery semantics to v2 rather than weakening
the v1 oracle silently.
