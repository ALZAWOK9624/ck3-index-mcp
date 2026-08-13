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
	Guidance   []string                 `json:"guidance,omitempty"`
	PNG        []byte                   `json:"-"`
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
		return db.coatOfArmsAssetList(ctx, spec)
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

	palette, err := db.coatOfArmsPalette(ctx)
	if err != nil {
		return result, err
	}
	data, err := os.ReadFile(location.path)
	if err != nil {
		return result, fmt.Errorf("read coat of arms file %q: %w", location.rel, err)
	}
	var definition *coatofarms.Definition
	for _, candidate := range coatofarms.Parse(script.ParseBytes(data).Nodes, palette) {
		if strings.EqualFold(candidate.ID, id) {
			found := candidate
			definition = &found
			break
		}
	}
	if definition == nil {
		return result, fmt.Errorf("the index locates %q in %s but the file no longer defines it; run ck3_refresh", id, location.rel)
	}
	result.Definition = definition

	textures, resolution, err := db.coatOfArmsTextures(ctx)
	if err != nil {
		return result, err
	}
	for _, name := range definition.TextureReferences() {
		ref := CoatOfArmsTextureRef{Name: name}
		if found, ok := resolution[strings.ToLower(name)]; ok {
			ref.Resolved, ref.Source, ref.Kind = true, found.source, found.kind
		}
		result.Textures = append(result.Textures, ref)
	}

	if operation == "inspect" {
		result.Summary = coatOfArmsInspectSummary(*definition, result.Textures)
		result.Guidance = []string{
			"Colours are resolved against the active named_colors files; an unresolved name renders as black in game and is reported in definition.warnings rather than substituted.",
			"Call this tool again with operation=render to see the design rather than read it.",
		}
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
	return result, nil
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
func (db *DB) coatOfArmsPalette(ctx context.Context) (coatofarms.Palette, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT path FROM files
		WHERE kind='script' AND overridden=0 AND rel_path LIKE ?
		ORDER BY source_rank DESC`, coatOfArmsNamedColorsD+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	palette := coatofarms.Palette{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		palette.ParseNamedColors(script.ParseBytes(data).Nodes)
	}
	return palette, rows.Err()
}

type coatOfArmsTextureOrigin struct {
	source string
	kind   string
}

// coatOfArmsTextures builds the texture library from the indexed resources.
// Lower source rank wins, which is the same precedence the index applies to
// script files, so a mod's replacement emblem is the one that draws.
func (db *DB) coatOfArmsTextures(ctx context.Context) (*coatofarms.PathTextures, map[string]coatOfArmsTextureOrigin, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT resource_path,source_name,path FROM resources
		WHERE resource_path LIKE ? ORDER BY source_rank`, coatOfArmsTextureRoot+"%")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	paths := map[string]string{}
	origins := map[string]coatOfArmsTextureOrigin{}
	for rows.Next() {
		var resourcePath, source, path string
		if err := rows.Scan(&resourcePath, &source, &path); err != nil {
			return nil, nil, err
		}
		if !strings.HasSuffix(strings.ToLower(resourcePath), ".dds") {
			continue
		}
		name := strings.ToLower(resourcePath[strings.LastIndex(resourcePath, "/")+1:])
		if _, taken := paths[name]; taken {
			continue
		}
		paths[name] = path
		origins[name] = coatOfArmsTextureOrigin{source: source, kind: coatOfArmsTextureKind(resourcePath)}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return coatofarms.NewPathTextures(paths), origins, nil
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

func (db *DB) coatOfArmsAssetList(ctx context.Context, spec CoatOfArmsSpec) (CoatOfArmsResult, error) {
	result := CoatOfArmsResult{Intent: "coat_of_arms_assets", Operation: "assets"}
	_, origins, err := db.coatOfArmsTextures(ctx)
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
	return result, nil
}

func filterSuffix(filter string) string {
	if filter == "" {
		return ""
	}
	return fmt.Sprintf(" matching %q", filter)
}
