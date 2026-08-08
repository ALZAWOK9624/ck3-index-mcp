package savefile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Token map bounds, matching the Rust reference implementation.
const (
	tokenMapMaxBytes    = 16 << 20
	tokenMapMaxLineLen  = 4 << 10
	tokenMapMaxNameLen  = 256
	tokenMapMinEntries  = 1
	tokenMapMaxEntries  = 1 << 20
	tokenMapNameCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_."
)

// TokenMap resolves raw binary identifiers to CK3 field names for one exact
// game version.
//
// A map that resolves every identifier in one patch may be wrong for another,
// so a map is only ever used when it fully covers the save being read.
type TokenMap struct {
	// Label identifies the map in reports; it is the file's base name.
	Label string
	names map[uint16]string
}

// Lookup resolves one raw identifier.
func (m *TokenMap) Lookup(id uint16) (string, bool) {
	name, ok := m.names[id]
	return name, ok
}

// Size reports how many mappings the map holds.
func (m *TokenMap) Size() int { return len(m.names) }

// LoadTokenMap reads one bounded token map from disk.
func LoadTokenMap(path string) (*TokenMap, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, wrapError(ErrTokenMap, "the token map could not be opened", err)
	}
	defer handle.Close()

	raw, err := io.ReadAll(io.LimitReader(handle, tokenMapMaxBytes+1))
	if err != nil {
		return nil, wrapError(ErrTokenMap, "the token map could not be read", err)
	}
	if len(raw) > tokenMapMaxBytes {
		return nil, newError(ErrTooLarge, "the token map exceeds its size limit")
	}
	return ParseTokenMap(filepath.Base(path), raw)
}

// LoadTokenMaps reads every *.tokens.txt file directly inside dir.
//
// A directory with no maps is not an error here; the caller decides whether
// that is fatal, because the honest answer for a save it cannot name differs
// from the answer for a save it can.
func LoadTokenMaps(dir string) ([]*TokenMap, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.tokens.txt"))
	if err != nil {
		return nil, wrapError(ErrTokenMap, "the token map directory could not be listed", err)
	}
	sort.Strings(matches)
	maps := make([]*TokenMap, 0, len(matches))
	for _, match := range matches {
		parsed, err := LoadTokenMap(match)
		if err != nil {
			return nil, err
		}
		maps = append(maps, parsed)
	}
	return maps, nil
}

// ParseTokenMap parses the `0xNNNN name` line format.
func ParseTokenMap(label string, raw []byte) (*TokenMap, error) {
	names := make(map[uint16]string)
	for index, line := range strings.Split(string(raw), "\n") {
		number := index + 1
		line = strings.TrimSuffix(line, "\r")
		if len(line) > tokenMapMaxLineLen {
			return nil, newError(ErrTooLarge,
				fmt.Sprintf("token map line %d exceeds its length limit", number))
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		id, name, found := strings.Cut(trimmed, " ")
		if !found {
			return nil, newError(ErrTokenMap,
				fmt.Sprintf("token map line %d has no token/name separator", number))
		}
		name = strings.TrimSpace(name)
		if name == "" || len(name) > tokenMapMaxNameLen {
			return nil, newError(ErrTokenMap,
				fmt.Sprintf("token map line %d has an empty or oversized name", number))
		}
		if strings.ContainsFunc(name, func(r rune) bool {
			return !strings.ContainsRune(tokenMapNameCharset, r)
		}) {
			return nil, newError(ErrTokenMap,
				fmt.Sprintf("token map line %d has an unsupported character in its name", number))
		}
		if !strings.HasPrefix(id, "0x") {
			return nil, newError(ErrTokenMap,
				fmt.Sprintf("token map line %d does not begin with a 0x identifier", number))
		}
		value, err := strconv.ParseUint(id[2:], 16, 16)
		if err != nil {
			return nil, newError(ErrTokenMap,
				fmt.Sprintf("token map line %d has a malformed identifier", number))
		}
		if _, duplicate := names[uint16(value)]; duplicate {
			return nil, newError(ErrTokenMap,
				fmt.Sprintf("token map line %d repeats identifier %s", number, id))
		}
		if len(names) >= tokenMapMaxEntries {
			return nil, newError(ErrTooLarge, "the token map exceeds its entry limit")
		}
		names[uint16(value)] = name
	}
	if len(names) < tokenMapMinEntries {
		return nil, newError(ErrTokenMap, "the token map is empty")
	}
	return &TokenMap{Label: label, names: names}, nil
}

// Coverage records how completely a token map named one section's
// identifiers. It is reported verbatim so a caller can tell a confident
// answer from a partial one.
type Coverage struct {
	// ObservedIdentifiers is how many distinct raw ids the section used.
	ObservedIdentifiers int `json:"observed_identifiers"`
	// ResolvedIdentifiers is how many of those the chosen map named.
	ResolvedIdentifiers int `json:"resolved_identifiers"`
	// Complete is true only when every observed id was named.
	Complete bool `json:"complete"`
	// TokenMap is the label of the map that was used.
	TokenMap string `json:"token_map"`
}

// SelectTokenMap picks the map that fully covers the observed identifiers.
//
// The save's own version field cannot choose the map, because reading that
// field already requires a map. Coverage breaks that circularity: only a map
// built for this exact patch names every identifier the save actually used.
func SelectTokenMap(maps []*TokenMap, observed []uint16) (*TokenMap, Coverage, error) {
	if len(maps) == 0 {
		return nil, Coverage{ObservedIdentifiers: len(observed)},
			newError(ErrTokenMap, "no token maps are configured, so no field can be named")
	}

	best := Coverage{ObservedIdentifiers: len(observed)}
	var bestMap *TokenMap
	for _, candidate := range maps {
		resolved := 0
		for _, id := range observed {
			if _, ok := candidate.Lookup(id); ok {
				resolved++
			}
		}
		if bestMap == nil || resolved > best.ResolvedIdentifiers {
			bestMap = candidate
			best.ResolvedIdentifiers = resolved
			best.TokenMap = candidate.Label
		}
		if resolved == len(observed) {
			break
		}
	}
	best.Complete = best.ResolvedIdentifiers == len(observed)
	if !best.Complete {
		return bestMap, best, newError(ErrTokenMap,
			fmt.Sprintf("no configured token map names every field this save uses (best match %s resolved %d of %d); generate a map for this CK3 version",
				best.TokenMap, best.ResolvedIdentifiers, best.ObservedIdentifiers))
	}
	return bestMap, best, nil
}
