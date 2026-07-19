package indexer

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// normalizeGUIRuntimeColor accepts only bounded color literals that can be
// reproduced safely in CSS. CK3 vector colors use normalized RGBA channels;
// HTML-style hexadecimal colors are accepted for caller-provided facts.
func normalizeGUIRuntimeColor(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if strings.HasPrefix(value, "#") {
		hex := strings.ToLower(value)
		switch len(hex) {
		case 4, 5, 7, 9:
		default:
			return "", false
		}
		for _, char := range hex[1:] {
			if !strings.ContainsRune("0123456789abcdef", char) {
				return "", false
			}
		}
		return hex, true
	}
	fields := strings.Fields(strings.NewReplacer(",", " ", ";", " ").Replace(value))
	if len(fields) != 3 && len(fields) != 4 {
		return "", false
	}
	channels := []float64{0, 0, 0, 1}
	for index, field := range fields {
		channel, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsNaN(channel) || math.IsInf(channel, 0) || channel < 0 || channel > 1 {
			return "", false
		}
		channels[index] = channel
	}
	return fmt.Sprintf("rgba(%d,%d,%d,%s)",
		int(math.Round(channels[0]*255)),
		int(math.Round(channels[1]*255)),
		int(math.Round(channels[2]*255)),
		strconv.FormatFloat(channels[3], 'f', 3, 64)), true
}
