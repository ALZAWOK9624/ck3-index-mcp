package indexer

import (
	"path"
	"strings"
)

// How CK3 combines two files that describe the same thing depends entirely on
// which folder they sit in, and the four behaviours are not interchangeable.
// Replacing a file under common/traits/ drops every trait the replaced file
// defined; doing the same under common/on_action/ also silently discards the
// appended entries that other files contributed to the same on_action. The
// index knows which file wins a path collision, so pairing that with the
// folder's policy is what turns "yours wins" into "and here is what it cost".
type MergePolicy string

const (
	// MergePolicyOverride: the winning file is loaded and the losing one is
	// not read at all. Everything the loser defined is gone unless the winner
	// redefines it. This is the behaviour of nearly all of common/ and events/.
	MergePolicyOverride MergePolicy = "override"
	// MergePolicyContainerMerge: list containers accumulate across files while
	// single-slot blocks still take the last writer. common/on_action/ only.
	MergePolicyContainerMerge MergePolicy = "container_merge"
	// MergePolicyPerKeyOverride: files combine key by key, so a key the winner
	// omits keeps whatever another file supplied. defines, history and
	// localization work this way.
	MergePolicyPerKeyOverride MergePolicy = "per_key_override"
	// MergePolicyFirstInWins: the first definition read is kept and later ones
	// are discarded, which is why gui/ overrides use a leading 00_ rather than
	// the zzz_ that works everywhere else.
	MergePolicyFirstInWins MergePolicy = "first_in_wins"
)

// onActionMergeContainers are the on_action fields that accumulate across
// files. Everything else in an on_action is a single slot whose last writer
// wins, which is why appending behaviour means adding a list entry that points
// at a separate custom on_action rather than editing the effect block in place.
var onActionMergeContainers = map[string]bool{
	"events":                 true,
	"on_actions":             true,
	"random_events":          true,
	"random_on_action":       true,
	"random_on_actions":      true,
	"first_valid":            true,
	"first_valid_on_action":  true,
	"first_valid_on_actions": true,
}

// onActionSingleSlotFields are the on_action fields that do not accumulate:
// the last file to declare one wins outright and every earlier declaration is
// discarded. Declaring any of them on a vanilla on_action silently replaces
// what vanilla put there, which is why appending behaviour goes through a
// merging container in onActionMergeContainers instead.
var onActionSingleSlotFields = map[string]bool{
	"trigger":           true,
	"effect":            true,
	"weight_multiplier": true,
	"fallback":          true,
}

// MergePolicyForPath reports how CK3 combines two files at this source-relative
// path. Unrecognized paths report MergePolicyOverride, which is both the common
// case and the conservative answer: it claims content is lost when it might
// have merged, rather than promising a merge that never happens.
func MergePolicyForPath(rel string) MergePolicy {
	p := strings.Trim(strings.ToLower(strings.ReplaceAll(rel, "\\", "/")), "/")
	switch {
	case strings.HasPrefix(p, "gui/"):
		return MergePolicyFirstInWins
	case strings.HasPrefix(p, "common/on_action/"):
		return MergePolicyContainerMerge
	case strings.HasPrefix(p, "common/defines/"),
		strings.HasPrefix(p, "history/"),
		strings.HasPrefix(p, "localization/"):
		return MergePolicyPerKeyOverride
	default:
		return MergePolicyOverride
	}
}

// Consequence describes, in one sentence, what replacing a file under this
// policy costs the definitions the replaced file held.
func (p MergePolicy) Consequence() string {
	switch p {
	case MergePolicyContainerMerge:
		return "CK3 accumulates the list containers of an on_action across files but keeps only the last trigger, effect, weight_multiplier and fallback; replacing the file discards both the definitions it held and any entries other files appended to them."
	case MergePolicyPerKeyOverride:
		return "CK3 combines these files key by key, so a key the replacement omits falls back to whatever another loaded file or the engine default supplies rather than to the replaced file."
	case MergePolicyFirstInWins:
		return "CK3 keeps the first definition it reads here and discards later ones, so an override has to load earlier — a leading 00_ rather than the trailing zzz_ that works in override folders."
	default:
		return "CK3 does not read the replaced file at all, so every definition it held is gone unless the replacement declares it again."
	}
}

// LoadOrderIntent is what a filename prefix is trying to do to load order. CK3
// reads a folder in filename order, so the prefix is the only lever a mod has
// over which of two files inside the same source is read first.
type LoadOrderIntent string

const (
	LoadOrderIntentNone  LoadOrderIntent = "none"
	LoadOrderIntentFirst LoadOrderIntent = "loads_first"
	LoadOrderIntentLast  LoadOrderIntent = "loads_last"
)

// LoadOrderPrefix reports what a filename's leading prefix does to load order
// and, when the prefix looks like it targets another file, the base name it
// appears to aim at. zzz_foo.txt aims at foo.txt; 00_foo.txt aims at foo.txt
// from the other end.
func LoadOrderPrefix(name string) (string, LoadOrderIntent) {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	lower := strings.ToLower(base)
	underscore := strings.Index(lower, "_")
	if underscore <= 0 || underscore == len(lower)-1 {
		return "", LoadOrderIntentNone
	}
	prefix := lower[:underscore]
	target := base[underscore+1:]
	if isAllDigits(prefix) {
		return target, LoadOrderIntentFirst
	}
	if isAllRune(prefix, 'z') {
		return target, LoadOrderIntentLast
	}
	return "", LoadOrderIntentNone
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func isAllRune(s string, want rune) bool {
	for _, r := range s {
		if r != want {
			return false
		}
	}
	return s != ""
}
