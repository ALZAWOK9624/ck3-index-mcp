package savefile

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Identifiers used by the synthetic fixtures. They deliberately sit outside
// the lexeme constant table, exactly as real CK3 identifiers do.
const (
	idMetaData         uint16 = 0x3155
	idVersion          uint16 = 0x00ee
	idSaveGameVersion  uint16 = 0x058f
	idDate             uint16 = 0x3157
	idRealDate         uint16 = 0x3183
	idPlayerName       uint16 = 0x29e6
	idTitleName        uint16 = 0x29e7
	idPlayerTier       uint16 = 0x2eff
	idHouseName        uint16 = 0x325e
	idGovernment       uint16 = 0x3154
	idNumberOfPlayers  uint16 = 0x3187
	idAchievements     uint16 = 0x34b5
	idMods             uint16 = 0x32c1
	idDLCs             uint16 = 0x32c0
	idGameRules        uint16 = 0x0698
	idSettings         uint16 = 0x34b4
	idPortraitsVersion uint16 = 0x3460
	idPortrait         uint16 = 0x1234
)

const fixtureTokenMap = `0x3155 meta_data
0x00ee version
0x058f save_game_version
0x3157 meta_date
0x3183 meta_real_date
0x29e6 meta_player_name
0x29e7 meta_title_name
0x2eff meta_player_tier
0x325e meta_house_name
0x3154 meta_government
0x3187 meta_number_of_players
0x34b5 can_get_achievements
0x32c1 mods
0x32c0 dlcs
0x0698 game_rules
0x34b4 settings
0x3460 portraits_version
0x1234 meta_main_portrait
`

func u16le(value uint16) []byte {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	return raw
}

func tokID(id uint16) []byte { return u16le(id) }
func tokEqual() []byte       { return u16le(lexEqual) }
func tokOpen() []byte        { return u16le(lexOpen) }
func tokClose() []byte       { return u16le(lexClose) }

func tokU32(value uint32) []byte {
	raw := append(u16le(lexU32), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(raw[2:], value)
	return raw
}

func tokI32(value int32) []byte {
	raw := append(u16le(lexI32), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(raw[2:], uint32(value))
	return raw
}

func tokQuoted(text string) []byte {
	raw := append(u16le(lexQuoted), u16le(uint16(len(text)))...)
	return append(raw, text...)
}

func tokUnquoted(text string) []byte {
	raw := append(u16le(lexUnquoted), u16le(uint16(len(text)))...)
	return append(raw, text...)
}

func tokBool(value bool) []byte {
	raw := u16le(lexBool)
	if value {
		return append(raw, 1)
	}
	return append(raw, 0)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func field(id uint16, value []byte) []byte {
	return concat(tokID(id), tokEqual(), value)
}

func container(parts ...[]byte) []byte {
	return concat(tokOpen(), concat(parts...), tokClose())
}

// fixtureMetadata mirrors the real shape: a meta_data wrapper holding scalars,
// two string lists, a nested game_rules.settings list, and a portrait blob
// that must be skipped rather than reported.
func fixtureMetadata() []byte {
	return field(idMetaData, container(
		field(idSaveGameVersion, tokU32(15)),
		field(idVersion, tokQuoted("1.19.0.6")),
		field(idPortraitsVersion, tokU32(5)),
		field(idDate, tokI32(53144712)),
		field(idPlayerName, tokQuoted("谢赫巴达")),
		field(idTitleName, tokQuoted("辛达班德谢赫国")),
		field(idPortrait, container(
			field(idPlayerTier, tokU32(999)),
			field(idVersion, tokQuoted("portrait-noise")),
		)),
		field(idPlayerTier, tokU32(2)),
		field(idHouseName, tokQuoted("巴达")),
		field(idGovernment, tokQuoted("tribal_government")),
		field(idRealDate, tokI32(44908776)),
		field(idNumberOfPlayers, tokU32(1)),
		field(idDLCs, container(tokQuoted("Roads to Power"), tokQuoted("Coronations"))),
		field(idMods, container(tokQuoted("mod/ugc_3733797942.mod"))),
		field(idGameRules, container(
			field(idSettings, container(tokUnquoted("normal_difficulty"), tokUnquoted("harm_dangerous"))),
		)),
		field(idAchievements, tokBool(true)),
	))
}

func fixtureGamestate() []byte {
	return field(idVersion, tokQuoted("gamestate-body"))
}

func header(kind uint16, metadataLen int) []byte {
	line := fmt.Sprintf("SAV01%02xdeadbeef%08x\n", kind, metadataLen)
	if len(line) != 24 {
		panic("fixture header must be 24 bytes")
	}
	return []byte(line)
}

func zipArchive(t *testing.T, entries [][2]any) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	for _, entry := range entries {
		name := entry[0].(string)
		payload := entry[1].([]byte)
		handle, err := writer.Create(name)
		if err != nil {
			t.Fatalf("creating zip entry %q: %v", name, err)
		}
		if _, err := handle.Write(payload); err != nil {
			t.Fatalf("writing zip entry %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buffer.Bytes()
}

func buildSave(t *testing.T, layout Layout, metadata, gamestate []byte) []byte {
	t.Helper()
	switch layout {
	case LayoutBinaryUncompressed:
		return concat(header(1, len(metadata)), metadata, gamestate)
	case LayoutUnifiedBinaryZip:
		archive := zipArchive(t, [][2]any{{gamestateEntry, gamestate}})
		return concat(header(3, len(metadata)), metadata, archive)
	case LayoutSplitBinaryZip:
		archive := zipArchive(t, [][2]any{{metaEntry, metadata}, {gamestateEntry, gamestate}})
		return concat(header(5, 0), archive)
	default:
		t.Fatalf("unknown layout %q", layout)
		return nil
	}
}

func fixtureMaps(t *testing.T) []*TokenMap {
	t.Helper()
	parsed, err := ParseTokenMap("ck3-fixture.tokens.txt", []byte(fixtureTokenMap))
	if err != nil {
		t.Fatalf("parsing fixture token map: %v", err)
	}
	return []*TokenMap{parsed}
}

func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return string(raw)
}

func readFixture(t *testing.T, save []byte) *Metadata {
	t.Helper()
	limits := DefaultLimits()
	envelope, err := Analyze(Bytes(save), limits)
	if err != nil {
		t.Fatalf("analyzing save: %v", err)
	}
	section, err := envelope.Metadata(Bytes(save), limits)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	metadata, err := ReadMetadata(section, fixtureMaps(t), limits)
	if err != nil {
		t.Fatalf("decoding metadata: %v", err)
	}
	return metadata
}

func TestEveryLayoutYieldsTheSameMetadata(t *testing.T) {
	layouts := []Layout{LayoutBinaryUncompressed, LayoutUnifiedBinaryZip, LayoutSplitBinaryZip}
	var first *Metadata
	for _, layout := range layouts {
		save := buildSave(t, layout, fixtureMetadata(), fixtureGamestate())
		envelope, err := Analyze(Bytes(save), DefaultLimits())
		if err != nil {
			t.Fatalf("%s: analyzing: %v", layout, err)
		}
		if envelope.Layout != layout {
			t.Fatalf("%s: resolved layout %s", layout, envelope.Layout)
		}
		metadata := readFixture(t, save)

		if metadata.Version != "1.19.0.6" {
			t.Errorf("%s: version = %q", layout, metadata.Version)
		}
		if metadata.Date != "1066.10.1" {
			t.Errorf("%s: date = %q", layout, metadata.Date)
		}
		if metadata.RealDate != "126.7.29" {
			t.Errorf("%s: real date = %q", layout, metadata.RealDate)
		}
		if metadata.PlayerName != "谢赫巴达" {
			t.Errorf("%s: player = %q", layout, metadata.PlayerName)
		}
		if metadata.TitleName != "辛达班德谢赫国" {
			t.Errorf("%s: title = %q", layout, metadata.TitleName)
		}
		if metadata.HouseName != "巴达" {
			t.Errorf("%s: house = %q", layout, metadata.HouseName)
		}
		if metadata.Government != "tribal_government" {
			t.Errorf("%s: government = %q", layout, metadata.Government)
		}
		// The portrait blob also carries meta_player_tier; reporting 999
		// would mean the walker descended into a container it must skip.
		if metadata.PlayerTier != 2 {
			t.Errorf("%s: player tier = %d, want the top-level value", layout, metadata.PlayerTier)
		}
		if metadata.SaveGameVersion != 15 || metadata.PortraitsVersion != 5 || metadata.NumberOfPlayers != 1 {
			t.Errorf("%s: numeric fields = %+v", layout, metadata)
		}
		if metadata.AchievementsEnabled == nil || !*metadata.AchievementsEnabled {
			t.Errorf("%s: achievements = %v", layout, metadata.AchievementsEnabled)
		}
		if strings.Join(metadata.Mods, ",") != "mod/ugc_3733797942.mod" {
			t.Errorf("%s: mods = %v", layout, metadata.Mods)
		}
		if strings.Join(metadata.DLCs, ",") != "Roads to Power,Coronations" {
			t.Errorf("%s: dlcs = %v", layout, metadata.DLCs)
		}
		if strings.Join(metadata.GameRules, ",") != "normal_difficulty,harm_dangerous" {
			t.Errorf("%s: game rules = %v", layout, metadata.GameRules)
		}
		if !metadata.Coverage.Complete {
			t.Errorf("%s: coverage = %+v", layout, metadata.Coverage)
		}

		if first == nil {
			first = metadata
			continue
		}
		// Compare the encoded form: the struct holds a pointer field, so a
		// value comparison would only be comparing addresses.
		if encodeJSON(t, first) != encodeJSON(t, metadata) {
			t.Errorf("%s: metadata differs from the first layout\n%s\n%s",
				layout, encodeJSON(t, first), encodeJSON(t, metadata))
		}
	}
}

func TestBinaryDateDecoding(t *testing.T) {
	cases := []struct {
		raw  int32
		want string
	}{
		// Both values are read from a real CK3 1.19.0.6 save.
		{53144712, "1066.10.1"},
		{44908776, "126.7.29"},
		// Year 1 day 1 is jomini's documented round-trip anchor.
		{43808760, "1.1.1"},
	}
	for _, testCase := range cases {
		got := date(Token{Kind: KindI32, Signed: int64(testCase.raw)})
		if got != testCase.want {
			t.Errorf("date(%d) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
	if got := date(Token{Kind: KindI32, Signed: -1}); got != "" {
		t.Errorf("a negative date decoded to %q", got)
	}
	if got := date(Token{Kind: KindQuoted, Text: []byte("1066.10.1")}); got != "" {
		t.Errorf("a non-numeric date decoded to %q", got)
	}
}

func TestHeaderAndContainerMustAgree(t *testing.T) {
	metadata := fixtureMetadata()
	gamestate := fixtureGamestate()
	archive := zipArchive(t, [][2]any{{gamestateEntry, gamestate}})

	cases := []struct {
		name string
		save []byte
		kind ErrorKind
	}{
		{
			name: "uncompressed header wrapping a real archive",
			save: concat(header(1, len(metadata)), metadata, archive),
			kind: ErrContainerMismatch,
		},
		{
			name: "unified header with no archive",
			save: concat(header(3, len(metadata)), metadata, gamestate),
			kind: ErrContainerMismatch,
		},
		{
			name: "split header claiming inline metadata",
			save: concat(header(5, len(metadata)), zipArchive(t, [][2]any{{metaEntry, metadata}, {gamestateEntry, gamestate}})),
			kind: ErrContainerMismatch,
		},
		{
			name: "text header",
			save: concat(header(0, len(metadata)), metadata, gamestate),
			kind: ErrUnsupportedLayout,
		},
		{
			name: "declared metadata past the end",
			save: concat(header(1, 1<<20), metadata),
			kind: ErrBounds,
		},
		{
			name: "metadata consuming the whole save",
			save: concat(header(1, len(metadata)), metadata),
			kind: ErrBounds,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Analyze(Bytes(testCase.save), DefaultLimits())
			if err == nil {
				t.Fatalf("expected a refusal")
			}
			if got := KindOf(err); got != testCase.kind {
				t.Fatalf("error kind = %q, want %q (%v)", got, testCase.kind, err)
			}
		})
	}
}

func TestHostileSectionsAreRefusedWithoutPanicking(t *testing.T) {
	limits := DefaultLimits()
	deep := bytes.Repeat(tokOpen(), limits.MaxDepth+8)
	unbalanced := concat(tokID(idMetaData), tokEqual(), tokOpen())
	truncatedString := concat(u16le(lexQuoted), u16le(64))
	compact := u16le(lexCompactFirst)
	strayClose := tokClose()
	oversizedString := concat(u16le(lexQuoted), u16le(65535), bytes.Repeat([]byte("a"), 65535))

	cases := []struct {
		name    string
		section []byte
	}{
		{"deep nesting", deep},
		{"unclosed container", unbalanced},
		{"truncated string payload", truncatedString},
		{"compact token CK3 never emits", compact},
		{"stray container close", strayClose},
		{"truncated identifier", []byte{0x01}},
		{"empty section", nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ReadMetadata(testCase.section, fixtureMaps(t), limits); err == nil {
				t.Fatalf("expected a refusal")
			}
		})
	}

	// A string at the byte-length ceiling is legal, so it must be accepted
	// by the decoder rather than caught by the hostile-input net.
	tight := limits
	tight.MaxStringBytes = 65535
	decoder := NewDecoder(oversizedString, tight)
	if _, err := decoder.Next(); err != nil {
		t.Fatalf("a maximum-length string was refused: %v", err)
	}
	tight.MaxStringBytes = 1024
	decoder = NewDecoder(oversizedString, tight)
	if _, err := decoder.Next(); KindOf(err) != ErrTooLarge {
		t.Fatalf("an oversized string produced %v", err)
	}
}

func TestTokenMapMustCoverTheSave(t *testing.T) {
	partial, err := ParseTokenMap("partial.tokens.txt", []byte("0x3155 meta_data\n0x00ee version\n"))
	if err != nil {
		t.Fatalf("parsing partial map: %v", err)
	}
	save := buildSave(t, LayoutUnifiedBinaryZip, fixtureMetadata(), fixtureGamestate())
	envelope, err := Analyze(Bytes(save), DefaultLimits())
	if err != nil {
		t.Fatalf("analyzing: %v", err)
	}
	section, err := envelope.Metadata(Bytes(save), DefaultLimits())
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}

	_, err = ReadMetadata(section, []*TokenMap{partial}, DefaultLimits())
	if KindOf(err) != ErrTokenMap {
		t.Fatalf("a partial map produced %v", err)
	}
	if !strings.Contains(err.Error(), "generate a map for this CK3 version") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}

	if _, err := ReadMetadata(section, nil, DefaultLimits()); KindOf(err) != ErrTokenMap {
		t.Fatalf("an absent map produced %v", err)
	}

	// The complete map must still win when it is offered alongside a
	// partial one, whatever the order.
	complete := fixtureMaps(t)[0]
	for _, maps := range [][]*TokenMap{{partial, complete}, {complete, partial}} {
		metadata, err := ReadMetadata(section, maps, DefaultLimits())
		if err != nil {
			t.Fatalf("selecting among maps: %v", err)
		}
		if metadata.Coverage.TokenMap != complete.Label || !metadata.Coverage.Complete {
			t.Fatalf("selected %+v", metadata.Coverage)
		}
	}
}

func TestListsReportTruncation(t *testing.T) {
	items := make([][]byte, 0, 64)
	for index := 0; index < 64; index++ {
		items = append(items, tokQuoted(fmt.Sprintf("dlc-%02d", index)))
	}
	metadata := field(idMetaData, container(field(idDLCs, container(items...))))
	save := buildSave(t, LayoutUnifiedBinaryZip, metadata, fixtureGamestate())

	limits := DefaultLimits()
	limits.MaxArrayItems = 8
	envelope, err := Analyze(Bytes(save), limits)
	if err != nil {
		t.Fatalf("analyzing: %v", err)
	}
	section, err := envelope.Metadata(Bytes(save), limits)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	decoded, err := ReadMetadata(section, fixtureMaps(t), limits)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(decoded.DLCs) != 8 {
		t.Fatalf("kept %d items, want the cap", len(decoded.DLCs))
	}
	if strings.Join(decoded.Truncated, ",") != fieldDLCs {
		t.Fatalf("truncation report = %v", decoded.Truncated)
	}
}

func TestArchiveEntryNamesAreReported(t *testing.T) {
	save := buildSave(t, LayoutSplitBinaryZip, fixtureMetadata(), fixtureGamestate())
	envelope, err := Analyze(Bytes(save), DefaultLimits())
	if err != nil {
		t.Fatalf("analyzing: %v", err)
	}
	names, err := envelope.ArchiveEntryNames(Bytes(save))
	if err != nil {
		t.Fatalf("listing entries: %v", err)
	}
	if strings.Join(names, ",") != "meta,gamestate" {
		t.Fatalf("entries = %v", names)
	}

	uncompressed := buildSave(t, LayoutBinaryUncompressed, fixtureMetadata(), fixtureGamestate())
	envelope, err = Analyze(Bytes(uncompressed), DefaultLimits())
	if err != nil {
		t.Fatalf("analyzing: %v", err)
	}
	if names, err := envelope.ArchiveEntryNames(Bytes(uncompressed)); err != nil || names != nil {
		t.Fatalf("an uncompressed save reported entries %v (%v)", names, err)
	}
}

func TestInvalidUTF8IsEncodedNotMangled(t *testing.T) {
	invalid := []byte{0xff, 0xfe}
	raw := append(u16le(lexQuoted), u16le(uint16(len(invalid)))...)
	raw = append(raw, invalid...)
	decoder := NewDecoder(raw, DefaultLimits())
	token, err := decoder.Next()
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got := text(token); got != "0xfffe" {
		t.Fatalf("text = %q, want a hexadecimal form", got)
	}
}
