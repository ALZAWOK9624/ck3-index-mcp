package indexer

import (
	"sort"
	"strings"
)

// fileScopeType returns the most likely root scope for CK3 script files
// that have a well-defined, unambiguous scope.
// relPath is relative to source root (no leading slash).
func fileScopeType(relPath string) EngineScope {
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
// A configured engine log is authoritative; the generated CK3 1.19 snapshot
// is the offline fallback for rule bundles without engine logs.
func LookupScope(key string) *ScopeLookup {
	kl := strings.ToLower(key)
	var ts, es EngineScope
	var isTrig, isEff bool
	if engineRulesConfigured() {
		ts, isTrig = engineRuleScope(kl, "trigger")
		es, isEff = engineRuleScope(kl, "effect")
	} else {
		ts, isTrig = engineTriggerScopes[kl]
		es, isEff = engineEffectScopes[kl]
	}
	if !isTrig && !isEff {
		return nil
	}
	var mask EngineScope
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

func scopeMaskDesc(m EngineScope) string {
	names := scopeNames(m)
	if len(names) == 0 {
		return "unknown"
	}
	return strings.Join(names, "|")
}

func scopeNames(m EngineScope) []string {
	if m == ScopeAllScopes {
		return []string{"any"}
	}
	names := make([]string, 0, 2)
	for _, entry := range engineScopeNames {
		if m.Intersects(entry.Scope) {
			names = append(names, entry.Name)
		}
	}
	return names
}

// LookupShape returns only current CK3 1.19 documentation facts for a
// trigger/effect key. The engine logs do not expose an exhaustive formal value
// grammar, so this deliberately does not reuse legacy boolean/block/vbv labels.
func LookupShape(key string) *ShapeLookup {
	kl := strings.ToLower(strings.TrimSpace(key))
	if s, ok := engineShapeData[kl]; ok {
		return &s
	}
	return nil
}

// IsDefine returns true if the key is a current CK3 1.19 @define name.
func IsDefine(key string) bool {
	_, ok := engineDefines[key]
	return ok
}

// IsOnAction returns true if the key is a known CK3 on_action name.
func IsOnAction(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	_, ok := engineOnActions[normalized]
	return ok || engineOnActionKnown(normalized)
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
	Source   string   `json:"source,omitempty"`
}

type generatedModifierTemplate struct {
	Pattern      string
	LiteralBytes int
	Info         ModifierInfo
}

var generatedModifierTemplates = buildGeneratedModifierTemplates()

func buildGeneratedModifierTemplates() []generatedModifierTemplate {
	templates := make([]generatedModifierTemplate, 0)
	for pattern, info := range engineModifiers {
		if !strings.Contains(pattern, "$") {
			continue
		}
		literalBytes := len(pattern)
		for rest := pattern; ; {
			start := strings.IndexByte(rest, '$')
			if start < 0 {
				break
			}
			end := strings.IndexByte(rest[start+1:], '$')
			if end < 0 {
				break
			}
			literalBytes -= end + 2
			rest = rest[start+end+2:]
		}
		templates = append(templates, generatedModifierTemplate{
			Pattern:      pattern,
			LiteralBytes: literalBytes,
			Info:         info,
		})
	}
	sort.Slice(templates, func(i, j int) bool {
		if templates[i].LiteralBytes != templates[j].LiteralBytes {
			return templates[i].LiteralBytes > templates[j].LiteralBytes
		}
		return templates[i].Pattern < templates[j].Pattern
	})
	return templates
}

// matchesGeneratedModifierTemplate accepts concrete expansions of formats
// published by modifiers.log, such as $MEN_AT_ARMS_TYPE$_damage_add.
// Placeholders must consume at least one byte and every literal segment must
// still match exactly.
func matchesGeneratedModifierTemplate(pattern, key string) bool {
	patternRest := pattern
	keyRest := key
	for {
		start := strings.IndexByte(patternRest, '$')
		if start < 0 {
			return keyRest == patternRest
		}
		if !strings.HasPrefix(keyRest, patternRest[:start]) {
			return false
		}
		keyRest = keyRest[start:]
		patternRest = patternRest[start+1:]
		end := strings.IndexByte(patternRest, '$')
		if end < 0 {
			return false
		}
		patternRest = patternRest[end+1:]
		next := strings.IndexByte(patternRest, '$')
		literal := patternRest
		if next >= 0 {
			literal = patternRest[:next]
		}
		if literal == "" {
			return false
		}
		valueEnd := strings.Index(keyRest, literal)
		if valueEnd <= 0 {
			return false
		}
		keyRest = keyRest[valueEnd:]
	}
}

// LookupModifier checks if a modifier tag is valid and returns its use areas.
// An empty UseAreas means that current vanilla format sources prove the name,
// but modifiers.log did not publish an area contract for it.
func LookupModifier(key string) ModifierLookup {
	key = strings.TrimSpace(key)
	if info, ok := engineModifier(key); ok {
		return ModifierLookup{Found: true, UseAreas: info.UseAreas, Source: "engine_log"}
	}
	if info, ok := engineModifiers[key]; ok {
		return ModifierLookup{Found: true, UseAreas: info.UseAreas, Source: info.Source}
	}
	for _, template := range generatedModifierTemplates {
		if matchesGeneratedModifierTemplate(template.Pattern, key) {
			return ModifierLookup{
				Found:    true,
				UseAreas: template.Info.UseAreas,
				Source:   template.Info.Source + "_template",
			}
		}
	}
	return ModifierLookup{Found: false}
}

// IsSound returns true if the key is a current CK3 1.19 sound event name.
func IsSound(key string) bool {
	_, ok := engineSounds[key]
	return ok
}

// IteratorLookup describes a CK3 iterator from the generated CK3 1.19 table.
type IteratorLookup struct {
	Key      string `json:"key"`
	ScopeIn  string `json:"scope_in"`
	ScopeOut string `json:"scope_out"`
}

// LookupIterator checks whether the given key is a known CK3 iterator
// (any_child, random_vassal, every_tributary, etc.) from CK3 1.19 data.
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
