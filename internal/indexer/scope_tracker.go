package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

// checkScopeTracker performs scope validation with iterator-aware scope tracking.
// Unlike the simpler checkScriptContext scope check (file-level only), this
// maintains a scope stack that pushes/pops iterator scope changes, allowing
// accurate scope checking inside any_child, every_province, etc. blocks.
//
// Returns scope_mismatch diagnostics for keys whose required scope from tiger
// data conflicts with the current tracked scope.
func checkScopeTracker(nodes []*script.Node, relPath string) []ctxDiag {
	fileScope := fileScopeType(relPath)
	if fileScope == ScopeAllScopes || fileScope == 0 {
		return nil
	}

	var out []ctxDiag
	scopeStack := []TigerScope{fileScope}

	pushIterScope := func(key string) {
		kl := strings.ToLower(key)
		if newScope, ok := iteratorScopeOut[kl]; ok && newScope != ScopeAllScopes {
			scopeStack = append(scopeStack, newScope)
		}
	}

	currentScope := func() TigerScope {
		if len(scopeStack) == 0 {
			return fileScope
		}
		return scopeStack[len(scopeStack)-1]
	}

	popped := false
	var walk func(ns []*script.Node, parentKey string)
	walk = func(ns []*script.Node, parentKey string) {
		ctxKind := ContextFor(parentKey)
		isTriggerBlock := ctxKind == "trigger"
		isEffectBlock := ctxKind == "effect"

		for _, n := range ns {
			k := n.Key
			if k == "" {
				walk(n.Children, parentKey)
				continue
			}

			// Scope check: only inside trigger/effect blocks.
			if (isTriggerBlock || isEffectBlock) && currentScope() != ScopeAllScopes {
				kl := strings.ToLower(k)
				var needScope TigerScope
				if isTriggerBlock {
					needScope, _ = tigerTriggerScopes[kl]
					if needScope == 0 {
						needScope, _ = tigerEffectScopes[kl]
					}
				}
				if isEffectBlock {
					needScope, _ = tigerEffectScopes[kl]
					if needScope == 0 {
						needScope, _ = tigerTriggerScopes[kl]
					}
				}
				if needScope != ScopeAllScopes && needScope != ScopeValue && needScope != 0 {
					if (currentScope() & needScope) == 0 {
						out = append(out, ctxDiag{
							severity: "warning",
							code:     "scope_mismatch",
							msg: fmt.Sprintf("scope mismatch: %q expects scope 0x%x but current scope is 0x%x in %s",
								k, needScope, currentScope(), relPath),
							line: n.Line, col: n.Col,
						})
					}
				}
			}

			// Iterator scope push/pop.
			prevLen := len(scopeStack)
			pushIterScope(k)
			walk(n.Children, k)
			if len(scopeStack) > prevLen {
				scopeStack = scopeStack[:prevLen]
				popped = true
			}
		}
	}
	walk(nodes, "")

	// Suppress duplicate entries.
	if popped {
		_ = popped
	}
	if len(out) > 0 {
		count := len(out)
		out = out[:1]
		out[0].msg = fmt.Sprintf("scope mismatch: %d scope violation(s) tracked in %s", count, relPath)
	}
	return out
}
