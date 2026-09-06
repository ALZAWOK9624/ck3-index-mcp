package indexer

import (
	"sort"
	"strings"
)

type DSLRuleDelta struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Change   string `json:"change"`
	Compiled string `json:"compiled,omitempty"`
	Live     string `json:"live,omitempty"`
}

func (c *StandaloneChecker) compiledRuleDrift() []DSLRuleDelta {
	result := []DSLRuleDelta{}
	if c.rules == nil {
		return result
	}
	for kind, compiled := range map[string]map[string]EngineScope{"effect": engineEffectScopes, "trigger": engineTriggerScopes, "target": engineScopeTransitionsIn} {
		for key, scope := range compiled {
			inputs, exists := c.rules.rules[key][kind]
			if !exists {
				result = append(result, DSLRuleDelta{Key: key, Kind: kind, Change: "compiled_only", Compiled: scopeMaskDesc(scope)})
				continue
			}
			live, known := engineScopesToMask(inputs)
			if known && live != scope {
				result = append(result, DSLRuleDelta{Key: key, Kind: kind, Change: "input_scopes_differ", Compiled: scopeMaskDesc(scope), Live: scopeMaskDesc(live)})
			}
		}
		for key, kinds := range c.rules.rules {
			if inputs, exists := kinds[kind]; exists {
				if _, known := compiled[key]; !known {
					result = append(result, DSLRuleDelta{Key: key, Kind: kind, Change: "live_only", Live: strings.Join(inputs, ", ")})
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Key < result[j].Key
	})
	return result
}

type StandaloneRule struct {
	Key                  string               `json:"key"`
	Known                bool                 `json:"known"`
	Source               string               `json:"source"`
	Fingerprint          string               `json:"fingerprint,omitempty"`
	InputScopes          map[string]string    `json:"input_scopes"`
	DocumentationVersion string               `json:"documentation_version"`
	Documentation        []ShapeDocumentation `json:"documentation,omitempty"`
	ExhaustiveShape      bool                 `json:"exhaustive_shape"`
}

// Rule exposes documented command facts, not guessed value-shape allowlists.
// Its optional prose always identifies the compiled documentation version,
// independently from a supplied live scope registry's fingerprint.
func (c *StandaloneChecker) Rule(key string) StandaloneRule {
	key = strings.ToLower(strings.TrimSpace(key))
	r := StandaloneRule{Key: key, Source: "compiled_ck3_1.19", InputScopes: map[string]string{}, DocumentationVersion: engineShapeTableVersion}
	if c.rules != nil {
		r.Source = "engine_logs"
		r.Fingerprint = c.fingerprint
		for kind, inputs := range c.rules.rules[key] {
			r.InputScopes[kind] = strings.Join(inputs, ", ")
			r.Known = true
		}
	} else {
		if scope, ok := engineEffectScopes[key]; ok {
			r.InputScopes["effect"] = scopeMaskDesc(scope)
			r.Known = true
		}
		if scope, ok := engineTriggerScopes[key]; ok {
			r.InputScopes["trigger"] = scopeMaskDesc(scope)
			r.Known = true
		}
		if scope, ok := engineScopeTransitionsIn[key]; ok {
			r.InputScopes["target"] = scopeMaskDesc(scope)
			r.Known = true
		}
	}
	if entry, ok := engineShapeData[key]; ok {
		r.Documentation = append([]ShapeDocumentation(nil), entry.Documentation...)
	}
	return r
}
