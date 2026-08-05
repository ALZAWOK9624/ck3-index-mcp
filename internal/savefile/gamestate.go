package savefile

import (
	"encoding/binary"
	"io"
	"math"
	"sort"
)

// Gamestate block and field names this package navigates by.
const (
	blockTraitsLookup = "traits_lookup"
	blockLandedTitles = "landed_titles"
	blockDynasties    = "dynasties"
	blockDynastyHouse = "dynasty_house"
	blockLiving       = "living"
	blockDead         = "dead_unprunable"
	blockCharacters   = "characters"
	blockPlayed       = "played_character"

	fieldKey           = "key"
	fieldHolder        = "holder"
	fieldTitleNameData = "title_name_data"
	fieldName          = "name"
	fieldAdj           = "adj"
	fieldCharacter     = "character"
	fieldFirstName     = "first_name"
	fieldBirth         = "birth"
	fieldNickname      = "nickname"
	fieldNicknameText  = "nickname_text"
	fieldCulture       = "culture"
	fieldFaith         = "faith"
	fieldDynastyHouse  = "dynasty_house"
	fieldSkill         = "skill"
	fieldTraits        = "traits"
	fieldFamilyData    = "family_data"
	fieldPrimarySpouse = "primary_spouse"
	fieldSpouse        = "spouse"
	fieldChild         = "child"
	fieldAliveData     = "alive_data"
	fieldCourtData     = "court_data"
	fieldEmployer      = "employer"
	fieldKnight        = "knight"
	fieldGold          = "gold"
	fieldPiety         = "piety"
	fieldPrestige      = "prestige"
	fieldCurrency      = "currency"
	fieldValue         = "value"
	fieldHealth        = "health"
	fieldFoundDate     = "found_date"
	fieldHeadOfHouse   = "head_of_house"
	fieldMotto         = "motto"
	fieldDeJureLiege   = "de_jure_liege"
	fieldHeldSince     = "date"
	fieldLocalizedName = "localized_name"
)

// GamestateQuery states exactly what one pass must collect.
//
// Nothing is gathered speculatively: a container the query does not ask for is
// skipped by token count alone, which is what keeps a single pass affordable
// on a save of any size.
type GamestateQuery struct {
	// Character selects one character record by save id. Zero collects none.
	Character int64
	// TitlesHeldBy collects every title whose holder is this character.
	// Titles appear before characters in the stream, so the id must be known
	// up front rather than discovered.
	TitlesHeldBy int64
	// House collects one dynasty house by save id. Houses also appear before
	// characters, so resolving a character's house needs a second pass.
	House int64
	// HouseValid distinguishes "house 0" from "no house requested".
	HouseValid bool
	// Inventory collects the id vocabularies an audit cross-references:
	// every trait name, every title key, every house name key.
	Inventory bool
}

// TitleRecord is one landed title as the save records it.
type TitleRecord struct {
	SaveID int64 `json:"save_id"`
	// Key is the script id, which is what cross-references back to the mod.
	Key string `json:"key"`
	// Name is already localized display text in the save.
	Name        string `json:"name,omitempty"`
	Adjective   string `json:"adjective,omitempty"`
	Holder      int64  `json:"holder,omitempty"`
	DeJureLiege int64  `json:"de_jure_liege,omitempty"`
	Since       string `json:"held_since,omitempty"`
}

// HouseRecord is one dynasty house.
type HouseRecord struct {
	SaveID int64 `json:"save_id"`
	// NameKey is a localization key, not display text. A house founded in
	// play has no key and carries LocalizedName instead.
	NameKey       string `json:"name_key,omitempty"`
	LocalizedName string `json:"localized_name,omitempty"`
	MottoKey      string `json:"motto_key,omitempty"`
	FoundDate     string `json:"found_date,omitempty"`
	HeadOfHouse   int64  `json:"head_of_house,omitempty"`
}

// CharacterRecord is one character as the save records them.
//
// Raw ids are kept alongside decoded values so a caller can both display the
// character and check the ids against the mod that is supposed to define them.
type CharacterRecord struct {
	SaveID int64 `json:"save_id"`
	// FirstNameKey is a name key such as "Artur", not display text.
	FirstNameKey string `json:"first_name_key,omitempty"`
	Birth        string `json:"birth,omitempty"`
	NicknameKey  string `json:"nickname_key,omitempty"`
	// NicknameText is already localized in the save.
	NicknameText string `json:"nickname_text,omitempty"`
	Ethnicity    string `json:"ethnicity,omitempty"`
	Alive        bool   `json:"alive"`

	CultureID int64 `json:"culture_id,omitempty"`
	FaithID   int64 `json:"faith_id,omitempty"`
	HouseID   int64 `json:"house_id,omitempty"`

	// Skills are diplomacy, martial, stewardship, intrigue, learning, prowess.
	Skills []int64 `json:"skills,omitempty"`
	// TraitIndices index into the save's own traits_lookup table.
	TraitIndices []int64 `json:"trait_indices,omitempty"`
	// Traits are those indices resolved through traits_lookup.
	Traits []string `json:"traits,omitempty"`

	PrimarySpouse int64   `json:"primary_spouse,omitempty"`
	Spouses       []int64 `json:"spouses,omitempty"`
	Children      []int64 `json:"children,omitempty"`
	Employer      int64   `json:"employer,omitempty"`
	Knight        bool    `json:"knight,omitempty"`

	Gold     float64 `json:"gold,omitempty"`
	Piety    float64 `json:"piety,omitempty"`
	Prestige float64 `json:"prestige,omitempty"`
	Health   float64 `json:"health,omitempty"`
}

// GamestateScan is what one pass collected.
type GamestateScan struct {
	TraitsLookup []string `json:"-"`
	// TitleKeys, HouseNameKeys are audit vocabularies, deduplicated.
	TitleKeys     []string `json:"-"`
	HouseNameKeys []string `json:"-"`

	Character       *CharacterRecord `json:"character,omitempty"`
	Titles          []TitleRecord    `json:"titles,omitempty"`
	House           *HouseRecord     `json:"house,omitempty"`
	PlayedCharacter int64            `json:"played_character,omitempty"`

	// Truncated names any collection that hit its element cap.
	Truncated []string `json:"truncated,omitempty"`
	// BytesRead is how much decompressed gamestate the pass consumed.
	BytesRead int64 `json:"bytes_read"`
}

// ScanGamestate makes one bounded streaming pass over a gamestate.
func ScanGamestate(src io.Reader, resolver *TokenMap, query GamestateQuery, limits Limits) (*GamestateScan, error) {
	if resolver == nil {
		return nil, newError(ErrTokenMap, "a token map is required to navigate a gamestate")
	}
	// Token count cannot exceed half the byte count, so the byte ceiling
	// already bounds the stream; a separate token budget would only add a
	// second number to keep in sync.
	streamLimits := limits
	streamLimits.MaxTokens = limits.gamestateCeiling()/2 + 1

	scan := &GamestateScan{}
	decoder := NewStreamDecoder(src, streamLimits)
	traitIndex := map[int64]string{}

	err := readObjectStream(decoder, resolver, func(name string, value Token, d *StreamDecoder) error {
		switch name {
		case blockTraitsLookup:
			names, truncated, err := readStreamList(d, value, limits)
			if err != nil {
				return err
			}
			scan.TraitsLookup = names
			for index, trait := range names {
				traitIndex[int64(index)] = trait
			}
			scan.Truncated = appendTruncated(scan.Truncated, truncated, blockTraitsLookup)
			return nil
		case blockLandedTitles:
			return scan.readLandedTitles(d, resolver, value, query, limits)
		case blockDynasties:
			return scan.readDynasties(d, resolver, value, query, limits)
		case blockLiving, blockDead, blockCharacters:
			return scan.readCharacters(d, resolver, value, query, limits, name == blockLiving)
		case blockPlayed:
			return scan.readPlayed(d, resolver, value)
		default:
			return d.SkipValue(value)
		}
	})
	if err != nil {
		return nil, err
	}

	if scan.Character != nil {
		for _, index := range scan.Character.TraitIndices {
			if trait, ok := traitIndex[index]; ok {
				scan.Character.Traits = append(scan.Character.Traits, trait)
			}
		}
	}
	scan.BytesRead = decoder.Consumed()
	return scan, nil
}

func (s *GamestateScan) readLandedTitles(d *StreamDecoder, resolver *TokenMap, value Token, query GamestateQuery, limits Limits) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	if !query.Inventory && query.TitlesHeldBy == 0 {
		return d.SkipValue(value)
	}
	seen := map[string]struct{}{}
	return readObjectStream(d, resolver, func(name string, inner Token, d *StreamDecoder) error {
		// The outer landed_titles wraps an inner map of the same name.
		if name != blockLandedTitles || inner.Kind != KindOpen {
			return d.SkipValue(inner)
		}
		return readNumberedEntries(d, resolver, limits, func(id int64, entry Token, d *StreamDecoder) error {
			record := TitleRecord{SaveID: id}
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldKey:
					record.Key = string(fieldValue.Text)
				case fieldHolder:
					record.Holder = signedOf(fieldValue)
				case fieldDeJureLiege:
					record.DeJureLiege = signedOf(fieldValue)
				case fieldHeldSince:
					record.Since = date(fieldValue)
				case fieldTitleNameData:
					return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
						switch sub {
						case fieldName:
							record.Name = string(subValue.Text)
						case fieldAdj:
							record.Adjective = string(subValue.Text)
						}
						return d.SkipValue(subValue)
					})
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			if query.Inventory && record.Key != "" {
				if _, duplicate := seen[record.Key]; !duplicate {
					if len(s.TitleKeys) >= limits.MaxArrayItems {
						s.Truncated = appendTruncated(s.Truncated, true, "title_keys")
					} else {
						seen[record.Key] = struct{}{}
						s.TitleKeys = append(s.TitleKeys, record.Key)
					}
				}
			}
			if query.TitlesHeldBy != 0 && record.Holder == query.TitlesHeldBy {
				if len(s.Titles) >= limits.MaxArrayItems {
					s.Truncated = appendTruncated(s.Truncated, true, "titles")
				} else {
					s.Titles = append(s.Titles, record)
				}
			}
			return nil
		})
	})
}

func (s *GamestateScan) readDynasties(d *StreamDecoder, resolver *TokenMap, value Token, query GamestateQuery, limits Limits) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	if !query.Inventory && !query.HouseValid {
		return d.SkipValue(value)
	}
	seen := map[string]struct{}{}
	return readObjectStream(d, resolver, func(name string, inner Token, d *StreamDecoder) error {
		if name != blockDynastyHouse || inner.Kind != KindOpen {
			return d.SkipValue(inner)
		}
		return readNumberedEntries(d, resolver, limits, func(id int64, entry Token, d *StreamDecoder) error {
			wanted := query.HouseValid && id == query.House
			if !wanted && !query.Inventory {
				return nil
			}
			record := HouseRecord{SaveID: id}
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldName:
					record.NameKey = string(fieldValue.Text)
				case fieldLocalizedName:
					record.LocalizedName = string(fieldValue.Text)
				case fieldFoundDate:
					record.FoundDate = date(fieldValue)
				case fieldHeadOfHouse:
					record.HeadOfHouse = signedOf(fieldValue)
				case fieldMotto:
					if fieldValue.Kind == KindOpen {
						return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
							if sub == fieldKey {
								record.MottoKey = string(subValue.Text)
							}
							return d.SkipValue(subValue)
						})
					}
					if fieldValue.Kind == KindQuoted || fieldValue.Kind == KindUnquoted {
						record.MottoKey = string(fieldValue.Text)
					}
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			if query.Inventory && record.NameKey != "" {
				if _, duplicate := seen[record.NameKey]; !duplicate {
					if len(s.HouseNameKeys) >= limits.MaxArrayItems {
						s.Truncated = appendTruncated(s.Truncated, true, "house_name_keys")
					} else {
						seen[record.NameKey] = struct{}{}
						s.HouseNameKeys = append(s.HouseNameKeys, record.NameKey)
					}
				}
			}
			if wanted {
				copied := record
				s.House = &copied
			}
			return nil
		})
	})
}

func (s *GamestateScan) readCharacters(d *StreamDecoder, resolver *TokenMap, value Token, query GamestateQuery, limits Limits, alive bool) error {
	if value.Kind != KindOpen || query.Character == 0 || s.Character != nil {
		return d.SkipValue(value)
	}
	return readNumberedEntries(d, resolver, limits, func(id int64, entry Token, d *StreamDecoder) error {
		if id != query.Character {
			return nil
		}
		record := CharacterRecord{SaveID: id, Alive: alive}
		if err := record.readFields(d, resolver, limits); err != nil {
			return err
		}
		s.Character = &record
		return nil
	})
}

func (c *CharacterRecord) readFields(d *StreamDecoder, resolver *TokenMap, limits Limits) error {
	return readObjectStream(d, resolver, func(field string, value Token, d *StreamDecoder) error {
		switch field {
		case fieldFirstName:
			c.FirstNameKey = string(value.Text)
		case fieldBirth:
			c.Birth = date(value)
		case fieldNickname:
			c.NicknameKey = string(value.Text)
		case fieldNicknameText:
			c.NicknameText = string(value.Text)
		case "ethnicity":
			c.Ethnicity = string(value.Text)
		case fieldCulture:
			c.CultureID = signedOf(value)
		case fieldFaith:
			c.FaithID = signedOf(value)
		case fieldDynastyHouse:
			c.HouseID = signedOf(value)
		case fieldSkill:
			values, _, err := readStreamNumbers(d, value, limits)
			c.Skills = values
			return err
		case fieldTraits:
			values, _, err := readStreamNumbers(d, value, limits)
			c.TraitIndices = values
			return err
		case fieldFamilyData:
			return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
				switch sub {
				case fieldPrimarySpouse:
					c.PrimarySpouse = signedOf(subValue)
				case fieldSpouse:
					if subValue.Kind == KindOpen {
						values, _, err := readStreamNumbers(d, subValue, limits)
						c.Spouses = values
						return err
					}
					c.Spouses = append(c.Spouses, signedOf(subValue))
				case fieldChild:
					values, _, err := readStreamNumbers(d, subValue, limits)
					c.Children = values
					return err
				}
				return d.SkipValue(subValue)
			})
		case fieldCourtData:
			return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
				switch sub {
				case fieldEmployer:
					c.Employer = signedOf(subValue)
				case fieldKnight:
					c.Knight = subValue.Kind == KindBool && subValue.Bool
				}
				return d.SkipValue(subValue)
			})
		case fieldAliveData:
			return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
				switch sub {
				case fieldHealth:
					c.Health = floatOf(subValue)
				case fieldGold:
					value, err := readWrappedNumber(d, resolver, subValue, fieldValue)
					c.Gold = value
					return err
				case fieldPiety:
					value, err := readWrappedNumber(d, resolver, subValue, fieldCurrency)
					c.Piety = value
					return err
				case fieldPrestige:
					value, err := readWrappedNumber(d, resolver, subValue, fieldCurrency)
					c.Prestige = value
					return err
				}
				return d.SkipValue(subValue)
			})
		}
		return d.SkipValue(value)
	})
}

// readWrappedNumber reads `{ inner = N }`, the shape CK3 uses for currencies.
func readWrappedNumber(d *StreamDecoder, resolver *TokenMap, value Token, inner string) (float64, error) {
	if value.Kind != KindOpen {
		return floatOf(value), nil
	}
	var out float64
	err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
		if field == inner {
			out = floatOf(fieldValue)
		}
		return d.SkipValue(fieldValue)
	})
	return out, err
}

func (s *GamestateScan) readPlayed(d *StreamDecoder, resolver *TokenMap, value Token) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	return readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
		if field == fieldCharacter {
			s.PlayedCharacter = signedOf(fieldValue)
		}
		return d.SkipValue(fieldValue)
	})
}

// numberedVisitor handles one `<number> = { ... }` entry.
type numberedVisitor func(id int64, entry Token, d *StreamDecoder) error

// readNumberedEntries walks the `id = { ... }` maps CK3 uses for its entity
// tables. Entries the visitor does not consume are skipped whole.
func readNumberedEntries(d *StreamDecoder, resolver *TokenMap, limits Limits, visit numberedVisitor) error {
	target := d.Depth() - 1
	for d.Depth() > target {
		token, err := d.Next()
		if err != nil {
			return err
		}
		switch token.Kind {
		case KindClose:
			continue
		case KindOpen:
			if err := d.SkipValue(token); err != nil {
				return err
			}
			continue
		}
		// An entry key is a raw number, not an identifier.
		id, ok := numericKey(token)
		if !ok {
			continue
		}
		next, err := d.Next()
		if err != nil {
			return err
		}
		if next.Kind != KindEqual {
			if err := d.SkipValue(next); err != nil {
				return err
			}
			continue
		}
		entry, err := d.Next()
		if err != nil {
			return err
		}
		if entry.Kind != KindOpen {
			continue
		}
		entryDepth := d.Depth()
		if err := visit(id, entry, d); err != nil {
			return err
		}
		// Whatever the visitor left unread belongs to this entry.
		if err := d.SkipToDepth(entryDepth - 1); err != nil {
			return err
		}
	}
	return nil
}

// streamVisitor handles one `key = value` pair from a streaming decoder.
type streamVisitor func(name string, value Token, d *StreamDecoder) error

// readObjectStream is the streaming twin of readObject.
func readObjectStream(d *StreamDecoder, resolver *TokenMap, visit streamVisitor) error {
	target := d.Depth() - 1
	for {
		if d.Depth() <= target || d.Done() {
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

// readStreamList collects the scalar text elements of a container.
func readStreamList(d *StreamDecoder, value Token, limits Limits) ([]string, bool, error) {
	if value.Kind != KindOpen {
		return nil, false, d.SkipValue(value)
	}
	items := make([]string, 0, 32)
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
			if err := d.SkipValue(token); err != nil {
				return nil, false, err
			}
		case KindQuoted, KindUnquoted:
			if len(items) >= limits.MaxArrayItems {
				truncated = true
				continue
			}
			items = append(items, string(token.Text))
		}
	}
	return items, truncated, nil
}

// readStreamNumbers collects the numeric elements of a container.
func readStreamNumbers(d *StreamDecoder, value Token, limits Limits) ([]int64, bool, error) {
	if value.Kind != KindOpen {
		return []int64{signedOf(value)}, false, nil
	}
	items := make([]int64, 0, 8)
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
			if err := d.SkipValue(token); err != nil {
				return nil, false, err
			}
		case KindU32, KindU64, KindI32, KindI64:
			if len(items) >= limits.MaxArrayItems {
				truncated = true
				continue
			}
			items = append(items, signedOf(token))
		}
	}
	return items, truncated, nil
}

// numericKey reads an entity table's numeric key.
func numericKey(token Token) (int64, bool) {
	switch token.Kind {
	case KindU32, KindU64, KindI32, KindI64:
		return signedOf(token), true
	default:
		return 0, false
	}
}

func signedOf(token Token) int64 {
	switch token.Kind {
	case KindU32, KindU64:
		return int64(token.Unsigned)
	case KindI32, KindI64:
		return token.Signed
	default:
		return 0
	}
}

func floatOf(token Token) float64 {
	switch token.Kind {
	case KindU32, KindU64:
		return float64(token.Unsigned)
	case KindI32, KindI64:
		return float64(token.Signed)
	case KindF32:
		return float32BitsToFloat(token.Bits)
	case KindF64:
		return float64BitsToFloat(token.Bits)
	default:
		return 0
	}
}

// The two float encodings are not the same shape, and guessing wrong is
// silent. Both were confirmed against a real CK3 1.19.0.6 save:
//
//   - F32 is IEEE 754: bytes 0ad7a33e decode to the age 0.32 that the save's
//     melted text shows.
//   - F64 is not IEEE at all. It is a signed fixed-point integer with five
//     decimal places: bytes 1af2020000000000 are 193050, and the melted text
//     shows income 1.9305.
func float32BitsToFloat(bits [8]byte) float64 {
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(bits[:4])))
}

func float64BitsToFloat(bits [8]byte) float64 {
	return float64(int64(binary.LittleEndian.Uint64(bits[:]))) / 100000.0
}

// SortedStrings returns a deterministic copy, so a report never depends on
// map iteration order.
func SortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
