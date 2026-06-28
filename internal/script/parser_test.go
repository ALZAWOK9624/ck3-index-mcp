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
