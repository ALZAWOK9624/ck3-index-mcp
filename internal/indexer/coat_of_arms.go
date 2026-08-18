package indexer

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"sort"
	"strings"

	"ck3-index/internal/coatofarms"
	"ck3-index/internal/script"
)

// A coat of arms is the one kind of CK3 content whose correctness is visual.
// The script says pattern_per_bend_sinister.dds and three colour names, and
// nothing short of drawing it answers whether the result is the design that was
// intended. This wires the pure renderer in internal/coatofarms to the index:
// definitions come from the active script layer, colour names from the active
// named_colors files, and textures from the indexed resources, so a render
// reflects the same load order the game would apply.

const (
	coatOfArmsObjectType  = "coat_of_arm"
	coatOfArmsDefaultSize = 256
	// CoatOfArmsMaxRenderSize is exported because the MCP schema advertises it
	// as the upper bound of the size argument; the two must not drift apart.
	CoatOfArmsMaxRenderSize = 1024
	coatOfArmsAssetsLimit   = 200
	coatOfArmsTextureRoot   = "gfx/coat_of_arms/"
	coatOfArmsNamedColorsD  = "common/named_colors/"
	// Only this folder holds heraldry. Its siblings under common/coat_of_arms/
	// share the indexed object type but describe atlases, template lists and
	// dynamic definitions instead.
	coatOfArmsDefinitionDir = "common/coat_of_arms/coat_of_arms/"
	// A parent chain this deep is already a mistake; the bound exists so a
	// malformed tree cannot make the walk unbounded even if cycle detection is
	// ever weakened.
	coatOfArmsMaxInheritanceDepth = 16
)

// CoatOfArmsSpec selects what to do. It never accepts a filesystem path: an id
// is resolved through the index, exactly like every other object lookup.
type CoatOfArmsSpec struct {
	Operation string `json:"operation,omitempty"`
	ID        string `json:"id,omitempty"`
	Size      int    `json:"size,omitempty"`
	Filter    string `json:"filter,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// CoatOfArmsTextureRef records where one referenced texture resolved, so a
// caller can tell "the mod supplies this" from "vanilla supplies this" from
// "nobody supplies this".
type CoatOfArmsTextureRef struct {
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`
	Source   string `json:"source,omitempty"`
	Resolved bool   `json:"resolved"`
}

// CoatOfArmsAsset is one texture available to build a design from.
type CoatOfArmsAsset struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

// CoatOfArmsResult is the tool response. PNG is carried out of band and is
// removed before the structured content is encoded.
type CoatOfArmsResult struct {
	Intent     string                   `json:"intent"`
	Summary    string                   `json:"summary"`
	Operation  string                   `json:"operation"`
	ID         string                   `json:"id,omitempty"`
	Source     string                   `json:"source,omitempty"`
	Path       string                   `json:"path,omitempty"`
	Definition *coatofarms.Definition   `json:"definition,omitempty"`
	Textures   []CoatOfArmsTextureRef   `json:"textures,omitempty"`
	Render     *coatofarms.RenderResult `json:"render,omitempty"`
	Assets     []CoatOfArmsAsset        `json:"assets,omitempty"`
	AssetTotal int                      `json:"asset_total,omitempty"`
	Truncated  bool                     `json:"truncated,omitempty"`
	Redacted   bool                     `json:"redacted,omitempty"`
	// PublicAssetsOnly reports that colours and textures were resolved from
	// public sources alone, and RedactedTextures names the referenced textures
	// whose winning source was private and was therefore skipped. A public
	// render is the public view of the design, not necessarily what the game
	// loads, and it has to say so rather than pass for the live answer.
	PublicAssetsOnly bool     `json:"public_assets_only,omitempty"`
	RedactedTextures []string `json:"redacted_textures,omitempty"`
	Guidance         []string `json:"guidance,omitempty"`
	PNG              []byte   `json:"-"`
}

// LLMCoatOfArms runs one coat-of-arms operation.
func (db *DB) LLMCoatOfArms(ctx context.Context, spec CoatOfArmsSpec, opts LLMOptions) (CoatOfArmsResult, error) {
	operation := strings.TrimSpace(strings.ToLower(spec.Operation))
	if operation == "" {
		operation = "inspect"
	}
	switch operation {
	case "inspect", "render":
		if strings.TrimSpace(spec.ID) == "" {
			return CoatOfArmsResult{}, fmt.Errorf("coat of arms %s requires an id", operation)
		}
		return db.coatOfArmsForID(ctx, operation, spec, opts)
	case "assets":
		return db.coatOfArmsAssetList(ctx, spec, opts)
	default:
		return CoatOfArmsResult{}, fmt.Errorf("unknown coat of arms operation %q; use inspect, render or assets", operation)
	}
}

func (db *DB) coatOfArmsForID(ctx context.Context, operation string, spec CoatOfArmsSpec, opts LLMOptions) (CoatOfArmsResult, error) {
	id := strings.TrimSpace(spec.ID)
	result := CoatOfArmsResult{Intent: "coat_of_arms_" + operation, Operation: operation, ID: id}

	location, err := db.coatOfArmsLocation(ctx, id)
	if err != nil {
		return result, err
	}
	// A definition is script content from its source layer, and a rendered
	// design is that content made legible. Public visibility withholds both when
	// the layer is private, the same way evidence from a private source is
	// withheld everywhere else; the miss is reported rather than disguised as
	// "no such coat of arms".
	if location.path != "" && opts.publicMode() && opts.sourceIsPrivate(location.source) {
		result.Redacted = true
		result.Summary = fmt.Sprintf("%q is defined in a private source; public visibility withholds its definition and its render.", id)
		result.Guidance = []string{
			"Call again with visibility=private to read a coat of arms defined in the project layer.",
		}
		return result, nil
	}
	if location.path == "" {
		result.Summary = fmt.Sprintf("No active coat of arms is defined as %q.", id)
		result.Guidance = []string{
			"Search for the id with ck3_search kind=object and object_type=" + coatOfArmsObjectType + " before assuming it is absent.",
			"An overridden definition is not active: a higher-priority source with the same file path replaces it entirely.",
		}
		return result, nil
	}
	result.Source, result.Path = location.source, location.rel
	result.PublicAssetsOnly = opts.publicMode()

	palette, paletteWithheld, err := db.coatOfArmsPalette(ctx, opts)
	if err != nil {
		return result, err
	}
	declared, err := db.readCoatOfArmsDefinition(location, id, palette)
	if err != nil {
		return result, err
	}
	resolved, chain, err := db.resolveCoatOfArmsParents(ctx, *declared, palette, opts)
	if err != nil {
		return result, err
	}
	// An inherited design is its parent's script with a few fields changed, so
	// resolving a public child against a private parent would hand back the
	// private definition under the child's name -- and render it. The boundary
	// is the same one the requested definition already answers to.
	if chain.privateParent != "" {
		result.Redacted = true
		result.Summary = fmt.Sprintf("%q inherits from %q, which is defined in a private source; public visibility withholds the resolved definition and its render.",
			id, chain.privateParent)
		result.Guidance = []string{
			"Call again with visibility=private to resolve a coat of arms whose parent is defined in the project layer.",
		}
		return result, nil
	}
	definition := &resolved
	result.Definition = definition

	textures, resolution, withheldTextures, err := db.coatOfArmsTextures(ctx, opts)
	if err != nil {
		return result, err
	}
	for _, name := range definition.TextureReferences() {
		ref := CoatOfArmsTextureRef{Name: name}
		if found, ok := resolution[strings.ToLower(name)]; ok {
			ref.Resolved, ref.Source, ref.Kind = true, found.source, found.kind
		}
		if withheldTextures[strings.ToLower(name)] {
			result.RedactedTextures = append(result.RedactedTextures, name)
		}
		result.Textures = append(result.Textures, ref)
	}
	publicAssetNotes := coatOfArmsPublicAssetNotes(opts, paletteWithheld, result.RedactedTextures)

	if operation == "inspect" {
		result.Summary = coatOfArmsInspectSummary(*definition, result.Textures)
		result.Guidance = []string{
			"Colours are resolved against the active named_colors files; an unresolved name renders as black in game and is reported in definition.warnings rather than substituted.",
			"Call this tool again with operation=render to see the design rather than read it.",
		}
		if len(definition.Inherited) > 0 {
			result.Guidance = append(result.Guidance,
				"This is the resolved design: definition.inherited lists the parents folded in, nearest first, and every field shown is the one that draws.")
		}
		result.Guidance = append(result.Guidance, publicAssetNotes...)
		return result, nil
	}

	size := spec.Size
	if size <= 0 {
		size = coatOfArmsDefaultSize
	}
	if size > CoatOfArmsMaxRenderSize {
		return result, fmt.Errorf("coat of arms render size %d exceeds the %d limit", size, CoatOfArmsMaxRenderSize)
	}
	rendered, err := coatofarms.Render(*definition, textures, coatofarms.RenderOptions{Size: size})
	if err != nil {
		return result, err
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, rendered.Image); err != nil {
		return result, err
	}
	rendered.Image = nil
	result.Render = &rendered
	result.PNG = encoded.Bytes()
	result.Summary = fmt.Sprintf("Rendered %s at %dx%d from %s: %d emblem(s) in %d instance(s).",
		id, size, size, location.rel, rendered.EmblemsDrawn, rendered.InstancesDrawn)
	result.Guidance = []string{
		"This is the field render CK3 composites before it applies the frame, material and dirt overlays, so the shape of the shield and its border are absent by design.",
		"A texture the index does not hold is named in render.missing_textures and skipped; the rest of the design is still drawn.",
	}
	result.Guidance = append(result.Guidance, publicAssetNotes...)
	return result, nil
}

// coatOfArmsPublicAssetNotes says what public visibility left out. A render
// drawn from public assets alone is a legitimate answer, but it is not the one
// the game composites when a private layer supplies the winning texture or the
// colour name, and the difference is invisible in the picture.
func coatOfArmsPublicAssetNotes(opts LLMOptions, paletteWithheld int, redactedTextures []string) []string {
	if !opts.publicMode() {
		return nil
	}
	var out []string
	if len(redactedTextures) > 0 {
		out = append(out, fmt.Sprintf("%d referenced texture(s) are supplied by a private source and were withheld: the highest-priority public texture was used instead, so this is not necessarily what the game draws.", len(redactedTextures)))
	}
	if paletteWithheld > 0 {
		out = append(out, "Colours were resolved from public named_colors files only; a name defined solely in a private source reads here as unresolved rather than being read out of that source.")
	}
	return out
}

// readCoatOfArmsDefinition parses one located file and returns the definition
// it was located for.
func (db *DB) readCoatOfArmsDefinition(location coatOfArmsLocation, id string, palette coatofarms.Palette) (*coatofarms.Definition, error) {
	data, err := os.ReadFile(location.path)
	if err != nil {
		return nil, fmt.Errorf("read coat of arms file %q: %w", location.rel, err)
	}
	for _, candidate := range coatofarms.Parse(script.ParseBytes(data).Nodes, palette) {
		if strings.EqualFold(candidate.ID, id) {
			found := candidate
			return &found, nil
		}
	}
	return nil, fmt.Errorf("the index locates %q in %s but the file no longer defines it; run ck3_refresh", id, location.rel)
}

// coatOfArmsChain records what the parent walk found beyond the folded design.
type coatOfArmsChain struct {
	// privateParent names the first ancestor public visibility withholds. It is
	// the child's own declared text, so naming it discloses nothing the child
	// does not already say.
	privateParent string
	warnings      []string
}

// resolveCoatOfArmsParents folds the parent chain into definition.
//
// A definition that declares a parent is not the design that draws: read alone
// it has no pattern, renders as a flat color1 field, and reports itself as a
// finished answer. Each link is resolved through the same index lookup as the
// requested id, so load order, overridden files and the evidence boundary apply
// to an inherited definition exactly as they do to a directly requested one.
func (db *DB) resolveCoatOfArmsParents(ctx context.Context, definition coatofarms.Definition, palette coatofarms.Palette, opts LLMOptions) (coatofarms.Definition, coatOfArmsChain, error) {
	var chain coatOfArmsChain
	visited := map[string]bool{strings.ToLower(strings.TrimSpace(definition.ID)): true}
	var ancestors []coatofarms.Definition
	current := definition
	for depth := 0; strings.TrimSpace(current.Parent) != ""; depth++ {
		parentID := strings.TrimSpace(current.Parent)
		if depth >= coatOfArmsMaxInheritanceDepth {
			chain.warnings = append(chain.warnings, fmt.Sprintf("the parent chain is more than %d definitions deep; the rest was not resolved", coatOfArmsMaxInheritanceDepth))
			break
		}
		if visited[strings.ToLower(parentID)] {
			chain.warnings = append(chain.warnings, fmt.Sprintf("the parent chain returns to %q, so the inheritance is circular; it was cut there and the fields above it are missing", parentID))
			break
		}
		visited[strings.ToLower(parentID)] = true
		location, err := db.coatOfArmsLocation(ctx, parentID)
		if err != nil {
			return definition, chain, err
		}
		if location.path == "" {
			chain.warnings = append(chain.warnings, fmt.Sprintf("parent %q is not defined by any active source; the pattern, colours and emblems it would supply are missing", parentID))
			break
		}
		if opts.publicMode() && opts.sourceIsPrivate(location.source) {
			chain.privateParent = parentID
			return definition, chain, nil
		}
		parent, err := db.readCoatOfArmsDefinition(location, parentID, palette)
		if err != nil {
			return definition, chain, err
		}
		ancestors = append(ancestors, *parent)
		current = *parent
	}
	resolved := definition
	if len(ancestors) > 0 {
		// Fold from the furthest ancestor down, so each level overrides the one
		// above it and the requested definition overrides them all.
		folded := ancestors[len(ancestors)-1]
		for i := len(ancestors) - 2; i >= 0; i-- {
			folded = ancestors[i].InheritFrom(folded)
		}
		resolved = definition.InheritFrom(folded)
	}
	for _, warning := range chain.warnings {
		resolved.AppendWarning(warning)
	}
	return resolved, chain, nil
}

func coatOfArmsInspectSummary(definition coatofarms.Definition, textures []CoatOfArmsTextureRef) string {
	missing := 0
	for _, texture := range textures {
		if !texture.Resolved {
			missing++
		}
	}
	summary := fmt.Sprintf("%s: pattern %s, colours %s/%s/%s, %d emblem(s)",
		definition.ID, orNone(definition.Pattern),
		definition.Colors[0].Hex, definition.Colors[1].Hex, definition.Colors[2].Hex,
		len(definition.Emblems))
	if missing > 0 {
		summary += fmt.Sprintf("; %d referenced texture(s) are not indexed", missing)
	}
	if len(definition.Warnings) > 0 {
		summary += fmt.Sprintf("; %d warning(s)", len(definition.Warnings))
	}
	return summary + "."
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

type coatOfArmsLocation struct {
	source string
	rel    string
	path   string
}

// coatOfArmsLocation finds the active definition.
//
// The path filter is not redundant with the object type. Everything under
// common/coat_of_arms/ indexes as coat_of_arm, including options/atlases.txt,
// template_lists/ and dynamic_definitions/, none of which describe heraldry —
// without the filter, an id like "atlas" resolves and renders as an empty black
// field that looks like a real answer.
//
// Overridden files are excluded because a definition inside one is not what the
// game loads.
func (db *DB) coatOfArmsLocation(ctx context.Context, id string) (coatOfArmsLocation, error) {
	var out coatOfArmsLocation
	row := db.sql.QueryRowContext(ctx, `SELECT o.source_name,f.rel_path,f.path
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE o.object_type=? AND lower(o.name)=lower(?) AND f.overridden=0
			AND f.rel_path LIKE ?
		ORDER BY o.source_rank LIMIT 1`, coatOfArmsObjectType, strings.TrimSpace(id), coatOfArmsDefinitionDir+"%")
	if err := row.Scan(&out.source, &out.rel, &out.path); err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return coatOfArmsLocation{}, nil
		}
		return coatOfArmsLocation{}, err
	}
	return out, nil
}

// coatOfArmsPalette merges every active named_colors file. CK3 combines the
// colors = {} blocks across files, so all of them are read rather than only the
// highest-priority one, and a lower-priority file still supplies names the
// others never mention.
//
// Public visibility reads only the public files and reports how many it left
// out. A named colour is script content from its source layer like any other,
// and resolving it into a hex value -- let alone into pixels -- would carry that
// content past the boundary in a form no redaction downstream can recognise.
func (db *DB) coatOfArmsPalette(ctx context.Context, opts LLMOptions) (coatofarms.Palette, int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT path,source_name FROM files
		WHERE kind='script' AND overridden=0 AND rel_path LIKE ?
		ORDER BY source_rank DESC`, coatOfArmsNamedColorsD+"%")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	publicOnly := opts.publicMode()
	withheld := 0
	palette := coatofarms.Palette{}
	for rows.Next() {
		var path, source string
		if err := rows.Scan(&path, &source); err != nil {
			return nil, 0, err
		}
		if publicOnly && opts.sourceIsPrivate(source) {
			withheld++
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		palette.ParseNamedColors(script.ParseBytes(data).Nodes)
	}
	return palette, withheld, rows.Err()
}

type coatOfArmsTextureOrigin struct {
	source string
	kind   string
}

// coatOfArmsTextures builds the texture library from the indexed resources.
// Lower source rank wins, which is the same precedence the index applies to
// script files, so a mod's replacement emblem is the one that draws.
//
// Public visibility skips private sources and re-resolves each name to the
// highest-priority public texture, returning the names whose real winner it
// withheld. A texture is artwork from its source layer, and a render encodes it
// into pixels: drawing with the private winner would put private content into a
// public answer in the one form the structured redaction cannot see.
func (db *DB) coatOfArmsTextures(ctx context.Context, opts LLMOptions) (*coatofarms.PathTextures, map[string]coatOfArmsTextureOrigin, map[string]bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT resource_path,source_name,path FROM resources
		WHERE resource_path LIKE ? ORDER BY source_rank`, coatOfArmsTextureRoot+"%")
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	publicOnly := opts.publicMode()
	paths := map[string]string{}
	origins := map[string]coatOfArmsTextureOrigin{}
	withheld := map[string]bool{}
	for rows.Next() {
		var resourcePath, source, path string
		if err := rows.Scan(&resourcePath, &source, &path); err != nil {
			return nil, nil, nil, err
		}
		if !strings.HasSuffix(strings.ToLower(resourcePath), ".dds") {
			continue
		}
		name := strings.ToLower(resourcePath[strings.LastIndex(resourcePath, "/")+1:])
		if _, taken := paths[name]; taken {
			continue
		}
		if publicOnly && opts.sourceIsPrivate(source) {
			// Rows arrive in load order, so reaching one before any public
			// texture of that name means the private layer is the winner.
			// Keep scanning: a public source further down still supplies
			// something to draw with, and that is the public view of the design.
			withheld[name] = true
			continue
		}
		paths[name] = path
		origins[name] = coatOfArmsTextureOrigin{source: source, kind: coatOfArmsTextureKind(resourcePath)}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	return coatofarms.NewPathTextures(paths), origins, withheld, nil
}

func coatOfArmsTextureKind(resourcePath string) string {
	lower := strings.ToLower(resourcePath)
	switch {
	case strings.Contains(lower, "/patterns/"):
		return "pattern"
	case strings.Contains(lower, "/colored_emblems/"):
		return "colored_emblem"
	case strings.Contains(lower, "/textured_emblems/"):
		return "textured_emblem"
	default:
		return "other"
	}
}

// coatOfArmsAssetList enumerates the textures a definition may reference.
//
// It takes the caller's options for the same reason every other view does: the
// listing names files and the sources that supply them, so under public
// visibility it must enumerate the public layers alone. Listing them without
// the options was a way to read a private layer's contents through a tool that
// never asks for an id.
func (db *DB) coatOfArmsAssetList(ctx context.Context, spec CoatOfArmsSpec, opts LLMOptions) (CoatOfArmsResult, error) {
	result := CoatOfArmsResult{Intent: "coat_of_arms_assets", Operation: "assets", PublicAssetsOnly: opts.publicMode()}
	_, origins, withheld, err := db.coatOfArmsTextures(ctx, opts)
	if err != nil {
		return result, err
	}
	filter := strings.ToLower(strings.TrimSpace(spec.Filter))
	limit := spec.Limit
	if limit <= 0 || limit > coatOfArmsAssetsLimit {
		limit = coatOfArmsAssetsLimit
	}
	assets := make([]CoatOfArmsAsset, 0, len(origins))
	for name, origin := range origins {
		if origin.kind == "other" {
			continue
		}
		if filter != "" && !strings.Contains(name, filter) && !strings.Contains(origin.kind, filter) {
			continue
		}
		assets = append(assets, CoatOfArmsAsset{Name: name, Kind: origin.kind, Source: origin.source})
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].Kind != assets[j].Kind {
			return assets[i].Kind < assets[j].Kind
		}
		return assets[i].Name < assets[j].Name
	})
	result.AssetTotal = len(assets)
	if len(assets) > limit {
		assets = assets[:limit]
		result.Truncated = true
	}
	result.Assets = assets
	result.Summary = fmt.Sprintf("%d coat of arms texture(s) available%s; showing %d.",
		result.AssetTotal, filterSuffix(filter), len(assets))
	result.Guidance = []string{
		"A name listed here is what a definition writes in pattern or texture; the leading directory is supplied by CK3 and is never written in script.",
		"Only the highest-priority source for each file name is listed, because that is the one the game loads.",
	}
	if opts.publicMode() && len(withheld) > 0 {
		result.Guidance = append(result.Guidance,
			fmt.Sprintf("%d texture name(s) whose winning source is private were withheld or listed from a lower-priority public source; call again with visibility=private for the set the game actually loads.", len(withheld)))
	}
	return result, nil
}

func filterSuffix(filter string) string {
	if filter == "" {
		return ""
	}
	return fmt.Sprintf(" matching %q", filter)
}
