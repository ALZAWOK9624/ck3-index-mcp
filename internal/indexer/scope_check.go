package indexer

import (
	"strings"
)

// fileScopeType returns the most likely root scope for CK3 script files
// that have a well-defined, unambiguous scope.
// relPath is relative to source root (no leading slash).
func fileScopeType(relPath string) TigerScope {
	p := relPath
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Only return a root when the CK3 object contract is stable for the whole
	// file type. Event roots are object-specific and handled by scope_tracker.
	switch {
	case strings.HasPrefix(p, "/common/landed_titles/"):
		return ScopeTitle
	case strings.HasPrefix(p, "/history/titles/"):
		return ScopeTitle
	case strings.HasPrefix(p, "/common/decisions/"):
		return ScopeCharacter
	case strings.HasPrefix(p, "/common/men_at_arms_types/"), strings.HasPrefix(p, "/common/men_at_arms/"):
		return ScopeCharacter
	}
	return ScopeAllScopes
}

// ScopeLookup describes scope requirements for a trigger or effect key.
type ScopeLookup struct {
	Key           string   `json:"key"`
	IsTrigger     bool     `json:"is_trigger"`
	IsEffect      bool     `json:"is_effect"`
	ScopeMask     uint64   `json:"scope_mask"`
	ScopeMaskHigh uint64   `json:"scope_mask_high,omitempty"`
	ScopeNames    []string `json:"scope_names"`
	ScopeDesc     string   `json:"scope_desc"`
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
	var mask TigerScope
	if isTrig {
		mask = scopeUnion(mask, ts)
	}
	if isEff {
		mask = scopeUnion(mask, es)
	}
	return &ScopeLookup{
		Key:           key,
		IsTrigger:     isTrig,
		IsEffect:      isEff,
		ScopeMask:     mask.Low,
		ScopeMaskHigh: mask.High,
		ScopeNames:    scopeNames(mask),
		ScopeDesc:     scopeMaskDesc(mask),
	}
}

func scopeMaskDesc(m TigerScope) string {
	names := scopeNames(m)
	if len(names) == 0 {
		return "unknown"
	}
	return strings.Join(names, "|")
}

func scopeNames(m TigerScope) []string {
	if m == ScopeAllScopes {
		return []string{"any"}
	}
	names := make([]string, 0, 2)
	for _, entry := range tigerScopeNames {
		if m.Intersects(entry.Scope) {
			names = append(names, entry.Name)
		}
	}
	return names
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
