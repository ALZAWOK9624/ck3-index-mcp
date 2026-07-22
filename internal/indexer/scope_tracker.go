package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

type scopeWalkState struct {
	root     EngineScope
	current  EngineScope
	previous EngineScope
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

func checkScopeNodes(nodes []*script.Node, relPath, objectName string, root EngineScope) []ctxDiag {
	namedScopes := map[string]EngineScope{}
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
						d.msg += "; generated 1.19 snapshot rule was not confirmed by current engine logs"
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
				} else if inScope, ok := engineRuleScope(key, "target"); state.context != "" && ok && isConcreteScope(inScope) && !state.current.IsZero() && !state.current.Intersects(inScope) {
					d := scopeContainerMismatchDiagnostic(relPath, objectName, n, "engine target", key, inScope, state.current, state.trace)
					d.code = "scope_uncertain"
					d.severity = "info"
					out = append(out, d)
				} else if inScope, ok := engineScopeTransitionsIn[key]; state.context != "" && ok && isConcreteScope(inScope) && !state.current.IsZero() && !state.current.Intersects(inScope) {
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

func requiredScopeForContext(key, contextKind string) (EngineScope, bool) {
	if contextKind == "trigger" {
		if scope, ok := engineTriggerScopes[key]; ok {
			return scope, true
		}
	} else if scope, ok := engineEffectScopes[key]; ok {
		return scope, true
	}
	// The engine log is authoritative for membership and documented input
	// scopes, but it does not describe iterator/context transitions. Use it to
	// cover newly introduced keys only after the richer static table misses.
	return engineRuleScope(key, contextKind)
}

func resolveChildScope(key string, state scopeWalkState, named map[string]EngineScope, relPath string) (EngineScope, string, bool) {
	if strings.Contains(key, ".") {
		chainState := state
		for _, segment := range strings.Split(key, ".") {
			target, _, ok := resolveChildScope(segment, chainState, named, relPath)
			if !ok {
				return EngineScope{}, "chain " + key, true
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
	if strings.HasPrefix(strings.ToLower(key), "flag:") {
		// flag:<name> is a literal used by constructs such as switch, not a
		// scope traversal. The engine log lists it as a value type, but using
		// that as a child scope would turn the enclosing script into value scope.
		return EngineScope{}, "", false
	}
	if scope, source, ok := typedTargetScope(key, state.current); ok {
		return scope, source, true
	}
	if scope, ok := explicitTypedScope(key); ok {
		return scope, key, true
	}
	if strings.Contains(key, ":") {
		return EngineScope{}, key, true
	}
	if scope, ok := iteratorScopeOut[key]; ok {
		return scope, "iterator " + key, true
	}
	if scope, ok := engineTargetOutputScope(key); ok {
		return scope, "engine target " + key, true
	}
	if scope, ok := engineScopeTransitionsOut[key]; ok {
		return scope, "transition " + key, true
	}
	if looksLikeIterator(key) {
		return EngineScope{}, "unknown iterator " + key, true
	}
	return EngineScope{}, "", false
}

// typedTargetScope handles targets whose names also happen to be scope type
// names. For example, court_position:<position_key> selects the character
// employed in that position; it is not a court_position scope. Prefer a
// documented target transition whenever the current scope satisfies its input
// contract, then fall back to an explicit typed scope such as title:<key>.
func typedTargetScope(key string, current EngineScope) (EngineScope, string, bool) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return EngineScope{}, "", false
	}
	target := strings.ToLower(parts[0])
	if scope, ok := engineScopeTransitionsOut[target]; ok && targetInputAllows(engineScopeTransitionsIn, target, current) {
		return scope, "transition " + target, true
	}
	if scope, ok := engineTargetOutputScope(target); ok && targetInputAllowsEngine(target, current) {
		return scope, "engine target " + target, true
	}
	return EngineScope{}, "", false
}

func targetInputAllows(inputs map[string]EngineScope, target string, current EngineScope) bool {
	input, documented := inputs[target]
	return !documented || current.IsZero() || !isConcreteScope(input) || current.Intersects(input)
}

func targetInputAllowsEngine(target string, current EngineScope) bool {
	input, documented := engineRuleScope(target, "target")
	return !documented || current.IsZero() || !isConcreteScope(input) || current.Intersects(input)
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

func isKnownScopeArgumentCollision(key string, current EngineScope, trace []string) bool {
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

func explicitTypedScope(key string) (EngineScope, bool) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return EngineScope{}, false
	}
	scope, ok := engineScopesByName[parts[0]]
	return scope, ok
}

func eventRootScope(obj *script.Node) EngineScope {
	for _, child := range obj.Children {
		if strings.EqualFold(child.Key, "scope") && child.Value != "" {
			if scope, ok := engineScopesByName[strings.ToLower(child.Value)]; ok {
				return scope
			}
			return EngineScope{}
		}
	}
	return ScopeCharacter
}

func isEventPath(relPath string) bool {
	p := "/" + strings.TrimPrefix(filepathSlash(strings.ToLower(relPath)), "/")
	return strings.HasPrefix(p, "/events/")
}

func isConcreteScope(scope EngineScope) bool {
	return !scope.IsZero() && scope != ScopeAllScopes && scope != ScopeValue
}

func appendScopeTrace(trace []string, item string) []string {
	out := append([]string(nil), trace...)
	return append(out, item)
}

func scopeMismatchDiagnostic(relPath, objectName string, n *script.Node, key string, need, current EngineScope, trace []string) ctxDiag {
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

func scopeContainerMismatchDiagnostic(relPath, objectName string, n *script.Node, kind, key string, need, current EngineScope, trace []string) ctxDiag {
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

func checkTriggerBlockScope(nodes []*script.Node, relPath, objectName string, rootScope EngineScope) []ctxDiag {
	return checkScopeNodes(nodes, relPath, objectName, rootScope)
}

func menAtArmsCanRecruitScopeMessage(objectName, key string, needScope, currentScope EngineScope) string {
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
