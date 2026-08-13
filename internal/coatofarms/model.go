package coatofarms

import (
	"image/color"
	"sort"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

// Definition is one coat of arms as written in
// common/coat_of_arms/coat_of_arms/. The three top-level colours are the
// palette every emblem draws from unless it names its own.
type Definition struct {
	ID       string   `json:"id"`
	Pattern  string   `json:"pattern,omitempty"`
	Colors   [3]Color `json:"colors"`
	Emblems  []Emblem `json:"emblems,omitempty"`
	Parent   string   `json:"parent,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Line     int      `json:"line,omitempty"`
}

// Emblem is one colored_emblem or textured_emblem entry. A textured emblem
// keeps its own artwork colours; a colored emblem is recoloured from the three
// colours named here, falling back to the definition's when it names none.
type Emblem struct {
	Texture  string     `json:"texture,omitempty"`
	Textured bool       `json:"textured,omitempty"`
	Colors   [3]Color   `json:"colors"`
	setColor [3]bool    `json:"-"`
	Instance []Instance `json:"instances,omitempty"`
}

// Instance places one copy of an emblem. CK3 defaults a missing instance to a
// single copy filling the field, which is why an emblem with no instance block
// still draws.
type Instance struct {
	Position [2]float64 `json:"position"`
	Scale    [2]float64 `json:"scale"`
	Rotation float64    `json:"rotation,omitempty"`
	Depth    float64    `json:"depth,omitempty"`
}

func defaultInstance() Instance {
	return Instance{Position: [2]float64{0.5, 0.5}, Scale: [2]float64{1, 1}}
}

// Parse reads every coat of arms defined in one parsed file. Colour names are
// resolved against palette; a name the palette does not hold is preserved
// unresolved and reported in Warnings rather than silently drawn as black.
func Parse(nodes []*script.Node, palette Palette) []Definition {
	var out []Definition
	for _, node := range nodes {
		if node.Kind != "block" || node.Key == "" {
			continue
		}
		out = append(out, parseDefinition(node, palette))
	}
	return out
}

func parseDefinition(node *script.Node, palette Palette) Definition {
	definition := Definition{ID: node.Key, Line: node.Line}
	// A definition may name fewer than three colours; vanilla dynasties often
	// stop at two. The unnamed slots have to start as opaque black rather than
	// as the zero Color, whose alpha of 0 would make the field transparent
	// wherever the pattern selected that slot.
	for slot := range definition.Colors {
		definition.Colors[slot] = newColor(ColorNotationNamed, "", color.NRGBA{A: 255})
	}
	for index := 0; index < len(node.Children); index++ {
		child := node.Children[index]
		key := strings.ToLower(child.Key)
		switch {
		case key == "pattern":
			definition.Pattern = unquote(child.Value)
		case key == "parent":
			definition.Parent = unquote(child.Value)
		case key == "color1" || key == "color2" || key == "color3":
			slot := int(key[len(key)-1] - '1')
			resolved, consumed, ok := parseColorAt(node.Children, index, palette)
			if !ok {
				continue
			}
			definition.Colors[slot] = resolved
			index += consumed
		case key == "colored_emblem" || key == "textured_emblem":
			if child.Kind != "block" {
				continue
			}
			definition.Emblems = append(definition.Emblems, parseEmblem(child, palette, key == "textured_emblem"))
		}
	}
	definition.Warnings = definitionWarnings(definition)
	return definition
}

func parseEmblem(node *script.Node, palette Palette, textured bool) Emblem {
	emblem := Emblem{Textured: textured}
	for index := 0; index < len(node.Children); index++ {
		child := node.Children[index]
		key := strings.ToLower(child.Key)
		switch {
		case key == "texture":
			emblem.Texture = unquote(child.Value)
		case key == "color1" || key == "color2" || key == "color3":
			slot := int(key[len(key)-1] - '1')
			resolved, consumed, ok := parseColorAt(node.Children, index, palette)
			if !ok {
				continue
			}
			emblem.Colors[slot] = resolved
			emblem.setColor[slot] = true
			index += consumed
		case key == "instance" && child.Kind == "block":
			emblem.Instance = append(emblem.Instance, parseInstance(child))
		}
	}
	// CK3 draws one full-field copy when no instance is given.
	if len(emblem.Instance) == 0 {
		emblem.Instance = []Instance{defaultInstance()}
	}
	return emblem
}

func parseInstance(node *script.Node) Instance {
	instance := defaultInstance()
	for _, child := range node.Children {
		switch strings.ToLower(child.Key) {
		case "position":
			if pair, ok := numericPair(child); ok {
				instance.Position = pair
			}
		case "scale":
			if pair, ok := numericPair(child); ok {
				instance.Scale = pair
			}
		case "rotation":
			if value, err := strconv.ParseFloat(strings.TrimSpace(child.Value), 64); err == nil {
				instance.Rotation = value
			}
		case "depth":
			if value, err := strconv.ParseFloat(strings.TrimSpace(child.Value), 64); err == nil {
				instance.Depth = value
			}
		}
	}
	return instance
}

func numericPair(node *script.Node) ([2]float64, bool) {
	var out [2]float64
	if node.Kind != "block" || len(node.Children) != 2 {
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

func unquote(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"`)
}

// EffectiveColors resolves an emblem's palette against its definition: a slot
// the emblem does not name falls back to the definition's colour for that slot,
// which is how most vanilla emblems stay in step with their field.
func (d Definition) EffectiveColors(e Emblem) [3]Color {
	out := e.Colors
	for slot := 0; slot < 3; slot++ {
		if !e.setColor[slot] {
			out[slot] = d.Colors[slot]
		}
	}
	return out
}

// UnresolvedColorNames lists every colour name the palette did not hold. These
// render as black in game, so a definition that names a colour a mod forgot to
// define looks deliberate and is easy to miss.
func (d Definition) UnresolvedColorNames() []string {
	seen := map[string]bool{}
	collect := func(colors [3]Color) {
		for _, c := range colors {
			if c.Notation == ColorNotationNamed && c.Hex == "" && c.Name != "" {
				seen[c.Name] = true
			}
		}
	}
	collect(d.Colors)
	for _, emblem := range d.Emblems {
		collect(emblem.Colors)
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TextureReferences lists every texture the definition needs, pattern first.
func (d Definition) TextureReferences() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	add(d.Pattern)
	for _, emblem := range d.Emblems {
		add(emblem.Texture)
	}
	return out
}

func definitionWarnings(d Definition) []string {
	var out []string
	if d.Pattern == "" && d.Parent == "" {
		out = append(out, "no pattern and no parent: CK3 has nothing to draw the field from")
	}
	for _, emblem := range d.Emblems {
		if emblem.Texture == "" {
			out = append(out, "an emblem entry declares no texture and cannot draw")
			break
		}
	}
	if names := d.UnresolvedColorNames(); len(names) > 0 {
		out = append(out, "colour name(s) not defined in any indexed named_colors file: "+strings.Join(names, ", "))
	}
	return out
}
