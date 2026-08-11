package savefile

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// A save is a document, not a record set. The targeted scans in gamestate.go
// answer questions this package already knew to ask; this file answers the
// ones it does not, by walking to any path and materialising what is there
// within a fixed budget.
//
// Two structural facts drive the design, and both are why the targeted
// walkers cannot be reused as they are:
//
//   - A container is not always a map. `traits={ 0 2 }` and `perk={ a b c }`
//     are ordered lists, and `landed_titles` holds `4501={ ... }` entries
//     whose keys are numbers rather than identifiers.
//   - Keys repeat. `triggered_event` occurs 1678 times at the top level of an
//     ordinary save, so a name alone does not identify a node.

// DocumentLimits bounds one materialisation.
//
// A save is up to half a gigabyte and a single block can hold hundreds of
// thousands of entries, so every extraction is capped and says when it hit a
// cap. Nothing here is a guess about what a caller wants; it is the ceiling
// that keeps a bad path from pulling the whole save into memory.
type DocumentLimits struct {
	// MaxNodes caps how many values one materialisation may produce.
	MaxNodes int
	// MaxDepth caps how far below the target the walk descends.
	MaxDepth int
	// MaxChildren caps entries or items reported for one container.
	MaxChildren int
	// MaxScalarBytes caps one reported scalar.
	MaxScalarBytes int
}

// DefaultDocumentLimits is sized so a full response stays inside an ordinary
// tool result rather than by what the format allows.
func DefaultDocumentLimits() DocumentLimits {
	return DocumentLimits{
		MaxNodes:       4096,
		MaxDepth:       6,
		MaxChildren:    256,
		MaxScalarBytes: 2048,
	}
}

// Node kinds a document position can have.
const (
	NodeObject = "object"
	NodeArray  = "array"
	NodeScalar = "scalar"
	NodeEmpty  = "empty"
)

// DocumentStep is one path component: a name, and which occurrence of it.
type DocumentStep struct {
	Name  string
	Index int
}

// DocumentPath is a parsed path into a save section.
type DocumentPath []DocumentStep

// ParseDocumentPath reads the `a.b[2].c` form.
//
// The index selects among repeated keys, which is the only way to name one of
// 1678 sibling `triggered_event` blocks. Absent, it means the first.
func ParseDocumentPath(raw string) (DocumentPath, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	path := make(DocumentPath, 0, 8)
	for _, component := range strings.Split(trimmed, ".") {
		if component == "" {
			return nil, newError(ErrPath, "a path component is empty")
		}
		step := DocumentStep{Name: component}
		if open := strings.IndexByte(component, '['); open >= 0 {
			if !strings.HasSuffix(component, "]") {
				return nil, newError(ErrPath,
					fmt.Sprintf("path component %q opens an index it never closes", component))
			}
			index, err := strconv.Atoi(component[open+1 : len(component)-1])
			if err != nil || index < 0 {
				return nil, newError(ErrPath,
					fmt.Sprintf("path component %q has a non-numeric index", component))
			}
			step.Name = component[:open]
			step.Index = index
		}
		if step.Name == "" {
			return nil, newError(ErrPath, "a path component names no key")
		}
		path = append(path, step)
	}
	return path, nil
}

// String renders the path back into the form ParseDocumentPath accepts.
func (p DocumentPath) String() string {
	parts := make([]string, 0, len(p))
	for _, step := range p {
		if step.Index == 0 {
			parts = append(parts, step.Name)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s[%d]", step.Name, step.Index))
	}
	return strings.Join(parts, ".")
}

// DocumentChild describes one child of a container without materialising it.
//
// This is what makes an unknown save explorable: the shape and size of every
// child is reported for the cost of skipping it, so a caller can decide what
// is worth reading before reading anything.
type DocumentChild struct {
	// Key is the field name, or the index for an array item.
	Key string `json:"key"`
	// Kind is object, array, scalar or empty.
	Kind string `json:"kind"`
	// Occurrences is how many times this key appears in this container.
	Occurrences int `json:"occurrences,omitempty"`
	// Children is how many entries or items the child holds.
	Children int `json:"children,omitempty"`
	// Preview is a bounded rendering of a scalar child.
	Preview string `json:"preview,omitempty"`
}

// Document is one bounded view of a position in a save.
type Document struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	// Children lists what is directly below, always.
	Children []DocumentChild `json:"children,omitempty"`
	// ChildCount is the true count, which Children may have been capped below.
	ChildCount int `json:"child_count"`
	// Value is the materialised subtree, present when a depth was asked for.
	//
	// A null inside it always means "not materialised at this depth", never
	// "absent": the save format has no null, so the two can never be
	// confused. Truncated says max_depth whenever one appears.
	Value any `json:"value,omitempty"`
	// Scalar is the value at this position when it is a leaf.
	Scalar string `json:"scalar,omitempty"`
	// RepeatedKeys names keys that occur more than once in the materialised
	// value, whose values are therefore arrays rather than single values.
	RepeatedKeys []string `json:"repeated_keys,omitempty"`
	// Truncated names every bound that was reached.
	Truncated []string `json:"truncated,omitempty"`
	// BytesRead is how much section the walk consumed.
	BytesRead int64 `json:"bytes_read"`
}

// ReadDocument walks to path and reports what is there.
//
// depth 0 lists the children only. A greater depth also materialises the
// subtree to that many levels. The stream is walked once, and everything off
// the path costs only the tokens needed to skip it.
func ReadDocument(encoding Encoding, src io.Reader, resolver *TokenMap, path DocumentPath,
	depth int, limits Limits, bounds DocumentLimits) (*Document, error) {
	if resolver == nil && encoding != EncodingText {
		return nil, newError(ErrTokenMap, "a token map is required to navigate a binary save")
	}
	if depth < 0 {
		depth = 0
	}
	if depth > bounds.MaxDepth {
		depth = bounds.MaxDepth
	}
	streamLimits := limits
	streamLimits.MaxTokens = limits.gamestateCeiling()/2 + 1
	decoder := NewStreamDecoderFor(encoding, src, streamLimits)

	walk := &documentWalk{decoder: decoder, resolver: resolver, bounds: bounds, limits: limits}
	document := &Document{Path: path.String()}
	if err := walk.descend(path, depth, document); err != nil {
		return nil, err
	}
	document.Truncated = walk.truncated
	document.BytesRead = decoder.Consumed()
	return document, nil
}

// documentWalk carries the budget shared by every level of one walk.
type documentWalk struct {
	decoder   *StreamDecoder
	resolver  *TokenMap
	bounds    DocumentLimits
	limits    Limits
	nodes     int
	truncated []string
	// scratch keeps one token's text alive across a lookahead.
	scratch []byte
}

// hold copies a token's payload out of the decoder's window.
//
// Token.Text is only valid until the next read, so a token kept across one is
// preserved here. One buffer is enough: only a single lookahead is ever open.
func (w *documentWalk) hold(token Token) Token {
	if len(token.Text) == 0 {
		return token
	}
	w.scratch = append(w.scratch[:0], token.Text...)
	token.Text = w.scratch
	return token
}

func (w *documentWalk) note(reason string) {
	for _, seen := range w.truncated {
		if seen == reason {
			return
		}
	}
	w.truncated = append(w.truncated, reason)
}

// spend charges one node against the budget and reports whether it was
// available. A materialisation stops when it runs out rather than growing.
func (w *documentWalk) spend() bool {
	if w.nodes >= w.bounds.MaxNodes {
		w.note("max_nodes")
		return false
	}
	w.nodes++
	return true
}

// descend follows path from the section root, then describes where it lands.
func (w *documentWalk) descend(path DocumentPath, depth int, out *Document) error {
	// The section root is an implicit container that the stream is already
	// inside, so the first level is walked at depth zero.
	for level, step := range path {
		found, value, err := w.seek(step)
		if err != nil {
			return err
		}
		if !found {
			return newError(ErrPath, fmt.Sprintf("no %q under %q",
				step.Name, DocumentPath(path[:level]).String()))
		}
		if value.Kind != KindOpen {
			// A scalar can only be the end of a path.
			if level != len(path)-1 {
				return newError(ErrPath, fmt.Sprintf("%q is a scalar and has no children",
					DocumentPath(path[:level+1]).String()))
			}
			out.Kind = NodeScalar
			out.Scalar = scalarText(value, w.bounds.MaxScalarBytes)
			return nil
		}
	}
	return w.describe(depth, out)
}

// seek advances through the current container to the requested occurrence of
// one key, skipping every value it passes. It leaves the decoder positioned
// just inside the matched value when that value is a container.
//
// A step whose name is a number also addresses a list element by position:
// relations, opinions and known_secrets are arrays, not keyed maps, and
// without this their contents could be materialised but never navigated into.
func (w *documentWalk) seek(step DocumentStep) (bool, Token, error) {
	seen, position := 0, 0
	wantPosition, isPositional := positionalStep(step)
	target := w.decoder.Depth()

	// item accounts for one list element, and reports the matched value when
	// this is the position the step asked for.
	item := func(value Token) (bool, Token, error) {
		matched := isPositional && position == wantPosition
		position++
		if matched {
			return true, value, nil
		}
		err := w.decoder.SkipValue(value)
		return false, Token{}, err
	}

	for {
		if w.decoder.Depth() < target || w.decoder.Done() {
			return false, Token{}, nil
		}
		token, err := w.decoder.Next()
		if err != nil {
			return false, Token{}, err
		}
		if token.Kind == KindClose {
			return false, Token{}, nil
		}
		key, ok := w.decoder.entryKey(token, w.resolver)
		if !ok {
			// An anonymous container: only a position can name it.
			if matched, value, err := item(token); matched || err != nil {
				return matched, value, err
			}
			continue
		}
		// Reading the next token can move the decoder's window, so a scalar
		// that might turn out to be a list element has to be preserved before
		// the lookahead rather than after it.
		held := w.hold(token)
		next, err := w.decoder.Next()
		if err != nil {
			return false, Token{}, err
		}
		if next.Kind != KindEqual {
			// A bare scalar element, and the token after it starts the next.
			if matched, value, err := item(held); matched || err != nil {
				return matched, value, err
			}
			if next.Kind == KindClose {
				return false, Token{}, nil
			}
			if matched, value, err := item(next); matched || err != nil {
				return matched, value, err
			}
			continue
		}
		value, err := w.decoder.Next()
		if err != nil {
			return false, Token{}, err
		}
		if key != step.Name {
			if err := w.decoder.SkipValue(value); err != nil {
				return false, Token{}, err
			}
			continue
		}
		if seen == step.Index {
			return true, value, nil
		}
		seen++
		if err := w.decoder.SkipValue(value); err != nil {
			return false, Token{}, err
		}
	}
}

// positionalStep reports the list position a numeric step names.
//
// A step keeps its key meaning too: a numeric key like `4501={ ... }` under
// landed_titles is matched as a key first, and the position is only reached
// when nothing in the container is keyed that way.
func positionalStep(step DocumentStep) (int, bool) {
	if step.Index != 0 {
		return 0, false
	}
	value, err := strconv.Atoi(step.Name)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// describe reports the container the decoder is currently inside.
func (w *documentWalk) describe(depth int, out *Document) error {
	container, err := w.materialise(depth)
	if err != nil {
		return err
	}
	out.Kind = container.kind
	out.ChildCount = container.count
	out.Children = container.children
	out.RepeatedKeys = container.repeated
	if depth > 0 {
		out.Value = container.value
	}
	return nil
}

// materialised is one container's shape, and optionally its contents.
type materialised struct {
	kind     string
	count    int
	children []DocumentChild
	repeated []string
	value    any
	// childIndex locates an already-listed key so repeats collapse onto it.
	childIndex map[string]int
}

// materialise walks the container the decoder is inside.
//
// It always reports the children, because their shape is the cheapest useful
// answer and costs only the skipping it would do anyway. It builds the value
// as well while depth remains.
func (w *documentWalk) materialise(depth int) (*materialised, error) {
	out := &materialised{kind: NodeEmpty}
	entries := map[string]int{}
	object := map[string]any{}
	// A repeated key is listed once with its count, not once per occurrence.
	// An ordinary save has 1678 sibling `triggered_event` blocks, and 1678
	// identical rows tell a reader less than one row saying 1678.
	out.childIndex = map[string]int{}
	var items []any
	// A container's kind is decided by its first `key = value` pair, or by the
	// absence of one. Both forms occur, and a few containers mix them.
	decided := false

	target := w.decoder.Depth()
	for {
		if w.decoder.Depth() < target || w.decoder.Done() {
			break
		}
		token, err := w.decoder.Next()
		if err != nil {
			return nil, err
		}
		if token.Kind == KindClose {
			break
		}

		key, isKey := w.decoder.entryKey(token, w.resolver)
		var value Token
		var isEntry bool
		if isKey {
			// Only a following `=` makes this an entry rather than an item.
			next, err := w.decoder.Next()
			if err != nil {
				return nil, err
			}
			switch {
			case next.Kind == KindEqual:
				value, err = w.decoder.Next()
				if err != nil {
					return nil, err
				}
				isEntry = true
			case next.Kind == KindClose:
				if err := w.recordItem(out, &items, token, depth); err != nil {
					return nil, err
				}
				return w.finish(out, object, items, entries, decided)
			default:
				// Two items in hand: the scalar just read, and whatever
				// followed it.
				if err := w.recordItem(out, &items, token, depth); err != nil {
					return nil, err
				}
				if err := w.recordItem(out, &items, next, depth); err != nil {
					return nil, err
				}
				continue
			}
		} else {
			value = token
		}

		if !isEntry {
			if err := w.recordItem(out, &items, value, depth); err != nil {
				return nil, err
			}
			continue
		}
		decided = true
		out.count++
		occurrence := entries[key]
		entries[key] = occurrence + 1

		decoded, err := w.recordEntry(out, key, value, depth)
		if err != nil {
			return nil, err
		}
		if depth > 0 {
			if occurrence == 0 {
				object[key] = decoded
			} else {
				if occurrence == 1 {
					object[key] = []any{object[key]}
					out.repeated = append(out.repeated, key)
				}
				object[key] = append(object[key].([]any), decoded)
			}
		}
	}
	return w.finish(out, object, items, entries, decided)
}

func (w *documentWalk) finish(out *materialised, object map[string]any,
	items []any, entries map[string]int, decided bool) (*materialised, error) {
	switch {
	case decided:
		out.kind = NodeObject
		out.value = object
		// Occurrence counts are only known once the whole container is read.
		for index := range out.children {
			out.children[index].Occurrences = entries[out.children[index].Key]
		}
	case out.count > 0 || len(items) > 0:
		out.kind = NodeArray
		out.value = items
	default:
		// `treasury={}` is empty, not absent. Answering null would say the
		// field is missing, which is a different fact about the save.
		out.kind = NodeEmpty
		out.value = map[string]any{}
	}
	return out, nil
}

// recordItem accounts for one array element and consumes it.
func (w *documentWalk) recordItem(out *materialised, items *[]any, token Token, depth int) error {
	out.count++
	addChild := func(child DocumentChild) {
		if len(out.children) < w.bounds.MaxChildren {
			out.children = append(out.children, child)
			return
		}
		w.note("max_children")
	}
	key := strconv.Itoa(out.count - 1)
	if token.Kind != KindOpen {
		addChild(DocumentChild{Key: key, Kind: NodeScalar,
			Preview: scalarText(token, w.bounds.MaxScalarBytes)})
		if depth > 0 && w.spend() {
			*items = append(*items, scalarValue(token, w.bounds.MaxScalarBytes))
		}
		return nil
	}
	// An anonymous container item is descended into on the same terms as a
	// named one: a memory's `variables.data` is a list of these, and eliding
	// them is what made the list read as empty.
	if depth <= 1 || !w.spend() {
		kind, count, err := w.countChildren()
		if err != nil {
			return err
		}
		addChild(DocumentChild{Key: key, Kind: kind, Children: count})
		if depth > 0 {
			w.note("max_depth")
			*items = append(*items, nil)
		}
		return nil
	}
	nested, err := w.materialise(depth - 1)
	if err != nil {
		return err
	}
	addChild(DocumentChild{Key: key, Kind: nested.kind, Children: nested.count})
	*items = append(*items, nested.value)
	return nil
}

// recordEntry describes one `key = value` entry, and decodes it while depth
// remains. The value is always consumed, whether it was decoded or skipped.
func (w *documentWalk) recordEntry(out *materialised, key string, value Token, depth int) (any, error) {
	// A key already listed keeps its first row; only its count grows, which
	// finish fills in once the whole container has been read.
	_, listed := out.childIndex[key]
	addChild := func(child DocumentChild) {
		if listed {
			return
		}
		if len(out.children) < w.bounds.MaxChildren {
			out.childIndex[key] = len(out.children)
			out.children = append(out.children, child)
			return
		}
		w.note("max_children")
	}
	if value.Kind != KindOpen {
		addChild(DocumentChild{Key: key, Kind: NodeScalar,
			Preview: scalarText(value, w.bounds.MaxScalarBytes)})
		if depth > 0 && w.spend() {
			return scalarValue(value, w.bounds.MaxScalarBytes), nil
		}
		return nil, nil
	}
	// A container is descended into only while depth and budget remain.
	// Otherwise it is skipped and only its shape is reported, which costs the
	// same walk the skip would have cost anyway.
	if depth <= 1 || !w.spend() {
		kind, count, err := w.countChildren()
		if err != nil {
			return nil, err
		}
		addChild(DocumentChild{Key: key, Kind: kind, Children: count})
		if depth > 0 {
			w.note("max_depth")
		}
		return nil, nil
	}
	nested, err := w.materialise(depth - 1)
	if err != nil {
		return nil, err
	}
	addChild(DocumentChild{Key: key, Kind: nested.kind, Children: nested.count})
	return nested.value, nil
}

// countChildren walks the container the decoder is inside, skipping every
// value whole, and reports its kind and how many children it holds.
//
// It never recurses: a grandchild costs one SkipValue, so the shape of a
// block with hundreds of thousands of entries is still one linear pass.
func (w *documentWalk) countChildren() (string, int, error) {
	count, decided := 0, false
	target := w.decoder.Depth()
	for {
		if w.decoder.Depth() < target || w.decoder.Done() {
			break
		}
		token, err := w.decoder.Next()
		if err != nil {
			return "", 0, err
		}
		if token.Kind == KindClose {
			break
		}
		if _, isKey := w.decoder.entryKey(token, w.resolver); isKey {
			next, err := w.decoder.Next()
			if err != nil {
				return "", 0, err
			}
			if next.Kind == KindEqual {
				value, err := w.decoder.Next()
				if err != nil {
					return "", 0, err
				}
				if err := w.decoder.SkipValue(value); err != nil {
					return "", 0, err
				}
				count++
				decided = true
				continue
			}
			if next.Kind == KindClose {
				count++
				break
			}
			count += 2
			if err := w.decoder.SkipValue(next); err != nil {
				return "", 0, err
			}
			continue
		}
		count++
		if err := w.decoder.SkipValue(token); err != nil {
			return "", 0, err
		}
	}
	switch {
	case decided:
		return NodeObject, count, nil
	case count > 0:
		return NodeArray, count, nil
	default:
		return NodeEmpty, 0, nil
	}
}

// scalarValue renders a scalar as the JSON type it really is, so a number
// stays a number and a date stays the string a save wrote.
func scalarValue(token Token, max int) any {
	switch token.Kind {
	case KindBool:
		return token.Bool
	case KindU32, KindU64:
		return token.Unsigned
	case KindI32, KindI64:
		return token.Signed
	case KindF32, KindF64, KindDecimal:
		return floatOf(token)
	default:
		return scalarText(token, max)
	}
}

// scalarText renders any scalar as display text, bounded.
func scalarText(token Token, max int) string {
	var text string
	switch token.Kind {
	case KindOpen, KindClose, KindEqual:
		return ""
	case KindBool:
		if token.Bool {
			return "yes"
		}
		return "no"
	case KindU32, KindU64:
		text = strconv.FormatUint(token.Unsigned, 10)
	case KindI32, KindI64:
		text = strconv.FormatInt(token.Signed, 10)
	case KindF32, KindF64, KindDecimal:
		text = strconv.FormatFloat(floatOf(token), 'f', -1, 64)
	case KindRGB:
		return fmt.Sprintf("rgb(%d,%d,%d)", token.RGB[0], token.RGB[1], token.RGB[2])
	default:
		text = string(token.Text)
	}
	if max > 0 && len(text) > max {
		return text[:max] + "…"
	}
	return text
}
