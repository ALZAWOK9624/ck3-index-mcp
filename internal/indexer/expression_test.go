package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestArithmeticExpressionIsNotIndexedAsDefineReference(t *testing.T) {
	parsed := script.Parse(`sample = {
	value = @[cultural_maa_extra_ai_score + 20]
}`)
	if len(parsed.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", parsed.Errors)
	}
	value := parsed.Nodes[0].Children[0]
	if got := fieldValueShape(value); got != "expression" {
		t.Fatalf("fieldValueShape(arithmetic expression)=%q want expression", got)
	}
	refs := extractRefs(fileRecord{ID: 1, RelPath: "common/script_values/test.txt"}, parsed.Nodes, nil)
	for _, ref := range refs {
		if ref.Kind == "define" {
			t.Fatalf("arithmetic expression became a false define reference: %+v", ref)
		}
	}
	for _, diagnostic := range checkDefineRefs(parsed.Nodes, "common/script_values/test.txt") {
		if diagnostic.code == "unknown_define" {
			t.Fatalf("arithmetic expression became an unknown define diagnostic: %+v", diagnostic)
		}
	}
}

func TestPlainDefineReferenceKeepsExistingSemantics(t *testing.T) {
	parsed := script.Parse(`sample = { value = @NCharacter|MAX_AGE }`)
	value := parsed.Nodes[0].Children[0]
	if got := fieldValueShape(value); got != "define_ref" {
		t.Fatalf("fieldValueShape(plain define)=%q want define_ref", got)
	}
	refs := extractRefs(fileRecord{ID: 1, RelPath: "common/script_values/test.txt"}, parsed.Nodes, nil)
	if len(refs) != 1 || refs[0].Kind != "define" || refs[0].Name != "@NCharacter|MAX_AGE" {
		t.Fatalf("plain define reference semantics changed: %+v", refs)
	}
}
