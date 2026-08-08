package savefile

import (
	"bytes"
	"io"
	"runtime"
	"strings"
	"testing"
)

const (
	idTraitsLookup  uint16 = 0x0f01
	idLandedTitles  uint16 = 0x0f02
	idDynasties     uint16 = 0x0f03
	idDynastyHouse  uint16 = 0x0f04
	idLiving        uint16 = 0x0f05
	idPlayed        uint16 = 0x0f06
	idTitleKey      uint16 = 0x0f07
	idHolder        uint16 = 0x0f08
	idTitleNameData uint16 = 0x0f09
	idNameField     uint16 = 0x0f0a
	idFoundDate     uint16 = 0x0f0b
	idHeadOfHouse   uint16 = 0x0f0c
	idFirstNameF    uint16 = 0x0f0d
	idTraitsField   uint16 = 0x0f0e
	idSkillField    uint16 = 0x0f0f
	idCharField     uint16 = 0x0f10
	idHouseField    uint16 = 0x0f11
	idFiller        uint16 = 0x0f12
	idHeldSince     uint16 = 0x0f13
)

const gamestateTokenMap = `0x0f01 traits_lookup
0x0f02 landed_titles
0x0f03 dynasties
0x0f04 dynasty_house
0x0f05 living
0x0f06 played_character
0x0f07 key
0x0f08 holder
0x0f09 title_name_data
0x0f0a name
0x0f0b found_date
0x0f0c head_of_house
0x0f0d first_name
0x0f0e traits
0x0f0f skill
0x0f10 character
0x0f11 dynasty_house
0x0f12 filler
0x0f13 date
`

func gamestateMaps(t *testing.T) *TokenMap {
	t.Helper()
	parsed, err := ParseTokenMap("gamestate.tokens.txt", []byte(gamestateTokenMap))
	if err != nil {
		t.Fatalf("parsing token map: %v", err)
	}
	return parsed
}

func numbered(id int64, value []byte) []byte {
	return concat(tokU32(uint32(id)), tokEqual(), value)
}

// syntheticGamestate mirrors the real block order, which is what the scanner's
// single-pass design depends on: dynasties precede living, so a character's
// house cannot be resolved in the same pass that finds the character.
func syntheticGamestate() []byte {
	return concat(
		field(idTraitsLookup, container(
			tokUnquoted("brave"), tokUnquoted("craven"), tokUnquoted("gregarious"),
		)),
		field(idLandedTitles, container(
			field(idLandedTitles, container(
				numbered(7, container(
					field(idTitleKey, tokQuoted("c_alpha")),
					field(idHolder, tokU32(42)),
					field(idHeldSince, tokI32(53144712)),
					field(idTitleNameData, container(field(idNameField, tokQuoted("阿尔法郡")))),
				)),
				numbered(8, container(
					field(idTitleKey, tokQuoted("c_beta")),
					field(idHolder, tokU32(99)),
				)),
			)),
		)),
		field(idDynasties, container(
			field(idDynastyHouse, container(
				numbered(3, container(
					field(idNameField, tokQuoted("dynn_Alpha")),
					field(idFoundDate, tokI32(53144712)),
					field(idHeadOfHouse, tokU32(42)),
				)),
			)),
		)),
		field(idLiving, container(
			numbered(42, container(
				field(idFirstNameF, tokQuoted("Aldric")),
				field(idHouseField, tokU32(3)),
				field(idSkillField, container(tokU32(1), tokU32(2), tokU32(3), tokU32(4), tokU32(5), tokU32(6))),
				field(idTraitsField, container(tokU32(0), tokU32(2))),
			)),
			numbered(99, container(field(idFirstNameF, tokQuoted("Other")))),
		)),
		field(idPlayed, container(field(idCharField, tokU32(42)))),
	)
}

func TestScanGamestateCollectsOnlyWhatWasAskedFor(t *testing.T) {
	resolver := gamestateMaps(t)
	limits := DefaultLimits()
	body := syntheticGamestate()

	inventory, err := ScanGamestate(bytes.NewReader(body), resolver, GamestateQuery{Inventory: true}, limits)
	if err != nil {
		t.Fatalf("inventory scan: %v", err)
	}
	if strings.Join(inventory.TraitsLookup, ",") != "brave,craven,gregarious" {
		t.Errorf("traits = %v", inventory.TraitsLookup)
	}
	if strings.Join(inventory.TitleKeys, ",") != "c_alpha,c_beta" {
		t.Errorf("title keys = %v", inventory.TitleKeys)
	}
	if strings.Join(inventory.HouseNameKeys, ",") != "dynn_Alpha" {
		t.Errorf("house name keys = %v", inventory.HouseNameKeys)
	}
	if inventory.PlayedCharacter != 42 {
		t.Errorf("played character = %d", inventory.PlayedCharacter)
	}
	// An inventory pass must not carry entity records it was not asked for.
	if inventory.Character != nil || inventory.House != nil || len(inventory.Titles) != 0 {
		t.Errorf("inventory pass collected records it was not asked for: %+v", inventory)
	}

	dossier, err := ScanGamestate(bytes.NewReader(body), resolver,
		GamestateQuery{Character: 42, TitlesHeldBy: 42}, limits)
	if err != nil {
		t.Fatalf("character scan: %v", err)
	}
	if dossier.Character == nil {
		t.Fatal("character 42 was not found")
	}
	character := dossier.Character
	if character.FirstNameKey != "Aldric" || character.HouseID != 3 || !character.Alive {
		t.Errorf("character = %+v", character)
	}
	if strings.Join(character.Traits, ",") != "brave,gregarious" {
		t.Errorf("traits = %v (indices %v)", character.Traits, character.TraitIndices)
	}
	if len(character.Skills) != 6 || character.Skills[5] != 6 {
		t.Errorf("skills = %v", character.Skills)
	}
	if len(dossier.Titles) != 1 || dossier.Titles[0].Key != "c_alpha" {
		t.Fatalf("titles = %+v", dossier.Titles)
	}
	if dossier.Titles[0].Name != "阿尔法郡" || dossier.Titles[0].Since != "1066.10.1" {
		t.Errorf("title = %+v", dossier.Titles[0])
	}

	houses, err := ScanGamestate(bytes.NewReader(body), resolver,
		GamestateQuery{House: 3, HouseValid: true}, limits)
	if err != nil {
		t.Fatalf("house scan: %v", err)
	}
	if houses.House == nil || houses.House.NameKey != "dynn_Alpha" || houses.House.HeadOfHouse != 42 {
		t.Fatalf("house = %+v", houses.House)
	}
}

func TestScanGamestateRefusesHostileInputWithoutPanicking(t *testing.T) {
	resolver := gamestateMaps(t)
	limits := DefaultLimits()
	body := syntheticGamestate()

	cases := map[string][]byte{
		"truncated mid-container": body[:len(body)/2],
		"truncated mid-token":     body[:len(body)-1],
		"deep nesting":            bytes.Repeat(tokOpen(), limits.MaxDepth+8),
		"stray close":             tokClose(),
		"compact token":           concat(field(idTraitsLookup, container()), u16le(lexCompactFirst)),
	}
	for name, section := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ScanGamestate(bytes.NewReader(section), resolver,
				GamestateQuery{Inventory: true, Character: 42}, limits); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}

	if _, err := ScanGamestate(bytes.NewReader(body), nil, GamestateQuery{}, limits); KindOf(err) != ErrTokenMap {
		t.Fatalf("a missing token map produced %v", err)
	}
}

// repeatReader produces the same block over and over without ever holding
// more than one copy, so a large gamestate can be simulated without a large
// fixture on disk or in memory.
type repeatReader struct {
	block     []byte
	remaining int
	offset    int
}

func (r *repeatReader) Read(out []byte) (int, error) {
	if r.remaining == 0 && r.offset == 0 {
		return 0, io.EOF
	}
	written := 0
	for written < len(out) {
		if r.offset == len(r.block) {
			r.offset = 0
			if r.remaining == 0 {
				break
			}
			r.remaining--
		}
		n := copy(out[written:], r.block[r.offset:])
		r.offset += n
		written += n
	}
	if written == 0 {
		return 0, io.EOF
	}
	return written, nil
}

// TestScanGamestateMemoryDoesNotScaleWithSaveSize is the load-bearing test for
// the streaming design. If the scanner ever materialises what it walks, this
// fails long before a real late-game save reaches production.
func TestScanGamestateMemoryDoesNotScaleWithSaveSize(t *testing.T) {
	resolver := gamestateMaps(t)
	limits := DefaultLimits()

	// A container the query never asks for: it must cost tokens, not memory.
	filler := field(idFiller, container(
		field(idNameField, tokQuoted(strings.Repeat("x", 512))),
		field(idFoundDate, tokI32(53144712)),
	))

	measure := func(copies int) (uint64, int64) {
		source := &repeatReader{block: filler, remaining: copies}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		scan, err := ScanGamestate(source, resolver, GamestateQuery{Character: 42}, limits)
		if err != nil {
			t.Fatalf("scanning %d copies: %v", copies, err)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, scan.BytesRead
	}

	smallAlloc, smallBytes := measure(200)
	largeAlloc, largeBytes := measure(200_000)

	if largeBytes < smallBytes*100 {
		t.Fatalf("the large stream was not actually larger: %d vs %d bytes", largeBytes, smallBytes)
	}
	t.Logf("small: %d bytes read, %d allocated; large: %d bytes read, %d allocated",
		smallBytes, smallAlloc, largeBytes, largeAlloc)

	// A thousandfold more input must not mean a thousandfold more allocation.
	// The bound is deliberately loose: the point is that growth is not
	// proportional, not that it is exactly zero.
	if largeAlloc > smallAlloc*8 {
		t.Fatalf("allocation grew with input size: %d bytes for %d input, %d bytes for %d input",
			smallAlloc, smallBytes, largeAlloc, largeBytes)
	}
}
