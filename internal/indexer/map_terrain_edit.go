package indexer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The terrain generators all take an *image.Gray and hand one back. That makes
// them testable, and it makes them unreachable: nothing in the tree loads the
// workspace heightmap or writes what comes out, so a caller outside Go has no
// way in. This file is that boundary.
//
// Two rules shape it, both inherited from ApplyProvinceSplitToImage. Generated
// rasters never overwrite their source, and they never land inside a configured
// source root -- a heightmap written straight into the Mod would be picked up by
// the next scan as if a human had drawn it, with no diff to review and no
// original left to compare against.

const (
	// MapTerrainOpCompose applies ordered, domain-aware physical-terrain layers.
	MapTerrainOpCompose = "compose"
	// MapTerrainOpLargeRivers derives and carves major river valleys into the
	// final relief. It deliberately does not paint rivers.png.
	MapTerrainOpLargeRivers = "large_rivers"
	// MapTerrainOpSmallRivers replaces the requested rivers.png window and cuts
	// a matching shallow bed without disturbing river indices outside it.
	MapTerrainOpSmallRivers = "small_rivers"
)

const (
	mapHeightmapRel = "map_data/heightmap.png"
	mapRiversRel    = "map_data/rivers.png"
)

// MapTerrainInputError marks a request the caller can correct: an unknown
// operation, a region off the map, a feature with no width. It mirrors
// MapSplitInputError so the tool layer can answer "your request is wrong"
// rather than "the server broke", which are different problems with different
// fixes.
type MapTerrainInputError struct{ message string }

func (e *MapTerrainInputError) Error() string { return e.message }

func terrainInputErrorf(format string, args ...any) error {
	return &MapTerrainInputError{message: fmt.Sprintf(format, args...)}
}

// MapTerrainRegion bounds the pixels a generator may read and change. Erosion
// and drainage routing both cost time proportional to the area they cover, and
// the workspace heightmap is large enough that running either over the whole
// map is rarely what a caller means.
type MapTerrainRegion struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (r MapTerrainRegion) rectangle() image.Rectangle {
	return image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height)
}

// MapTerrainEditSpec is one deterministic physical-terrain edit. All height
// values exposed by this contract use the normalized 0..1 range.
type MapTerrainEditSpec struct {
	Operation      string                        `json:"operation"`
	BaseArtifactID string                        `json:"base_artifact_id,omitempty"`
	Layers         []MapTerrainLayer             `json:"layers,omitempty"`
	Erosion        *MapErosionSettings           `json:"erosion,omitempty"`
	LargeRivers    *MapTerrainLargeRiverSettings `json:"large_rivers,omitempty"`
	SmallRivers    *MapTerrainSmallRiverSettings `json:"small_rivers,omitempty"`
	Region         *MapTerrainRegion             `json:"region,omitempty"`
}

// MapTerrainEditOutput is one raster written to the output directory.
type MapTerrainEditOutput struct {
	Kind   string `json:"kind"`
	Rel    string `json:"rel"`
	Path   string `json:"path,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// MapTerrainEditResult reports what changed without returning the rasters
// themselves, which are far too large to travel in a tool response.
type MapTerrainEditResult struct {
	Operation         string                     `json:"operation"`
	ArtifactID        string                     `json:"artifact_id,omitempty"`
	ParentArtifactID  string                     `json:"parent_artifact_id,omitempty"`
	ManifestRel       string                     `json:"manifest_rel,omitempty"`
	SourceRel         string                     `json:"source_rel"`
	SourceName        string                     `json:"source_name"`
	SourceFingerprint string                     `json:"source_fingerprint"`
	Width             int                        `json:"width"`
	Height            int                        `json:"height"`
	BitDepth          int                        `json:"bit_depth"`
	Region            MapTerrainRegion           `json:"region"`
	ModifiedBounds    *MapTerrainRegion          `json:"modified_bounds,omitempty"`
	Outputs           []MapTerrainEditOutput     `json:"outputs,omitempty"`
	Layers            []MapTerrainLayerStats     `json:"layers,omitempty"`
	LargeRivers       *MapTerrainLargeRiverStats `json:"large_rivers,omitempty"`
	SmallRivers       *MapTerrainSmallRiverStats `json:"small_rivers,omitempty"`
	HydrologyStatus   string                     `json:"hydrology_status"`
	RequiresCK3Repack bool                       `json:"requires_ck3_repack"`
	Applied           bool                       `json:"applied"`
	Guidance          []string                   `json:"guidance,omitempty"`
	PreviewPNG        []byte                     `json:"-"`
	Warnings          []string                   `json:"warnings,omitempty"`
	Replayed          bool                       `json:"replayed,omitempty"`
}

// ActiveMapAsset resolves one source-root-relative map asset through the same
// source ranking the indexer uses, so a generator reads exactly the file the
// index describes rather than guessing which configured root wins.
func ActiveMapAsset(cfg Config, rel string) (path string, sourceName string, err error) {
	active, err := collectActiveMapFiles(cfg)
	if err != nil {
		return "", "", err
	}
	file := active[strings.ToLower(rel)]
	if file.Path == "" {
		return "", "", fmt.Errorf("the active map has no %s; configure a source that provides it and run a full scan", rel)
	}
	return file.Path, file.Src.Name, nil
}

// EditMapTerrain runs one operation against the active heightmap or a verified
// parent artifact and writes its raw rasters into outputDir. It is the shared
// low-level entry used by the artifact publisher and tests; callers normally
// use PreviewMapTerrainEdit or CreateMapTerrainEditArtifact instead.
func EditMapTerrain(cfg Config, spec MapTerrainEditSpec, outputDir string) (MapTerrainEditResult, error) {
	if strings.TrimSpace(outputDir) == "" {
		return MapTerrainEditResult{}, terrainInputErrorf("an output directory is required; generated rasters are never written over their source")
	}
	if err := EnsureMapOutputOutsideSources(outputDir, cfg.Sources); err != nil {
		return MapTerrainEditResult{}, err
	}
	if err := rejectSymlinkPath(outputDir); err != nil && !os.IsNotExist(err) {
		return MapTerrainEditResult{}, terrainInputErrorf("output directory is unsafe: %v", err)
	}
	run, err := prepareMapTerrainEdit(cfg, spec)
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	return run.write(outputDir)
}

// resolveTerrainRegion defaults to the whole map and rejects a window that
// falls outside it, which is otherwise silently clipped to nothing.
func resolveTerrainRegion(region *MapTerrainRegion, bounds image.Rectangle) (image.Rectangle, error) {
	if region == nil {
		return bounds, nil
	}
	if region.Width <= 0 || region.Height <= 0 {
		return image.Rectangle{}, terrainInputErrorf("region width and height must both be positive")
	}
	clipped := region.rectangle().Intersect(bounds)
	if clipped.Empty() {
		return image.Rectangle{}, terrainInputErrorf("region (%d,%d %dx%d) lies outside the %dx%d heightmap",
			region.X, region.Y, region.Width, region.Height, bounds.Dx(), bounds.Dy())
	}
	return clipped, nil
}

func writeHeightmapOutput(dir string, img image.Image) (MapTerrainEditOutput, error) {
	return writeMapRaster(dir, mapHeightmapRel, "heightmap", img)
}

func writeHeightmapOutputContext(ctx context.Context, dir string, img image.Image) (MapTerrainEditOutput, error) {
	return writeMapRasterContext(ctx, dir, mapHeightmapRel, "heightmap", img)
}

func writeRiverOverlayOutput(dir string, img *image.Paletted) (MapTerrainEditOutput, error) {
	return writeMapRaster(dir, mapRiversRel, "rivers", img)
}

func writeRiverOverlayOutputContext(ctx context.Context, dir string, img *image.Paletted) (MapTerrainEditOutput, error) {
	return writeMapRasterContext(ctx, dir, mapRiversRel, "rivers", img)
}

// WriteMapTerrainPreview saves the optional CLI thumbnail without allowing it
// to overwrite an existing file, follow a symbolic-link path, or land inside
// any configured CK3 source.
func WriteMapTerrainPreview(path string, data []byte, cfg Config) error {
	if !strings.EqualFold(filepath.Ext(path), ".png") {
		return terrainInputErrorf("--preview-out must name a .png file")
	}
	if len(data) == 0 {
		return terrainInputErrorf("terrain edit returned no preview PNG")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(absolute)
	info, err := os.Stat(parent)
	if err != nil {
		return terrainInputErrorf("preview output directory must already exist: %v", err)
	}
	if !info.IsDir() {
		return terrainInputErrorf("preview output parent is not a directory")
	}
	if err := EnsureMapOutputOutsideSources(absolute, cfg.Sources); err != nil {
		return err
	}
	if err := rejectSymlinkPath(parent); err != nil {
		return terrainInputErrorf("preview output path is unsafe: %v", err)
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return terrainInputErrorf("refusing to overwrite an existing preview")
		}
		return err
	}
	written := false
	defer func() {
		if !written {
			_ = os.Remove(absolute)
		}
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	written = true
	return nil
}

// writeMapRaster lays the output out under its CK3-relative path so the result
// can be copied into a Mod wholesale, and refuses to replace a file already
// there: overwriting would destroy the previous run's evidence with no diff.
func writeMapRaster(dir, rel, kind string, img image.Image) (MapTerrainEditOutput, error) {
	return writeMapRasterContext(context.Background(), dir, rel, kind, img)
}

type contextCheckingWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer contextCheckingWriter) Write(data []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	return writer.writer.Write(data)
}

func writeMapRasterContext(ctx context.Context, dir, rel, kind string, img image.Image) (MapTerrainEditOutput, error) {
	output := MapTerrainEditOutput{Kind: kind, Rel: rel}
	if err := ctx.Err(); err != nil {
		return output, err
	}
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if _, err := os.Lstat(path); err == nil {
		return output, terrainInputErrorf("refusing to overwrite %s; nominate an empty output directory", path)
	} else if !os.IsNotExist(err) {
		return output, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return output, err
	}
	if err := rejectSymlinkPath(filepath.Dir(path)); err != nil {
		return output, terrainInputErrorf("output path is unsafe: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return output, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	// CK3 reads both rasters by exact value -- grey level for the heightmap,
	// palette index for rivers -- so the encoder must not quantise or dither.
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(contextCheckingWriter{ctx: ctx, writer: file}, img); err != nil {
		file.Close()
		return output, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return output, err
	}
	if err := file.Close(); err != nil {
		return output, err
	}
	if err := ctx.Err(); err != nil {
		return output, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return output, err
	}
	hash, err := sha256FileContext(ctx, path)
	if err != nil {
		return output, err
	}
	output.Path, output.Bytes, output.SHA256 = path, info.Size(), hash
	complete = true
	return output, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// EnsureMapOutputOutsideSources keeps generated rasters out of every configured
// source root. Inside one, the next scan would index a generated file as
// authored map data and the original would already be gone. Every writer of map
// rasters calls this, so the rule holds no matter which tool is writing.
func EnsureMapOutputOutsideSources(dir string, sources []Source) error {
	target, err := filepath.Abs(dir)
	if err != nil {
		return terrainInputErrorf("unusable output directory: %s", err)
	}
	for _, source := range sources {
		if source.Path == "" {
			continue
		}
		root, err := filepath.Abs(source.Path)
		if err != nil {
			return fmt.Errorf("unusable configured source %q path: %w", source.Name, err)
		}
		if pathsOverlap(target, root) {
			return terrainInputErrorf("refusing to write generated map rasters inside configured source %q; they must land outside every indexed root", source.Name)
		}
	}
	return nil
}

// pathsOverlap reports whether either path contains the other, so a nested
// output directory is caught as well as an exact match. It mirrors the
// migrator's containment check so both writers agree on what "inside a source"
// means.
func pathsOverlap(a, b string) bool {
	return pathContainsPath(a, b) || pathContainsPath(b, a)
}

func pathContainsPath(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
