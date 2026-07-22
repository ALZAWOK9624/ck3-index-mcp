package mcpserver

import (
	"strings"

	"ck3-index/internal/indexer"
)

func lookupScopeTool(key string) (any, error) {
	sl := indexer.LookupScope(key)
	if sl == nil {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No trigger/effect scope rule was found; try lookup_iterator and lookup_example before using this key."},
		}, nil
	}
	return map[string]any{
		"found":           true,
		"key":             sl.Key,
		"is_trigger":      sl.IsTrigger,
		"is_effect":       sl.IsEffect,
		"scope_mask":      sl.ScopeMask,
		"scope_mask_high": sl.ScopeMaskHigh,
		"scope_names":     sl.ScopeNames,
		"scope_desc":      sl.ScopeDesc,
		"guidance":        []string{"scope_names is authoritative; use this key only in a compatible root/current scope and confirm nested syntax with lookup_shape and lookup_example."},
	}, nil
}

func lookupShapeTool(key string) (any, error) {
	sd := indexer.LookupShape(key)
	if sd == nil {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No current engine documentation was found; confirm syntax from query_examples or game script before generating this key."},
		}, nil
	}
	return map[string]any{
		"found":         true,
		"key":           sd.Key,
		"evidence_kind": sd.EvidenceKind,
		"documentation": sd.Documentation,
		"guidance":      []string{"These are direct CK3 1.19 documentation excerpts. They do not assert an exhaustive value grammar; use scope evidence and a concrete example before generating complex syntax."},
	}, nil
}

func lookupDefineTool(key string) (any, error) {
	found := indexer.IsDefine(key)
	return map[string]any{"found": found, "key": key, "guidance": []string{"Use found=false as a warning only; mods can define custom @names outside engine defines."}}, nil
}

func lookupOnActionTool(key string) (any, error) {
	found := indexer.IsOnAction(key)
	return map[string]any{"found": found, "key": key, "guidance": []string{"For on_action edits, query_object and find_refs should still be used to inspect local overrides and consumers."}}, nil
}

func lookupExampleTool(key string) (any, error) {
	ex := indexer.LookupExample(key)
	if ex == nil {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No official trigger/effect example was found; use query_examples against vanilla scripts before generating."},
		}, nil
	}
	return map[string]any{
		"found":    true,
		"key":      key,
		"desc":     ex.Desc,
		"example":  ex.Example,
		"guidance": []string{"Prefer this syntax when it is non-empty; if example is empty, use desc plus query_examples for concrete script."},
	}, nil
}

func lookupModifierTool(key string) (any, error) {
	ml := indexer.LookupModifier(key)
	if !ml.Found {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"Do not use this as a static modifier unless query_object or vanilla examples confirm it exists."},
		}, nil
	}
	return map[string]any{
		"found":     true,
		"key":       key,
		"use_areas": ml.UseAreas,
		"source":    ml.Source,
		"guidance": func() []string {
			if len(ml.UseAreas) == 0 {
				return []string{"Current vanilla format sources confirm this modifier name, but modifiers.log did not publish a use-area contract; inspect vanilla context before choosing a modifier block."}
			}
			return []string{"Use only in modifier blocks that apply to one of these scope/use areas."}
		}(),
	}, nil
}

func lookupIteratorTool(key string) (any, error) {
	il := indexer.LookupIterator(key)
	if il == nil {
		if ex := indexer.LookupExample(key); ex != nil && strings.Contains(strings.ToLower(ex.Example+" "+ex.Desc), "iterate") {
			return map[string]any{
				"found":     true,
				"key":       key,
				"source":    "example_log",
				"scope_in":  "",
				"scope_out": "",
				"example":   ex.Example,
				"desc":      ex.Desc,
				"guidance":  []string{"Iterator exists in official examples, but scope input/output was not in the compact rule table; verify with vanilla examples before complex nesting."},
			}, nil
		}
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No iterator rule was found; try lookup_example or query_examples before generating this block."},
		}, nil
	}
	return map[string]any{
		"found":     true,
		"key":       il.Key,
		"scope_in":  il.ScopeIn,
		"scope_out": il.ScopeOut,
		"guidance":  []string{"Iterator block syntax is usually key = { limit = { <triggers> } <effects> }; scope_out is the current scope inside the block."},
	}, nil
}
