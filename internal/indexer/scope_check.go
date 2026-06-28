package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

// fileScopeType returns the most likely root scope for CK3 script files
// that have a well-defined, unambiguous scope.
// relPath is relative to source root (no leading slash).
func fileScopeType(relPath string) TigerScope {
	p := relPath
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Only traits and landed_titles have a root scope that is deterministic
	// enough for file-level checking without full scope-stack tracking.
	// Other file types (history/characters, common/religion, common/culture,
	// common/artifacts, etc.) contain frequent property assignments that
	// alias trigger/effect names and would produce excessive false positives.
	switch {
	case strings.HasPrefix(p, "/common/traits/"):
		return ScopeCharacter
	case strings.HasPrefix(p, "/common/landed_titles/"):
		return ScopeTitle
	case strings.HasPrefix(p, "/history/titles/"):
		return ScopeTitle
	}
	return ScopeAllScopes
}

// checkTriggerEffectScope walks the AST of a script file and checks each
// trigger/effect-like key against ck3-tiger scope data. Returns scope_mismatch
// diagnostics for keys requiring a scope incompatible with the file's root scope.
//
// Limitations (to be addressed in future iterations):
//   - Does not track scope changes inside iterators (any_child, every_province,
//     etc.), so it may produce false positives for correctly scoped triggers
//     inside iterator blocks.  Currently this is mitigated by narrowing fileScopeType
//     to only the 11 file types whose root scope is unambiguous.
//   - fileScopeType skips events, decisions, modifiers, and other dynamic files
//     where the root scope is not determined by file location alone.
func checkTriggerEffectScope(nodes []*script.Node, relPath string) []ctxDiag {
	fileScope := fileScopeType(relPath)
	if fileScope == ScopeAllScopes || fileScope == 0 {
		return nil
	}

	var diags []ctxDiag
	mismatchCount := 0
	var walk func(ns []*script.Node)
	walk = func(ns []*script.Node) {
		for _, n := range ns {
			k := n.Key
			if k == "" {
				walk(n.Children)
				continue
			}
			kl := strings.ToLower(k)

			needScope, ok := tigerTriggerScopes[kl]
			if !ok {
				needScope, ok = tigerEffectScopes[kl]
			}
			if ok && needScope != ScopeAllScopes && needScope != ScopeValue && (fileScope&needScope) == 0 {
				kind := "trigger/effect"
				if _, isTrigger := tigerTriggerScopes[kl]; isTrigger {
					kind = "trigger"
					_, _ = isTrigger, kind
				}
				if _, isEffect := tigerEffectScopes[kl]; isEffect {
					kind = "effect"
					_, _ = isEffect, kind
				}
				diags = append(diags, ctxDiag{
					severity: "warning",
					code:     "scope_mismatch",
					msg: fmt.Sprintf("scope mismatch: %s %q expects a different scope type (need=0x%x file=0x%x in %s)",
						kind, k, needScope, fileScope, relPath),
					line: n.Line,
					col:  n.Col,
				})
				mismatchCount++
			}
			walk(n.Children)
		}
	}
	walk(nodes)

	if mismatchCount > 0 {
		// Condense to one diagnostic per file for SN ratio.
		diags = diags[:1]
		diags[0].msg = fmt.Sprintf("scope mismatch: %d trigger(s)/effect(s) expect a different scope in %s",
			mismatchCount, relPath)
	}
	return diags
}

// ScopeLookup describes scope requirements for a trigger or effect key.
type ScopeLookup struct {
	Key       string `json:"key"`
	IsTrigger bool   `json:"is_trigger"`
	IsEffect  bool   `json:"is_effect"`
	ScopeMask uint64 `json:"scope_mask"`
	ScopeDesc string `json:"scope_desc"`
}

// LookupScope returns the scope requirement for a trigger or effect key.
// Returns nil if the key is not found in tiger data.
func LookupScope(key string) *ScopeLookup {
	kl := strings.ToLower(key)
	ts, isTrig := tigerTriggerScopes[kl]
	es, isEff := tigerEffectScopes[kl]
	if !isTrig && !isEff {
		return nil
	}
	mask := uint64(0)
	if isTrig {
		mask |= uint64(ts)
	}
	if isEff {
		mask |= uint64(es)
	}
	return &ScopeLookup{
		Key:       key,
		IsTrigger: isTrig,
		IsEffect:  isEff,
		ScopeMask: mask,
		ScopeDesc: scopeMaskDesc(TigerScope(mask)),
	}
}

func scopeMaskDesc(m TigerScope) string {
	switch m {
	case ScopeAllScopes:
		return "any"
	case ScopeValue:
		return "value"
	case ScopeCharacter:
		return "character"
	case ScopeTitle:
		return "title"
	case ScopeProvince:
		return "province"
	case ScopeFaith:
		return "faith"
	case ScopeCulture:
		return "culture"
	case ScopeDynasty:
		return "dynasty"
	case ScopeArtifact:
		return "artifact"
	case ScopeWar:
		return "war"
	case ScopeReligion:
		return "religion"
	case ScopeAccolade:
		return "accolade"
	case ScopeActivity:
		return "activity"
	case ScopeArmy:
		return "army"
	case ScopeScheme:
		return "scheme"
	case ScopeSecret:
		return "secret"
	case ScopeStruggle:
		return "struggle"
	case ScopeCombat:
		return "combat"
	case ScopeDynastyHouse:
		return "dynasty_house"
	case ScopeLegend:
		return "legend"
	case ScopeHolyOrder:
		return "holy_order"
	case ScopeDomicile:
		return "domicile"
	case ScopeFaction:
		return "faction"
	default:
		return fmt.Sprintf("scope 0x%x", uint64(m))
	}
}

// LookupShape returns the value shape for a trigger or effect key (from
// ck3-tiger data). Nil if the key is not found, or if it is in the
// untrusted set (not confirmed in game scripts).
func LookupShape(key string) *ShapeDesc {
	kl := strings.ToLower(key)
	if s, ok := tigerShapeData[kl]; ok {
		return &s
	}
	return nil
}

// IsDefine returns true if the key is a known CK3 @define name.
func IsDefine(key string) bool {
	_, ok := tigerDefines[key]
	return ok
}

// IsOnAction returns true if the key is a known CK3 on_action name.
func IsOnAction(key string) bool {
	_, ok := tigerOnActions[key]
	return ok
}

// LookupExample returns description and usage example for a trigger/effect key
// from CK3's official effects.log / triggers.log dumps.
func LookupExample(key string) *struct{ Desc, Example string } {
	kl := strings.ToLower(key)
	if s, ok := effectExamples[kl]; ok {
		return &s
	}
	if s, ok := triggerExamples[kl]; ok {
		return &s
	}
	return nil
}

// ModifierLookup describes a static modifier tag.
type ModifierLookup struct {
	Found    bool     `json:"found"`
	UseAreas []string `json:"use_areas"`
}

// LookupModifier checks if a modifier tag is valid and returns its use areas.
func LookupModifier(key string) ModifierLookup {
	if kinds, ok := tigerModifKinds[key]; ok {
		return ModifierLookup{Found: true, UseAreas: kinds}
	}
	if info, ok := tigerModifiers[key]; ok {
		return ModifierLookup{Found: true, UseAreas: info.UseAreas}
	}
	return ModifierLookup{Found: false}
}

// IsSound returns true if the key is a known CK3 sound event name.
func IsSound(key string) bool {
	_, ok := tigerSounds[key]
	return ok
}

// IsLocMacro returns true if the key is a known CK3 localization macro.
func IsLocMacro(key string) bool {
	_, ok := tigerLocMacros[key]
	return ok
}

// IteratorLookup describes a CK3 iterator (scope) from ck3-tiger data.
type IteratorLookup struct {
	Key      string `json:"key"`
	ScopeIn  string `json:"scope_in"`
	ScopeOut string `json:"scope_out"`
}

// LookupIterator checks whether the given key is a known CK3 iterator
// (any_child, random_vassal, every_tributary, etc.) from ck3-tiger data.
// Returns nil if the key is not a known iterator.
func LookupIterator(key string) *IteratorLookup {
	kl := strings.ToLower(key)
	scopeIn, okIn := iteratorScopeIn[kl]
	scopeOut, okOut := iteratorScopeOut[kl]
	if !okIn && !okOut {
		return nil
	}
	descIn := ""
	descOut := ""
	if okIn {
		descIn = scopeMaskDesc(scopeIn)
	}
	if okOut {
		descOut = scopeMaskDesc(scopeOut)
	}
	return &IteratorLookup{
		Key:      key,
		ScopeIn:  descIn,
		ScopeOut: descOut,
	}
}

// IsInGameScripts returns whether this key was found in the full CK3
// game installation, Godherja, or game source via grep. Engine-level
// keys (any_tributary, etc.) return false — they live in the binary.
func IsInGameScripts(key string) bool {
	kl := strings.ToLower(key)
	return !tigerUntrustedKeys[kl]
}
