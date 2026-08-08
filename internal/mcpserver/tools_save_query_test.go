package mcpserver

import "testing"

func TestParseCharacterIDRequiresWholePositiveDecimal(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  int64
		ok    bool
	}{
		{input: "3758", want: 3758, ok: true},
		{input: " 3758 ", want: 3758, ok: true},
		{input: "3758junk"},
		{input: "0"},
		{input: "-1"},
		{input: ""},
	} {
		test := test
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			got, err := parseCharacterID(test.input)
			if test.ok {
				if err != nil || got != test.want {
					t.Fatalf("parseCharacterID(%q) = %d, %v; want %d, nil", test.input, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseCharacterID(%q) = %d, nil; want rejection", test.input, got)
			}
		})
	}
}
