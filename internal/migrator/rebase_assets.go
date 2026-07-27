package migrator

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"ck3-index/internal/indexer"
)

// rebaseRasterMaxPixels puts a hard cap on decoded candidate rasters. It is
// large enough for CK3's usual map and terrain layers (for example 8192x8192)
// while preventing a malformed header from allocating several gigabytes during
// planning.
const rebaseRasterMaxPixels = 64 * 1024 * 1024

// rebaseRaster is a canonical, top-left-origin RGBA representation used only
// while merging. Keeping a fixed representation makes PNG and TGA comparisons
// about visible pixels rather than compression, palette, or row-order details.
type rebaseRaster struct {
	width  int
	height int
	pixels []byte // RGBA, width*height*4
	tga    rebaseTGAFormat
}

type rebaseTGAFormat struct {
	depth       int
	rle         bool
	topOrigin   bool
	rightOrigin bool
	xOrigin     uint16
	yOrigin     uint16
	id          []byte
}

// buildRebaseRasterCandidate merges PNG and TGA assets at decoded-pixel level.
// A result is materialized only when every changed pixel is independently
// changed by at most one side (or both sides reached the same pixel). Encoding
// differences alone therefore never create a merge conflict.
func buildRebaseRasterCandidate(rel string, base, project, target []byte, baseFile, projectFile, targetFile *SnapshotFile) ([]byte, []RebaseConflict, error) {
	ext := strings.ToLower(filepath.Ext(rel))
	var (
		baseRaster    rebaseRaster
		projectRaster rebaseRaster
		targetRaster  rebaseRaster
		err           error
	)
	switch ext {
	case ".png":
		if baseRaster, err = decodeRebasePNG(base); err != nil {
			return nil, nil, fmt.Errorf("decode base PNG %s: %w", rel, err)
		}
		if projectRaster, err = decodeRebasePNG(project); err != nil {
			return nil, nil, fmt.Errorf("decode project PNG %s: %w", rel, err)
		}
		if targetRaster, err = decodeRebasePNG(target); err != nil {
			return nil, nil, fmt.Errorf("decode target PNG %s: %w", rel, err)
		}
	case ".tga":
		if baseRaster, err = decodeRebaseTGA(base); err != nil {
			return nil, nil, fmt.Errorf("decode base TGA %s: %w", rel, err)
		}
		if projectRaster, err = decodeRebaseTGA(project); err != nil {
			return nil, nil, fmt.Errorf("decode project TGA %s: %w", rel, err)
		}
		if targetRaster, err = decodeRebaseTGA(target); err != nil {
			return nil, nil, fmt.Errorf("decode target TGA %s: %w", rel, err)
		}
	default:
		return nil, nil, fmt.Errorf("raster adapter does not support %q", rel)
	}

	merged, conflictReason := mergeRebaseRasters(baseRaster, projectRaster, targetRaster)
	if conflictReason != "" {
		message := fmt.Sprintf("%s (project_sha256=%s target_sha256=%s)", conflictReason, fileHash(projectFile), fileHash(targetFile))
		conflict := newRebaseConflict(
			"pixel_merge_conflict", rel, "", message,
			[]string{"keep_project", "use_target", "manual"}, "manual",
			baseFile, projectFile, targetFile,
		)
		return nil, []RebaseConflict{conflict}, nil
	}

	switch ext {
	case ".png":
		candidate, err := encodeRebasePNG(merged)
		if err != nil {
			return nil, nil, fmt.Errorf("encode merged PNG %s: %w", rel, err)
		}
		return candidate, nil, nil
	case ".tga":
		// Retain the project's TGA representation when that does not discard
		// alpha. This keeps a successful semantic merge close to the project's
		// asset format while the decoded comparison remains format-independent.
		format := projectRaster.tga
		if format.depth != 24 && format.depth != 32 {
			format.depth = 32
		}
		if format.depth == 24 && !rebaseRasterOpaque(merged) {
			format.depth = 32
		}
		candidate, err := encodeRebaseTGA(merged, format)
		if err != nil {
			return nil, nil, fmt.Errorf("encode merged TGA %s: %w", rel, err)
		}
		return candidate, nil, nil
	}
	panic("unreachable raster extension")
}

// buildRebaseMapCoordinateCandidate turns Base -> Ours into a transparent PNG
// patch and replays those exact changed coordinates over Theirs. The final
// candidate preserves the target file type; the transparent PNG is durable
// review evidence, not the CK3-facing output.
func buildRebaseMapCoordinateCandidate(rel string, base, project, target []byte, baseFile, projectFile, targetFile *SnapshotFile) ([]byte, []byte, RebaseMapDelta, []RebaseConflict, error) {
	baseRaster, err := decodeRebaseRasterByPath(rel, base)
	if err != nil {
		return nil, nil, RebaseMapDelta{}, nil, fmt.Errorf("decode base map raster %s: %w", rel, err)
	}
	projectRaster, err := decodeRebaseRasterByPath(rel, project)
	if err != nil {
		return nil, nil, RebaseMapDelta{}, nil, fmt.Errorf("decode project map raster %s: %w", rel, err)
	}
	targetRaster, err := decodeRebaseRasterByPath(rel, target)
	if err != nil {
		return nil, nil, RebaseMapDelta{}, nil, fmt.Errorf("decode target map raster %s: %w", rel, err)
	}
	if baseRaster.width != projectRaster.width || baseRaster.height != projectRaster.height ||
		baseRaster.width != targetRaster.width || baseRaster.height != targetRaster.height {
		message := fmt.Sprintf(
			"coordinate delta requires identical canvas dimensions (base=%dx%d project=%dx%d target=%dx%d); geometric warping is not automatic",
			baseRaster.width, baseRaster.height, projectRaster.width, projectRaster.height, targetRaster.width, targetRaster.height,
		)
		conflict := newRebaseConflict(
			"map_coordinate_dimension_mismatch", rel, "", message,
			[]string{"keep_project", "use_target", "manual"}, "manual",
			baseFile, projectFile, targetFile,
		)
		return nil, nil, RebaseMapDelta{}, []RebaseConflict{conflict}, nil
	}
	deltaRaster := rebaseRaster{
		width:  baseRaster.width,
		height: baseRaster.height,
		pixels: make([]byte, len(baseRaster.pixels)),
	}
	changed := 0
	for offset := 0; offset < len(baseRaster.pixels); offset += 4 {
		if sameRebasePixel(baseRaster.pixels[offset:offset+4], projectRaster.pixels[offset:offset+4]) {
			continue
		}
		// The transparent layer uses alpha as its coordinate-presence mask.
		// Requiring opaque map pixels keeps the artifact lossless and prevents
		// an invisible "changed to alpha=0" pixel from being misrepresented.
		if projectRaster.pixels[offset+3] != 0xff {
			conflict := newRebaseConflict(
				"map_coordinate_alpha_unsupported", rel, "",
				"coordinate delta cannot encode a changed non-opaque project pixel losslessly",
				[]string{"keep_project", "use_target", "manual"}, "manual",
				baseFile, projectFile, targetFile,
			)
			return nil, nil, RebaseMapDelta{}, []RebaseConflict{conflict}, nil
		}
		copy(deltaRaster.pixels[offset:offset+4], projectRaster.pixels[offset:offset+4])
		changed++
	}
	deltaData, err := encodeRebasePNG(deltaRaster)
	if err != nil {
		return nil, nil, RebaseMapDelta{}, nil, fmt.Errorf("encode coordinate delta %s: %w", rel, err)
	}
	delta := RebaseMapDelta{
		OriginX: 0, OriginY: 0,
		Width: baseRaster.width, Height: baseRaster.height,
		ChangedPixels: changed,
	}

	merged, conflictReason := mergeRebaseRasters(baseRaster, projectRaster, targetRaster)
	if conflictReason != "" {
		message := fmt.Sprintf("%s (project_sha256=%s target_sha256=%s)", conflictReason, fileHash(projectFile), fileHash(targetFile))
		conflict := newRebaseConflict(
			"pixel_merge_conflict", rel, "", message,
			[]string{"keep_project", "use_target", "manual"}, "manual",
			baseFile, projectFile, targetFile,
		)
		return nil, deltaData, delta, []RebaseConflict{conflict}, nil
	}
	if bytes.Equal(merged.pixels, targetRaster.pixels) {
		return append([]byte(nil), target...), deltaData, delta, nil, nil
	}
	candidate, err := encodeRebaseRasterByPath(rel, merged, targetRaster.tga)
	if err != nil {
		return nil, nil, RebaseMapDelta{}, nil, fmt.Errorf("encode coordinate-merged map raster %s: %w", rel, err)
	}
	return candidate, deltaData, delta, nil, nil
}

func decodeRebaseRasterByPath(rel string, data []byte) (rebaseRaster, error) {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png":
		return decodeRebasePNG(data)
	case ".tga":
		return decodeRebaseTGA(data)
	default:
		return rebaseRaster{}, fmt.Errorf("unsupported raster extension %q", filepath.Ext(rel))
	}
}

func encodeRebaseRasterByPath(rel string, raster rebaseRaster, tgaFormat rebaseTGAFormat) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png":
		return encodeRebasePNG(raster)
	case ".tga":
		if tgaFormat.depth != 24 && tgaFormat.depth != 32 {
			tgaFormat.depth = 32
		}
		if tgaFormat.depth == 24 && !rebaseRasterOpaque(raster) {
			tgaFormat.depth = 32
		}
		return encodeRebaseTGA(raster, tgaFormat)
	default:
		return nil, fmt.Errorf("unsupported raster extension %q", filepath.Ext(rel))
	}
}

func planRebaseProjectCoordinateMapRaster(
	artifactRoot, transactionID, rel string,
	project, base, target indexer.Source,
	baseFile, projectFile, targetFile *SnapshotFile,
	decision RebaseFileDecision,
) (RebaseFileDecision, []RebaseConflict, error) {
	decision.Adapter = "map_coordinate_delta"
	set := func(classification, action, reason string, safe bool, result *SnapshotFile) (RebaseFileDecision, []RebaseConflict, error) {
		decision.Classification, decision.Action, decision.Reason, decision.Safe = classification, action, reason, safe
		decision.Result = fileState(result)
		return decision, nil, nil
	}
	if projectFile == nil {
		return set("map_coordinate_no_project_delta", "delete_project", "project has no raster override; inherit the target map canvas", true, nil)
	}
	if baseFile == nil {
		switch {
		case targetFile == nil:
			return set("map_coordinate_project_added", "keep_project", "project-added map raster has no upstream baseline or target counterpart", true, projectFile)
		case projectFile.SHA256 == targetFile.SHA256:
			return set("map_coordinate_both_added_same", "delete_project", "target supplies the same added map raster", true, nil)
		default:
			conflict := newRebaseConflict(
				"map_coordinate_baseline_missing", rel, "",
				"project and target both provide a map raster but no old-upstream raster exists for coordinate delta extraction",
				[]string{"keep_project", "use_target", "manual"}, "manual",
				baseFile, projectFile, targetFile,
			)
			decision.Classification, decision.Action, decision.Reason = "map_coordinate_baseline_missing", "conflict", conflict.Message
			decision.ConflictIDs = []string{conflict.ID}
			return decision, []RebaseConflict{conflict}, nil
		}
	}
	if projectFile.SHA256 == baseFile.SHA256 {
		return set("map_coordinate_empty_delta", "delete_project", "project raster is identical to the old upstream; inherit the target map canvas", true, nil)
	}
	if targetFile == nil {
		conflict := newRebaseConflict(
			"delete_modify_conflict", rel, "",
			"target deleted a map raster that the project changed",
			[]string{"keep_project", "drop"}, "keep_project",
			baseFile, projectFile, targetFile,
		)
		decision.Classification, decision.Action, decision.Reason = "delete_modify_conflict", "conflict", conflict.Message
		decision.ConflictIDs = []string{conflict.ID}
		return decision, []RebaseConflict{conflict}, nil
	}
	if projectFile.SHA256 == targetFile.SHA256 {
		return set("map_coordinate_target_matches_project", "delete_project", "target already contains the project raster result", true, nil)
	}
	baseData, err := readSourceFile(base.Path, rel)
	if err != nil {
		return decision, nil, err
	}
	projectData, err := readSourceFile(project.Path, rel)
	if err != nil {
		return decision, nil, err
	}
	targetData, err := readSourceFile(target.Path, rel)
	if err != nil {
		return decision, nil, err
	}
	candidate, deltaData, delta, conflicts, err := buildRebaseMapCoordinateCandidate(
		rel, baseData, projectData, targetData, baseFile, projectFile, targetFile,
	)
	if err != nil {
		return decision, nil, err
	}
	if len(deltaData) > 0 {
		if delta.ChangedPixels == 0 && len(conflicts) == 0 {
			return set("map_coordinate_empty_delta", "delete_project", "project raster differs from the old upstream only by encoding; inherit the target map canvas", true, nil)
		}
		if err := storeRebaseMapDelta(artifactRoot, transactionID, rel, deltaData, delta, &decision); err != nil {
			return decision, nil, err
		}
	}
	if len(conflicts) > 0 {
		decision.Classification, decision.Action, decision.Reason = "map_coordinate_conflict", "conflict", conflicts[0].Message
		for _, conflict := range conflicts {
			decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
		}
		return decision, conflicts, nil
	}
	if targetFile != nil && hashBytes(candidate) == targetFile.SHA256 {
		decision.Classification, decision.Action, decision.Reason, decision.Safe = "map_coordinate_delta", "delete_project", "coordinate delta is already present in the target raster", true
		decision.Result = RebaseFileState{}
		return decision, nil, nil
	}
	if err := storeRebaseCandidate(artifactRoot, transactionID, rel, candidate, &decision); err != nil {
		return decision, nil, err
	}
	decision.Classification, decision.Action, decision.Reason, decision.Safe = "map_coordinate_delta", "write_candidate", "transparent Base-to-Ours delta applied to the target at origin (0,0) with zero conflicting pixels", true
	return decision, nil, nil
}

func storeRebaseMapDelta(root, id, rel string, data []byte, delta RebaseMapDelta, decision *RebaseFileDecision) error {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return err
	}
	normalized, err := normalizeRel(rel)
	if err != nil {
		return err
	}
	deltaRel := filepath.ToSlash(filepath.Join("map-deltas", filepath.FromSlash(normalized)))
	if !strings.HasSuffix(strings.ToLower(deltaRel), ".png") {
		deltaRel += ".png"
	}
	full, err := safeRebaseContainedPath(dir, filepath.Join(dir, filepath.FromSlash(deltaRel)))
	if err != nil {
		return fmt.Errorf("map delta path escapes transaction root: %w", err)
	}
	parent, err := ensureRebaseDirectory(filepath.Dir(full))
	if err != nil {
		return err
	}
	full, err = safeRebaseContainedPath(dir, filepath.Join(parent, filepath.Base(full)))
	if err != nil {
		return fmt.Errorf("map delta path escapes transaction root after directory creation: %w", err)
	}
	file, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	delta.Path = deltaRel
	delta.SHA256 = hashBytes(data)
	decision.MapDelta = &delta
	return nil
}

func rebaseCoordinateMapRasterPath(rel string) bool {
	if !isCoreMapPath(rel) {
		return false
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png", ".tga":
		return true
	default:
		return false
	}
}

func decodeRebasePNG(data []byte) (rebaseRaster, error) {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return rebaseRaster{}, err
	}
	if err := validateRebaseRasterDimensions(config.Width, config.Height); err != nil {
		return rebaseRaster{}, err
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return rebaseRaster{}, err
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != config.Width || bounds.Dy() != config.Height {
		return rebaseRaster{}, fmt.Errorf("decoded PNG dimensions differ from header: %dx%d versus %dx%d", bounds.Dx(), bounds.Dy(), config.Width, config.Height)
	}
	pixels := make([]byte, config.Width*config.Height*4)
	for y := 0; y < config.Height; y++ {
		for x := 0; x < config.Width; x++ {
			pixel := color.NRGBAModel.Convert(decoded.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			offset := (y*config.Width + x) * 4
			pixels[offset+0] = pixel.R
			pixels[offset+1] = pixel.G
			pixels[offset+2] = pixel.B
			pixels[offset+3] = pixel.A
		}
	}
	return rebaseRaster{width: config.Width, height: config.Height, pixels: pixels}, nil
}

func encodeRebasePNG(raster rebaseRaster) ([]byte, error) {
	if err := validateRebaseRaster(raster); err != nil {
		return nil, err
	}
	decoded := image.NewNRGBA(image.Rect(0, 0, raster.width, raster.height))
	for y := 0; y < raster.height; y++ {
		for x := 0; x < raster.width; x++ {
			offset := (y*raster.width + x) * 4
			decoded.SetNRGBA(x, y, color.NRGBA{
				R: raster.pixels[offset+0],
				G: raster.pixels[offset+1],
				B: raster.pixels[offset+2],
				A: raster.pixels[offset+3],
			})
		}
	}
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(&output, decoded); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func mergeRebaseRasters(base, project, target rebaseRaster) (rebaseRaster, string) {
	if err := validateRebaseRaster(base); err != nil {
		return rebaseRaster{}, "base raster is invalid: " + err.Error()
	}
	if err := validateRebaseRaster(project); err != nil {
		return rebaseRaster{}, "project raster is invalid: " + err.Error()
	}
	if err := validateRebaseRaster(target); err != nil {
		return rebaseRaster{}, "target raster is invalid: " + err.Error()
	}
	if base.width != project.width || base.height != project.height || base.width != target.width || base.height != target.height {
		return rebaseRaster{}, fmt.Sprintf(
			"raster dimensions differ (base=%dx%d project=%dx%d target=%dx%d)",
			base.width, base.height, project.width, project.height, target.width, target.height,
		)
	}
	merged := rebaseRaster{
		width:  base.width,
		height: base.height,
		pixels: make([]byte, len(base.pixels)),
	}
	changedDifferently := 0
	for offset := 0; offset < len(base.pixels); offset += 4 {
		switch {
		case sameRebasePixel(project.pixels[offset:offset+4], base.pixels[offset:offset+4]):
			copy(merged.pixels[offset:offset+4], target.pixels[offset:offset+4])
		case sameRebasePixel(target.pixels[offset:offset+4], base.pixels[offset:offset+4]):
			copy(merged.pixels[offset:offset+4], project.pixels[offset:offset+4])
		case sameRebasePixel(project.pixels[offset:offset+4], target.pixels[offset:offset+4]):
			copy(merged.pixels[offset:offset+4], project.pixels[offset:offset+4])
		default:
			changedDifferently++
		}
	}
	if changedDifferently > 0 {
		return rebaseRaster{}, fmt.Sprintf("project and target changed %d pixel(s) differently", changedDifferently)
	}
	return merged, ""
}

func sameRebasePixel(left, right []byte) bool {
	return left[0] == right[0] && left[1] == right[1] && left[2] == right[2] && left[3] == right[3]
}

func validateRebaseRasterDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid raster dimensions %dx%d", width, height)
	}
	if width > rebaseRasterMaxPixels/height {
		return fmt.Errorf("raster dimensions %dx%d exceed %d pixels", width, height, rebaseRasterMaxPixels)
	}
	return nil
}

func validateRebaseRaster(raster rebaseRaster) error {
	if err := validateRebaseRasterDimensions(raster.width, raster.height); err != nil {
		return err
	}
	expected := raster.width * raster.height * 4
	if len(raster.pixels) != expected {
		return fmt.Errorf("invalid raster pixel buffer: got %d bytes, need %d", len(raster.pixels), expected)
	}
	return nil
}

func rebaseRasterOpaque(raster rebaseRaster) bool {
	for offset := 3; offset < len(raster.pixels); offset += 4 {
		if raster.pixels[offset] != 0xff {
			return false
		}
	}
	return true
}

// decodeRebaseTGA accepts only true-color TGA type 2 and type 10 images. The
// output is normalized to top-left RGBA, including the optional right-to-left
// origin flag. TGA packet streams are decoded as a single continuous pixel
// sequence because RLE packets may legally cross scan-line boundaries.
func decodeRebaseTGA(data []byte) (rebaseRaster, error) {
	if len(data) < 18 {
		return rebaseRaster{}, fmt.Errorf("truncated TGA header")
	}
	idLength := int(data[0])
	if data[1] != 0 {
		return rebaseRaster{}, fmt.Errorf("color-mapped TGA is unsupported")
	}
	imageType := data[2]
	if imageType != 2 && imageType != 10 {
		return rebaseRaster{}, fmt.Errorf("only uncompressed or RLE true-color TGA is supported")
	}
	width := int(binary.LittleEndian.Uint16(data[12:14]))
	height := int(binary.LittleEndian.Uint16(data[14:16]))
	depth := int(data[16])
	if depth != 24 && depth != 32 {
		return rebaseRaster{}, fmt.Errorf("only 24-bit or 32-bit TGA is supported, got %d-bit", depth)
	}
	if err := validateRebaseRasterDimensions(width, height); err != nil {
		return rebaseRaster{}, err
	}
	offset := 18 + idLength
	if offset > len(data) {
		return rebaseRaster{}, fmt.Errorf("truncated TGA image ID")
	}
	format := rebaseTGAFormat{
		depth:       depth,
		rle:         imageType == 10,
		topOrigin:   data[17]&0x20 != 0,
		rightOrigin: data[17]&0x10 != 0,
		xOrigin:     binary.LittleEndian.Uint16(data[8:10]),
		yOrigin:     binary.LittleEndian.Uint16(data[10:12]),
		id:          append([]byte(nil), data[18:offset]...),
	}
	raster := rebaseRaster{
		width:  width,
		height: height,
		pixels: make([]byte, width*height*4),
		tga:    format,
	}
	pixelBytes := depth / 8
	pixelCount := width * height
	writePixel := func(fileIndex int, value []byte) {
		row := fileIndex / width
		column := fileIndex % width
		y := row
		if !format.topOrigin {
			y = height - 1 - row
		}
		x := column
		if format.rightOrigin {
			x = width - 1 - column
		}
		destination := (y*width + x) * 4
		raster.pixels[destination+0] = value[2]
		raster.pixels[destination+1] = value[1]
		raster.pixels[destination+2] = value[0]
		if pixelBytes == 4 {
			raster.pixels[destination+3] = value[3]
		} else {
			raster.pixels[destination+3] = 0xff
		}
	}

	if imageType == 2 {
		needed := pixelCount * pixelBytes
		if len(data)-offset < needed {
			return rebaseRaster{}, fmt.Errorf("truncated uncompressed TGA pixel data: need %d bytes, have %d", needed, len(data)-offset)
		}
		for index := 0; index < pixelCount; index++ {
			start := offset + index*pixelBytes
			writePixel(index, data[start:start+pixelBytes])
		}
		return raster, nil
	}

	for written := 0; written < pixelCount; {
		if offset >= len(data) {
			return rebaseRaster{}, fmt.Errorf("truncated RLE packet stream after %d of %d pixels", written, pixelCount)
		}
		packet := data[offset]
		offset++
		count := int(packet&0x7f) + 1
		if count > pixelCount-written {
			return rebaseRaster{}, fmt.Errorf("RLE packet overruns image by %d pixels", count-(pixelCount-written))
		}
		if packet&0x80 != 0 {
			if len(data)-offset < pixelBytes {
				return rebaseRaster{}, fmt.Errorf("truncated RLE run at pixel %d", written)
			}
			value := data[offset : offset+pixelBytes]
			offset += pixelBytes
			for index := 0; index < count; index++ {
				writePixel(written+index, value)
			}
		} else {
			needed := count * pixelBytes
			if len(data)-offset < needed {
				return rebaseRaster{}, fmt.Errorf("truncated raw RLE packet at pixel %d", written)
			}
			for index := 0; index < count; index++ {
				start := offset + index*pixelBytes
				writePixel(written+index, data[start:start+pixelBytes])
			}
			offset += needed
		}
		written += count
	}
	return raster, nil
}

// encodeRebaseTGA writes a true-color 24/32-bit TGA. It supports both type 2
// and type 10 output so a merged project asset can retain its original RLE
// choice. Pixel data is emitted in the requested origin order.
func encodeRebaseTGA(raster rebaseRaster, format rebaseTGAFormat) ([]byte, error) {
	if err := validateRebaseRaster(raster); err != nil {
		return nil, err
	}
	if raster.width > 0xffff || raster.height > 0xffff {
		return nil, fmt.Errorf("TGA dimensions exceed 16-bit header limits: %dx%d", raster.width, raster.height)
	}
	if format.depth != 24 && format.depth != 32 {
		return nil, fmt.Errorf("unsupported TGA output depth %d", format.depth)
	}
	if len(format.id) > 255 {
		return nil, fmt.Errorf("TGA image ID is too large: %d bytes", len(format.id))
	}
	if format.depth == 24 && !rebaseRasterOpaque(raster) {
		return nil, fmt.Errorf("cannot encode non-opaque pixels as 24-bit TGA")
	}
	pixelBytes := format.depth / 8
	header := make([]byte, 18)
	header[0] = byte(len(format.id))
	if format.rle {
		header[2] = 10
	} else {
		header[2] = 2
	}
	binary.LittleEndian.PutUint16(header[8:10], format.xOrigin)
	binary.LittleEndian.PutUint16(header[10:12], format.yOrigin)
	binary.LittleEndian.PutUint16(header[12:14], uint16(raster.width))
	binary.LittleEndian.PutUint16(header[14:16], uint16(raster.height))
	header[16] = byte(format.depth)
	if format.topOrigin {
		header[17] |= 0x20
	}
	if format.rightOrigin {
		header[17] |= 0x10
	}
	if format.depth == 32 {
		header[17] |= 8
	}

	filePixels := make([]byte, raster.width*raster.height*pixelBytes)
	for fileIndex := 0; fileIndex < raster.width*raster.height; fileIndex++ {
		row := fileIndex / raster.width
		column := fileIndex % raster.width
		y := row
		if !format.topOrigin {
			y = raster.height - 1 - row
		}
		x := column
		if format.rightOrigin {
			x = raster.width - 1 - column
		}
		source := (y*raster.width + x) * 4
		destination := fileIndex * pixelBytes
		filePixels[destination+0] = raster.pixels[source+2]
		filePixels[destination+1] = raster.pixels[source+1]
		filePixels[destination+2] = raster.pixels[source+0]
		if pixelBytes == 4 {
			filePixels[destination+3] = raster.pixels[source+3]
		}
	}

	output := make([]byte, 0, len(header)+len(format.id)+len(filePixels)+len(filePixels)/128+1)
	output = append(output, header...)
	output = append(output, format.id...)
	if !format.rle {
		return append(output, filePixels...), nil
	}
	return append(output, encodeRebaseTGARLE(filePixels, pixelBytes)...), nil
}

func encodeRebaseTGARLE(filePixels []byte, pixelBytes int) []byte {
	pixelCount := len(filePixels) / pixelBytes
	output := make([]byte, 0, len(filePixels)+len(filePixels)/128+1)
	same := func(left, right int) bool {
		leftOffset, rightOffset := left*pixelBytes, right*pixelBytes
		return bytes.Equal(filePixels[leftOffset:leftOffset+pixelBytes], filePixels[rightOffset:rightOffset+pixelBytes])
	}
	for position := 0; position < pixelCount; {
		run := 1
		for position+run < pixelCount && run < 128 && same(position, position+run) {
			run++
		}
		if run >= 2 {
			output = append(output, 0x80|byte(run-1))
			offset := position * pixelBytes
			output = append(output, filePixels[offset:offset+pixelBytes]...)
			position += run
			continue
		}

		start := position
		position++
		for position < pixelCount && position-start < 128 {
			nextRun := 1
			for position+nextRun < pixelCount && nextRun < 128 && same(position, position+nextRun) {
				nextRun++
			}
			if nextRun >= 2 {
				break
			}
			position++
		}
		output = append(output, byte(position-start-1))
		output = append(output, filePixels[start*pixelBytes:position*pixelBytes]...)
	}
	return output
}
