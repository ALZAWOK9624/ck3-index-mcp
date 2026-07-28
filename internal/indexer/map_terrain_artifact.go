package indexer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const terrainArtifactSchemaVersion = 1

var terrainArtifactIDPattern = regexp.MustCompile(`^map-edit-[0-9a-f]{32}$`)
var terrainSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type MapTerrainArtifactFile struct {
	Kind     string `json:"kind"`
	Rel      string `json:"rel"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	BitDepth int    `json:"bit_depth,omitempty"`
}

type MapTerrainArtifactManifest struct {
	SchemaVersion     int                      `json:"schema_version"`
	ArtifactID        string                   `json:"artifact_id"`
	ParentArtifactID  string                   `json:"parent_artifact_id,omitempty"`
	CreatedAt         string                   `json:"created_at"`
	Operation         string                   `json:"operation"`
	SourceName        string                   `json:"source_name"`
	SourceFingerprint string                   `json:"source_fingerprint"`
	NormalizedSpec    json.RawMessage          `json:"normalized_spec"`
	Width             int                      `json:"width"`
	Height            int                      `json:"height"`
	BitDepth          int                      `json:"bit_depth"`
	ModifiedBounds    *MapTerrainRegion        `json:"modified_bounds,omitempty"`
	HydrologyStatus   string                   `json:"hydrology_status"`
	RequiresCK3Repack bool                     `json:"requires_ck3_repack"`
	Files             []MapTerrainArtifactFile `json:"files"`
}

type terrainEditInput struct {
	HeightmapPath     string
	RiversPath        string
	SourceName        string
	SourceFingerprint string
	ParentArtifactID  string
}

// PreviewMapTerrainEdit validates and executes an edit entirely in memory. It
// never creates an artifact directory.
func PreviewMapTerrainEdit(cfg Config, spec MapTerrainEditSpec) (MapTerrainEditResult, error) {
	prepared, err := prepareMapTerrainEdit(cfg, normalizeMapTerrainEditSpec(spec))
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	result := prepared.result
	result.Guidance = append([]string{"Preview only: confirm=false created no files or artifact."}, result.Guidance...)
	return result, nil
}

// CreateMapTerrainEditArtifact publishes a fresh immutable artifact under the
// configured server-controlled root. It never accepts a caller path.
func CreateMapTerrainEditArtifact(cfg Config, spec MapTerrainEditSpec) (MapTerrainEditResult, error) {
	if strings.TrimSpace(cfg.ArtifactRoot) == "" {
		return MapTerrainEditResult{}, terrainInputErrorf("artifact_root is not configured")
	}
	base := filepath.Join(cfg.ArtifactRoot, "map-edits")
	if err := EnsureMapOutputOutsideSources(base, cfg.Sources); err != nil {
		return MapTerrainEditResult{}, err
	}
	if err := rejectSymlinkPath(cfg.ArtifactRoot); err != nil && !os.IsNotExist(err) {
		return MapTerrainEditResult{}, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return MapTerrainEditResult{}, err
	}
	if err := rejectSymlinkPath(base); err != nil {
		return MapTerrainEditResult{}, terrainInputErrorf("artifact root is unsafe: %v", err)
	}
	normalized := normalizeMapTerrainEditSpec(spec)
	prepared, err := prepareMapTerrainEdit(cfg, normalized)
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	id, err := newTerrainArtifactID()
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	temp, err := os.MkdirTemp(base, ".building-"+id+"-")
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temp)
		}
	}()
	result, err := prepared.write(temp)
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	specJSON, err := json.Marshal(normalized)
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	manifest := MapTerrainArtifactManifest{
		SchemaVersion: terrainArtifactSchemaVersion, ArtifactID: id,
		ParentArtifactID: result.ParentArtifactID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Operation: result.Operation, SourceName: result.SourceName,
		SourceFingerprint: result.SourceFingerprint, NormalizedSpec: specJSON,
		Width: result.Width, Height: result.Height, BitDepth: result.BitDepth,
		ModifiedBounds: result.ModifiedBounds, HydrologyStatus: result.HydrologyStatus,
		RequiresCK3Repack: true,
	}
	for _, output := range result.Outputs {
		bitDepth := 0
		if output.Rel == mapHeightmapRel {
			bitDepth = result.BitDepth
		} else if output.Rel == mapRiversRel {
			bitDepth = 8
		}
		manifest.Files = append(manifest.Files, MapTerrainArtifactFile{
			Kind: output.Kind, Rel: output.Rel, SHA256: output.SHA256,
			Bytes: output.Bytes, BitDepth: bitDepth,
		})
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	manifestPath := filepath.Join(temp, "manifest.json")
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return MapTerrainEditResult{}, err
	}
	if _, err := file.Write(append(manifestData, '\n')); err != nil {
		file.Close()
		return MapTerrainEditResult{}, err
	}
	if err := file.Close(); err != nil {
		return MapTerrainEditResult{}, err
	}
	final := filepath.Join(base, id)
	if _, err := os.Lstat(final); err == nil {
		return MapTerrainEditResult{}, fmt.Errorf("artifact id collision")
	} else if !os.IsNotExist(err) {
		return MapTerrainEditResult{}, err
	}
	if err := os.Rename(temp, final); err != nil {
		return MapTerrainEditResult{}, err
	}
	published = true
	result.ArtifactID = id
	result.ManifestRel = "manifest.json"
	for index := range result.Outputs {
		result.Outputs[index].Path = ""
	}
	result.Guidance = append([]string{"A new immutable artifact was created under the controlled map-edits store."}, result.Guidance...)
	return result, nil
}

func normalizeMapTerrainEditSpec(spec MapTerrainEditSpec) MapTerrainEditSpec {
	spec.Operation = strings.ToLower(strings.TrimSpace(spec.Operation))
	spec.BaseArtifactID = strings.TrimSpace(spec.BaseArtifactID)
	for index := range spec.Layers {
		spec.Layers[index].ID = strings.TrimSpace(spec.Layers[index].ID)
		spec.Layers[index].Kind = strings.ToLower(strings.TrimSpace(spec.Layers[index].Kind))
		spec.Layers[index].Geometry.Type = strings.ToLower(strings.TrimSpace(spec.Layers[index].Geometry.Type))
		spec.Layers[index].Domain = normalizedTerrainDomain(spec.Layers[index])
		if spec.Layers[index].Detail == 0 {
			spec.Layers[index].Detail = 1
		}
	}
	if spec.Operation == MapTerrainOpLargeRivers && spec.LargeRivers == nil {
		value := DefaultTerrainLargeRivers()
		spec.LargeRivers = &value
	}
	if spec.Operation == MapTerrainOpSmallRivers && spec.SmallRivers == nil {
		value := DefaultTerrainSmallRivers()
		spec.SmallRivers = &value
	}
	return spec
}

func resolveMapTerrainEditInput(cfg Config, baseArtifactID string) (terrainEditInput, error) {
	fingerprint, sourceName, err := activeTerrainSourceFingerprint(cfg)
	if err != nil {
		return terrainEditInput{}, err
	}
	if strings.TrimSpace(baseArtifactID) == "" {
		height, _, err := ActiveMapAsset(cfg, mapHeightmapRel)
		if err != nil {
			return terrainEditInput{}, err
		}
		rivers, _, riverErr := ActiveMapAsset(cfg, mapRiversRel)
		if riverErr != nil {
			rivers = ""
		}
		return terrainEditInput{
			HeightmapPath: height, RiversPath: rivers, SourceName: sourceName,
			SourceFingerprint: fingerprint,
		}, nil
	}
	manifest, dir, err := verifyTerrainArtifact(cfg, strings.TrimSpace(baseArtifactID), fingerprint, map[string]bool{})
	if err != nil {
		return terrainEditInput{}, err
	}
	input := terrainEditInput{
		SourceName: manifest.SourceName, SourceFingerprint: manifest.SourceFingerprint,
		ParentArtifactID: manifest.ArtifactID,
	}
	for _, file := range manifest.Files {
		switch file.Rel {
		case mapHeightmapRel:
			input.HeightmapPath = filepath.Join(dir, filepath.FromSlash(file.Rel))
		case mapRiversRel:
			input.RiversPath = filepath.Join(dir, filepath.FromSlash(file.Rel))
		}
	}
	if input.HeightmapPath == "" {
		return terrainEditInput{}, terrainInputErrorf("base artifact %q has no raw heightmap.png", baseArtifactID)
	}
	if input.RiversPath == "" {
		input.RiversPath, err = terrainArtifactLineageFile(cfg, manifest.ParentArtifactID, mapRiversRel, fingerprint)
		if err != nil {
			return terrainEditInput{}, err
		}
		if input.RiversPath == "" {
			if activeRivers, _, activeErr := ActiveMapAsset(cfg, mapRiversRel); activeErr == nil {
				input.RiversPath = activeRivers
			}
		}
	}
	return input, nil
}

func terrainArtifactLineageFile(cfg Config, id, rel, fingerprint string) (string, error) {
	if id == "" {
		return "", nil
	}
	manifest, dir, err := verifyTerrainArtifact(cfg, id, fingerprint, map[string]bool{})
	if err != nil {
		return "", err
	}
	for _, file := range manifest.Files {
		if file.Rel == rel {
			return filepath.Join(dir, filepath.FromSlash(rel)), nil
		}
	}
	return terrainArtifactLineageFile(cfg, manifest.ParentArtifactID, rel, fingerprint)
}

func activeTerrainSourceFingerprint(cfg Config) (fingerprint, sourceName string, err error) {
	active, err := collectActiveMapFiles(cfg)
	if err != nil {
		return "", "", err
	}
	required := []string{mapHeightmapRel, "map_data/provinces.png", "map_data/definition.csv", "map_data/default.map"}
	optional := []string{mapRiversRel}
	hash := sha256.New()
	for _, rel := range append(required, optional...) {
		file := active[strings.ToLower(rel)]
		if file.Path == "" {
			if rel == mapHeightmapRel {
				return "", "", fmt.Errorf("the active map has no %s", rel)
			}
			if rel == mapRiversRel {
				continue
			}
			// Domain assets are part of the fingerprint when present. Compose
			// itself reports the clearer missing-asset error when it needs them.
			continue
		}
		if sourceName == "" && rel == mapHeightmapRel {
			sourceName = file.Src.Name
		}
		if _, err := io.WriteString(hash, rel+"\x00"); err != nil {
			return "", "", err
		}
		f, err := os.Open(file.Path)
		if err != nil {
			return "", "", err
		}
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", "", copyErr
		}
		if closeErr != nil {
			return "", "", closeErr
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), sourceName, nil
}

func verifyTerrainArtifact(cfg Config, id, expectedFingerprint string, visited map[string]bool) (MapTerrainArtifactManifest, string, error) {
	if !terrainArtifactIDPattern.MatchString(id) {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base_artifact_id %q is not a valid controlled map-edit id", id)
	}
	if visited[id] {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("artifact lineage contains a cycle at %q", id)
	}
	visited[id] = true
	root := filepath.Join(cfg.ArtifactRoot, "map-edits")
	dir := filepath.Join(root, id)
	if !pathContainsPath(root, dir) {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("artifact path escaped the controlled root")
	}
	if err := rejectSymlinkPath(dir); err != nil {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q is unavailable or unsafe: %v", id, err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q has no readable manifest", id)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q manifest is not a regular file", id)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return MapTerrainArtifactManifest{}, "", err
	}
	var manifest MapTerrainArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q has a damaged manifest: %v", id, err)
	}
	if manifest.SchemaVersion != terrainArtifactSchemaVersion || manifest.ArtifactID != id {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q manifest identity or schema is invalid", id)
	}
	if manifest.SourceFingerprint != expectedFingerprint {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q is stale because the active source-map fingerprint changed", id)
	}
	if manifest.Width <= 0 || manifest.Height <= 0 || (manifest.BitDepth != 8 && manifest.BitDepth != 16) {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q has invalid raster metadata", id)
	}
	if !terrainSHA256Pattern.MatchString(manifest.SourceFingerprint) || len(manifest.NormalizedSpec) == 0 {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q has invalid source or spec metadata", id)
	}
	var spec MapTerrainEditSpec
	if err := json.Unmarshal(manifest.NormalizedSpec, &spec); err != nil {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q normalized spec is damaged", id)
	}
	spec = normalizeMapTerrainEditSpec(spec)
	canonicalSpec, err := json.Marshal(spec)
	var compactSpec bytes.Buffer
	compactErr := json.Compact(&compactSpec, manifest.NormalizedSpec)
	if err != nil || compactErr != nil || !bytes.Equal(canonicalSpec, compactSpec.Bytes()) ||
		spec.Operation != manifest.Operation || spec.BaseArtifactID != manifest.ParentArtifactID {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q normalized spec or lineage metadata is inconsistent", id)
	}
	requiredFiles := map[string]bool{
		mapHeightmapRel:                 true,
		"previews/hillshade_before.png": true,
		"previews/hillshade_after.png":  true,
		"previews/height_diff.png":      true,
	}
	allowedFiles := map[string]bool{}
	for rel := range requiredFiles {
		allowedFiles[rel] = true
	}
	allowedFiles[mapRiversRel] = true
	seenFiles := map[string]bool{}
	for _, file := range manifest.Files {
		if !allowedFiles[file.Rel] || seenFiles[file.Rel] || !terrainSHA256Pattern.MatchString(file.SHA256) || file.Bytes <= 0 {
			return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q has invalid or duplicate file metadata for %q", id, file.Rel)
		}
		seenFiles[file.Rel] = true
		path, err := safeTerrainArtifactFile(dir, file.Rel)
		if err != nil {
			return MapTerrainArtifactManifest{}, "", err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q file %q is missing or unsafe", id, file.Rel)
		}
		hash, err := sha256File(path)
		if err != nil {
			return MapTerrainArtifactManifest{}, "", err
		}
		if hash != file.SHA256 || info.Size() != file.Bytes {
			return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q file %q failed its hash or size check", id, file.Rel)
		}
		if file.Rel == mapHeightmapRel {
			height, err := loadTerrainHeightRaster(path)
			if err != nil {
				return MapTerrainArtifactManifest{}, "", err
			}
			if height.bounds().Dx() != manifest.Width || height.bounds().Dy() != manifest.Height || height.bitDepth != manifest.BitDepth {
				return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q heightmap dimensions or bit depth drifted", id)
			}
		}
	}
	for rel := range requiredFiles {
		if !seenFiles[rel] {
			return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q is missing required file metadata for %q", id, rel)
		}
	}
	if manifest.Operation == MapTerrainOpSmallRivers && !seenFiles[mapRiversRel] {
		return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q small-river operation is missing rivers.png", id)
	}
	if manifest.ParentArtifactID != "" {
		if _, _, err := verifyTerrainArtifact(cfg, manifest.ParentArtifactID, expectedFingerprint, visited); err != nil {
			return MapTerrainArtifactManifest{}, "", terrainInputErrorf("base artifact %q has an invalid parent: %v", id, err)
		}
	}
	return manifest, dir, nil
}

func safeTerrainArtifactFile(dir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return "", terrainInputErrorf("artifact manifest contains unsafe relative path %q", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", terrainInputErrorf("artifact manifest path %q escapes its artifact", rel)
	}
	path := filepath.Join(dir, clean)
	if !pathContainsPath(dir, path) {
		return "", terrainInputErrorf("artifact manifest path %q escapes its artifact", rel)
	}
	return path, nil
}

func rejectSymlinkPath(path string) error {
	clean, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(clean) + string(filepath.Separator)
	rel := strings.TrimPrefix(clean, volume)
	current := volume
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", current)
		}
	}
	return nil
}

func newTerrainArtifactID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "map-edit-" + hex.EncodeToString(bytes[:]), nil
}

func terrainPreviewImages(before, after *terrainHeightRaster, region image.Rectangle) (beforePNG, afterPNG, diffPNG []byte) {
	const maxSide = 512
	scale := 1.0
	if region.Dx() > maxSide || region.Dy() > maxSide {
		scale = mathMin(float64(maxSide)/float64(region.Dx()), float64(maxSide)/float64(region.Dy()))
	}
	width := max(1, int(float64(region.Dx())*scale))
	height := max(1, int(float64(region.Dy())*scale))
	beforeShade := image.NewGray(image.Rect(0, 0, width, height))
	afterShade := image.NewGray(image.Rect(0, 0, width, height))
	diff := image.NewRGBA(image.Rect(0, 0, width, height))
	sample := func(r *terrainHeightRaster, px, py int) float64 {
		x := region.Min.X + min(region.Dx()-1, int((float64(px)+0.5)*float64(region.Dx())/float64(width)))
		y := region.Min.Y + min(region.Dy()-1, int((float64(py)+0.5)*float64(region.Dy())/float64(height)))
		return r.normalizedAt(x, y)
	}
	shade := func(r *terrainHeightRaster, x, y int) uint8 {
		left := sample(r, max(x-1, 0), y)
		right := sample(r, min(x+1, width-1), y)
		up := sample(r, x, max(y-1, 0))
		down := sample(r, x, min(y+1, height-1))
		value := 0.58 + (left-right)*5 + (up-down)*7
		return uint8(mathRound(clamp01(value) * 255))
	}
	// Scale the diff to the change it is actually showing. A fixed gain rendered
	// a river bed -- a few thousandths of the height range -- as flat black, so
	// the one preview meant to answer "what moved" answered "nothing". The floor
	// stops a near-empty edit being amplified into noise.
	const minDiffSpan = 1.0 / 255.0
	peak := minDiffSpan
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if d := mathAbs(sample(after, x, y) - sample(before, x, y)); d > peak {
				peak = d
			}
		}
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			beforeShade.SetGray(x, y, color.Gray{Y: shade(before, x, y)})
			afterShade.SetGray(x, y, color.Gray{Y: shade(after, x, y)})
			delta := sample(after, x, y) - sample(before, x, y)
			intensity := uint8(mathRound(mathMin(mathAbs(delta)/peak, 1) * 255))
			switch {
			case delta > 0:
				diff.SetRGBA(x, y, color.RGBA{R: intensity, G: intensity / 4, A: 255})
			case delta < 0:
				diff.SetRGBA(x, y, color.RGBA{B: intensity, G: intensity / 4, A: 255})
			default:
				diff.SetRGBA(x, y, color.RGBA{A: 255})
			}
		}
	}
	return encodePNGImage(beforeShade), encodePNGImage(afterShade), encodePNGImage(diff)
}

func encodePNGImage(img image.Image) []byte {
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&out, img); err != nil {
		return nil
	}
	return out.Bytes()
}

func decodePNGBytes(data []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(data))
}

// Small wrappers keep the preview code independent of any Go version-specific
// generic inference around built-in min/max.
func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
func mathRound(value float64) float64 {
	if value < 0 {
		return float64(int64(value - 0.5))
	}
	return float64(int64(value + 0.5))
}
