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
	ID      string   `json:"id"`
	Pattern string   `json:"pattern,omitempty"`
	Colors  [3]Color `json:"colors"`
	Emblems []Emblem `json:"emblems,omitempty"`
	Parent  string   `json:"parent,omitempty"`
	// Inherited names the ancestors folded into this definition, nearest
	// first. Empty for a definition that declares no parent.
	Inherited []string `json:"inherited,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Line      int      `json:"line,omitempty"`
	// setColor records which slots the definition names itself, so inheritance
	// can tell "declared black" from "never mentioned". The parse-time default
	// for an unnamed slot is opaque black, which is indistinguishable from a
	// declared one without this.
	setColor [3]bool
}

// InheritFrom folds parent into d and returns the resolved definition. CK3
// starts a child from its parent's design and then applies the child's own
// declarations, so single-valued keys overwrite and the repeated emblem keys
// append -- a child that adds a charge adds it to the parent's field rather
// than replacing the field with it.
//
// The resolved warnings are recomputed, because they describe the design that
// will actually draw: a child with no pattern of its own is not missing a field
// once its parent has supplied one.
func (d Definition) InheritFrom(parent Definition) Definition {
	out := d
	if out.Pattern == "" {
		out.Pattern = parent.Pattern
	}
	for slot := 0; slot < 3; slot++ {
		if !out.setColor[slot] {
			out.Colors[slot] = parent.Colors[slot]
			out.setColor[slot] = parent.setColor[slot]
		}
	}
	// The parent's emblems are painted under the child's, which is the order
	// they were declared in once the child is read as a continuation of the
	// parent. Copied rather than appended in place: append would write into the
	// parent's backing array and make one resolution visible in another.
	if len(parent.Emblems) > 0 {
		merged := make([]Emblem, 0, len(parent.Emblems)+len(out.Emblems))
		merged = append(merged, parent.Emblems...)
		merged = append(merged, out.Emblems...)
		out.Emblems = merged
	}
	out.Inherited = append(append([]string(nil), parent.ID), parent.Inherited...)
	out.Warnings = definitionWarnings(out)
	return out
}

// AppendWarning records a resolution problem that the definition's own text
// cannot show, such as a parent no active source defines.
func (d *Definition) AppendWarning(warning string) {
	d.Warnings = append(d.Warnings, warning)
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
			definition.setColor[slot] = true
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
	if d.Pattern == "" && d.Parent != "" && len(d.Inherited) > 0 {
		out = append(out, "neither this definition nor any parent declares a pattern: the field is filled with color1")
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
