package savefile

import (
	"encoding/hex"
	"fmt"
	"unicode/utf8"
)

// Metadata is everything this package extracts from a save's metadata
// section. It is deliberately a closed set of known fields: the section also
// carries large portrait gene blobs and coat-of-arms trees that no caller has
// asked for and that would dominate a response.
type Metadata struct {
	Version          string `json:"version,omitempty"`
	SaveGameVersion  uint64 `json:"save_game_version,omitempty"`
	PortraitsVersion uint64 `json:"portraits_version,omitempty"`

	Date     string `json:"date,omitempty"`
	RealDate string `json:"real_date,omitempty"`

	PlayerName      string `json:"player_name,omitempty"`
	TitleName       string `json:"title_name,omitempty"`
	PlayerTier      uint64 `json:"player_tier,omitempty"`
	HouseName       string `json:"house_name,omitempty"`
	Government      string `json:"government,omitempty"`
	NumberOfPlayers uint64 `json:"number_of_players,omitempty"`

	// AchievementsEnabled is a pointer so an absent field is distinguishable
	// from a field that is present and false.
	AchievementsEnabled *bool `json:"achievements_enabled,omitempty"`

	Mods      []string `json:"mods"`
	DLCs      []string `json:"dlcs"`
	GameRules []string `json:"game_rules"`

	// Truncated names any list that hit the configured element cap.
	Truncated []string `json:"truncated,omitempty"`

	// Coverage records which token map named these fields and whether it
	// named every identifier the section used.
	Coverage Coverage `json:"token_map_coverage"`
}

// Field names this package recognises inside the meta_data container.
const (
	fieldMetaData         = "meta_data"
	fieldVersion          = "version"
	fieldSaveGameVersion  = "save_game_version"
	fieldPortraitsVersion = "portraits_version"
	fieldDate             = "meta_date"
	fieldRealDate         = "meta_real_date"
	fieldPlayerName       = "meta_player_name"
	fieldTitleName        = "meta_title_name"
	fieldPlayerTier       = "meta_player_tier"
	fieldHouseName        = "meta_house_name"
	fieldGovernment       = "meta_government"
	fieldNumberOfPlayers  = "meta_number_of_players"
	fieldAchievements     = "can_get_achievements"
	fieldMods             = "mods"
	fieldDLCs             = "dlcs"
	fieldGameRules        = "game_rules"
	fieldSettings         = "settings"
)

// ReadMetadata decodes one metadata section into the closed field set.
//
// It runs two passes: the first inventories the identifiers the section
// actually uses so a token map can be chosen by coverage, the second extracts
// the fields. Both passes are bounded, and the section is small enough that
// two passes cost nothing measurable.
func ReadMetadata(section []byte, maps []*TokenMap, limits Limits) (*Metadata, error) {
	observed, err := observedIdentifiers(section, limits)
	if err != nil {
		return nil, err
	}
	tokenMap, coverage, err := SelectTokenMap(maps, observed)
	if err != nil {
		return nil, err
	}

	metadata := &Metadata{
		Mods:      []string{},
		DLCs:      []string{},
		GameRules: []string{},
		Coverage:  coverage,
	}
	decoder := NewDecoder(section, limits)
	found := false
	err = readObject(decoder, tokenMap, func(name string, value Token, d *Decoder) error {
		if name != fieldMetaData || value.Kind != KindOpen {
			return d.SkipValue(value)
		}
		found = true
		return readObject(d, tokenMap, func(name string, value Token, d *Decoder) error {
			return metadata.assign(tokenMap, name, value, d)
		})
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, newError(ErrMalformedToken, "the metadata section has no meta_data container")
	}
	return metadata, nil
}

// observedIdentifiers inventories every distinct raw identifier in the
// section, including identifiers used as values rather than as field keys.
func observedIdentifiers(section []byte, limits Limits) ([]uint16, error) {
	decoder := NewDecoder(section, limits)
	seen := make(map[uint16]struct{})
	order := make([]uint16, 0, 64)
	for !decoder.Done() {
		token, err := decoder.Next()
		if err != nil {
			return nil, err
		}
		if token.Kind != KindID {
			continue
		}
		if _, ok := seen[token.ID]; ok {
			continue
		}
		seen[token.ID] = struct{}{}
		order = append(order, token.ID)
	}
	if decoder.Depth() != 0 {
		return nil, newError(ErrTruncated, "the metadata section ends inside an unclosed container")
	}
	return order, nil
}

// assign records one recognised meta_data field.
//
// Cases that consume a container return directly; the shared tail only runs
// for scalars, where SkipValue is a no-op, and for unrecognised containers.
func (m *Metadata) assign(resolver *TokenMap, name string, value Token, d *Decoder) error {
	switch name {
	case fieldVersion:
		m.Version = text(value)
	case fieldSaveGameVersion:
		m.SaveGameVersion = unsigned(value)
	case fieldPortraitsVersion:
		m.PortraitsVersion = unsigned(value)
	case fieldDate:
		m.Date = date(value)
	case fieldRealDate:
		m.RealDate = date(value)
	case fieldPlayerName:
		m.PlayerName = text(value)
	case fieldTitleName:
		m.TitleName = text(value)
	case fieldPlayerTier:
		m.PlayerTier = unsigned(value)
	case fieldHouseName:
		m.HouseName = text(value)
	case fieldGovernment:
		m.Government = text(value)
	case fieldNumberOfPlayers:
		m.NumberOfPlayers = unsigned(value)
	case fieldAchievements:
		if value.Kind == KindBool {
			enabled := value.Bool
			m.AchievementsEnabled = &enabled
		}
	case fieldMods:
		items, truncated, err := readList(d, value)
		if err != nil {
			return err
		}
		m.Mods, m.Truncated = items, appendTruncated(m.Truncated, truncated, fieldMods)
		return nil
	case fieldDLCs:
		items, truncated, err := readList(d, value)
		if err != nil {
			return err
		}
		m.DLCs, m.Truncated = items, appendTruncated(m.Truncated, truncated, fieldDLCs)
		return nil
	case fieldGameRules:
		return m.readGameRules(resolver, d, value)
	default:
		return d.SkipValue(value)
	}
	return d.SkipValue(value)
}

func (m *Metadata) readGameRules(resolver *TokenMap, d *Decoder, value Token) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	return readObject(d, resolver, func(name string, inner Token, d *Decoder) error {
		// The settings container is the only child that matters, and it is
		// addressed by resolved name, so an unnamed map cannot reach it.
		if name != fieldSettings {
			return d.SkipValue(inner)
		}
		items, truncated, err := readList(d, inner)
		if err != nil {
			return err
		}
		m.GameRules = items
		m.Truncated = appendTruncated(m.Truncated, truncated, fieldGameRules)
		return nil
	})
}

func appendTruncated(current []string, truncated bool, name string) []string {
	if !truncated {
		return current
	}
	return append(current, name)
}

// objectVisitor handles one `key = value` pair. The value token has already
// been read; the visitor owns skipping it.
type objectVisitor func(name string, value Token, d *Decoder) error

// readObject walks `identifier = value` pairs until the container closes.
//
// When resolver is nil, or an identifier is unnamed, the visitor still runs
// with an empty name so the value is consumed and the stream stays aligned.
func readObject(d *Decoder, resolver *TokenMap, visit objectVisitor) error {
	for {
		if d.Done() {
			return nil
		}
		token, err := d.Next()
		if err != nil {
			return err
		}
		if token.Kind == KindClose {
			return nil
		}
		if token.Kind != KindID {
			if err := d.SkipValue(token); err != nil {
				return err
			}
			continue
		}

		next, err := d.Next()
		if err != nil {
			return err
		}
		if next.Kind != KindEqual {
			// A bare identifier item rather than a key. The token already
			// read belongs to the next element, so consume it here to keep
			// the stream aligned instead of guessing.
			if next.Kind == KindClose {
				return nil
			}
			if err := d.SkipValue(next); err != nil {
				return err
			}
			continue
		}

		value, err := d.Next()
		if err != nil {
			return err
		}
		name := ""
		if resolver != nil {
			name, _ = resolver.Lookup(token.ID)
		}
		if err := visit(name, value, d); err != nil {
			return err
		}
	}
}

// readList collects the scalar elements of a container value.
func readList(d *Decoder, value Token) ([]string, bool, error) {
	if value.Kind != KindOpen {
		return nil, false, d.SkipValue(value)
	}
	items := make([]string, 0, 16)
	truncated := false
	target := d.Depth() - 1
	for d.Depth() > target {
		token, err := d.Next()
		if err != nil {
			return nil, false, err
		}
		switch token.Kind {
		case KindClose:
			continue
		case KindOpen:
			// A nested container is not a scalar element; skip it whole
			// rather than flattening it into the list.
			if err := d.SkipValue(token); err != nil {
				return nil, false, err
			}
			continue
		case KindQuoted, KindUnquoted:
			if len(items) >= d.limits.MaxArrayItems {
				truncated = true
				continue
			}
			items = append(items, text(token))
		default:
			continue
		}
	}
	return items, truncated, nil
}

// text renders a scalar as a string.
//
// Invalid UTF-8 is reported as a hexadecimal form rather than replaced,
// because a mangled mod name is worse than an obviously encoded one.
func text(token Token) string {
	switch token.Kind {
	case KindQuoted, KindUnquoted:
		if utf8.Valid(token.Text) {
			return string(token.Text)
		}
		return "0x" + hex.EncodeToString(token.Text)
	case KindU32, KindU64:
		return fmt.Sprintf("%d", token.Unsigned)
	case KindI32, KindI64:
		return fmt.Sprintf("%d", token.Signed)
	case KindBool:
		if token.Bool {
			return "yes"
		}
		return "no"
	default:
		return ""
	}
}

func unsigned(token Token) uint64 {
	switch token.Kind {
	case KindU32, KindU64:
		return token.Unsigned
	case KindI32, KindI64:
		if token.Signed < 0 {
			return 0
		}
		return uint64(token.Signed)
	default:
		return 0
	}
}

// Cumulative days before each month in the fixed 365-day calendar the binary
// date encoding uses. There are no leap years in this representation.
var monthOrdinal = [12]int64{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}

// date decodes the Paradox binary date encoding.
//
// A date is stored as hours since year -5000, over a 365-day year:
//
//	binary = ((year + 5000) * 365 + ordinal_day) * 24 + hour
//
// Verified against a real CK3 1.19.0.6 save, where meta_date 53144712 decodes
// to 1066.10.1 and meta_real_date 44908776 to 126.7.29.
func date(token Token) string {
	var raw int64
	switch token.Kind {
	case KindI32, KindI64:
		raw = token.Signed
	case KindU32, KindU64:
		if token.Unsigned > uint64(1)<<62 {
			return ""
		}
		raw = int64(token.Unsigned)
	default:
		return ""
	}
	if raw < 0 {
		return ""
	}
	hour := raw % 24
	remaining := raw / 24
	ordinal := remaining % 365
	year := remaining/365 - 5000
	month := 11
	for index := 11; index >= 0; index-- {
		if ordinal >= monthOrdinal[index] {
			month = index
			break
		}
	}
	day := ordinal - monthOrdinal[month] + 1
	if hour != 0 {
		return fmt.Sprintf("%d.%d.%d.%d", year, month+1, day, hour)
	}
	return fmt.Sprintf("%d.%d.%d", year, month+1, day)
}
