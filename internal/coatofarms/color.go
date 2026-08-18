package coatofarms

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

// CK3 writes a colour four ways: a name defined in common/named_colors, and
// three literal notations that each read the same three numbers differently.
// Getting the notation wrong does not fail, it just produces a different
// colour, so the notation is carried explicitly rather than guessed from the
// values.
type ColorNotation string

const (
	ColorNotationNamed  ColorNotation = "named"
	ColorNotationRGB    ColorNotation = "rgb"
	ColorNotationHSV    ColorNotation = "hsv"
	ColorNotationHSV360 ColorNotation = "hsv360"
	// ColorNotationList is a bare { a b c } with no keyword, which is how
	// named_colors files spell most of their entries.
	ColorNotationList ColorNotation = "list"
)

// Color is one resolved colour together with how it was written, so a report
// can quote the source spelling rather than only the resulting bytes.
type Color struct {
	Notation ColorNotation `json:"notation"`
	Name     string        `json:"name,omitempty"`
	RGBA     color.NRGBA   `json:"-"`
	Hex      string        `json:"hex"`
}

func newColor(notation ColorNotation, name string, rgba color.NRGBA) Color {
	return Color{
		Notation: notation,
		Name:     name,
		RGBA:     rgba,
		Hex:      fmt.Sprintf("#%02x%02x%02x", rgba.R, rgba.G, rgba.B),
	}
}

// Palette resolves the colour names a definition refers to. It is loaded from
// common/named_colors, which mods extend and override like any other folder.
type Palette map[string]color.NRGBA

// ParseNamedColors reads the colors = { ... } blocks of a named_colors file and
// adds them to the palette. Later files override earlier ones, matching the
// per-key merge CK3 applies to this folder.
func (p Palette) ParseNamedColors(nodes []*script.Node) {
	for _, node := range nodes {
		if node.Kind != "block" || !strings.EqualFold(node.Key, "colors") {
			continue
		}
		for index := 0; index < len(node.Children); index++ {
			child := node.Children[index]
			if child.Key == "" {
				continue
			}
			resolved, consumed, ok := parseColorAt(node.Children, index, nil)
			if !ok {
				continue
			}
			p[strings.ToLower(child.Key)] = resolved.RGBA
			index += consumed
		}
	}
}

// parseColorAt reads the colour beginning at nodes[index]. It returns how many
// extra siblings it consumed, because the parser splits `color1 = rgb { 1 2 3 }`
// into an atom holding the keyword and a following anonymous block holding the
// components; reading only the atom would silently yield the keyword as a name.
func parseColorAt(nodes []*script.Node, index int, palette Palette) (Color, int, bool) {
	node := nodes[index]
	switch node.Kind {
	case "block":
		// A bare { a b c } directly under the key.
		values, ok := numericChildren(node)
		if !ok {
			return Color{}, 0, false
		}
		return newColor(ColorNotationList, "", listColor(values)), 0, true
	case "atom":
		keyword := strings.ToLower(strings.TrimSpace(node.Value))
		switch keyword {
		case "rgb", "hsv", "hsv360":
			if index+1 >= len(nodes) {
				return Color{}, 0, false
			}
			block := nodes[index+1]
			if block.Kind != "block" || block.Key != "" {
				return Color{}, 0, false
			}
			values, ok := numericChildren(block)
			if !ok {
				return Color{}, 0, false
			}
			switch keyword {
			case "rgb":
				return newColor(ColorNotationRGB, "", listColor(values)), 1, true
			case "hsv":
				return newColor(ColorNotationHSV, "", hsvColor(values[0], values[1], values[2])), 1, true
			default:
				return newColor(ColorNotationHSV360, "", hsvColor(values[0]/360, values[1]/100, values[2]/100)), 1, true
			}
		default:
			if keyword == "" {
				return Color{}, 0, false
			}
			rgba, known := palette[keyword]
			if !known {
				// An unresolved name is still reported, so the caller can say
				// which name is missing instead of rendering a silent black.
				return Color{Notation: ColorNotationNamed, Name: keyword}, 0, true
			}
			return newColor(ColorNotationNamed, keyword, rgba), 0, true
		}
	}
	return Color{}, 0, false
}

func numericChildren(node *script.Node) ([3]float64, bool) {
	var out [3]float64
	if len(node.Children) != 3 {
		return out, false
	}
	for i, child := range node.Children {
		text := strings.TrimSpace(child.Key)
		if text == "" {
			text = strings.TrimSpace(child.Value)
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return out, false
		}
		out[i] = value
	}
	return out, true
}

// listColor reads three numbers that are either 0..1 floats or 0..255 bytes.
// CK3 distinguishes them by whether any component exceeds 1, which is why
// { 1 1 1 } is white in both readings and never ambiguous in practice.
func listColor(values [3]float64) color.NRGBA {
	scale := 255.0
	if values[0] > 1 || values[1] > 1 || values[2] > 1 {
		scale = 1
	}
	return color.NRGBA{
		R: clampByte(values[0] * scale),
		G: clampByte(values[1] * scale),
		B: clampByte(values[2] * scale),
		A: 255,
	}
}

func hsvColor(h, s, v float64) color.NRGBA {
	h = math.Mod(h, 1)
	if h < 0 {
		h++
	}
	s = clampUnit(s)
	v = clampUnit(v)
	if s == 0 {
		grey := clampByte(v * 255)
		return color.NRGBA{R: grey, G: grey, B: grey, A: 255}
	}
	sector := h * 6
	index := int(sector)
	fraction := sector - float64(index)
	p := v * (1 - s)
	q := v * (1 - s*fraction)
	t := v * (1 - s*(1-fraction))
	var r, g, b float64
	switch index % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return color.NRGBA{R: clampByte(r * 255), G: clampByte(g * 255), B: clampByte(b * 255), A: 255}
}

func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
