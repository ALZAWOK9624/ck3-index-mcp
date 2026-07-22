# Agent Tool-Selection Evals

`testdata/agent_tool_selection/v1.json` is a deterministic, offline contract
suite for an agent that plans CK3-index MCP calls. It measures tool choice and
minimal call sequencing; it does not run a language model, contact a network
service, or require a CK3 installation/database.

Each case contains a realistic CK3 task, an explanation of why the selected
path is appropriate, and the exact canonical MCP calls expected. The fixture is
sorted by case id. Calls must use a currently registered standard canonical
tool and must pass its live input schema, so tool renames, schema changes, and
legacy-alias regressions fail CI immediately.

Run the fixture gate:

```text
go test ./internal/mcpserver -run TestAgentToolSelectionEval
go run ./cmd/tool-eval
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

Then run `go run ./cmd/tool-eval -plans planner-output.json`. A plan passes a
case only when its complete call list matches the fixture's canonical minimal
path. This is deliberately strict: adding an unnecessary search before an
exact-id inspection, or using an expert-profile alias, is a selection miss.

When adding a case, keep the task grounded in a CK3 workflow, omit default
arguments unless they disambiguate the operation, use only canonical tools,
and place the case in lexical id order. If multiple paths are genuinely equally
minimal, prefer the path recommended by the canonical tool descriptions; add a
new fixture version rather than weakening an existing oracle silently.
