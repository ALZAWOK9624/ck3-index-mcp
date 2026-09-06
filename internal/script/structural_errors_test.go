package script

import (
	"strings"
	"testing"
)

func TestStructuralErrorsAndRecovery(t *testing.T) {
	for _, source := range []string{"}", "a={", "{", "a={ b={}", "a = 1 = 2", "a={ b= }", "a ! yes"} {
		t.Run(source, func(t *testing.T) {
			if f := Parse(source); len(f.Errors) == 0 {
				t.Fatalf("malformed source accepted: %q", source)
			}
		})
	}
	f := Parse("a=yes } b=no")
	if len(f.Errors) != 1 || len(f.Nodes) != 2 || f.Nodes[1].Key != "b" {
		t.Fatalf("lost source after extra brace: %+v", f)
	}
	f = Parse("a={ b= } c=yes")
	if len(f.Errors) != 1 || len(f.Nodes) != 2 || f.Nodes[1].Key != "c" {
		t.Fatalf("missing value swallowed enclosing brace: %+v", f)
	}
	for _, source := range []string{`types X { type a = widget {`, `template Shared {`, `block "slot" {`} {
		if f := ParseGUI(source); len(f.Errors) == 0 {
			t.Fatalf("malformed GUI accepted: %q", source)
		}
	}
}

func TestParserDepthBound(t *testing.T) {
	f := Parse(strings.Repeat("a={", 10000) + strings.Repeat("}", 10000))
	if len(f.Errors) == 0 || len(f.Errors) > MaxDepth+1 {
		t.Fatalf("unbounded depth/error handling: %d", len(f.Errors))
	}
}

func TestTaggedColorAndSourceSpans(t *testing.T) {
	f := Parse("a = { color = hsv{ 0.98 0.9 0.9 } next = yes }")
	if len(f.Errors) > 0 {
		t.Fatal(f.Errors)
	}
	children := f.Nodes[0].Children
	if len(children) != 2 || children[0].Value != "hsv" || children[0].Kind != "block" || len(children[0].Children) != 3 || children[1].Key != "next" {
		t.Fatalf("tagged color detached from property: %+v", children)
	}
	f = Parse("key = \"a\\\"b\"\nexpression = @[1 +\n 2]\nlast = yes")
	if len(f.Errors) > 0 || f.Nodes[0].EndCol != 13 || f.Nodes[1].EndLine != 3 || f.Nodes[1].EndCol != 4 || f.Nodes[2].Line != 4 {
		t.Fatalf("spans refer to decoded values instead of source: %+v", f)
	}
}

func FuzzParseStructural(f *testing.F) {
	for _, seed := range []string{"a={}", "a={b=}", "}tail=yes", `color=hsv{0 1 1}`, "x=@[1+2]"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 65536 {
			t.Skip()
		}
		parsed := Parse(source)
		for _, n := range parsed.Nodes {
			if n.Depth != 0 || n.Line < 1 || n.Col < 1 {
				t.Fatalf("invalid root metadata: %+v", n)
			}
		}
	})
}
