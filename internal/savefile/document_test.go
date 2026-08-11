package savefile

import (
	"reflect"
	"strings"
	"testing"
)

// documentFixture exercises the shapes a save actually uses: numeric entity
// keys, repeated keys, lists of scalars, lists of anonymous containers, an
// empty container, and a colour whose braces belong to its value.
const documentFixture = `
landed_titles={
	landed_titles={
		7={ key="c_alpha" color=rgb { 255 207 51 } treasury={} history={ 1066.10.1={ type=created holder=42 } } }
		8={ key="c_beta" }
	}
}
triggered_event={ id=one }
triggered_event={ id=two }
triggered_event={ id=three }
traits_lookup={ brave craven }
variables={ data={ { flag=reason } { flag=title } } }
`

func documentOf(t *testing.T, raw string, depth int) *Document {
	t.Helper()
	path, err := ParseDocumentPath(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	document, err := ReadDocument(EncodingText, strings.NewReader(documentFixture), nil,
		path, depth, DefaultLimits(), DefaultDocumentLimits())
	if err != nil {
		t.Fatalf("reading %q: %v", raw, err)
	}
	return document
}

func TestParseDocumentPath(t *testing.T) {
	good := map[string]string{
		"":                   "",
		"a":                  "a",
		"a.b.c":              "a.b.c",
		"a[2].b":             "a[2].b",
		"landed_titles.4501": "landed_titles.4501",
	}
	for raw, want := range good {
		path, err := ParseDocumentPath(raw)
		if err != nil {
			t.Fatalf("parsing %q: %v", raw, err)
		}
		if got := path.String(); got != want {
			t.Errorf("%q round-tripped to %q, want %q", raw, got, want)
		}
	}
	for _, bad := range []string{"a..b", "a[", "a[x]", "a[-1]", "[0]", "."} {
		if _, err := ParseDocumentPath(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// TestDocumentListsRepeatedKeysOnce pins the discovery behaviour that makes a
// real save legible: an ordinary one holds 1678 sibling triggered_event
// blocks, and one row saying 1678 is the useful answer.
func TestDocumentListsRepeatedKeysOnce(t *testing.T) {
	root := documentOf(t, "", 0)
	if root.Kind != NodeObject {
		t.Fatalf("root kind = %s", root.Kind)
	}
	var triggered *DocumentChild
	for index, child := range root.Children {
		if child.Key == "triggered_event" {
			if triggered != nil {
				t.Fatalf("triggered_event was listed twice: %+v", root.Children)
			}
			triggered = &root.Children[index]
		}
	}
	if triggered == nil {
		t.Fatalf("triggered_event was not listed: %+v", root.Children)
	}
	if triggered.Occurrences != 3 {
		t.Errorf("occurrences = %d, want 3", triggered.Occurrences)
	}
	// child_count counts occurrences, not distinct names.
	if root.ChildCount != 6 {
		t.Errorf("child_count = %d, want 6", root.ChildCount)
	}
}

func TestDocumentIndexSelectsAmongRepeatedKeys(t *testing.T) {
	for index, want := range map[int]string{0: "one", 1: "two", 2: "three"} {
		document := documentOf(t, "triggered_event["+itoa(index)+"]", 1)
		value, ok := document.Value.(map[string]any)
		if !ok {
			t.Fatalf("occurrence %d is %T", index, document.Value)
		}
		if value["id"] != want {
			t.Errorf("occurrence %d id = %v, want %q", index, value["id"], want)
		}
	}
	if _, err := ReadDocument(EncodingText, strings.NewReader(documentFixture), nil,
		DocumentPath{{Name: "triggered_event", Index: 9}}, 0,
		DefaultLimits(), DefaultDocumentLimits()); err == nil {
		t.Error("an out-of-range occurrence was accepted")
	}
}

func TestDocumentReadsSaveShapes(t *testing.T) {
	title := documentOf(t, "landed_titles.landed_titles.7", 3)
	value, ok := title.Value.(map[string]any)
	if !ok {
		t.Fatalf("title value is %T", title.Value)
	}
	if value["key"] != "c_alpha" {
		t.Errorf("key = %v", value["key"])
	}
	// The braces after a colour name belong to the value, not the container.
	if value["color"] != "rgb(255,207,51)" {
		t.Errorf("color = %v, want the folded colour value", value["color"])
	}
	// An empty container is empty, not absent.
	if empty, ok := value["treasury"].(map[string]any); !ok || len(empty) != 0 {
		t.Errorf("treasury = %#v, want an empty object", value["treasury"])
	}
	history, ok := value["history"].(map[string]any)
	if !ok {
		t.Fatalf("history is %T", value["history"])
	}
	entry, ok := history["1066.10.1"].(map[string]any)
	if !ok {
		t.Fatalf("the dated history entry is %T", history["1066.10.1"])
	}
	if entry["type"] != "created" || entry["holder"] != int64(42) {
		t.Errorf("history entry = %+v", entry)
	}

	// A list of scalars, and a list of anonymous containers.
	traits := documentOf(t, "traits_lookup", 1)
	if traits.Kind != NodeArray {
		t.Errorf("traits_lookup kind = %s", traits.Kind)
	}
	if !reflect.DeepEqual(traits.Value, []any{"brave", "craven"}) {
		t.Errorf("traits_lookup = %#v", traits.Value)
	}
	data := documentOf(t, "variables.data", 2)
	items, ok := data.Value.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("variables.data = %#v", data.Value)
	}
	first, ok := items[0].(map[string]any)
	if !ok || first["flag"] != "reason" {
		t.Fatalf("the first anonymous item = %#v", items[0])
	}
}

// TestDocumentDepthElisionIsDeclared keeps the bound honest: the save format
// has no null, so a null in a value can only mean "not materialised", and the
// answer has to say so rather than leaving it to be inferred.
func TestDocumentDepthElisionIsDeclared(t *testing.T) {
	shallow := documentOf(t, "landed_titles.landed_titles.7", 1)
	value, ok := shallow.Value.(map[string]any)
	if !ok {
		t.Fatalf("value is %T", shallow.Value)
	}
	if value["history"] != nil {
		t.Errorf("history was materialised at depth 1: %#v", value["history"])
	}
	found := false
	for _, reason := range shallow.Truncated {
		if reason == "max_depth" {
			found = true
		}
	}
	if !found {
		t.Errorf("an elided container was not declared: %+v", shallow.Truncated)
	}
	// The child listing still reports the shape of what was elided.
	for _, child := range shallow.Children {
		if child.Key == "history" && child.Children != 1 {
			t.Errorf("elided history reported %d children, want 1", child.Children)
		}
	}
}

// TestDocumentIndexesIntoLists covers the half of the format that is not a
// map: relations, opinions and known_secrets are all arrays, and without a
// positional step their contents could be materialised but never navigated.
func TestDocumentIndexesIntoLists(t *testing.T) {
	// A list of anonymous containers.
	second := documentOf(t, "variables.data.1", 1)
	value, ok := second.Value.(map[string]any)
	if !ok {
		t.Fatalf("variables.data.1 is %T", second.Value)
	}
	if value["flag"] != "title" {
		t.Errorf("variables.data.1 = %#v, want the second item", value)
	}
	// A list of bare scalars.
	trait := documentOf(t, "traits_lookup.1", 0)
	if trait.Kind != NodeScalar || trait.Scalar != "craven" {
		t.Errorf("traits_lookup.1 = %s %q, want the scalar craven", trait.Kind, trait.Scalar)
	}
	// A numeric key still wins over a position, so an entity table keyed by
	// number is unaffected.
	title := documentOf(t, "landed_titles.landed_titles.8", 1)
	keyed, ok := title.Value.(map[string]any)
	if !ok || keyed["key"] != "c_beta" {
		t.Errorf("landed_titles.landed_titles.8 = %#v, want the entry keyed 8", title.Value)
	}
	if _, err := ReadDocument(EncodingText, strings.NewReader(documentFixture), nil,
		DocumentPath{{Name: "traits_lookup"}, {Name: "9"}}, 0,
		DefaultLimits(), DefaultDocumentLimits()); err == nil {
		t.Error("an out-of-range list position was accepted")
	}
}

func TestDocumentRefusesAPathThatIsNotThere(t *testing.T) {
	for _, raw := range []string{"nowhere", "landed_titles.nowhere", "traits_lookup.brave.deeper"} {
		path, err := ParseDocumentPath(raw)
		if err != nil {
			t.Fatalf("parsing %q: %v", raw, err)
		}
		if _, err := ReadDocument(EncodingText, strings.NewReader(documentFixture), nil,
			path, 1, DefaultLimits(), DefaultDocumentLimits()); err == nil {
			t.Errorf("%q was answered instead of refused", raw)
		}
	}
}

// TestDocumentBudgetStopsGrowing is the load-bearing bound: a path a caller
// picked badly must cost a capped answer, not the whole save in memory.
func TestDocumentBudgetStopsGrowing(t *testing.T) {
	bounds := DefaultDocumentLimits()
	bounds.MaxNodes = 3
	bounds.MaxChildren = 2
	document, err := ReadDocument(EncodingText, strings.NewReader(documentFixture), nil,
		nil, bounds.MaxDepth, DefaultLimits(), bounds)
	if err != nil {
		t.Fatalf("reading the root: %v", err)
	}
	if len(document.Children) > bounds.MaxChildren {
		t.Errorf("children = %d, past the cap of %d", len(document.Children), bounds.MaxChildren)
	}
	// The true totals are still reported, so the caller knows what was cut.
	if document.ChildCount != 6 {
		t.Errorf("child_count = %d, want the true 6", document.ChildCount)
	}
	if len(document.Truncated) == 0 {
		t.Error("a capped answer did not say so")
	}
}
