package indexer

import (
	"fmt"
	"sort"
	"strings"

	"ck3-index/internal/script"
)

// Boolean connectives written by hand drift into shapes that are provably
// redundant or provably unsatisfiable: an AND nested straight inside an AND, a
// condition next to its own negation, a branch of an OR that the OR already
// covers. These are decidable from the tree alone, and they are the shapes a
// generator produces most often, because each round of "add one more guard"
// appends rather than restructures.
//
// The rules apply only inside an explicit AND / OR / NOT / NOR / NAND block.
// Every other container is off limits on purpose: a repeated line in
// effect = { add_gold = 5 add_gold = 5 } is not redundant, it is ten gold, and
// a rule that cannot tell those apart has no business flagging either.

var booleanConnectives = map[string]bool{
	"and":  true,
	"or":   true,
	"not":  true,
	"nor":  true,
	"nand": true,
}

// negatingConnectives invert their contents. NOT with several children means
// "none of these", which is NOR, so both share the negating behaviour.
var negatingConnectives = map[string]bool{
	"not":  true,
	"nor":  true,
	"nand": true,
}

func connectiveKey(node *script.Node) string {
	if node == nil || node.Kind != "block" {
		return ""
	}
	key := strings.ToLower(node.Key)
	if !booleanConnectives[key] {
		return ""
	}
	return key
}

// conditionIdentity is the canonical shape of one condition, so that two
// spellings of the same check compare equal while a different value does not.
func conditionIdentity(node *script.Node) string {
	hash := canonicalOverrideNodeHash(node)
	return string(hash[:])
}

// negationTarget reports the single condition a NOT wraps. Only a one-child NOT
// is a plain negation; NOT with several children is NOR and negates their
// disjunction instead, which these rules do not try to match.
func negationTarget(node *script.Node) (*script.Node, bool) {
	if strings.ToLower(node.Key) != "not" || node.Kind != "block" || len(node.Children) != 1 {
		return nil, false
	}
	return node.Children[0], true
}

func checkTriggerAlgebra(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	walkNodes(nodes, func(node *script.Node) {
		key := connectiveKey(node)
		if key == "" {
			return
		}
		out = append(out, checkNestedSameConnective(node, key)...)
		out = append(out, checkDuplicateConditions(node, key)...)
		out = append(out, checkContradiction(node, key)...)
		out = append(out, checkDoubleNegation(node, key)...)
		out = append(out, checkCommonBranchCondition(node, key)...)
		out = append(out, checkAbsorbedBranch(node, key)...)
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].line != out[j].line {
			return out[i].line < out[j].line
		}
		if out[i].col != out[j].col {
			return out[i].col < out[j].col
		}
		return out[i].code < out[j].code
	})
	return out
}

// OT.1 associativity: AND inside AND, OR inside OR. The inner block's members
// belong to the outer one and the nesting only costs a level of reading.
func checkNestedSameConnective(node *script.Node, key string) []ctxDiag {
	if key != "and" && key != "or" {
		return nil
	}
	var out []ctxDiag
	for _, child := range node.Children {
		if connectiveKey(child) != key {
			continue
		}
		out = append(out, ctxDiag{
			severity: "info",
			code:     "trigger_nested_same_operator",
			msg: fmt.Sprintf("%s nested directly inside %s; its members can move up into the parent block",
				strings.ToUpper(key), strings.ToUpper(key)),
			line: child.Line, col: child.Col,
		})
	}
	return out
}

// OT.2 idempotence: checking the same condition twice in one block cannot
// produce a different answer the second time.
func checkDuplicateConditions(node *script.Node, key string) []ctxDiag {
	var out []ctxDiag
	seen := map[string]*script.Node{}
	for _, child := range node.Children {
		identity := conditionIdentity(child)
		first, ok := seen[identity]
		if !ok {
			seen[identity] = child
			continue
		}
		out = append(out, ctxDiag{
			severity: "warning",
			code:     "trigger_duplicate_condition",
			msg: fmt.Sprintf("%q repeats the condition already checked at line %d of the same %s block; the second check cannot change the result",
				child.Key, first.Line, strings.ToUpper(key)),
			line: child.Line, col: child.Col,
		})
	}
	return out
}

// OT.3 contradiction: a condition beside its own negation. Under AND the block
// can never be satisfied; under OR it is satisfied by everything. Either way
// the block does not express what it looks like it expresses.
func checkContradiction(node *script.Node, key string) []ctxDiag {
	if key != "and" && key != "or" {
		return nil
	}
	positives := map[string]*script.Node{}
	negatives := map[string]*script.Node{}
	for _, child := range node.Children {
		if target, ok := negationTarget(child); ok {
			negatives[conditionIdentity(target)] = child
			continue
		}
		positives[conditionIdentity(child)] = child
	}
	var out []ctxDiag
	for identity, positive := range positives {
		negative, ok := negatives[identity]
		if !ok {
			continue
		}
		later := positive
		if negative.Line > positive.Line {
			later = negative
		}
		code, msg := "trigger_always_false", fmt.Sprintf("AND block requires %q and its negation at the same time, so it can never be satisfied", positive.Key)
		if key == "or" {
			code, msg = "trigger_always_true", fmt.Sprintf("OR block accepts %q and its negation, so it is satisfied by everything and filters nothing", positive.Key)
		}
		out = append(out, ctxDiag{severity: "error", code: code, msg: msg, line: later.Line, col: later.Col})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

// OT.4 double negation: saying no twice is saying yes, at the cost of two
// levels of reading. NOT { NOR { ... } } collapses to an OR the same way.
func checkDoubleNegation(node *script.Node, key string) []ctxDiag {
	inner, ok := negationTarget(node)
	if !ok {
		return nil
	}
	innerKey := strings.ToLower(inner.Key)
	if inner.Kind != "block" || !negatingConnectives[innerKey] {
		return nil
	}
	replacement := "the condition itself"
	if innerKey == "nor" {
		replacement = "a single OR block"
	}
	if innerKey == "nand" {
		replacement = "a single AND block"
	}
	return []ctxDiag{{
		severity: "warning",
		code:     "trigger_double_negation",
		msg: fmt.Sprintf("%s wrapped directly in NOT negates twice; replace both with %s",
			strings.ToUpper(innerKey), replacement),
		line: inner.Line, col: inner.Col,
	}}
}

// OT.5 distributivity: when every branch of an OR checks the same condition,
// that condition holds regardless of which branch matches and belongs above
// the OR, where it is checked once and read once.
func checkCommonBranchCondition(node *script.Node, key string) []ctxDiag {
	if key != "or" || len(node.Children) < 2 {
		return nil
	}
	branches := make([][]*script.Node, 0, len(node.Children))
	for _, child := range node.Children {
		if connectiveKey(child) != "and" || len(child.Children) < 2 {
			return nil
		}
		branches = append(branches, child.Children)
	}
	shared := map[string]*script.Node{}
	for _, condition := range branches[0] {
		shared[conditionIdentity(condition)] = condition
	}
	for _, branch := range branches[1:] {
		present := map[string]bool{}
		for _, condition := range branch {
			present[conditionIdentity(condition)] = true
		}
		for identity := range shared {
			if !present[identity] {
				delete(shared, identity)
			}
		}
		if len(shared) == 0 {
			return nil
		}
	}
	var out []ctxDiag
	for _, condition := range shared {
		out = append(out, ctxDiag{
			severity: "info",
			code:     "trigger_common_condition",
			msg: fmt.Sprintf("every branch of this OR checks %q; lift it out of the OR so it is checked once",
				condition.Key),
			line: node.Line, col: node.Col,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].msg < out[j].msg })
	return out
}

// OT.6 absorption: X OR (X AND Y) is just X, and X AND (X OR Y) is just X. In
// both shapes the compound branch can never decide the outcome.
func checkAbsorbedBranch(node *script.Node, key string) []ctxDiag {
	if key != "and" && key != "or" {
		return nil
	}
	opposite := "and"
	if key == "and" {
		opposite = "or"
	}
	plain := map[string]bool{}
	for _, child := range node.Children {
		if connectiveKey(child) != "" {
			continue
		}
		plain[conditionIdentity(child)] = true
	}
	if len(plain) == 0 {
		return nil
	}
	var out []ctxDiag
	for _, child := range node.Children {
		if connectiveKey(child) != opposite {
			continue
		}
		for _, inner := range child.Children {
			if !plain[conditionIdentity(inner)] {
				continue
			}
			out = append(out, ctxDiag{
				severity: "info",
				code:     "trigger_absorbed_branch",
				msg: fmt.Sprintf("this %s branch repeats %q, which the enclosing %s already decides; the branch cannot change the result",
					strings.ToUpper(opposite), inner.Key, strings.ToUpper(key)),
				line: child.Line, col: child.Col,
			})
			break
		}
	}
	return out
}

// Calling a scripted effect does not push a scope, so prev at the top of a
// macro body names whatever scope the caller had entered before calling. The
// macro keeps working until someone calls it from one level deeper, and then it
// silently operates on the wrong character. Naming the scope as a parameter
// makes the requirement checkable at the call site.
//
// The companion rule on root, which the smell catalogue this came from treats
// as the same defect, is deliberately absent: root inside a scripted effect
// appears 2712 times in CK3 1.19's own scripted_effects and 2222 times in the
// upstream mod. A rule that indicts the content base it is meant to check is
// reporting a house style, not a defect. Top-level prev appears 9 and 15 times
// in the same two trees, which is the rate of a real mistake.
func checkHiddenScopeDependency(nodes []*script.Node, relPath string) []ctxDiag {
	lower := strings.ToLower(strings.ReplaceAll(relPath, "\\", "/"))
	if !strings.Contains(lower, "common/scripted_effects/") && !strings.Contains(lower, "common/scripted_triggers/") {
		return nil
	}
	var out []ctxDiag
	for _, macro := range nodes {
		if macro.Kind != "block" || macro.Key == "" {
			continue
		}
		// A macro that says prev in its own name has documented the requirement
		// in the one place every caller already reads.
		if strings.Contains(strings.ToLower(macro.Key), "prev") {
			continue
		}
		// Only direct children are unambiguous: below that, prev may be
		// referring to a scope the macro itself entered.
		for _, child := range macro.Children {
			if !referencesScopeWord(child, "prev") {
				continue
			}
			out = append(out, ctxDiag{
				severity: "warning",
				code:     "hidden_scope_dependency",
				msg:      fmt.Sprintf("%q uses prev before entering any scope of its own, so it reads a scope opened outside its body; pass that scope as a parameter instead", macro.Key),
				line:     child.Line, col: child.Col,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].line != out[j].line {
			return out[i].line < out[j].line
		}
		return out[i].col < out[j].col
	})
	return out
}

// referencesScopeWord matches the scope word as a key, as a value, and as the
// head of a dotted path, but never as a substring of a longer identifier: a
// trait named root_of_the_matter is not a scope reference.
func referencesScopeWord(node *script.Node, word string) bool {
	return scopeWordMatches(node.Key, word) || scopeWordMatches(node.Value, word)
}

func scopeWordMatches(text, word string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == word {
		return true
	}
	return strings.HasPrefix(lower, word+".")
}
