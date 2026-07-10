package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

type scopeWalkState struct {
	root     TigerScope
	current  TigerScope
	previous TigerScope
	context  string
	trace    []string
}

// checkScopeTracker validates only contexts whose root scope is known. It
// carries trigger/effect context through structural blocks, and applies both
// iterators and scope-to-scope transitions. Unknown roots fail closed: they do
// not produce a hard scope_mismatch.
func checkScopeTracker(nodes []*script.Node, relPath string) []ctxDiag {
	if isEventPath(relPath) {
		var out []ctxDiag
		for _, obj := range nodes {
			if obj.Kind != "block" || obj.Key == "" || !strings.Contains(obj.Key, ".") {
				continue
			}
			root := eventRootScope(obj)
			if root.IsZero() || root == ScopeAllScopes || root == ScopeValue {
				continue
			}
			out = append(out, checkScopeNodes(obj.Children, relPath, obj.Key, root)...)
		}
		return dedupeScopeDiagnostics(out)
	}

	root := fileScopeType(relPath)
	if root.IsZero() || root == ScopeAllScopes || root == ScopeValue {
		return nil
	}
	var out []ctxDiag
	for _, obj := range nodes {
		if obj.Kind != "block" || obj.Key == "" {
			continue
		}
		out = append(out, checkScopeNodes(obj.Children, relPath, obj.Key, root)...)
	}
	return dedupeScopeDiagnostics(out)
}

func checkScopeNodes(nodes []*script.Node, relPath, objectName string, root TigerScope) []ctxDiag {
	namedScopes := map[string]TigerScope{}
	initial := scopeWalkState{
		root:    root,
		current: root,
		trace:   []string{"root=" + scopeMaskDesc(root)},
	}
	var out []ctxDiag

	var walk func([]*script.Node, scopeWalkState)
	walk = func(ns []*script.Node, state scopeWalkState) {
		for _, n := range ns {
			key := strings.ToLower(n.Key)
			if key == "" {
				walk(n.Children, state)
				continue
			}

			if (state.context == "trigger" || state.context == "effect") && !state.current.IsZero() {
				need, found := requiredScopeForContext(key, state.context)
				if found && isConcreteScope(need) && !state.current.Intersects(need) && !isKnownScopeArgumentCollision(key, state.current, state.trace) {
					d := scopeMismatchDiagnostic(relPath, objectName, n, key, need, state.current, state.trace)
					if !engineScopeConfirms(key, state.context, need) {
						d.code = "scope_uncertain"
						d.severity = "info"
						d.msg += "; Tiger rule was not confirmed by current engine logs"
					}
					out = append(out, d)
				}
			}

			if key == "save_scope_as" && n.Value != "" && !state.current.IsZero() {
				name := strings.ToLower(n.Value)
				namedScopes[name] = scopeUnion(namedScopes[name], state.current)
			}

			child := state
			if nextContext := ContextFor(key); nextContext != "" && (state.context != "" || isScopeContextEntry(relPath, key)) {
				child.context = nextContext
			}

			scopeBlock := false
			if n.Kind == "block" {
				if target, source, ok := resolveChildScope(key, state, namedScopes, relPath); (state.context != "" || isScopeContextEntry(relPath, key)) && ok {
					scopeBlock = true
					child.previous = state.current
					child.current = target
					child.trace = appendScopeTrace(state.trace, source+"="+scopeMaskDesc(target))
				}
				if inScope, ok := iteratorScopeIn[key]; state.context != "" && ok && isConcreteScope(inScope) && !state.current.IsZero() && !state.current.Intersects(inScope) {
					d := scopeContainerMismatchDiagnostic(relPath, objectName, n, "iterator", key, inScope, state.current, state.trace)
					d.code = "scope_uncertain"
					d.severity = "info"
					out = append(out, d)
				} else if inScope, ok := scopeTransitionsIn[key]; state.context != "" && ok && isConcreteScope(inScope) && !state.current.IsZero() && !state.current.Intersects(inScope) {
					d := scopeContainerMismatchDiagnostic(relPath, objectName, n, "transition", key, inScope, state.current, state.trace)
					d.code = "scope_uncertain"
					d.severity = "info"
					out = append(out, d)
				}
			}
			if n.Kind == "block" && state.context != "" && !scopeBlock && !isScopeStructuralBlock(key) {
				// Unknown/native block arguments are data, not nested script. A
				// scripted trigger such as foo_trigger = { TERRAIN = hills }
				// must not reinterpret TERRAIN as the native terrain trigger.
				continue
			}
			walk(n.Children, child)
		}
	}
	walk(nodes, initial)
	return out
}

func requiredScopeForContext(key, contextKind string) (TigerScope, bool) {
	if contextKind == "trigger" {
		scope, ok := tigerTriggerScopes[key]
		return scope, ok
	}
	scope, ok := tigerEffectScopes[key]
	return scope, ok
}

func resolveChildScope(key string, state scopeWalkState, named map[string]TigerScope, relPath string) (TigerScope, string, bool) {
	if strings.Contains(key, ".") {
		chainState := state
		for _, segment := range strings.Split(key, ".") {
			target, _, ok := resolveChildScope(segment, chainState, named, relPath)
			if !ok {
				return TigerScope{}, "chain " + key, true
			}
			chainState.previous = chainState.current
			chainState.current = target
		}
		return chainState.current, "chain " + key, true
	}
	p := "/" + strings.TrimPrefix(filepathSlash(strings.ToLower(relPath)), "/")
	if strings.HasPrefix(p, "/common/landed_titles/") && key == "can_create" {
		return ScopeCharacter, "field can_create", true
	}
	switch key {
	case "root":
		return state.root, "root", !state.root.IsZero()
	case "this":
		return state.current, "this", !state.current.IsZero()
	case "prev":
		return state.previous, "prev", true
	}
	if strings.HasPrefix(key, "scope:") {
		scope := named[strings.TrimPrefix(key, "scope:")]
		return scope, key, true
	}
	if scope, ok := explicitTypedScope(key); ok {
		return scope, key, true
	}
	if strings.Contains(key, ":") {
		return TigerScope{}, key, true
	}
	if scope, ok := iteratorScopeOut[key]; ok {
		return scope, "iterator " + key, true
	}
	if scope, ok := scopeTransitionsOut[key]; ok {
		return scope, "transition " + key, true
	}
	if looksLikeIterator(key) {
		return TigerScope{}, "unknown iterator " + key, true
	}
	return TigerScope{}, "", false
}

func looksLikeIterator(key string) bool {
	return strings.HasPrefix(key, "any_") || strings.HasPrefix(key, "every_") || strings.HasPrefix(key, "random_") || strings.HasPrefix(key, "ordered_")
}

func isScopeStructuralBlock(key string) bool {
	if ContextFor(key) != "" {
		return true
	}
	switch key {
	case "and", "or", "not", "nand", "nor", "xor", "custom_description", "custom_tooltip", "switch", "while":
		return true
	}
	return false
}

func isKnownScopeArgumentCollision(key string, current TigerScope, trace []string) bool {
	if key != "category" || current != ScopeTrait {
		return false
	}
	for i := len(trace) - 1; i >= 0; i-- {
		if strings.Contains(trace[i], "any_trait_in_category") {
			return true
		}
	}
	return false
}

func isScopeContextEntry(relPath, key string) bool {
	p := "/" + strings.TrimPrefix(filepathSlash(strings.ToLower(relPath)), "/")
	switch {
	case strings.HasPrefix(p, "/events/"):
		return key == "trigger" || key == "immediate" || key == "option" || key == "after"
	case strings.HasPrefix(p, "/common/decisions/"):
		return key == "potential" || key == "possible" || key == "is_shown" || key == "is_valid" || key == "is_valid_showing_failures_only" || key == "effect"
	case strings.HasPrefix(p, "/common/landed_titles/"):
		return key == "can_create"
	case strings.HasPrefix(p, "/history/titles/"):
		return key == "effect"
	case isMenAtArmsTypesPath(relPath):
		return key == "can_recruit"
	}
	return false
}

func explicitTypedScope(key string) (TigerScope, bool) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return TigerScope{}, false
	}
	scope, ok := tigerScopesByName[parts[0]]
	return scope, ok
}

func eventRootScope(obj *script.Node) TigerScope {
	for _, child := range obj.Children {
		if strings.EqualFold(child.Key, "scope") && child.Value != "" {
			if scope, ok := tigerScopesByName[strings.ToLower(child.Value)]; ok {
				return scope
			}
			return TigerScope{}
		}
	}
	return ScopeCharacter
}

func isEventPath(relPath string) bool {
	p := "/" + strings.TrimPrefix(filepathSlash(strings.ToLower(relPath)), "/")
	return strings.HasPrefix(p, "/events/")
}

func isConcreteScope(scope TigerScope) bool {
	return !scope.IsZero() && scope != ScopeAllScopes && scope != ScopeValue
}

func appendScopeTrace(trace []string, item string) []string {
	out := append([]string(nil), trace...)
	return append(out, item)
}

func scopeMismatchDiagnostic(relPath, objectName string, n *script.Node, key string, need, current TigerScope, trace []string) ctxDiag {
	message := fmt.Sprintf(
		"scope mismatch in %q: %q expects %s but current scope is %s; trace: %s",
		objectName, key, scopeMaskDesc(need), scopeMaskDesc(current), strings.Join(trace, " -> "),
	)
	if isMenAtArmsTypesPath(relPath) {
		message = menAtArmsCanRecruitScopeMessage(objectName, key, need, current) + "; trace: " + strings.Join(trace, " -> ")
	}
	return ctxDiag{
		severity: "warning",
		code:     "scope_mismatch",
		msg:      message,
		line:     n.Line,
		col:      n.Col,
	}
}

func scopeContainerMismatchDiagnostic(relPath, objectName string, n *script.Node, kind, key string, need, current TigerScope, trace []string) ctxDiag {
	return ctxDiag{
		severity: "warning",
		code:     "scope_mismatch",
		msg: fmt.Sprintf(
			"scope mismatch in %q: %s %q expects input %s but current scope is %s; trace: %s",
			objectName, kind, key, scopeMaskDesc(need), scopeMaskDesc(current), strings.Join(trace, " -> "),
		),
		line: n.Line,
		col:  n.Col,
	}
}

func dedupeScopeDiagnostics(in []ctxDiag) []ctxDiag {
	seen := map[string]bool{}
	out := make([]ctxDiag, 0, len(in))
	for _, diag := range in {
		key := fmt.Sprintf("%d:%d:%s:%s", diag.line, diag.col, diag.code, diag.msg)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, diag)
	}
	return out
}

func checkMenAtArmsCanRecruitScope(nodes []*script.Node, relPath string) []ctxDiag {
	if !isMenAtArmsTypesPath(relPath) {
		return nil
	}
	var out []ctxDiag
	for _, obj := range nodes {
		if obj.Kind != "block" || obj.Key == "" {
			continue
		}
		for _, child := range obj.Children {
			if child.Kind != "block" || child.Key != "can_recruit" {
				continue
			}
			out = append(out, checkTriggerBlockScope(child.Children, relPath, obj.Key, ScopeCharacter)...)
		}
	}
	return dedupeScopeDiagnostics(out)
}

func checkTriggerBlockScope(nodes []*script.Node, relPath, objectName string, rootScope TigerScope) []ctxDiag {
	return checkScopeNodes(nodes, relPath, objectName, rootScope)
}

func menAtArmsCanRecruitScopeMessage(objectName, key string, needScope, currentScope TigerScope) string {
	base := fmt.Sprintf("men_at_arms_type %q can_recruit scope mismatch: trigger %q expects %s scope but current scope is %s",
		objectName, key, scopeMaskDesc(needScope), scopeMaskDesc(currentScope))
	if needScope == ScopeCulture {
		return base + "; wrap it in culture = { ... } or use vanilla style valid_for_maa_trigger = { PARAMETER = unlock_maa_xxx }"
	}
	return base + "; move it under an appropriate scope transition or follow a matching vanilla can_recruit example"
}

func isMenAtArmsTypesPath(relPath string) bool {
	p := filepathSlash(strings.ToLower(relPath))
	return strings.Contains(p, "common/men_at_arms_types/") || strings.Contains(p, "common/men_at_arms/")
}
