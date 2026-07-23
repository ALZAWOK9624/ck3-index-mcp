package script

import (
	"strconv"
	"strings"
	"testing"
)

func benchmarkScript(size int) string {
	var builder strings.Builder
	builder.Grow(size + 256)
	for index := 0; builder.Len() < size; index++ {
		builder.WriteString("fixture_")
		builder.WriteString(strconv.Itoa(index))
		builder.WriteString(" = { trigger = { always = yes } effect = { add_gold = 5 } value = \"benchmark\" }\n")
	}
	return builder.String()
}

func benchmarkLex(b *testing.B, size int) {
	source := benchmarkScript(size)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = Lex(source)
	}
}

func benchmarkParse(b *testing.B, size int) {
	source := benchmarkScript(size)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = Parse(source)
	}
}

func BenchmarkLexScript100KB(b *testing.B)   { benchmarkLex(b, 100<<10) }
func BenchmarkLexScript1MB(b *testing.B)     { benchmarkLex(b, 1<<20) }
func BenchmarkParseScript100KB(b *testing.B) { benchmarkParse(b, 100<<10) }
func BenchmarkParseScript1MB(b *testing.B)   { benchmarkParse(b, 1<<20) }
