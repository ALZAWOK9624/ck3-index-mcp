package script

import "testing"

func TestParseCK3Script(t *testing.T) {
	src := `my_event = {
	id = test.0001
	desc = test_desc
	trigger = { is_alive = yes }
	immediate = { add_gold = 5 }
	repeated = yes
	repeated = no
	target = title:c_test
	template = $ARG$
}`
	f := Parse(src)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", f.Errors)
	}
	if len(f.Nodes) != 1 {
		t.Fatalf("nodes=%d", len(f.Nodes))
	}
	root := f.Nodes[0]
	if root.Key != "my_event" || root.Kind != "block" {
		t.Fatalf("bad root: %+v", root)
	}
	if got := len(root.Children); got != 8 {
		t.Fatalf("children=%d", got)
	}
}

func TestLexCRLFCountsOneLine(t *testing.T) {
	tokens := Lex("first = yes\r\nsecond = yes\rthird = yes\nfourth = yes")
	want := map[string]int{
		"first":  1,
		"second": 2,
		"third":  3,
		"fourth": 4,
	}
	for _, tok := range tokens {
		line, ok := want[tok.Text]
		if !ok {
			continue
		}
		if tok.Line != line {
			t.Fatalf("token %q line=%d want=%d", tok.Text, tok.Line, line)
		}
		delete(want, tok.Text)
	}
	if len(want) != 0 {
		t.Fatalf("missing tokens: %v", want)
	}
}

func TestParseKeepsArithmeticExpressionAsOneValue(t *testing.T) {
	f := Parse(`ai_quality = {
	value = @[cultural_maa_extra_ai_score + 20]
	add = counter_synergy_ai_weight_pikemen
}`)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", f.Errors)
	}
	if len(f.Nodes) != 1 || len(f.Nodes[0].Children) != 2 {
		t.Fatalf("arithmetic expression split the surrounding block: %+v", f.Nodes)
	}
	value := f.Nodes[0].Children[0]
	if value.Key != "value" || value.Value != "@[cultural_maa_extra_ai_score + 20]" || value.Kind != "atom" {
		t.Fatalf("arithmetic expression was not preserved: %+v", value)
	}
}

func TestLexArithmeticExpressionSupportsNestedBracketsAndLines(t *testing.T) {
	tokens := Lex("value = @[outer[1 + 2]\r\n + 3]\r\nnext = yes")
	if len(tokens) < 7 || tokens[2].Kind != TokenIdent || tokens[2].Text != "@[outer[1 + 2]\r\n + 3]" {
		t.Fatalf("nested arithmetic expression token missing: %+v", tokens)
	}
	for _, token := range tokens {
		if token.Text == "next" && token.Line != 3 {
			t.Fatalf("line tracking drifted after arithmetic expression: %+v", token)
		}
	}
}

func TestLexReportsUnterminatedArithmeticExpression(t *testing.T) {
	tokens := Lex("value = @[1 + 2")
	if len(tokens) < 3 || tokens[2].Kind != TokenError || tokens[2].Text != "unterminated arithmetic expression" {
		t.Fatalf("unterminated expression was not diagnosed: %+v", tokens)
	}
}

func TestParseGUIJominiPrefixes(t *testing.T) {
	src := `types HUD {
	type icon_hud_background_container = container {
		size = { 100% 64 }
		position = { -12 8 }
		block "parent" {
			parentanchor = right
		}
		blockoverride "content" {
			icon = { texture = "gfx/interface/test.dds" }
		}
	}
}`
	f := ParseGUI(src)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", f.Errors)
	}
	if len(f.Nodes) != 1 {
		t.Fatalf("root nodes=%d want=1", len(f.Nodes))
	}
	ns := f.Nodes[0]
	if ns.Key != "types" || ns.Value != "HUD" || ns.Kind != "block" {
		t.Fatalf("bad namespace node: %+v", ns)
	}
	if len(ns.Children) != 1 {
		t.Fatalf("namespace children=%d want=1", len(ns.Children))
	}
	typ := ns.Children[0]
	if typ.Key != "icon_hud_background_container" || typ.Operator != "type" || typ.Value != "container" || typ.Kind != "block" {
		t.Fatalf("bad type node: %+v", typ)
	}
	if typ.EndLine != 11 {
		t.Fatalf("type end line=%d want=11", typ.EndLine)
	}
	if len(typ.Children) != 4 {
		t.Fatalf("type children=%d want=4", len(typ.Children))
	}
	if typ.Children[2].Key != "block" || typ.Children[2].Operator != "slot" || typ.Children[2].Value != "parent" {
		t.Fatalf("bad block slot: %+v", typ.Children[2])
	}
	if typ.Children[3].Key != "blockoverride" || typ.Children[3].Value != "content" {
		t.Fatalf("bad blockoverride slot: %+v", typ.Children[3])
	}
	size := typ.Children[0]
	if len(size.Children) != 2 || size.Children[0].Key != "100%" || size.Children[1].Key != "64" {
		t.Fatalf("bare vector values were not preserved: %+v", size.Children)
	}
}

func TestParseGUIInheritedInstanceBody(t *testing.T) {
	f := ParseGUI(`types Demo {
	type base = widget {}
	type child = base {}
	child = base { position = { 1 2 } }
}`)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", f.Errors)
	}
	ns := f.Nodes[0]
	instance := ns.Children[2]
	if instance.Key != "child" || instance.Value != "base" || instance.Kind != "block" || instance.Operator != "=" {
		t.Fatalf("bad inherited instance: %+v", instance)
	}
}

func TestParseGUITemplates(t *testing.T) {
	f := ParseGUI(`template SharedAnimation {
	animation = { duration = 0.2 }
}
local_template LocalWidget { size = { 10 20 } }`)
	if len(f.Errors) != 0 || len(f.Nodes) != 2 {
		t.Fatalf("unexpected parse result: %+v", f)
	}
	if f.Nodes[0].Key != "SharedAnimation" || f.Nodes[0].Operator != "template" || f.Nodes[0].Kind != "block" {
		t.Fatalf("bad template node: %+v", f.Nodes[0])
	}
	if f.Nodes[1].Key != "LocalWidget" || f.Nodes[1].Operator != "local_template" {
		t.Fatalf("bad local template node: %+v", f.Nodes[1])
	}
}
