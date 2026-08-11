package savefile

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// syntheticTextGamestate is syntheticGamestate written out in the text form,
// field for field and in the same order.
//
// The two are kept side by side deliberately: a save says the same thing in
// either encoding, so the scan has to answer the same way, and any divergence
// between the two lexers shows up as a difference between two structs rather
// than as a subtly wrong number nobody checks.
const syntheticTextGamestate = `
# a comment the lexer must skip entirely
traits_lookup={ brave craven gregarious }
landed_titles={
	landed_titles={
		7={
			key="c_alpha"
			holder=42
			date=1066.10.1
			title_name_data={ name="阿尔法郡" }
		}
		8={ key="c_beta" holder=99 }
	}
}
dynasties={
	dynasty_house={
		3={ name="dynn_Alpha" found_date=1066.10.1 head_of_house=42 }
	}
}
living={
	42={
		first_name="Aldric"
		dynasty_house=3
		skill={ 1 2 3 4 5 6 }
		traits={ 0 2 }
	}
	99={ first_name="Other" }
}
played_character={ character=42 }
`

func TestTextAndBinaryGamestatesAnswerAlike(t *testing.T) {
	resolver := gamestateMaps(t)
	limits := DefaultLimits()

	for _, query := range []struct {
		name  string
		query GamestateQuery
	}{
		{"inventory", GamestateQuery{Inventory: true}},
		{"character", GamestateQuery{Character: 42, TitlesHeldBy: 42}},
		{"house", GamestateQuery{House: 3, HouseValid: true}},
	} {
		t.Run(query.name, func(t *testing.T) {
			binary, err := ScanGamestate(bytes.NewReader(syntheticGamestate()), resolver, query.query, limits)
			if err != nil {
				t.Fatalf("binary scan: %v", err)
			}
			text, err := ScanGamestateFor(EncodingText,
				strings.NewReader(syntheticTextGamestate), nil, query.query, limits)
			if err != nil {
				t.Fatalf("text scan: %v", err)
			}
			// Byte counts differ by construction; everything else must not.
			binary.BytesRead, text.BytesRead = 0, 0
			if !reflect.DeepEqual(binary, text) {
				t.Fatalf("the two encodings disagree:\nbinary %+v\ntext   %+v", binary, text)
			}
		})
	}
}

// TestTextGamestateNeedsNoTokenMap pins the reason a text save is worth
// supporting separately: the whole token-map apparatus exists to name binary
// identifiers, and a text save has none to name.
func TestTextGamestateNeedsNoTokenMap(t *testing.T) {
	scan, err := ScanGamestateFor(EncodingText, strings.NewReader(syntheticTextGamestate),
		nil, GamestateQuery{Inventory: true}, DefaultLimits())
	if err != nil {
		t.Fatalf("scanning without a token map: %v", err)
	}
	if scan.PlayedCharacter != 42 {
		t.Errorf("played character = %d", scan.PlayedCharacter)
	}
	if _, err := ScanGamestate(bytes.NewReader(syntheticGamestate()), nil,
		GamestateQuery{Inventory: true}, DefaultLimits()); KindOf(err) != ErrTokenMap {
		t.Fatalf("a binary scan without a token map produced %v", err)
	}
}

func TestTextScalarsAreClassifiedNarrowly(t *testing.T) {
	cases := []struct {
		raw  string
		kind TokenKind
		// check inspects the decoded token when the kind alone is not enough.
		check func(Token) bool
	}{
		{"yes", KindBool, func(tk Token) bool { return tk.Bool }},
		{"no", KindBool, func(tk Token) bool { return !tk.Bool }},
		{"42", KindI64, func(tk Token) bool { return tk.Signed == 42 }},
		{"-7", KindI64, func(tk Token) bool { return tk.Signed == -7 }},
		{"1.9305", KindDecimal, func(tk Token) bool { return textDecimal(tk) == 1.9305 }},
		{"-0.5", KindDecimal, func(tk Token) bool { return textDecimal(tk) == -0.5 }},
		// A date has two dots, so it must never become a decimal.
		{"1066.10.1", KindUnquoted, func(tk Token) bool { return string(tk.Text) == "1066.10.1" }},
		// ParseFloat would take both of these; a save means them literally.
		{"1e5", KindUnquoted, nil},
		{"NaN", KindUnquoted, nil},
		{"tribal_government", KindUnquoted, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.raw, func(t *testing.T) {
			decoder := NewDecoderFor(EncodingText, []byte(testCase.raw), DefaultLimits())
			token, err := decoder.Next()
			if err != nil {
				t.Fatalf("decoding %q: %v", testCase.raw, err)
			}
			if token.Kind != testCase.kind {
				t.Fatalf("%q decoded as kind %d, want %d", testCase.raw, token.Kind, testCase.kind)
			}
			if testCase.check != nil && !testCase.check(token) {
				t.Fatalf("%q decoded to the wrong value: %+v", testCase.raw, token)
			}
			if !decoder.Done() {
				t.Errorf("%q left the decoder unfinished", testCase.raw)
			}
		})
	}
}

func TestTextQuotedScalarsAndComments(t *testing.T) {
	section := "a=\"plain\" b=\"with \\\"quotes\\\" inside\" # trailing comment\nc=\"\"\n"
	decoder := NewDecoderFor(EncodingText, []byte(section), DefaultLimits())
	got := map[string]string{}
	if err := readObject(decoder, nil, func(name string, value Token, d *Decoder) error {
		got[name] = string(value.Text)
		return d.SkipValue(value)
	}); err != nil {
		t.Fatalf("reading: %v", err)
	}
	want := map[string]string{"a": "plain", "b": `with "quotes" inside`, "c": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestTextScalarWiderThanTheLookAhead keeps the streaming decoder honest: a
// scalar far longer than the lexer's look-ahead chunk must be delivered whole,
// not truncated at the chunk boundary and not refused.
func TestTextScalarWiderThanTheLookAhead(t *testing.T) {
	limits := DefaultLimits()
	long := strings.Repeat("x", limits.MaxStringBytes-1)
	for _, form := range []struct {
		name    string
		section string
	}{
		{"quoted", "name=\"" + long + "\"\n"},
		{"unquoted", "name=" + long + "\n"},
	} {
		t.Run(form.name, func(t *testing.T) {
			seen := false
			decoder := NewStreamDecoderFor(EncodingText, strings.NewReader(form.section), limits)
			if err := readObjectStream(decoder, nil, func(name string, value Token, d *StreamDecoder) error {
				seen = true
				if name != "name" {
					t.Errorf("field name = %q", name)
				}
				if len(value.Text) != len(long) {
					t.Errorf("scalar length = %d, want %d", len(value.Text), len(long))
				}
				return d.SkipValue(value)
			}); err != nil {
				t.Fatalf("reading: %v", err)
			}
			if !seen {
				t.Fatal("the field was never visited")
			}
		})
	}
}

func TestTextSectionsRefuseHostileInputWithoutPanicking(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxStringBytes = 1 << 12
	cases := map[string]string{
		"unclosed quote":     `name="never closed`,
		"stray close":        `}`,
		"deep nesting":       strings.Repeat("a={", limits.MaxDepth+8),
		"oversized scalar":   `name="` + strings.Repeat("x", limits.MaxStringBytes+16) + `"`,
		"unterminated block": `living={ 42={ first_name="a"`,
	}
	for name, section := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ScanGamestateFor(EncodingText, strings.NewReader(section), nil,
				GamestateQuery{Inventory: true, Character: 42}, limits)
			if err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

// TestTextFieldNamesAreInterned guards the one place where reading a text save
// could cost memory proportional to the save: a gamestate has millions of
// fields, and naming each one afresh would allocate a string per field.
func TestTextFieldNamesAreInterned(t *testing.T) {
	var table nameTable
	first := table.intern([]byte("first_name"))
	second := table.intern([]byte("first_name"))
	if first != second {
		t.Fatalf("interning returned %q and %q", first, second)
	}
	if unsafe.StringData(first) != unsafe.StringData(second) {
		t.Error("the same name was allocated twice")
	}
	for index := 0; index < maxInternedNames+16; index++ {
		table.intern([]byte(strings.Repeat("n", index%64) + string(rune('a'+index%26)) + itoa(index)))
	}
	if len(table.names) > maxInternedNames {
		t.Fatalf("the name table grew to %d entries, past its %d cap", len(table.names), maxInternedNames)
	}
}
