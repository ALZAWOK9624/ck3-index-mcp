package indexer

import "strings"

//go:generate python ../../tools/extract_on_actions.py --output on_action_data.gen.go

// TigerOnActionValueKind describes a literal value in ck3-tiger's versioned
// on_action table. It is static documentation evidence, not an inferred CK3
// runtime scope.
type TigerOnActionValueKind string

const (
	TigerOnActionValueKindScope   TigerOnActionValueKind = "scope"
	TigerOnActionValueKindNone    TigerOnActionValueKind = "none"
	TigerOnActionValueKindFlag    TigerOnActionValueKind = "flag"
	TigerOnActionValueKindValue   TigerOnActionValueKind = "value"
	TigerOnActionValueKindBool    TigerOnActionValueKind = "bool"
	TigerOnActionValueKindDynamic TigerOnActionValueKind = "dynamic"
)

// These seed types remain unexported so generated ck3-tiger literals cannot
// become an accidental validation API. ResolveTigerOnActionContract projects
// them into an explicitly review-only public result.
type tigerOnActionBindingSeed struct {
	Name       string
	StaticType string
	Kind       TigerOnActionValueKind
	Review     bool
}

type tigerOnActionDirectSeed struct {
	Root   tigerOnActionBindingSeed
	Named  []tigerOnActionBindingSeed
	Lists  []tigerOnActionBindingSeed
	Review bool
}

type tigerOnActionAliasSeed struct {
	Target string
}

// TigerOnActionBinding is one static named/root/list binding from the
// generated Tiger table. StaticType is the literal table token; it must not be
// read as an engine-confirmed effective scope.
type TigerOnActionBinding struct {
	Name       string                 `json:"name"`
	ValueKind  TigerOnActionValueKind `json:"value_kind"`
	StaticType string                 `json:"static_type"`
	Review     bool                   `json:"review,omitempty"`
}

// TigerOnActionContract is a read-only, versioned projection of ck3-tiger's
// structured on_action table. It intentionally has no diagnostic effect.
type TigerOnActionContract struct {
	Key              string                 `json:"key"`
	Definition       string                 `json:"definition"`
	AliasPath        []string               `json:"alias_path,omitempty"`
	SourceVersion    string                 `json:"source_version"`
	Root             TigerOnActionBinding   `json:"root"`
	Named            []TigerOnActionBinding `json:"named,omitempty"`
	Lists            []TigerOnActionBinding `json:"lists,omitempty"`
	Review           bool                   `json:"review,omitempty"`
	RuleSource       string                 `json:"rule_source"`
	Confidence       string                 `json:"confidence"`
	DiagnosticEffect string                 `json:"diagnostic_effect"`
	Guidance         []string               `json:"guidance"`
}

// ResolveTigerOnActionContract returns static Tiger evidence for a known
// on_action. Alias expansion is intentionally visible to callers so a lookup
// never pretends that the alias had an independently documented contract.
func ResolveTigerOnActionContract(key string) (TigerOnActionContract, bool) {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return TigerOnActionContract{}, false
	}
	current := normalized
	path := []string{current}
	seen := map[string]bool{}
	for {
		if seen[current] {
			// The generator rejects cycles. Keep this guard anyway: callers should
			// receive no contract rather than an accidental infinite loop if a
			// generated table is ever damaged.
			return TigerOnActionContract{}, false
		}
		seen[current] = true
		if direct, ok := tigerOnActionDirect[current]; ok {
			contract := TigerOnActionContract{
				Key:              normalized,
				Definition:       current,
				SourceVersion:    tigerOnActionTableVersion,
				Root:             projectTigerOnActionBinding(direct.Root),
				Named:            projectTigerOnActionBindings(direct.Named),
				Lists:            projectTigerOnActionBindings(direct.Lists),
				Review:           direct.Review,
				RuleSource:       "tiger_static",
				Confidence:       "medium",
				DiagnosticEffect: "none",
				Guidance: []string{
					"Tiger table data is static, versioned support evidence; live engine logs take precedence when available.",
					"This contract is read-only and does not add scope diagnostics or alter validation.",
					"Aliases resolve to their documented definition; list bindings are reported separately from named bindings.",
				},
			}
			if len(path) > 1 {
				contract.AliasPath = append([]string(nil), path...)
			}
			return contract, true
		}
		alias, ok := tigerOnActionAliases[current]
		if !ok {
			return TigerOnActionContract{}, false
		}
		current = alias.Target
		path = append(path, current)
	}
}

func projectTigerOnActionBinding(seed tigerOnActionBindingSeed) TigerOnActionBinding {
	return TigerOnActionBinding{
		Name:       seed.Name,
		ValueKind:  seed.Kind,
		StaticType: seed.StaticType,
		Review:     seed.Review,
	}
}

func projectTigerOnActionBindings(seeds []tigerOnActionBindingSeed) []TigerOnActionBinding {
	if len(seeds) == 0 {
		return nil
	}
	bindings := make([]TigerOnActionBinding, 0, len(seeds))
	for _, seed := range seeds {
		bindings = append(bindings, projectTigerOnActionBinding(seed))
	}
	return bindings
}
