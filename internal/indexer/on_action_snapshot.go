package indexer

import "strings"

// Regenerate on_action_data.gen.go with tools/extract_engine_on_actions.py
// against an explicit CK3 engine-log bundle and vanilla game source. It is not
// a go:generate target because those two versioned inputs are workspace data.

// OnActionSnapshotValueKind describes a literal value in the generated CK3
// 1.19 on_action snapshot.
type OnActionSnapshotValueKind string

const (
	OnActionSnapshotValueKindScope   OnActionSnapshotValueKind = "scope"
	OnActionSnapshotValueKindNone    OnActionSnapshotValueKind = "none"
	OnActionSnapshotValueKindFlag    OnActionSnapshotValueKind = "flag"
	OnActionSnapshotValueKindValue   OnActionSnapshotValueKind = "value"
	OnActionSnapshotValueKindBool    OnActionSnapshotValueKind = "bool"
	OnActionSnapshotValueKindDynamic OnActionSnapshotValueKind = "dynamic"
)

// These seed types remain unexported so generated engine literals cannot
// become an accidental validation API. ResolveOnActionSnapshotContract projects
// them into an explicitly review-only public result.
type onActionSnapshotBindingSeed struct {
	Name       string
	StaticType string
	Kind       OnActionSnapshotValueKind
	Review     bool
}

type onActionSnapshotDirectSeed struct {
	Root   onActionSnapshotBindingSeed
	Named  []onActionSnapshotBindingSeed
	Lists  []onActionSnapshotBindingSeed
	Review bool
}

type onActionSnapshotAliasSeed struct {
	Target string
}

// OnActionSnapshotBinding is one named/root/list binding from the generated CK3
// 1.19 snapshot. StaticType is a literal token, not an inferred runtime scope.
type OnActionSnapshotBinding struct {
	Name       string                    `json:"name"`
	ValueKind  OnActionSnapshotValueKind `json:"value_kind"`
	StaticType string                    `json:"static_type"`
	Review     bool                      `json:"review,omitempty"`
}

// OnActionSnapshotContract is a read-only projection of the generated CK3 1.19
// on_action evidence. It intentionally has no diagnostic effect.
type OnActionSnapshotContract struct {
	Key              string                    `json:"key"`
	Definition       string                    `json:"definition"`
	AliasPath        []string                  `json:"alias_path,omitempty"`
	SourceVersion    string                    `json:"source_version"`
	Root             OnActionSnapshotBinding   `json:"root"`
	Named            []OnActionSnapshotBinding `json:"named,omitempty"`
	Lists            []OnActionSnapshotBinding `json:"lists,omitempty"`
	Review           bool                      `json:"review,omitempty"`
	RuleSource       string                    `json:"rule_source"`
	Confidence       string                    `json:"confidence"`
	DiagnosticEffect string                    `json:"diagnostic_effect"`
	Guidance         []string                  `json:"guidance"`
}

// ResolveOnActionSnapshotContract returns generated CK3 1.19 evidence for one
// known on_action.
func ResolveOnActionSnapshotContract(key string) (OnActionSnapshotContract, bool) {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return OnActionSnapshotContract{}, false
	}
	current := normalized
	path := []string{current}
	seen := map[string]bool{}
	for {
		if seen[current] {
			// The generator rejects cycles. Keep this guard anyway: callers should
			// receive no contract rather than an accidental infinite loop if a
			// generated table is ever damaged.
			return OnActionSnapshotContract{}, false
		}
		seen[current] = true
		if direct, ok := engineOnActionDirect[current]; ok {
			contract := OnActionSnapshotContract{
				Key:              normalized,
				Definition:       current,
				SourceVersion:    engineOnActionSnapshotVersion,
				Root:             projectOnActionSnapshotBinding(direct.Root),
				Named:            projectOnActionSnapshotBindings(direct.Named),
				Lists:            projectOnActionSnapshotBindings(direct.Lists),
				Review:           direct.Review,
				RuleSource:       "engine_1_19_snapshot",
				Confidence:       "high",
				DiagnosticEffect: "none",
				Guidance: []string{
					"This snapshot is generated from CK3 1.19 engine logs and current vanilla comments; live configured logs take precedence when available.",
					"Only explicitly typed vanilla comment bindings are included; untyped legacy bindings were deliberately discarded.",
					"This contract is read-only and does not add scope diagnostics or alter validation.",
				},
			}
			if len(path) > 1 {
				contract.AliasPath = append([]string(nil), path...)
			}
			return contract, true
		}
		alias, ok := engineOnActionAliases[current]
		if !ok {
			return OnActionSnapshotContract{}, false
		}
		current = alias.Target
		path = append(path, current)
	}
}

func projectOnActionSnapshotBinding(seed onActionSnapshotBindingSeed) OnActionSnapshotBinding {
	return OnActionSnapshotBinding{
		Name:       seed.Name,
		ValueKind:  seed.Kind,
		StaticType: seed.StaticType,
		Review:     seed.Review,
	}
}

func projectOnActionSnapshotBindings(seeds []onActionSnapshotBindingSeed) []OnActionSnapshotBinding {
	if len(seeds) == 0 {
		return nil
	}
	bindings := make([]OnActionSnapshotBinding, 0, len(seeds))
	for _, seed := range seeds {
		bindings = append(bindings, projectOnActionSnapshotBinding(seed))
	}
	return bindings
}
